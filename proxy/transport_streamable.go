package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// errNotificationStreamNotSupported indicates the server does not support GET notification streams.
var errNotificationStreamNotSupported = errors.New("server does not support GET notification stream")

const (
	// MCPProtocolVersion is the protocol version for Streamable HTTP transport.
	MCPProtocolVersion = "2025-11-25"

	// HeaderMCPSessionID is the session ID header name.
	HeaderMCPSessionID = "Mcp-Session-Id"

	// HeaderMCPProtocolVersion is the protocol version header name.
	HeaderMCPProtocolVersion = "Mcp-Protocol-Version"
)

// StreamableHTTPTransport implements the Streamable HTTP transport (MCP 2025-11-25).
// It uses a single endpoint for both POST (sending messages) and GET (receiving notifications).
type StreamableHTTPTransport struct {
	endpoint     string
	client       *http.Client
	headers      map[string]string
	getAuthToken func() string

	sessionID   string
	lastEventID string

	onMessage func(event string, data []byte)
	onError   func(err error)

	notifyCancel context.CancelFunc
	mu           sync.Mutex
}

// StreamableHTTPTransportConfig holds configuration for creating a StreamableHTTPTransport.
type StreamableHTTPTransportConfig struct {
	Endpoint     string
	Client       *http.Client
	Headers      map[string]string
	GetAuthToken func() string
}

// NewStreamableHTTPTransport creates a new Streamable HTTP transport.
func NewStreamableHTTPTransport(cfg StreamableHTTPTransportConfig) *StreamableHTTPTransport {
	return &StreamableHTTPTransport{
		endpoint:     cfg.Endpoint,
		client:       cfg.Client,
		headers:      cfg.Headers,
		getAuthToken: cfg.GetAuthToken,
	}
}

func (t *StreamableHTTPTransport) Connect(ctx context.Context) error {
	// Streamable HTTP does not require a persistent connection on Connect.
	// Optionally open a GET request for server-initiated notifications.
	t.startNotificationStream(ctx)
	return nil
}

func (t *StreamableHTTPTransport) Send(ctx context.Context, message []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(message))
	if err != nil {
		return fmt.Errorf("failed to create POST request: %w", err)
	}

	t.setCommonHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("POST request failed: %w", err)
	}

	// Extract session ID from response
	if sid := resp.Header.Get(HeaderMCPSessionID); sid != "" {
		t.mu.Lock()
		t.sessionID = sid
		t.mu.Unlock()
	}

	contentType := resp.Header.Get("Content-Type")

	switch {
	case resp.StatusCode == http.StatusAccepted:
		// Server accepted but will send response via notification stream
		if err := resp.Body.Close(); err != nil {
			log.Printf("Warning: failed to close response body: %v", err)
		}
		return nil

	case strings.HasPrefix(contentType, "text/event-stream"):
		// SSE stream response - read events in background
		go t.readSSEResponse(ctx, resp)
		return nil

	case strings.HasPrefix(contentType, "application/json"):
		defer func() {
			if err := resp.Body.Close(); err != nil {
				log.Printf("Warning: failed to close response body: %v", err)
			}
		}()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("server returned error status: %d - %s", resp.StatusCode, string(body))
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read response body: %w", err)
		}

		if t.onMessage != nil && len(body) > 0 {
			t.onMessage("message", body)
		}
		return nil

	default:
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
}

func (t *StreamableHTTPTransport) SetOnMessage(handler func(event string, data []byte)) {
	t.onMessage = handler
}

func (t *StreamableHTTPTransport) SetOnError(handler func(err error)) {
	t.onError = handler
}

