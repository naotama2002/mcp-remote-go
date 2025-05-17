package transport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"

	"github.com/naotama2002/mcp-remote-go/internal/auth"
	"github.com/r3labs/sse/v2"
)

// SSEClientTransport is a client transport that uses Server-Sent Events (SSE)
type SSEClientTransport struct {
	BaseTransport
	url          *url.URL
	authProvider auth.OAuthProvider
	client       *sse.Client
	headers      map[string]string
	mu           sync.Mutex
}

// SSEClientOptions represents options for the SSE client
type SSEClientOptions struct {
	AuthProvider auth.OAuthProvider
	Headers      map[string]string
}

// NewSSEClientTransport creates a new SSE client transport
func NewSSEClientTransport(serverURL *url.URL, options SSEClientOptions) *SSEClientTransport {
	return &SSEClientTransport{
		url:          serverURL,
		authProvider: options.AuthProvider,
		headers:      options.Headers,
	}
}

// Start initiates the transport
func (t *SSEClientTransport) Start() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.IsClosed() {
		return ErrTransportClosed
	}

	// Create SSE client
	client := sse.NewClient(t.url.String())

	// Set headers
	headers := make(map[string]string)
	for k, v := range t.headers {
		headers[k] = v
	}

	// Add Authorization header if auth token exists
	if t.authProvider != nil {
		tokens, err := t.authProvider.Tokens()
		if err != nil {
			return fmt.Errorf("failed to get token: %w", err)
		}

		if tokens != nil && tokens.AccessToken != "" {
			headers["Authorization"] = fmt.Sprintf("Bearer %s", tokens.AccessToken)
		} else {
			return ErrAuthRequired
		}
	}

	// Set headers
	client.Headers = headers

	// Set up event handlers
	client.OnConnect(func(c *sse.Client) {
		log.Println("SSE connection established")
	})

	// Set message handler
	events := make(chan *sse.Event)
	err := client.SubscribeChan("messages", events)
	if err != nil {
		return fmt.Errorf("failed to subscribe to SSE channel: %w", err)
	}

	// Process message
	go func() {
		for event := range events {
			if t.IsClosed() {
				return
			}

			// Decode JSON message
			var message map[string]interface{}
			if err := json.Unmarshal(event.Data, &message); err != nil {
				t.HandleError(fmt.Errorf("failed to decode message: %w", err))
				continue
			}

			// Process message
			t.HandleMessage(message)
		}
	}()

	t.client = client
	return nil
}

// Close terminates the transport
func (t *SSEClientTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.IsClosed() {
		return nil
	}

	t.MarkClosed()

	// Disconnect SSE client
	if t.client != nil {
		// Currently, the r3labs/sse/v2 library does not have a Close method,
		// so there is no way to disconnect
		// In the future, proper cleanup should be performed here
	}

	t.HandleClose()
	return nil
}

// Send transmits a message
func (t *SSEClientTransport) Send(message interface{}) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.IsClosed() {
		return ErrTransportClosed
	}

	// Encode message to JSON
	jsonData, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to encode message: %w", err)
	}
	
	// Create HTTP client
	client := &http.Client{}

	// Create request
	req, err := http.NewRequest("POST", t.url.String(), bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	// Add Authorization header if auth token exists
	if t.authProvider != nil {
		tokens, err := t.authProvider.Tokens()
		if err != nil {
			return fmt.Errorf("failed to get token: %w", err)
		}

		if tokens != nil && tokens.AccessToken != "" {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", tokens.AccessToken))
		} else {
			return ErrAuthRequired
		}
	}

	// Send request
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Check response status code
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("received error response from server: %d", resp.StatusCode)
	}

	return nil
}
