package proxy

import (
	"bytes"
	"context"
	"encoding/json"
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
	// MCPProtocolVersion is the protocol version assumed until the local
	// client's own traffic tells us otherwise. It is the last revision that
	// used the initialize handshake, so it is the safe assumption for a client
	// that has not declared a version yet.
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

	// protocolVersion is the revision declared on outgoing requests. The proxy
	// never picks it: it is observed from the local client's own messages, so
	// that a client on either era is forwarded faithfully.
	protocolVersion string

	onMessage func(event string, data []byte)
	onError   func(err error)

	// skipNotificationStream suppresses the GET stream for servers already
	// known to have dropped it.
	skipNotificationStream bool

	// toolHeaders remembers the x-mcp-header bindings advertised in tools/list
	// so a later tools/call can be decorated with them, and pendingToolsList
	// records which in-flight ids will bring such a list back. Responses carry
	// only an id, so the method has to be remembered from the request.
	toolHeaders      *toolHeaderRegistry
	pendingToolsList map[string]struct{}

	// inflight maps a request id to the cancel func of its response stream.
	// Each request on this transport has its own stream, and closing that
	// stream is how the request is cancelled, so cancellation needs the
	// streams to be addressable one at a time.
	inflight map[string]context.CancelFunc

	notifyCancel context.CancelFunc
	mu           sync.Mutex
}

// StreamableHTTPTransportConfig holds configuration for creating a StreamableHTTPTransport.
type StreamableHTTPTransportConfig struct {
	Endpoint     string
	Client       *http.Client
	Headers      map[string]string
	GetAuthToken func() string

	// SkipNotificationStream suppresses the GET notification stream, which
	// 2026-07-28 removed. Leave it false when the era is unknown: the
	// transport handles the 405 and stops on its own.
	SkipNotificationStream bool
}

// NewStreamableHTTPTransport creates a new Streamable HTTP transport.
func NewStreamableHTTPTransport(cfg StreamableHTTPTransportConfig) *StreamableHTTPTransport {
	return &StreamableHTTPTransport{
		endpoint:               cfg.Endpoint,
		client:                 cfg.Client,
		headers:                cfg.Headers,
		getAuthToken:           cfg.GetAuthToken,
		skipNotificationStream: cfg.SkipNotificationStream,
		protocolVersion:        MCPProtocolVersion,
		toolHeaders:            newToolHeaderRegistry(),
		pendingToolsList:       make(map[string]struct{}),
		inflight:               make(map[string]context.CancelFunc),
	}
}

func (t *StreamableHTTPTransport) Connect(ctx context.Context) error {
	// Streamable HTTP does not require a persistent connection on Connect.
	// Optionally open a GET request for server-initiated notifications.
	if t.skipNotificationStream {
		log.Println("Skipping GET notification stream: the server's protocol revision does not have one")
		return nil
	}
	t.startNotificationStream(ctx)
	return nil
}

