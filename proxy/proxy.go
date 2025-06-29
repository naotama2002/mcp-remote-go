package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/naotama2002/mcp-remote-go/auth"
	"github.com/pkg/browser"
)

// Proxy handles the bidirectional communication between stdio (MCP client) and the remote server
type Proxy struct {
	serverURL       string
	callbackPort    int
	headers         map[string]string
	serverURLHash   string
	authCoord       *auth.Coordinator
	ctx             context.Context
	cancel          context.CancelFunc
	client          *http.Client
	eventSource     *EventSource
	stdioReader     *bufio.Reader
	stdioWriter     *bufio.Writer
	wg              sync.WaitGroup
	commandEndpoint string     // MCP command endpoint URL
	mu              sync.Mutex // ミューテックスをcommandEndpointの保護に使用
}

// NewProxy creates a new MCP proxy
func NewProxy(serverURL string, callbackPort int, headers map[string]string, serverURLHash string) (*Proxy, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// Create auth coordinator
	authCoord, err := auth.NewCoordinator(serverURLHash, callbackPort)
	if err != nil {
		cancel() // リソースリークを防ぐためにcancelを呼び出す
		return nil, fmt.Errorf("failed to create auth coordinator: %w", err)
	}

	return &Proxy{
		serverURL:     serverURL,
		callbackPort:  callbackPort,
		headers:       headers,
		serverURLHash: serverURLHash,
		authCoord:     authCoord,
		ctx:           ctx,
		cancel:        cancel,
		client:        &http.Client{},
		stdioReader:   bufio.NewReader(os.Stdin),
		stdioWriter:   bufio.NewWriter(os.Stdout),
	}, nil
}

// Start initializes the proxy and begins bidirectional communication
func (p *Proxy) Start() error {
	log.Println("Starting MCP proxy")

	// Connect to the SSE server
	log.Println("Connecting to remote server:", p.serverURL)

	// Try to connect, handle auth if needed
	if err := p.connectToServer(); err != nil {
		return fmt.Errorf("failed to connect to server: %w", err)
	}

	// Start processing messages from stdio
	p.wg.Add(1)
	go p.processStdioInput()

	// Wait for completion
	p.wg.Wait()
	return nil
}

// Shutdown gracefully stops the proxy
func (p *Proxy) Shutdown() {
	log.Println("Shutting down proxy")
	if p.eventSource != nil {
		p.eventSource.Close()
	}
	p.cancel()
	p.wg.Wait()
}

// getCommandURL converts the SSE URL to the command endpoint URL
func (p *Proxy) getCommandURL() string {
	commandURL := p.GetCommandEndpoint()
	if commandURL != "" {
		// ベースURLを抽出（スキーム+ホスト部分）
		baseURL, err := url.Parse(p.serverURL)
		if err != nil {
			log.Printf("Failed to parse server URL: %v, using direct concatenation", err)
			return p.serverURL + commandURL
		}

		// スキームとホストのみを取得（パスは除外）
		baseURL.Path = ""

		// ベースURLにcommandURLを結合
		resultURL, err := url.Parse(baseURL.String())
		if err != nil {
			return resultURL.String() + commandURL // フォールバック
		}

		// commandURLが絶対URLなら、そのまま返す
		if strings.HasPrefix(commandURL, "http://") || strings.HasPrefix(commandURL, "https://") {
			return commandURL
		}

		// 相対URLなら、ベースURLと結合
		relativeURL, err := url.Parse(commandURL)
		if err != nil {
			return baseURL.String() + commandURL // フォールバック
		}

		return baseURL.ResolveReference(relativeURL).String()
	}

	// サーバーURLからベースURL（スキーム+ホスト）を抽出し、コマンドエンドポイントを作成
	u, err := url.Parse(p.serverURL)
	if err != nil {
		// パース失敗時はフォールバック（元のURLに/messageを追加）
		return p.serverURL + "/message"
	}

	// ベースURLに/messageパスを設定（既存のパスは上書き）
	u.Path = "/message"

	return u.String()
}

// openBrowser opens the specified URL in the default browser
func openBrowser(rawURL string) error {
	// URLの検証
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	// スキームの制限（HTTPとHTTPSのみ許可）
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return errors.New("only http and https URLs are allowed")
	}

	// github.com/pkg/browser を使用してブラウザを開く
	return browser.OpenURL(rawURL)
}

// connectToServer establishes a connection to the SSE server with authentication if needed
func (p *Proxy) connectToServer() error {
	// Create request with auth headers if available
	req, err := http.NewRequestWithContext(p.ctx, http.MethodGet, p.serverURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Add custom headers
	for k, v := range p.headers {
		req.Header.Set(k, v)
	}

	// Check for existing auth tokens
	tokens, err := p.authCoord.LoadTokens()
	if err == nil && tokens.AccessToken != "" {
		log.Println("Using existing auth token")
		req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	}

	// Accept header for SSE
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Connection", "keep-alive")

	// Create event source
	p.eventSource = NewEventSource(req, p.client)
	p.eventSource.OnMessage = p.handleServerMessage
	p.eventSource.OnError = p.handleServerError

	// Start the event source
	err = p.eventSource.Connect()
	if err != nil {
		// Check if auth error
		if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "Unauthorized") {
			log.Println("Authentication required")
			return p.handleAuthentication()
		}
		return fmt.Errorf("failed to connect to SSE: %w", err)
	}

	log.Println("Connected to SSE server successfully")
	return nil
}

