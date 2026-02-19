package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// SSETransport implements the legacy SSE transport (MCP 2024-11-05).
// It uses two endpoints: GET for SSE stream, POST for sending messages.
type SSETransport struct {
	serverURL    string
	client       *http.Client
	headers      map[string]string
	getAuthToken func() string

	eventSource     *EventSource
	commandEndpoint string
	mu              sync.Mutex

	onMessage func(event string, data []byte)
	onError   func(err error)
}

// SSETransportConfig holds configuration for creating an SSETransport.
type SSETransportConfig struct {
	ServerURL    string
	Client       *http.Client
	Headers      map[string]string
	GetAuthToken func() string
}

// NewSSETransport creates a new legacy SSE transport.
func NewSSETransport(cfg SSETransportConfig) *SSETransport {
	return &SSETransport{
		serverURL:    cfg.ServerURL,
		client:       cfg.Client,
		headers:      cfg.Headers,
		getAuthToken: cfg.GetAuthToken,
	}
}

func (t *SSETransport) Connect(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.serverURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	if t.getAuthToken != nil {
		if token := t.getAuthToken(); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Connection", "keep-alive")

	t.eventSource = NewEventSource(req, t.client)
	t.eventSource.OnMessage = t.handleMessage
	t.eventSource.OnError = func(err error) {
		if t.onError != nil {
			t.onError(err)
		}
	}

	if err := t.eventSource.Connect(); err != nil {
		return err
	}

	return nil
}

func (t *SSETransport) Send(ctx context.Context, message []byte) error {
	t.mu.Lock()
	endpoint := t.commandEndpoint
	t.mu.Unlock()

	// Check if we have a command endpoint set (indicates connected state)
	if t.eventSource == nil && endpoint == "" {
		return errors.New("not connected to server")
	}

	commandURL := t.getCommandURL()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, commandURL, strings.NewReader(string(message)))
	if err != nil {
		return fmt.Errorf("failed to create POST request: %w", err)
	}

	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	if t.getAuthToken != nil {
		if token := t.getAuthToken(); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
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

func (t *SSETransport) SetOnMessage(handler func(event string, data []byte)) {
	t.onMessage = handler
}

func (t *SSETransport) SetOnError(handler func(err error)) {
	t.onError = handler
}

func (t *SSETransport) Close() error {
	if t.eventSource != nil {
		t.eventSource.Close()
	}
	return nil
}

func (t *SSETransport) SessionID() string {
	return "" // Legacy SSE has no session concept
}

// handleMessage processes messages from the EventSource and dispatches them.
func (t *SSETransport) handleMessage(event string, data []byte) {
	if event == "endpoint" {
		endpoint := string(data)
		log.Printf("Received command endpoint: %s", endpoint)
		t.setCommandEndpoint(endpoint)
		return
	}

	if t.onMessage != nil {
		t.onMessage(event, data)
	}
}

func (t *SSETransport) setCommandEndpoint(endpoint string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.commandEndpoint = endpoint
}

func (t *SSETransport) getCommandEndpointValue() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.commandEndpoint
}

// getCommandURL resolves the full command endpoint URL.
func (t *SSETransport) getCommandURL() string {
	commandURL := t.getCommandEndpointValue()
	if commandURL != "" {
		// If it's already an absolute URL, return as-is
		if strings.HasPrefix(commandURL, "http://") || strings.HasPrefix(commandURL, "https://") {
			return commandURL
		}

		// Extract base URL (scheme + host)
		baseURL, err := url.Parse(t.serverURL)
		if err != nil {
			log.Printf("Failed to parse server URL: %v, using direct concatenation", err)
			return t.serverURL + commandURL
		}
		baseURL.Path = ""

		relativeURL, err := url.Parse(commandURL)
		if err != nil {
			return baseURL.String() + commandURL
		}

		return baseURL.ResolveReference(relativeURL).String()
	}

	// Fallback: derive from server URL
	u, err := url.Parse(t.serverURL)
	if err != nil {
		return t.serverURL + "/message"
	}
	u.Path = "/message"
	return u.String()
}
