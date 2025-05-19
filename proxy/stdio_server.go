package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// StdioServerTransport はSTDIOベースのMCPサーバートランスポートを実装します
type StdioServerTransport struct {
	stdin          *bufio.Reader
	stdout         io.Writer
	onNotification func(notification mcp.JSONRPCNotification)
	notifyMu       sync.RWMutex
	done           chan struct{}
	responses      map[string]chan *transport.JSONRPCResponse
	mu             sync.RWMutex
}

// NewStdioServerTransport は新しいSTDIOサーバートランスポートを作成します
func NewStdioServerTransport() *StdioServerTransport {
	return &StdioServerTransport{
		stdin:     bufio.NewReader(os.Stdin),
		stdout:    os.Stdout,
		done:      make(chan struct{}),
		responses: make(map[string]chan *transport.JSONRPCResponse),
	}
}

// Start はSTDIOサーバートランスポートを開始します
func (s *StdioServerTransport) Start(ctx context.Context) error {
	go s.readRequests()
	return nil
}

// Close はSTDIOサーバートランスポートを閉じます
func (s *StdioServerTransport) Close() error {
	select {
	case <-s.done:
		return nil
	default:
		close(s.done)
	}
	return nil
}

// SetNotificationHandler は通知ハンドラを設定します
func (s *StdioServerTransport) SetNotificationHandler(handler func(notification mcp.JSONRPCNotification)) {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	s.onNotification = handler
}

// SendRequest はリクエストを送信します
func (s *StdioServerTransport) SendRequest(ctx context.Context, request transport.JSONRPCRequest) (*transport.JSONRPCResponse, error) {
	// リクエストをJSONに変換
	data, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// レスポンスを受け取るためのチャネルを作成
	idKey := request.ID.String()
	responseCh := make(chan *transport.JSONRPCResponse, 1)

	// レスポンスチャネルをマップに登録
	s.mu.Lock()
	s.responses[idKey] = responseCh
	s.mu.Unlock()

	// リクエスト送信後にマップからチャネルを削除するための遅延処理
	defer func() {
		s.mu.Lock()
		delete(s.responses, idKey)
		s.mu.Unlock()
	}()

	// リクエストを送信
	if _, err := fmt.Fprintln(s.stdout, string(data)); err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	// レスポンスを待機
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, fmt.Errorf("transport closed")
	case response := <-responseCh:
		return response, nil
	}
}

// SendNotification は通知を送信します
func (s *StdioServerTransport) SendNotification(ctx context.Context, notification mcp.JSONRPCNotification) error {
	// 通知をJSONに変換
	data, err := json.Marshal(notification)
	if err != nil {
		return fmt.Errorf("failed to marshal notification: %w", err)
	}

	// 通知を送信
	if _, err := fmt.Fprintln(s.stdout, string(data)); err != nil {
		return fmt.Errorf("failed to send notification: %w", err)
	}

	return nil
}

// readRequests はSTDINからリクエストを読み取ります
func (s *StdioServerTransport) readRequests() {
	for {
		select {
		case <-s.done:
			return
		default:
			line, err := s.stdin.ReadString('\n')
			if err != nil {
				if err != io.EOF {
					log.Printf("Error reading from stdin: %v", err)
				}
				return
			}

			// 空行はスキップ
			if line == "" || line == "\n" {
				continue
			}

			// JSONメッセージの基本構造を解析
			var baseMessage struct {
				JSONRPC string      `json:"jsonrpc"`
				ID      *mcp.RequestId `json:"id"`
				Method  string      `json:"method,omitempty"`
			}

			if err := json.Unmarshal([]byte(line), &baseMessage); err != nil {
				log.Printf("Error parsing message: %v", err)
				continue
			}

			// IDがない場合は通知として処理
			if baseMessage.ID == nil || baseMessage.ID.IsNil() {
				var notification mcp.JSONRPCNotification
				if err := json.Unmarshal([]byte(line), &notification); err != nil {
					log.Printf("Error parsing notification: %v", err)
					continue
				}

				s.notifyMu.RLock()
				if s.onNotification != nil {
					s.onNotification(notification)
				}
				s.notifyMu.RUnlock()
				continue
			}

			// リクエストとしてレスポンスを処理
			var response transport.JSONRPCResponse
			if err := json.Unmarshal([]byte(line), &response); err != nil {
				log.Printf("Error parsing response: %v", err)
				continue
			}

			// レスポンスチャネルを取得
			idKey := response.ID.String()
			s.mu.RLock()
			ch, exists := s.responses[idKey]
			s.mu.RUnlock()

			if exists {
				ch <- &response
			} else {
				log.Printf("No handler for response with ID %s", idKey)
			}
		}
	}
}
