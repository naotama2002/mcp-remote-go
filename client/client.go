package client

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/naotama2002/mcp-remote-go/auth"
	"github.com/naotama2002/mcp-remote-go/client/transport"
)

// MCPRemoteVersion はMCP-Remoteのバージョンです
const MCPRemoteVersion = "0.1.0"

// TransportStrategy はトランスポート選択戦略を表します
type TransportStrategy string

const (
	// HTTPFirst はHTTPトランスポートを優先し、失敗した場合にSSEを試みます
	HTTPFirst TransportStrategy = "http-first"
	// SSEFirst はSSEトランスポートを優先し、失敗した場合にHTTPを試みます
	SSEFirst TransportStrategy = "sse-first"
	// HTTPOnly はHTTPトランスポートのみを使用します
	HTTPOnly TransportStrategy = "http-only"
	// SSEOnly はSSEトランスポートのみを使用します
	SSEOnly TransportStrategy = "sse-only"
)

// ClientOptions はクライアントのオプションを表します
type ClientOptions struct {
	ServerURL         string
	CallbackPort      int
	Headers           map[string]string
	TransportStrategy TransportStrategy
}

// RunClient はMCPクライアントを実行します
func RunClient(options ClientOptions) error {
	// サーバーURLハッシュを取得
	serverURLHash := auth.GetServerURLHash(options.ServerURL)

	// 認証コーディネーターを作成
	authCoordinator := auth.NewLazyAuthCoordinator(serverURLHash, options.CallbackPort)

	// OAuth クライアントプロバイダーを作成
	authProvider := auth.NewGoOAuthClientProvider(auth.OAuthProviderOptions{
		ServerURL:       options.ServerURL,
		CallbackPort:    options.CallbackPort,
		ClientName:      "MCP CLI Client",
		SoftwareVersion: MCPRemoteVersion,
	})

	// クライアントは後で作成する
	var mcpClient *client.Client

	// シグナルハンドラーのセットアップ
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signalCh
		log.Println("\nClosing connection...")
		cancel()
	}()

	// リモートサーバーに接続
	authenticatedTransport, err := connectToRemoteServer(
		ctx,
		nil, // mcpClientはまだ作成されていない
		options.ServerURL,
		authProvider,
		authCoordinator,
		options.Headers,
		options.TransportStrategy,
	)
	if err != nil {
		return fmt.Errorf("failed to connect to remote server: %w", err)
	}

	// クライアントを作成
	mcpClient = client.NewClient(authenticatedTransport)

	// メッセージとエラーハンドラーを設定
	authenticatedTransport.SetNotificationHandler(func(notification mcp.JSONRPCNotification) {
		log.Printf("Received message: %+v\n", notification)
	})

	log.Println("Connected successfully!")

	// クライアントを初期化
	log.Println("Initializing client...")
	initRequest := mcp.InitializeRequest{}
	initRequest.Params.ProtocolVersion = "2025-03-26"
	initRequest.Params.ClientInfo = mcp.Implementation{
		Name:    "mcp-remote-go",
		Version: MCPRemoteVersion,
	}
	initRequest.Params.Capabilities = mcp.ClientCapabilities{}
	initResult, err := mcpClient.Initialize(ctx, initRequest)
	if err != nil {
		log.Printf("Error initializing client: %v\n", err)
		return err
	}
	log.Printf("Initialized client: %+v\n", initResult)

	// ツール一覧をリクエスト
	log.Println("Requesting tools list...")
	toolsResp, err := mcpClient.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		log.Printf("Error requesting tools list: %v\n", err)
	} else {
		log.Printf("Tools: %+v\n", toolsResp)
	}

	// リソース一覧をリクエスト
	log.Println("Requesting resource list...")
	resourcesResp, err := mcpClient.ListResources(ctx, mcp.ListResourcesRequest{})
	if err != nil {
		log.Printf("Error requesting resources list: %v\n", err)
	} else {
		log.Printf("Resources: %+v\n", resourcesResp)
	}

	log.Println("Exiting OK...")
	return nil
}

// connectToRemoteServer はリモートサーバーに接続します
func connectToRemoteServer(
	ctx context.Context,
	_ *client.Client, // 使用しないが後方互換性のために残す
	serverURL string,
	authProvider auth.OAuthClientProvider,
	authCoordinator auth.AuthCoordinator,
	headers map[string]string,
	strategy TransportStrategy,
) (transport.AuthenticatedTransport, error) {
	log.Printf("Connecting to remote server: %s\n", serverURL)

	var transportInstance transport.AuthenticatedTransport
	var err error

	// 戦略に基づいてトランスポートを選択
	switch strategy {
	case HTTPOnly:
		transportInstance, err = createHTTPTransport(serverURL, authProvider, authCoordinator, headers)
	case SSEOnly:
		transportInstance, err = createSSETransport(serverURL, authProvider, authCoordinator, headers)
	case HTTPFirst:
		transportInstance, err = createHTTPTransport(serverURL, authProvider, authCoordinator, headers)
		if err != nil {
			log.Printf("HTTP transport failed, falling back to SSE: %v\n", err)
			transportInstance, err = createSSETransport(serverURL, authProvider, authCoordinator, headers)
		}
	case SSEFirst:
		transportInstance, err = createSSETransport(serverURL, authProvider, authCoordinator, headers)
		if err != nil {
			log.Printf("SSE transport failed, falling back to HTTP: %v\n", err)
			transportInstance, err = createHTTPTransport(serverURL, authProvider, authCoordinator, headers)
		}
	default:
		// デフォルトはHTTPFirst
		transportInstance, err = createHTTPTransport(serverURL, authProvider, authCoordinator, headers)
		if err != nil {
			log.Printf("HTTP transport failed, falling back to SSE: %v\n", err)
			transportInstance, err = createSSETransport(serverURL, authProvider, authCoordinator, headers)
		}
	}

	if err != nil {
		return nil, err
	}

	// トランスポートを開始
	if err := transportInstance.Start(ctx); err != nil {
		return nil, fmt.Errorf("failed to start transport: %w", err)
	}

	return transportInstance, nil
}

// createHTTPTransport はHTTPトランスポートを作成します
func createHTTPTransport(
	serverURL string,
	authProvider auth.OAuthClientProvider,
	authCoordinator auth.AuthCoordinator,
	headers map[string]string,
) (*transport.AuthenticatedStreamableHTTP, error) {
	return transport.NewAuthenticatedStreamableHTTP(serverURL, authProvider, authCoordinator, headers)
}

// createSSETransport はSSEトランスポートを作成します
func createSSETransport(
	serverURL string,
	authProvider auth.OAuthClientProvider,
	authCoordinator auth.AuthCoordinator,
	headers map[string]string,
) (*transport.AuthenticatedSSE, error) {
	return transport.NewAuthenticatedSSE(serverURL, authProvider, authCoordinator, headers)
}
