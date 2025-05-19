package auth

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
)

// OAuthCallbackServerOptions は、OAuthコールバックサーバーのオプションを表します
type OAuthCallbackServerOptions struct {
	Port int
	Path string
}

// SetupOAuthCallbackServer は、OAuthコールバックを処理するExpressサーバーをセットアップします
func SetupOAuthCallbackServer(port int, path string) (*http.Server, func() (string, error), error) {
	// 認証コードを格納するチャネル
	authCodeChan := make(chan string, 1)
	var authCodeMutex sync.Mutex
	var authCompleted bool

	// ハンドラーの設定
	mux := http.NewServeMux()

	// コールバックパスのハンドラー
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Authorization code not found", http.StatusBadRequest)
			return
		}

		authCodeMutex.Lock()
		authCompleted = true
		authCodeMutex.Unlock()

		// 認証コードをチャネルに送信
		select {
		case authCodeChan <- code:
			// コードが送信された
		default:
			// チャネルがすでに閉じられているか、バッファがいっぱい
		}

		// ユーザーに成功メッセージを表示
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `
		<!DOCTYPE html>
		<html>
		<head>
			<title>Authorization Successful</title>
			<style>
				body { font-family: Arial, sans-serif; text-align: center; padding: 50px; }
				.success { color: #4CAF50; }
				.container { max-width: 600px; margin: 0 auto; }
			</style>
		</head>
		<body>
			<div class="container">
				<h1 class="success">Authorization Successful!</h1>
				<p>You have successfully authorized the MCP client.</p>
				<p>You can now close this window and return to the application.</p>
			</div>
		</body>
		</html>
		`)
	})

	// 認証待機エンドポイント
	mux.HandleFunc("/wait-for-auth", func(w http.ResponseWriter, r *http.Request) {
		poll := r.URL.Query().Get("poll") != "false"

		authCodeMutex.Lock()
		completed := authCompleted
		authCodeMutex.Unlock()

		if completed {
			w.WriteHeader(http.StatusOK)
			return
		}

		if poll {
			// ロングポーリングは実装しない（シンプルにするため）
			w.WriteHeader(http.StatusAccepted)
		} else {
			w.WriteHeader(http.StatusAccepted)
		}
	})

	// 利用可能なポートを見つける
	actualPort := port
	if actualPort <= 0 {
		p, err := findAvailablePort(8000)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to find available port: %w", err)
		}
		actualPort = p
	}

	// サーバーの作成
	server := &http.Server{
		Addr:    ":" + strconv.Itoa(actualPort),
		Handler: mux,
	}

	// サーバーを別のゴルーチンで開始
	go func() {
		log.Printf("Starting OAuth callback server on port %d", actualPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("OAuth callback server error: %v", err)
		}
	}()

	// 認証コードを待機する関数
	waitForAuthCode := func() (string, error) {
		code, ok := <-authCodeChan
		if !ok {
			return "", fmt.Errorf("auth code channel closed")
		}
		return code, nil
	}

	return server, waitForAuthCode, nil
}

// findAvailablePort は、利用可能なポートを見つけます
func findAvailablePort(preferredPort int) (int, error) {
	port := preferredPort

	for i := 0; i < 100; i++ { // 最大100回試行
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			ln.Close()
			return port, nil
		}
		port++
	}

	return 0, fmt.Errorf("could not find an available port")
}
