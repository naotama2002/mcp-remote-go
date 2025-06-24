package proxy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/naotama2002/mcp-remote-go/auth"
)

// StreamableHTTPTransport implements the Transport interface for Streamable HTTP
type StreamableHTTPTransport struct {
	config         *TransportConfig
	authCoord      *auth.Coordinator
	client         *http.Client
	sessionID      string
	ctx            context.Context
	cancel         context.CancelFunc
	mu             sync.RWMutex
	connected      bool
	
	messageHandler func(data []byte)
	errorHandler   func(err error)
	
	// SSE connection for receiving messages
	sseResponse *http.Response
	sseReader   *bufio.Reader
}

// NewStreamableHTTPTransport creates a new Streamable HTTP transport
func NewStreamableHTTPTransport(config *TransportConfig, authCoord *auth.Coordinator) *StreamableHTTPTransport {
	ctx, cancel := context.WithCancel(context.Background())
	
	return &StreamableHTTPTransport{
		config:    config,
		authCoord: authCoord,
		client:    &http.Client{},
		sessionID: config.SessionID,
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Connect establishes the connection
func (t *StreamableHTTPTransport) Connect(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	
	if t.connected {
		return nil
	}
	
	// First, establish SSE connection for receiving messages
	if err := t.connectSSE(ctx); err != nil {
		return fmt.Errorf("failed to establish SSE connection: %w", err)
	}
	
	t.connected = true
	
	// Start reading SSE messages
	go t.readSSEMessages()
	
	return nil
}

// connectSSE establishes the SSE connection for receiving messages
func (t *StreamableHTTPTransport) connectSSE(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.config.ServerURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create SSE request: %w", err)
	}
	
	// Set required headers
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Connection", "keep-alive")
	
	// Add session ID if available
	if t.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	
	// Add custom headers
	for k, v := range t.config.Headers {
		req.Header.Set(k, v)
	}
	
	// Add auth header if available
	tokens, err := t.authCoord.LoadTokens()
	if err == nil && tokens.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	}
	
	// Validate Origin header (security requirement)
	if origin := req.Header.Get("Origin"); origin != "" {
		if err := t.validateOrigin(origin); err != nil {
			return fmt.Errorf("invalid origin: %w", err)
		}
	}
	
	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("SSE connection failed: %w", err)
	}
	
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return fmt.Errorf("server returned error status: %d", resp.StatusCode)
	}
	
	// Verify content type
	contentType := resp.Header.Get("Content-Type")
	if contentType != "text/event-stream" {
		resp.Body.Close()
		return fmt.Errorf("expected content-type text/event-stream, got %s", contentType)
	}
	
	// Extract session ID if provided
	if sessionID := resp.Header.Get("Mcp-Session-Id"); sessionID != "" {
		t.sessionID = sessionID
		log.Printf("Received session ID: %s", sessionID)
	}
	
	t.sseResponse = resp
	t.sseReader = bufio.NewReader(resp.Body)
	
	return nil
}

// SendMessage sends a message via HTTP POST
func (t *StreamableHTTPTransport) SendMessage(message []byte) error {
	t.mu.RLock()
	if !t.connected {
		t.mu.RUnlock()
		return fmt.Errorf("not connected")
	}
	t.mu.RUnlock()
	
	req, err := http.NewRequestWithContext(t.ctx, http.MethodPost, t.config.ServerURL, bytes.NewReader(message))
	if err != nil {
		return fmt.Errorf("failed to create POST request: %w", err)
	}
	
	// Set required headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept", "text/event-stream") // Support both response types
	
	// Add session ID if available
	if t.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	
	// Add custom headers
	for k, v := range t.config.Headers {
		req.Header.Set(k, v)
	}
	
	// Add auth header if available
	tokens, err := t.authCoord.LoadTokens()
	if err == nil && tokens.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	}
	
	// Validate Origin header (security requirement)
	if origin := req.Header.Get("Origin"); origin != "" {
		if err := t.validateOrigin(origin); err != nil {
			return fmt.Errorf("invalid origin: %w", err)
		}
	}
	
	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("POST request failed: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned error status: %d - %s", resp.StatusCode, string(body))
	}
	
	// Check if response contains immediate JSON response
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/json") {
		// Handle immediate JSON response
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read response body: %w", err)
		}
		
		if len(body) > 0 && t.messageHandler != nil {
			t.messageHandler(body)
		}
	}
	
	return nil
}

// Close closes the connection
func (t *StreamableHTTPTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	
	if !t.connected {
		return nil
	}
	
	t.connected = false
	t.cancel()
	
	if t.sseResponse != nil && t.sseResponse.Body != nil {
		t.sseResponse.Body.Close()
	}
	
	return nil
}

// SetMessageHandler sets the message handler
func (t *StreamableHTTPTransport) SetMessageHandler(handler func(data []byte)) {
	t.messageHandler = handler
}

// SetErrorHandler sets the error handler
func (t *StreamableHTTPTransport) SetErrorHandler(handler func(err error)) {
	t.errorHandler = handler
}

// IsConnected returns true if connected
func (t *StreamableHTTPTransport) IsConnected() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.connected
}

// readSSEMessages continuously reads SSE messages
func (t *StreamableHTTPTransport) readSSEMessages() {
	defer func() {
		t.mu.Lock()
		t.connected = false
		if t.sseResponse != nil && t.sseResponse.Body != nil {
			t.sseResponse.Body.Close()
		}
		t.mu.Unlock()
	}()
	
	var data bytes.Buffer
	
	for {
		select {
		case <-t.ctx.Done():
			return
		default:
		}
		
		// Read a line
		line, err := t.sseReader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				return
			}
			
			if t.errorHandler != nil {
				t.errorHandler(err)
			}
			return
		}
		
		// Trim the line
		line = bytes.TrimSpace(line)
		
		// Empty line marks the end of an event
		if len(line) == 0 {
			if data.Len() > 0 && t.messageHandler != nil {
				// Forward the message data
				t.messageHandler(data.Bytes())
				
				// Reset for next event
				data.Reset()
			}
			continue
		}
		
		// Parse the line
		if bytes.HasPrefix(line, []byte("event:")) {
			// Handle event type if needed
		} else if bytes.HasPrefix(line, []byte("data:")) {
			dataLine := bytes.TrimSpace(line[5:])
			data.Write(dataLine)
		} else if bytes.HasPrefix(line, []byte("id:")) {
			// Handle event ID if needed
		}
	}
}

// validateOrigin validates the Origin header to prevent DNS rebinding attacks
func (t *StreamableHTTPTransport) validateOrigin(origin string) error {
	// Parse the origin URL
	originURL, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("invalid origin URL: %w", err)
	}
	
	// Parse the server URL
	serverURL, err := url.Parse(t.config.ServerURL)
	if err != nil {
		return fmt.Errorf("invalid server URL: %w", err)
	}
	
	// For localhost, allow any localhost origin
	if serverURL.Hostname() == "localhost" || serverURL.Hostname() == "127.0.0.1" {
		if originURL.Hostname() == "localhost" || originURL.Hostname() == "127.0.0.1" {
			return nil
		}
	}
	
	// For other hosts, origin must match the server host
	if originURL.Hostname() != serverURL.Hostname() {
		return fmt.Errorf("origin %s does not match server host %s", originURL.Hostname(), serverURL.Hostname())
	}
	
	return nil
}

// GetSessionID returns the current session ID
func (t *StreamableHTTPTransport) GetSessionID() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.sessionID
}