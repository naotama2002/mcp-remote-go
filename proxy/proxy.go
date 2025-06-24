package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
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
	config          *TransportConfig
	authCoord       *auth.Coordinator
	ctx             context.Context
	cancel          context.CancelFunc
	transport       Transport
	stdioReader     *bufio.Reader
	stdioWriter     *bufio.Writer
	wg              sync.WaitGroup
	commandEndpoint string     // MCP command endpoint URL (for SSE compatibility)
	mu              sync.Mutex // ミューテックスをcommandEndpointの保護に使用
}

// NewProxy creates a new MCP proxy with the specified transport type
func NewProxy(config *TransportConfig) (*Proxy, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// Create auth coordinator
	authCoord, err := auth.NewCoordinator(config.ServerURLHash, config.CallbackPort)
	if err != nil {
		cancel() // リソースリークを防ぐためにcancelを呼び出す
		return nil, fmt.Errorf("failed to create auth coordinator: %w", err)
	}

	// Create the appropriate transport
	var transport Transport
	switch config.Type {
	case SSETransportType:
		transport = NewSSETransport(config, authCoord)
	case StreamableHTTPTransportType:
		transport = NewStreamableHTTPTransport(config, authCoord)
	default:
		cancel()
		return nil, fmt.Errorf("unsupported transport type: %s", config.Type)
	}

	proxy := &Proxy{
		config:        config,
		authCoord:     authCoord,
		ctx:           ctx,
		cancel:        cancel,
		transport:     transport,
		stdioReader:   bufio.NewReader(os.Stdin),
		stdioWriter:   bufio.NewWriter(os.Stdout),
	}

	// Set up transport callbacks
	transport.SetMessageHandler(proxy.handleServerMessage)
	transport.SetErrorHandler(proxy.handleServerError)

	return proxy, nil
}

// NewProxyLegacy creates a new MCP proxy with legacy parameters (for backward compatibility)
func NewProxyLegacy(serverURL string, callbackPort int, headers map[string]string, serverURLHash string) (*Proxy, error) {
	config := &TransportConfig{
		Type:          SSETransportType, // Default to SSE for backward compatibility
		ServerURL:     serverURL,
		Headers:       headers,
		CallbackPort:  callbackPort,
		ServerURLHash: serverURLHash,
	}
	return NewProxy(config)
}

// Start initializes the proxy and begins bidirectional communication
func (p *Proxy) Start() error {
	log.Printf("Starting MCP proxy with %s transport", p.config.Type)

	// Connect to the remote server
	log.Println("Connecting to remote server:", p.config.ServerURL)

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
	if p.transport != nil {
		p.transport.Close()
	}
	p.cancel()
	p.wg.Wait()
}

// getCommandURL converts the SSE URL to the command endpoint URL (legacy method for SSE transport)
func (p *Proxy) getCommandURL() string {
	commandURL := p.GetCommandEndpoint()
	if commandURL != "" {
		// ベースURLを抽出（スキーム+ホスト部分）
		baseURL, err := url.Parse(p.config.ServerURL)
		if err != nil {
			log.Printf("Failed to parse server URL: %v, using direct concatenation", err)
			return p.config.ServerURL + commandURL
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
	u, err := url.Parse(p.config.ServerURL)
	if err != nil {
		// パース失敗時はフォールバック（元のURLに/messageを追加）
		return p.config.ServerURL + "/message"
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

// connectToServer establishes a connection to the remote server with authentication if needed
func (p *Proxy) connectToServer() error {
	// Try to connect using the transport
	err := p.transport.Connect(p.ctx)
	if err != nil {
		// Check if auth error
		if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "Unauthorized") {
			log.Println("Authentication required")
			return p.handleAuthentication()
		}
		return fmt.Errorf("failed to connect: %w", err)
	}

	log.Printf("Connected to server successfully using %s transport", p.config.Type)
	return nil
}

// handleAuthentication handles the OAuth flow
func (p *Proxy) handleAuthentication() error {
	// Initialize OAuth flow
	authURL, err := p.authCoord.InitializeAuth(p.config.ServerURL)
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

			// Parse message with improved batch support
			parsed, err := ParseMessage([]byte(strings.TrimSpace(line)))
			if err != nil {
				log.Printf("Failed to parse message: %v", err)
			} else {
				// Log based on message type
				switch parsed.Type {
				case SingleMessage:
					if len(parsed.Methods) > 0 {
						log.Printf("[Local→Remote] %s", parsed.Methods[0])
					} else if len(parsed.IDs) > 0 {
						log.Printf("[Local→Remote] Response ID: %v", parsed.IDs[0])
					}
				case BatchMessage:
					if len(parsed.Methods) > 0 {
						log.Printf("[Local→Remote] Batch: %s", parsed.GetMethodsString())
					} else if len(parsed.IDs) > 0 {
						log.Printf("[Local→Remote] Batch Response IDs: %s", parsed.GetIDsString())
					}
				}
			}

			// Send message to server
			if err := p.transport.SendMessage([]byte(line)); err != nil {
				log.Printf("Error sending to server: %v", err)
			}
		}
	}
}

// sendToServer sends a message to the remote server (legacy method, now delegated to transport)
func (p *Proxy) sendToServer(message string) error {
	return p.transport.SendMessage([]byte(message))
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

// handleServerMessage processes messages received from the server (callback from transport)
func (p *Proxy) handleServerMessage(data []byte) {
	// Parse message with improved batch support
	parsed, err := ParseMessage(data)
	if err != nil {
		log.Printf("Failed to parse server message: %v", err)
	} else {
		// Log based on message type
		switch parsed.Type {
		case SingleMessage:
			if len(parsed.Methods) > 0 {
				log.Printf("[Remote→Local] %s", parsed.Methods[0])
			} else if len(parsed.IDs) > 0 {
				log.Printf("[Remote→Local] Response ID: %v", parsed.IDs[0])
			}
		case BatchMessage:
			if len(parsed.Methods) > 0 {
				log.Printf("[Remote→Local] Batch: %s", parsed.GetMethodsString())
			} else if len(parsed.IDs) > 0 {
				log.Printf("[Remote→Local] Batch Response IDs: %s", parsed.GetIDsString())
			}
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

// handleServerMessageLegacy processes messages received from the SSE server (legacy method for SSE transport)
func (p *Proxy) handleServerMessageLegacy(event string, data []byte) {
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

	// Forward to the new handler
	p.handleServerMessage(data)
}

// handleServerError handles errors from the transport connection (callback from transport)
func (p *Proxy) handleServerError(err error) {
	log.Printf("Transport error: %v", err)

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
