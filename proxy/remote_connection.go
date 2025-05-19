package proxy

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/naotama2002/mcp-remote-go/auth"
)

// ReasonAuthNeeded は認証が必要な理由を示す定数
const ReasonAuthNeeded = "authentication-needed"

// ReasonTransportFallback はトランスポートフォールバックの理由を示す定数
const ReasonTransportFallback = "falling-back-to-alternate-transport"

// ConnectToRemoteServer はリモートサーバーに接続します
func ConnectToRemoteServer(
	client *client.Client,
	serverURL string,
	authProvider auth.OAuthClientProvider,
	headers map[string]string,
	authInitializer func() (*auth.AuthState, error),
	transportStrategy TransportStrategy,
) (transport.Interface, error) {
	log.Printf("Connecting to remote server: %s", serverURL)
	_, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	// 認証トークンを含むヘッダーを作成
	authHeaders := make(map[string]string)
	for k, v := range headers {
		authHeaders[k] = v
	}

	// トークンが存在する場合は認証ヘッダーを追加
	tokens, err := authProvider.Tokens()
	if err == nil && tokens != nil {
		authHeaders["Authorization"] = fmt.Sprintf("Bearer %s", tokens.AccessToken)
	}

	log.Printf("Using transport strategy: %s", transportStrategy)
	// フォールバックを試みるかどうかを判断
	shouldAttemptFallback := transportStrategy == HTTPFirst || transportStrategy == SSEFirst

	// 戦略に基づいてトランスポートインスタンスを作成
	useSSE := transportStrategy == SSEOnly || transportStrategy == SSEFirst
	var t transport.Interface
	var transportErr error

	if useSSE {
		// SSEトランスポートを作成
		t, transportErr = transport.NewSSE(serverURL, transport.WithHeaders(authHeaders))
	} else {
		// HTTPトランスポートを作成
		// 注: StreamableHTTPはヘッダーを直接サポートしていないため、標準のHTTPクライアントを使用
		t, transportErr = transport.NewStreamableHTTP(serverURL)
	}

	if transportErr != nil {
		return nil, fmt.Errorf("failed to create transport: %w", transportErr)
	}

	// 接続を試みる
	try := func() error {
		if client != nil {
			return client.Start(context.Background())
		} else {
			return t.Start(context.Background())
		}
	}

	err = try()
	if err != nil {
		// プロトコルエラーの場合、フォールバックを試みる
		if shouldAttemptFallback && (strings.Contains(err.Error(), "405") ||
			strings.Contains(err.Error(), "Method Not Allowed") ||
			strings.Contains(err.Error(), "404") ||
			strings.Contains(err.Error(), "Not Found")) {
			log.Printf("Received error: %v", err)
			log.Printf("Falling back to alternate transport")

			// フォールバックのトランスポートを作成
			var fallbackTransport transport.Interface
			var fallbackErr error

			if useSSE {
				// SSEからHTTPにフォールバック
				fallbackTransport, fallbackErr = transport.NewStreamableHTTP(serverURL)
			} else {
				// HTTPからSSEにフォールバック
				fallbackTransport, fallbackErr = transport.NewSSE(serverURL, transport.WithHeaders(authHeaders))
			}

			if fallbackErr != nil {
				return nil, fmt.Errorf("failed to create fallback transport: %w", fallbackErr)
			}

			// フォールバックトランスポートで接続を試みる
			if client != nil {
				fallbackErr = client.Start(context.Background())
			} else {
				fallbackErr = fallbackTransport.Start(context.Background())
			}

			if fallbackErr != nil {
				return nil, fmt.Errorf("fallback transport failed: %w", fallbackErr)
			}

			log.Printf("Connected to remote server using fallback transport")
			return fallbackTransport, nil
		} else if strings.Contains(err.Error(), "Unauthorized") || strings.Contains(err.Error(), "401") {
			// 認証が必要な場合
			log.Println("Authentication required. Initializing auth...")

			// オンデマンドで認証を初期化
			authState, authErr := authInitializer()
			if authErr != nil {
				return nil, fmt.Errorf("failed to initialize authentication: %w", authErr)
			}

			if authState.SkipBrowserAuth {
				log.Println("Authentication required but skipping browser auth - using shared auth")
			} else {
				log.Println("Authentication required. Waiting for authorization...")
			}

			// コールバックから認証コードを待機
			authCode, authErr := authState.WaitForAuthCode()
			if authErr != nil {
				return nil, fmt.Errorf("failed to get authorization code: %w", authErr)
			}
			
			// 認証コードのログ出力（デバッグ用）
			log.Printf("Received auth code: %s...", authCode[:5])

			// 認証を完了
			log.Println("Completing authorization...")
			
			// 認証完了後、トランスポートを再接続
			tokens, err := authProvider.Tokens()
			if err != nil {
				return nil, fmt.Errorf("failed to get tokens: %w", err)
			}
			
			if tokens == nil {
				return nil, errors.New("no tokens available after authentication")
			}
			
			// 新しい認証ヘッダーでトランスポートを再作成
			authHeaders["Authorization"] = fmt.Sprintf("Bearer %s", tokens.AccessToken)
			
			// 元のトランスポート戦略に基づいて新しいトランスポートを作成
			var newTransport transport.Interface
			var newErr error
			
			if useSSE {
				newTransport, newErr = transport.NewSSE(serverURL, transport.WithHeaders(authHeaders))
			} else {
				newTransport, newErr = transport.NewStreamableHTTP(serverURL)
			}
			
			if newErr != nil {
				return nil, fmt.Errorf("failed to create transport after authentication: %w", newErr)
			}
			
			// 新しいトランスポートで接続を試みる
			if client != nil {
				newErr = client.Start(context.Background())
			} else {
				newErr = newTransport.Start(context.Background())
			}
			
			if newErr != nil {
				return nil, fmt.Errorf("failed to connect after authentication: %w", newErr)
			}
			
			log.Println("Successfully authenticated and connected")
			return newTransport, nil
		} else {
			// その他のエラー
			return nil, fmt.Errorf("connection error: %w", err)
		}
	}

	log.Printf("Connected to remote server using %T", t)
	return t, nil
}
