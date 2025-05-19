package transport

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/naotama2002/mcp-remote-go/auth"
)

// AuthenticatedSSE は、OAuth2.1認証機能を持つSSEトランスポートです
type AuthenticatedSSE struct {
	baseTransport *transport.SSE
	authProvider  auth.OAuthClientProvider
	authFlow      *auth.OAuthFlow
}

// NewAuthenticatedSSE は、新しい認証付きSSEトランスポートを作成します
func NewAuthenticatedSSE(baseURL string, authProvider auth.OAuthClientProvider, authCoordinator auth.AuthCoordinator, headers map[string]string) (*AuthenticatedSSE, error) {
	// 基本となるSSEトランスポートを作成
	baseTransport, err := transport.NewSSE(baseURL, transport.WithHeaders(headers))
	if err != nil {
		return nil, fmt.Errorf("failed to create base SSE transport: %w", err)
	}

	// 認証フローを作成
	authFlow, err := auth.NewOAuthFlow(baseURL, authProvider, authCoordinator, headers)
	if err != nil {
		return nil, fmt.Errorf("failed to create OAuth flow: %w", err)
	}

	return &AuthenticatedSSE{
		baseTransport: baseTransport,
		authProvider:  authProvider,
		authFlow:      authFlow,
	}, nil
}

// Start は、SSE接続を開始し、必要に応じて認証を行います
func (c *AuthenticatedSSE) Start(ctx context.Context) error {
	// 認証を実行
	if err := c.authFlow.Authenticate(ctx); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	// トークンを取得してヘッダーに追加
	tokens, err := c.authProvider.Tokens()
	if err != nil {
		return fmt.Errorf("failed to get tokens: %w", err)
	}

	// ヘッダーを設定
	headers := make(map[string]string)
	
	// SSE接続に必要なAcceptヘッダーを追加
	headers["Accept"] = "text/event-stream"
	
	if tokens != nil && tokens.AccessToken != "" {
		// 認証ヘッダーを追加
		headers["Authorization"] = fmt.Sprintf("Bearer %s", tokens.AccessToken)
	}

	// 既存のヘッダーを保持しつつ、新しいヘッダーを追加
	baseURL := c.baseTransport.GetBaseURL().String()
	log.Printf("Creating SSE transport with URL: %s and headers: %v", baseURL, headers)
	newTransport, err := transport.NewSSE(baseURL, transport.WithHeaders(headers))
	if err != nil {
		return fmt.Errorf("failed to create new SSE transport with headers: %w", err)
	}

	// 基本トランスポートを更新
	c.baseTransport = newTransport

	// 基本トランスポートを開始
	return c.baseTransport.Start(ctx)
}

// SendRequest は、JSONRPCリクエストを送信し、レスポンスを待ちます
func (c *AuthenticatedSSE) SendRequest(ctx context.Context, request transport.JSONRPCRequest) (*transport.JSONRPCResponse, error) {
	resp, err := c.baseTransport.SendRequest(ctx, request)
	if err != nil {
		// 認証エラーの場合は再認証を試みる
		if isAuthError(err) {
			if err := c.reAuthenticate(ctx); err != nil {
				return nil, fmt.Errorf("re-authentication failed: %w", err)
			}
			// 再認証後に再度リクエストを送信
			return c.baseTransport.SendRequest(ctx, request)
		}
		return nil, err
	}
	return resp, nil
}

// SendNotification は、JSONRPCの通知を送信します
func (c *AuthenticatedSSE) SendNotification(ctx context.Context, notification mcp.JSONRPCNotification) error {
	err := c.baseTransport.SendNotification(ctx, notification)
	if err != nil {
		// 認証エラーの場合は再認証を試みる
		if isAuthError(err) {
			if err := c.reAuthenticate(ctx); err != nil {
				return fmt.Errorf("re-authentication failed: %w", err)
			}
			// 再認証後に再度通知を送信
			return c.baseTransport.SendNotification(ctx, notification)
		}
		return err
	}
	return nil
}

// SetNotificationHandler は、通知ハンドラーを設定します
func (c *AuthenticatedSSE) SetNotificationHandler(handler func(mcp.JSONRPCNotification)) {
	c.baseTransport.SetNotificationHandler(handler)
}

// Close は、SSE接続を閉じます
func (c *AuthenticatedSSE) Close() error {
	return c.baseTransport.Close()
}

// GetEndpoint は、SSE接続のエンドポイントURLを返します
func (c *AuthenticatedSSE) GetEndpoint() *url.URL {
	return c.baseTransport.GetEndpoint()
}

// GetBaseURL は、SSEコンストラクタで設定されたベースURLを返します
func (c *AuthenticatedSSE) GetBaseURL() *url.URL {
	return c.baseTransport.GetBaseURL()
}

// GetBaseTransport は、基本となるトランスポートを返します
func (c *AuthenticatedSSE) GetBaseTransport() *transport.SSE {
	return c.baseTransport
}

// reAuthenticate は、認証を再実行します
func (c *AuthenticatedSSE) reAuthenticate(ctx context.Context) error {
	// 認証を実行
	if err := c.authFlow.Authenticate(ctx); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	// トークンを取得してヘッダーに追加
	tokens, err := c.authProvider.Tokens()
	if err != nil {
		return fmt.Errorf("failed to get tokens: %w", err)
	}

	if tokens != nil && tokens.AccessToken != "" {
		// 認証ヘッダーを設定
		headers := make(map[string]string)
		headers["Authorization"] = fmt.Sprintf("Bearer %s", tokens.AccessToken)

		// 既存のヘッダーを保持しつつ、認証ヘッダーを追加
		baseURL := c.baseTransport.GetBaseURL().String()
		newTransport, err := transport.NewSSE(baseURL, transport.WithHeaders(headers))
		if err != nil {
			return fmt.Errorf("failed to create new SSE transport with auth headers: %w", err)
		}

		// 基本トランスポートを更新
		c.baseTransport = newTransport
		
		// 新しいトランスポートを開始
		return c.baseTransport.Start(ctx)
	}

	return nil
}

// isAuthError は、エラーが認証エラーかどうかを判断します
func isAuthError(err error) bool {
	// エラーメッセージに基づいて認証エラーを検出
	if err == nil {
		return false
	}
	errStr := err.Error()
	return http.StatusText(http.StatusUnauthorized) == errStr ||
		errStr == "authentication-needed" ||
		errStr == "invalid_token" ||
		errStr == "expired_token"
}
