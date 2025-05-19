package proxy

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/naotama2002/mcp-remote-go/auth"
)

// TransportStrategy はトランスポート選択戦略を定義します
type TransportStrategy string

const (
	// SSEOnly はSSEトランスポートのみを使用する戦略
	SSEOnly TransportStrategy = "sse-only"
	// HTTPOnly はHTTPトランスポートのみを使用する戦略
	HTTPOnly TransportStrategy = "http-only"
	// SSEFirst はSSEトランスポートを優先し、失敗した場合はHTTPトランスポートにフォールバックする戦略
	SSEFirst TransportStrategy = "sse-first"
	// HTTPFirst はHTTPトランスポートを優先し、失敗した場合はSSEトランスポートにフォールバックする戦略
	HTTPFirst TransportStrategy = "http-first"
)

// MCPProxy はMCPプロキシの構造体です
type MCPProxy struct {
	localTransport  transport.Interface
	remoteTransport transport.Interface
	cleanup         func() error
}

// NewMCPProxy は新しいMCPプロキシを作成します
func NewMCPProxy(localTransport transport.Interface, remoteTransport transport.Interface) *MCPProxy {
	return &MCPProxy{
		localTransport:  localTransport,
		remoteTransport: remoteTransport,
	}
}

// Start はプロキシを開始します
func (p *MCPProxy) Start() error {
	// ローカルトランスポートからリモートトランスポートへのメッセージハンドラを設定
	p.localTransport.SetNotificationHandler(func(notification mcp.JSONRPCNotification) {
		log.Printf("[Local→Remote] %s", notification.Notification.Method)
		err := p.remoteTransport.SendNotification(context.Background(), notification)
		if err != nil {
			log.Printf("Error sending notification to remote: %v", err)
		}
	})

	// リモートトランスポートからローカルトランスポートへのメッセージハンドラを設定
	p.remoteTransport.SetNotificationHandler(func(notification mcp.JSONRPCNotification) {
		log.Printf("[Remote→Local] %s", notification.Notification.Method)
		err := p.localTransport.SendNotification(context.Background(), notification)
		if err != nil {
			log.Printf("Error sending notification to local: %v", err)
		}
	})

	// クリーンアップ関数を設定
	p.cleanup = func() error {
		log.Println("Closing transports...")
		
		// リモートトランスポートを閉じる
		if err := p.remoteTransport.Close(); err != nil {
			log.Printf("Error closing remote transport: %v", err)
		}
		
		// ローカルトランスポートを閉じる
		if err := p.localTransport.Close(); err != nil {
			log.Printf("Error closing local transport: %v", err)
			return err
		}
		
		return nil
	}

	// シグナルハンドラを設定
	setupSignalHandlers(p.cleanup)

	return nil
}

// Close はプロキシを閉じます
func (p *MCPProxy) Close() error {
	if p.cleanup != nil {
		return p.cleanup()
	}
	return nil
}

// RunProxy はプロキシを実行します
func RunProxy(serverURL string, callbackPort int, headers map[string]string, transportStrategy TransportStrategy) error {
	// サーバーURLハッシュを取得
	serverURLHash := auth.GetServerURLHash(serverURL)

	// 認証コーディネーターを作成
	authCoordinator := auth.NewLazyAuthCoordinator(serverURLHash, callbackPort)

	// OAuth認証プロバイダーを作成
	authProvider := auth.NewNodeOAuthClientProvider(auth.OAuthProviderOptions{
		ServerURL:    serverURL,
		CallbackPort: callbackPort,
		ClientName:   "MCP CLI Proxy",
	})

	// ローカルSTDIOトランスポートを作成
	localTransport := NewStdioServerTransport()

	// サーバーインスタンスを追跡するための変数
	var server *http.Server

	// 認証初期化関数を定義
	authInitializer := func() (*auth.AuthState, error) {
		authState, err := authCoordinator.InitializeAuth()
		if err != nil {
			return nil, err
		}

		// 外部スコープのサーバーを設定
		server = authState.Server

		// 他のインスタンスによって認証が完了した場合
		if authState.SkipBrowserAuth {
			log.Println("Authentication was completed by another instance - will use tokens from disk")
			// トークン交換が完了する前にコールバックが発生するため、少し待機
			// TODO: 削除、コールバックはトークン交換前に発生するため、少し早すぎる
			<-time.After(time.Second)
		}

		return authState, nil
	}

	// リモートサーバーに接続
	remoteTransport, err := ConnectToRemoteServer(nil, serverURL, authProvider, headers, authInitializer, transportStrategy)
	if err != nil {
		// エラーが自己署名証明書に関連する場合、VPN関連のヒントを表示
		if strings.Contains(err.Error(), "self-signed certificate in certificate chain") {
			log.Println(`You may be behind a VPN!

If you are behind a VPN, you can try setting the SSL_CERT_FILE environment variable to point
to the CA certificate file. If using claude_desktop_config.json, this might look like:

{
  "mcpServers": {
    "${mcpServerName}": {
      "command": "mcp-remote-go",
      "args": [
        "https://remote.mcp.server/sse"
      ],
      "env": {
        "SSL_CERT_FILE": "${your CA certificate file path}.pem"
      }
    }
  }
}
			`)
		}
		
		// サーバーが初期化されている場合のみ閉じる
		if server != nil {
			server.Close()
		}
		
		return fmt.Errorf("failed to connect to remote server: %w", err)
	}

	// ローカルとリモートのトランスポート間の双方向プロキシを設定
	proxy := NewMCPProxy(localTransport, remoteTransport)
	if err := proxy.Start(); err != nil {
		return fmt.Errorf("failed to start proxy: %w", err)
	}

	// ローカルSTDIOサーバーを開始
	if err := localTransport.Start(context.Background()); err != nil {
		return fmt.Errorf("failed to start local STDIO server: %w", err)
	}

	log.Println("Local STDIO server running")
	log.Printf("Proxy established successfully between local STDIO and remote %T", remoteTransport)
	log.Println("Press Ctrl+C to exit")

	// クリーンアップハンドラを設定
	cleanup := func() error {
		if err := remoteTransport.Close(); err != nil {
			log.Printf("Error closing remote transport: %v", err)
		}
		
		if err := localTransport.Close(); err != nil {
			log.Printf("Error closing local transport: %v", err)
		}
		
		// サーバーが初期化されている場合のみ閉じる
		if server != nil {
			if err := server.Close(); err != nil {
				log.Printf("Error closing server: %v", err)
			}
		}
		
		return nil
	}
	
	setupSignalHandlers(cleanup)
	
	// メインスレッドをブロックし、シグナルを待つ
	waitForSignal()
	
	return nil
}

// setupSignalHandlers はシグナルハンドラを設定します
func setupSignalHandlers(cleanup func() error) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	
	go func() {
		<-c
		log.Println("Received termination signal")
		if err := cleanup(); err != nil {
			log.Printf("Error during cleanup: %v", err)
		}
		os.Exit(0)
	}()
}

// waitForSignal はシグナルを待ちます
func waitForSignal() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
}
