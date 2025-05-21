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
	"os"
	"strings"
	"sync"
	"time"

	"github.com/naotama2002/mcp-remote-go/auth"
)

// Proxy handles the bidirectional communication between stdio (MCP client) and the remote server
type Proxy struct {
	serverURL     string
	callbackPort  int
	headers       map[string]string
	serverURLHash string
	authCoord     *auth.Coordinator
	ctx           context.Context
	cancel        context.CancelFunc
	client        *http.Client
	eventSource   *EventSource
	stdioReader   *bufio.Reader
	stdioWriter   *bufio.Writer
	wg            sync.WaitGroup
}

// NewProxy creates a new MCP proxy
func NewProxy(serverURL string, callbackPort int, headers map[string]string, serverURLHash string) (*Proxy, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// Create auth coordinator
	authCoord, err := auth.NewCoordinator(serverURLHash, callbackPort)
	if err != nil {
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

	// For SSE, we need to send messages via separate HTTP POST
	req, err := http.NewRequestWithContext(p.ctx, http.MethodPost, p.serverURL, strings.NewReader(message))
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
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned error status: %d - %s", resp.StatusCode, string(body))
	}

	return nil
}

// handleServerMessage processes messages received from the SSE server
func (p *Proxy) handleServerMessage(event string, data []byte) {
	if event != "message" {
		// Handle non-message events if needed
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
	p.stdioWriter.Flush()
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
