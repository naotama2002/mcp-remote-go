package transport

import (
	"context"
	"fmt"
	"net/url"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/naotama2002/mcp-remote-go/auth"
)

// AuthenticatedStreamableHTTP は、OAuth2.1認証機能を持つStreamable HTTPトランスポートです
type AuthenticatedStreamableHTTP struct {
	baseTransport *transport.StreamableHTTP
	authProvider  auth.OAuthClientProvider
	authFlow      *auth.OAuthFlow
}

// NewAuthenticatedStreamableHTTP は、新しい認証付きStreamable HTTPトランスポートを作成します
func NewAuthenticatedStreamableHTTP(baseURL string, authProvider auth.OAuthClientProvider, authCoordinator auth.AuthCoordinator, headers map[string]string) (*AuthenticatedStreamableHTTP, error) {
	// 基本となるStreamable HTTPトランスポートを作成
	baseTransport, err := transport.NewStreamableHTTP(baseURL, transport.WithHTTPHeaders(headers))
	if err != nil {
		return nil, fmt.Errorf("failed to create base Streamable HTTP transport: %w", err)
	}

	// 認証フローを作成
	authFlow, err := auth.NewOAuthFlow(baseURL, authProvider, authCoordinator, headers)
	if err != nil {
		return nil, fmt.Errorf("failed to create OAuth flow: %w", err)
	}

	return &AuthenticatedStreamableHTTP{
		baseTransport: baseTransport,
		authProvider:  authProvider,
		authFlow:      authFlow,
	}, nil
}

// Start は、Streamable HTTP接続を開始し、必要に応じて認証を行います
func (c *AuthenticatedStreamableHTTP) Start(ctx context.Context) error {
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

		// 新しいトランスポートを作成
		baseURL := c.baseTransport.GetSessionId()
		// セッションIDがない場合は元のURLを使用
		if baseURL == "" {
			parsedURL, err := url.Parse(c.authFlow.GetServerURL())
			if err != nil {
				return fmt.Errorf("invalid server URL: %w", err)
			}
			baseURL = parsedURL.String()
		}
		newTransport, err := transport.NewStreamableHTTP(baseURL, transport.WithHTTPHeaders(headers))
		if err != nil {
			return fmt.Errorf("failed to create new Streamable HTTP transport with auth headers: %w", err)
		}

		// 基本トランスポートを更新
		c.baseTransport = newTransport
	}

	// 基本トランスポートを開始
	return c.baseTransport.Start(ctx)
}

// SendRequest は、JSONRPCリクエストを送信し、レスポンスを待ちます
func (c *AuthenticatedStreamableHTTP) SendRequest(ctx context.Context, request transport.JSONRPCRequest) (*transport.JSONRPCResponse, error) {
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
func (c *AuthenticatedStreamableHTTP) SendNotification(ctx context.Context, notification mcp.JSONRPCNotification) error {
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
func (c *AuthenticatedStreamableHTTP) SetNotificationHandler(handler func(mcp.JSONRPCNotification)) {
	c.baseTransport.SetNotificationHandler(handler)
}

// Close は、Streamable HTTP接続を閉じます
func (c *AuthenticatedStreamableHTTP) Close() error {
	return c.baseTransport.Close()
}

// GetSessionId は、Streamable HTTP接続のセッションIDを返します
func (c *AuthenticatedStreamableHTTP) GetSessionId() string {
	return c.baseTransport.GetSessionId()
}

// GetBaseTransport は、基本となるトランスポートを返します
func (c *AuthenticatedStreamableHTTP) GetBaseTransport() *transport.StreamableHTTP {
	return c.baseTransport
}

// reAuthenticate は、認証を再実行します
func (c *AuthenticatedStreamableHTTP) reAuthenticate(ctx context.Context) error {
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

		// 新しいトランスポートを作成
		sessionId := c.baseTransport.GetSessionId()
		// セッションIDがない場合は元のURLを使用
		if sessionId == "" {
			parsedURL, err := url.Parse(c.authFlow.GetServerURL())
			if err != nil {
				return fmt.Errorf("invalid server URL: %w", err)
			}
			sessionId = parsedURL.String()
		}
		newTransport, err := transport.NewStreamableHTTP(sessionId, transport.WithHTTPHeaders(headers))
		if err != nil {
			return fmt.Errorf("failed to create new Streamable HTTP transport with auth headers: %w", err)
		}

		// 基本トランスポートを更新
		c.baseTransport = newTransport
		
		// 新しいトランスポートを開始
		return c.baseTransport.Start(ctx)
	}

	return nil
}
