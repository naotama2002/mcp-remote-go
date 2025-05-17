package transport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/naotama2002/mcp-remote-go/internal/auth"
)

// HTTPClientTransport is a client transport that uses HTTP
type HTTPClientTransport struct {
	BaseTransport
	url          *url.URL
	authProvider auth.OAuthProvider
	client       *http.Client
	headers      map[string]string
	mu           sync.Mutex
	pollInterval time.Duration
	stopPolling  chan struct{}
}

// HTTPClientOptions represents HTTP client options
type HTTPClientOptions struct {
	AuthProvider auth.OAuthProvider
	Headers      map[string]string
	PollInterval time.Duration
}

// NewHTTPClientTransport creates a new HTTP client transport
func NewHTTPClientTransport(serverURL *url.URL, options HTTPClientOptions) *HTTPClientTransport {
	pollInterval := options.PollInterval
	if pollInterval == 0 {
		pollInterval = 1 * time.Second
	}

	return &HTTPClientTransport{
		url:          serverURL,
		authProvider: options.AuthProvider,
		client:       &http.Client{},
		headers:      options.Headers,
		pollInterval: pollInterval,
		stopPolling:  make(chan struct{}),
	}
}

// Start initiates the transport
func (t *HTTPClientTransport) Start() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.IsClosed() {
		return ErrTransportClosed
	}

	// Start polling
	go t.startPolling()

	return nil
}

// startPolling polls messages from the server
func (t *HTTPClientTransport) startPolling() {
	ticker := time.NewTicker(t.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Get messages from server
			messages, err := t.pollMessages()
			if err != nil {
				if !t.IsClosed() {
					t.HandleError(err)
				}
				continue
			}

			// Process each message
			for _, message := range messages {
				if t.IsClosed() {
					return
				}
				t.HandleMessage(message)
			}

		case <-t.stopPolling:
			return
		}
	}
}

// pollMessages polls messages from the server
func (t *HTTPClientTransport) pollMessages() ([]map[string]interface{}, error) {
	// Create request
	req, err := http.NewRequest("GET", t.url.String()+"/poll", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	// Add Authorization header if auth token exists
	if t.authProvider != nil {
		tokens, err := t.authProvider.Tokens()
		if err != nil {
		return nil, fmt.Errorf("failed to get token: %w", err)
		}

		if tokens != nil && tokens.AccessToken != "" {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", tokens.AccessToken))
		} else {
			return nil, ErrAuthRequired
		}
	}

	// Send request
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Check response status code
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("received error response from server: %d", resp.StatusCode)
	}

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Return empty array if message is empty
	if len(body) == 0 {
		return []map[string]interface{}{}, nil
	}

	// Decode JSON message
	var messages []map[string]interface{}
	if err := json.Unmarshal(body, &messages); err != nil {
		return nil, fmt.Errorf("failed to decode message: %w", err)
	}

	return messages, nil
}

// Close terminates the transport
func (t *HTTPClientTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.IsClosed() {
		return nil
	}

	t.MarkClosed()
	close(t.stopPolling)
	t.HandleClose()
	return nil
}

// Send transmits a message
func (t *HTTPClientTransport) Send(message interface{}) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.IsClosed() {
		return ErrTransportClosed
	}

	// Encode message to JSON
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to encode message: %w", err)
	}

	// Create request
	req, err := http.NewRequest("POST", t.url.String(), bytes.NewBuffer(data))
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
	resp, err := t.client.Do(req)
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
