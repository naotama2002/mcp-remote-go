package proxy

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	"github.com/naotama2002/mcp-remote-go/auth"
)

// SSETransport implements the Transport interface for Server-Sent Events
type SSETransport struct {
	config      *TransportConfig
	authCoord   *auth.Coordinator
	client      *http.Client
	eventSource *EventSource
	
	messageHandler func(data []byte)
	errorHandler   func(err error)
}

// NewSSETransport creates a new SSE transport
func NewSSETransport(config *TransportConfig, authCoord *auth.Coordinator) *SSETransport {
	return &SSETransport{
		config:    config,
		authCoord: authCoord,
		client:    &http.Client{},
	}
}

// Connect establishes the SSE connection
func (t *SSETransport) Connect(ctx context.Context) error {
	// Create request with auth headers if available
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.config.ServerURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Add custom headers
	for k, v := range t.config.Headers {
		req.Header.Set(k, v)
	}

	// Check for existing auth tokens
	tokens, err := t.authCoord.LoadTokens()
	if err == nil && tokens.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	}

	// Accept header for SSE
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Connection", "keep-alive")

	// Create event source
	t.eventSource = NewEventSource(req, t.client)
	t.eventSource.OnMessage = t.handleMessage
	t.eventSource.OnError = t.handleError

	// Start the event source
	return t.eventSource.Connect()
}

// SendMessage sends a message to the server via HTTP POST
func (t *SSETransport) SendMessage(message []byte) error {
	if t.eventSource == nil {
		return fmt.Errorf("not connected")
	}

	// Use a simple command URL for SSE transport
	commandURL := t.config.ServerURL + "/message"

	// Create POST request
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, commandURL, 
		bytes.NewReader(message))
	if err != nil {
		return fmt.Errorf("failed to create POST request: %w", err)
	}

	// Add headers
	for k, v := range t.config.Headers {
		req.Header.Set(k, v)
	}

	// Add auth header if we have a token
	tokens, err := t.authCoord.LoadTokens()
	if err == nil && tokens.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	}

	req.Header.Set("Content-Type", "application/json")

	// Send the request
	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("POST request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("server returned error status: %d", resp.StatusCode)
	}

	return nil
}

// Close closes the SSE connection
func (t *SSETransport) Close() error {
	if t.eventSource != nil {
		t.eventSource.Close()
	}
	return nil
}

// SetMessageHandler sets the message handler
func (t *SSETransport) SetMessageHandler(handler func(data []byte)) {
	t.messageHandler = handler
}

// SetErrorHandler sets the error handler
func (t *SSETransport) SetErrorHandler(handler func(err error)) {
	t.errorHandler = handler
}

// IsConnected returns true if connected
func (t *SSETransport) IsConnected() bool {
	return t.eventSource != nil && t.eventSource.connected
}

// handleMessage processes messages from the event source
func (t *SSETransport) handleMessage(event string, data []byte) {
	// Handle special event types
	if event == "endpoint" {
		// TODO: Handle endpoint events if needed for SSE transport
		// For now, we'll pass it through to maintain compatibility
		return
	}

	if t.messageHandler != nil {
		if event == "message" || event == "" {
			t.messageHandler(data)
		}
	}
}

// handleError processes errors from the event source
func (t *SSETransport) handleError(err error) {
	if t.errorHandler != nil {
		t.errorHandler(err)
	}
}