// handleAuthentication handles the OAuth flow
func (p *Proxy) handleAuthentication() error {
	// Initialize OAuth flow
	authURL, err := p.authCoord.InitializeAuth(p.serverURL)
	if err != nil {
		return fmt.Errorf("failed to initialize auth: %w", err)
	}

	log.Println("Please authorize access in your browser at:", authURL)

	// 自動でブラウザを起動
	if err := openBrowser(authURL); err != nil {
		log.Printf("Failed to open browser automatically: %v", err)
		log.Println("Please open the URL manually in your browser.")
	} else {
		log.Println("Opening browser...")
	}

	// Wait for auth code
	code, err := p.authCoord.WaitForAuthCode()
	if err != nil {
		return fmt.Errorf("auth code retrieval failed: %w", err)
	}

	log.Println("Auth code received, exchanging for tokens...")

	// Get tokens
	tokens, err := p.authCoord.ExchangeCode(code)
	if err != nil {
		return fmt.Errorf("token exchange failed: %w", err)
	}

	// Save tokens
	if err := p.authCoord.SaveTokens(tokens); err != nil {
		return fmt.Errorf("failed to save tokens: %w", err)
	}

	// Try connecting again with the new token
	return p.connectToServer()
}

// processStdioInput reads messages from stdin and forwards them to the server
func (p *Proxy) processStdioInput() {
	defer p.wg.Done()

	for {
		select {
		case <-p.ctx.Done():
			return
		default:
			// Read one line from stdin
			line, err := p.stdioReader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					log.Println("STDIO input closed")
					p.Shutdown()
					return
				}
				log.Printf("Error reading from STDIO: %v", err)
				continue
			}

			// Parse message to log method (but don't modify it)
			var msg map[string]interface{}
			if err := json.Unmarshal([]byte(line), &msg); err == nil {
				if method, ok := msg["method"].(string); ok {
					log.Printf("[Local→Remote] %s", method)
				} else if id, ok := msg["id"].(float64); ok {
					log.Printf("[Local→Remote] Response ID: %v", id)
				}
			}

			// Send message to server
			if err := p.sendToServer(line); err != nil {
				log.Printf("Error sending to server: %v", err)
			}
		}
	}
}

// sendToServer sends a message to the remote server
func (p *Proxy) sendToServer(message string) error {
	if p.eventSource == nil {
		return errors.New("not connected to server")
	}

	commandURL := p.getCommandURL()

	// Send message to command endpoint via HTTP POST
	req, err := http.NewRequestWithContext(p.ctx, http.MethodPost, commandURL, strings.NewReader(message))
	if err != nil {
		return fmt.Errorf("failed to create POST request: %w", err)
	}

	// Add headers
	for k, v := range p.headers {
		req.Header.Set(k, v)
	}

	// Add auth header if we have a token
	tokens, err := p.authCoord.LoadTokens()
	if err == nil && tokens.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	}

	req.Header.Set("Content-Type", "application/json")

	// Send the request
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("POST request failed: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("Warning: failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned error status: %d - %s", resp.StatusCode, string(body))
	}

	return nil
}

// SetCommandEndpoint sets the command endpoint URL
func (p *Proxy) SetCommandEndpoint(endpoint string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.commandEndpoint = endpoint
}

// GetCommandEndpoint returns the command endpoint URL
func (p *Proxy) GetCommandEndpoint() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.commandEndpoint
}

// handleServerMessage processes messages received from the SSE server
func (p *Proxy) handleServerMessage(event string, data []byte) {
	// Handle special event types
	if event == "endpoint" {
		// MCPの仕様に基づき、サーバーが提供するcommandエンドポイントを保存
		endpoint := string(data)
		log.Printf("Received command endpoint: %s", endpoint)
		p.SetCommandEndpoint(endpoint)
		return
	}

	if event != "message" {
		// Handle other non-message events if needed
		return
	}

	// Parse message to log method (but don't modify it)
	var msg map[string]interface{}
	if err := json.Unmarshal(data, &msg); err == nil {
		if method, ok := msg["method"].(string); ok {
			log.Printf("[Remote→Local] %s", method)
		} else if id, ok := msg["id"].(float64); ok {
			log.Printf("[Remote→Local] Response ID: %v", id)
		}
	}

	// Forward message to stdout
	data = append(data, '\n')
	if _, err := p.stdioWriter.Write(data); err != nil {
		log.Printf("Error writing to STDIO: %v", err)
		return
	}
	if err := p.stdioWriter.Flush(); err != nil {
		log.Printf("Error flushing STDIO: %v", err)
	}
}

// handleServerError handles errors from the SSE connection
func (p *Proxy) handleServerError(err error) {
	log.Printf("SSE error: %v", err)

	// If context is done, this is a normal shutdown
	if errors.Is(err, context.Canceled) {
		return
	}

	// If it's an auth error, try to re-authenticate
	if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "Unauthorized") {
		log.Println("Authentication error, trying to re-authenticate...")
		if err := p.handleAuthentication(); err != nil {
			log.Printf("Re-authentication failed: %v", err)
			p.Shutdown()
		}
		return
	}

	// For other errors, attempt to reconnect after a delay
	time.Sleep(5 * time.Second)
	log.Println("Attempting to reconnect...")
	if err := p.connectToServer(); err != nil {
		log.Printf("Reconnection failed: %v", err)
		p.Shutdown()
	}
}
