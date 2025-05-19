package transport

import (
	"context"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// AuthenticatedTransport は、認証機能を持つトランスポートのインターフェースです
type AuthenticatedTransport interface {
	// Start は、トランスポートを開始します
	Start(ctx context.Context) error

	// SendRequest は、JSONRPCリクエストを送信し、レスポンスを待ちます
	SendRequest(ctx context.Context, request transport.JSONRPCRequest) (*transport.JSONRPCResponse, error)

	// SendNotification は、JSONRPCの通知を送信します
	SendNotification(ctx context.Context, notification mcp.JSONRPCNotification) error

	// SetNotificationHandler は、通知ハンドラーを設定します
	SetNotificationHandler(handler func(mcp.JSONRPCNotification))

	// Close は、トランスポート接続を閉じます
	Close() error
}