func (t *StreamableHTTPTransport) Send(ctx context.Context, message []byte) error {
	md := parseRequestMetadata(message)
	t.observeProtocolVersion(md)

	if handled := t.handleCancellation(md); handled {
		return nil
	}

	// The reply to a tools/list carries the schemas the header mirroring needs,
	// but a reply names only its id, so the association is made here.
	if md.method == methodToolsList && md.isModern() {
		t.expectToolsList(md.id)
	}

	// Give each request its own context so its response stream can be closed
	// without disturbing the others. The context outlives Send: an SSE
	// response is read by a goroutine that keeps running after we return.
	reqCtx, cancel := context.WithCancel(ctx)

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, t.endpoint, bytes.NewReader(message))
	if err != nil {
		cancel()
		return fmt.Errorf("failed to create POST request: %w", err)
	}

	t.setCommonHeaders(req, md)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := t.client.Do(req)
	if err != nil {
		cancel()
		return fmt.Errorf("POST request failed: %w", err)
	}

	// Extract session ID from response
	if sid := resp.Header.Get(HeaderMCPSessionID); sid != "" {
		t.mu.Lock()
		t.sessionID = sid
		t.mu.Unlock()
	}

	contentType := resp.Header.Get("Content-Type")

	if resp.StatusCode == http.StatusUnauthorized {
		// unauthorizedFromResponse drains and closes the body, so the request
		// context is finished with by the time it returns.
		unauthErr := unauthorizedFromResponse(resp)
		cancel()
		return unauthErr
	}

	if strings.HasPrefix(contentType, "text/event-stream") {
		// This stream is the response to this request. Its context must
		// outlive Send, and stays addressable so a later cancellation can
		// close it -- which is what tells the server to stop the work.
		t.trackInflight(md.id, cancel)
		go t.readSSEResponse(reqCtx, resp, md.id)
		return nil
	}

	// Every other outcome is complete once this function returns.
	defer cancel()

	switch {
	case resp.StatusCode == http.StatusAccepted:
		// Server accepted but will send response via notification stream
		if err := resp.Body.Close(); err != nil {
			log.Printf("Warning: failed to close response body: %v", err)
		}
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

		if len(body) > 0 {
			t.deliver("message", body)
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

	// Close every response stream still open. Each close is read by the server
	// as cancellation of that request, which is the right message to send when
	// the proxy is going away.
	t.mu.Lock()
	inflight := t.inflight
	t.inflight = make(map[string]context.CancelFunc)
	t.mu.Unlock()
	for _, cancel := range inflight {
		cancel()
	}

	// Send DELETE to terminate the session. Only legacy servers mint session
	// IDs, so this is naturally skipped on 2026-07-28 and later.
	t.mu.Lock()
	sid := t.sessionID
	t.mu.Unlock()

	if sid != "" {
		req, err := http.NewRequest(http.MethodDelete, t.endpoint, nil)
		if err != nil {
			return fmt.Errorf("failed to create DELETE request: %w", err)
		}
		t.setCommonHeaders(req, requestMetadata{})

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

// deliver forwards a message from the server, after taking from it anything
// the transport itself needs. A tools/list result is where x-mcp-header
// bindings are learned, and where tools carrying invalid ones are dropped
// before the client can call them.
func (t *StreamableHTTPTransport) deliver(event string, data []byte) {
	if t.onMessage == nil {
		return
	}

	if t.claimToolsListResponse(data) {
		data = filterToolsList(t.toolHeaders, data)
	}

	t.onMessage(event, data)
}

// claimToolsListResponse reports whether this message answers a tools/list we
// sent, consuming the record so a stream carrying several messages only
// matches the response itself.
func (t *StreamableHTTPTransport) claimToolsListResponse(data []byte) bool {
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || len(envelope.Result) == 0 {
		return false
	}

	id := requestKey(envelope.ID)
	if id == "" {
		return false
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.pendingToolsList[id]; !ok {
		return false
	}
	delete(t.pendingToolsList, id)
	return true
}

// expectToolsList records that the response to this id will carry tool
// definitions worth reading.
func (t *StreamableHTTPTransport) expectToolsList(id string) {
	if id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pendingToolsList[id] = struct{}{}
}

// handleCancellation deals with a notifications/cancelled message and reports
// whether it consumed it.
//
// From 2026-07-28 the cancellation signal on this transport is closing the
// request's response stream, and notifications/cancelled is a stdio-only
// message that the server does not expect to receive. So for a modern client
// the notification is translated into a stream close and not forwarded; for a
// legacy client, whose revision did expect it on the wire, it is passed
// through untouched.
func (t *StreamableHTTPTransport) handleCancellation(md requestMetadata) bool {
	if md.method != methodCancelled {
		return false
	}

	t.mu.Lock()
	modern := t.protocolVersion >= ProtocolVersion20260728
	t.mu.Unlock()

	if !modern {
		return false
	}

	if md.cancelTarget == "" {
		// Nothing identifies the request to stop, so there is nothing to do
		// and nothing worth sending.
		log.Println("Ignoring notifications/cancelled with no requestId")
		return true
	}

	if cancel := t.releaseInflight(md.cancelTarget); cancel != nil {
		log.Printf("Cancelling request %s by closing its response stream", md.cancelTarget)
		cancel()
	}
	return true
}

// trackInflight records the cancel func for a request's response stream. A
// notification has no id and so cannot be cancelled individually.
func (t *StreamableHTTPTransport) trackInflight(id string, cancel context.CancelFunc) {
	if id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inflight[id] = cancel
}

// releaseInflight removes a request from the table and returns its cancel
// func, or nil if it was not there.
func (t *StreamableHTTPTransport) releaseInflight(id string) context.CancelFunc {
	if id == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	cancel, ok := t.inflight[id]
	if !ok {
		return nil
	}
	delete(t.inflight, id)
	return cancel
}

// observeProtocolVersion records the revision the local client is speaking.
// A modern client declares it in `_meta` on every request; a legacy client
// declares it once, in the `initialize` handshake.
func (t *StreamableHTTPTransport) observeProtocolVersion(md requestMetadata) {
	version := md.protocolVersion
	if version == "" && md.method == "initialize" {
		version = md.initializeVersion
	}
	if version == "" {
		return
	}

	t.mu.Lock()
	t.protocolVersion = version
	t.mu.Unlock()
}

// setCommonHeaders sets headers common to all requests. Pass the zero
// requestMetadata for requests that carry no JSON-RPC body (GET, DELETE).
func (t *StreamableHTTPTransport) setCommonHeaders(req *http.Request, md requestMetadata) {
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	if t.getAuthToken != nil {
		if token := t.getAuthToken(); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	t.mu.Lock()
	version := t.protocolVersion
	sessionID := t.sessionID
	t.mu.Unlock()

	// The header must match the version in the body, so a message that carries
	// its own declaration always wins over the transport's running value.
	if md.protocolVersion != "" {
		version = md.protocolVersion
	}
	req.Header.Set(HeaderMCPProtocolVersion, version)

	if version >= ProtocolVersion20260728 {
		// Sessions were removed in 2026-07-28; a modern server ignores the
		// header, but sending it would misrepresent us to intermediaries.
		if md.method != "" {
			req.Header.Set(HeaderMCPMethod, md.method)
			if md.hasName {
				req.Header.Set(HeaderMCPName, encodeHeaderValue(md.name))
			}
		}

		if md.method == methodToolsCall {
			for name, value := range paramHeaders(t.toolHeaders.get(md.name), md.params) {
				req.Header.Set(name, value)
			}
		}
		return
	}

	if sessionID != "" {
		req.Header.Set(HeaderMCPSessionID, sessionID)
	}
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
				var unauth *UnauthorizedError
				if errors.As(err, &unauth) {
					if t.onError != nil {
						t.onError(unauth)
					}
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

	t.setCommonHeaders(req, requestMetadata{})
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

	if resp.StatusCode == http.StatusUnauthorized {
		return unauthorizedFromResponse(resp)
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

		t.deliver(evt.Event, evt.Data)
	})
}

// readSSEResponse reads SSE events from a POST response body. It owns the
// request's slot in the inflight table for as long as the stream is open.
func (t *StreamableHTTPTransport) readSSEResponse(ctx context.Context, resp *http.Response, id string) {
	defer func() {
		// The stream is over, so nothing is left to cancel. Releasing the
		// cancel func here is also what keeps the table from growing for the
		// lifetime of the process.
		if cancel := t.releaseInflight(id); cancel != nil {
			cancel()
		}
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

		t.deliver(evt.Event, evt.Data)
	})

	if err != nil && t.onError != nil {
		t.onError(err)
	}
}