func (t *StreamableHTTPTransport) Close() error {
	// Cancel notification stream
	if t.notifyCancel != nil {
		t.notifyCancel()
	}

	// Send DELETE to terminate the session
	t.mu.Lock()
	sid := t.sessionID
	t.mu.Unlock()

	if sid != "" {
		req, err := http.NewRequest(http.MethodDelete, t.endpoint, nil)
		if err != nil {
			return fmt.Errorf("failed to create DELETE request: %w", err)
		}
		t.setCommonHeaders(req)

		resp, err := t.client.Do(req)
		if err != nil {
			log.Printf("Warning: failed to send session termination: %v", err)
			return nil
		}
		if err := resp.Body.Close(); err != nil {
			log.Printf("Warning: failed to close response body: %v", err)
		}
	}

	return nil
}

func (t *StreamableHTTPTransport) SessionID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessionID
}

// setCommonHeaders sets headers common to all requests.
func (t *StreamableHTTPTransport) setCommonHeaders(req *http.Request) {
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	if t.getAuthToken != nil {
		if token := t.getAuthToken(); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	req.Header.Set(HeaderMCPProtocolVersion, MCPProtocolVersion)

	t.mu.Lock()
	if t.sessionID != "" {
		req.Header.Set(HeaderMCPSessionID, t.sessionID)
	}
	t.mu.Unlock()
}

// startNotificationStream opens a GET SSE stream for server-initiated notifications.
func (t *StreamableHTTPTransport) startNotificationStream(ctx context.Context) {
	notifyCtx, cancel := context.WithCancel(ctx)
	t.notifyCancel = cancel

	go func() {
		for {
			select {
			case <-notifyCtx.Done():
				return
			default:
			}

			if err := t.openNotificationStream(notifyCtx); err != nil {
				if notifyCtx.Err() != nil {
					return
				}
				if errors.Is(err, errNotificationStreamNotSupported) {
					return
				}
				log.Printf("Notification stream error: %v, reconnecting...", err)
				select {
				case <-notifyCtx.Done():
					return
				case <-time.After(3 * time.Second):
				}
			}
		}
	}()
}

func (t *StreamableHTTPTransport) openNotificationStream(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create GET request: %w", err)
	}

	t.setCommonHeaders(req)
	req.Header.Set("Accept", "text/event-stream")

	t.mu.Lock()
	if t.lastEventID != "" {
		req.Header.Set("Last-Event-ID", t.lastEventID)
	}
	t.mu.Unlock()

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("GET request failed: %w", err)
	}

	if resp.StatusCode == http.StatusMethodNotAllowed {
		// Server does not support GET notification stream; stop trying
		if err := resp.Body.Close(); err != nil {
			log.Printf("Warning: failed to close response body: %v", err)
		}
		log.Println("Server does not support GET notification stream (405), notifications will arrive via POST responses")
		// Return a sentinel error to stop the reconnection loop
		return errNotificationStreamNotSupported
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if err := resp.Body.Close(); err != nil {
			log.Printf("Warning: failed to close response body: %v", err)
		}
		return fmt.Errorf("server returned error status: %d - %s", resp.StatusCode, string(body))
	}

	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("Warning: failed to close response body: %v", err)
		}
	}()

	return ReadSSEEvents(ctx, resp.Body, func(evt SSEEvent) {
		if evt.ID != "" {
			t.mu.Lock()
			t.lastEventID = evt.ID
			t.mu.Unlock()
		}

		if t.onMessage != nil {
			t.onMessage(evt.Event, evt.Data)
		}
	})
}

// readSSEResponse reads SSE events from a POST response body.
func (t *StreamableHTTPTransport) readSSEResponse(ctx context.Context, resp *http.Response) {
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("Warning: failed to close response body: %v", err)
		}
	}()

	err := ReadSSEEvents(ctx, resp.Body, func(evt SSEEvent) {
		if evt.ID != "" {
			t.mu.Lock()
			t.lastEventID = evt.ID
			t.mu.Unlock()
		}

		if t.onMessage != nil {
			t.onMessage(evt.Event, evt.Data)
		}
	})

	if err != nil && t.onError != nil {
		t.onError(err)
	}
}
