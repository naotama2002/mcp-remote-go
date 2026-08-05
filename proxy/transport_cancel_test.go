package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// cancelFixture is a server that answers POSTs with an SSE stream it holds
// open, and records what it received and when a stream was closed by the peer.
type cancelFixture struct {
	mu       sync.Mutex
	posts    []string
	closed   chan string
	streamUp chan struct{}
}

func newCancelFixture(t *testing.T) (*cancelFixture, string) {
	t.Helper()

	f := &cancelFixture{
		closed:   make(chan string, 8),
		streamUp: make(chan struct{}, 8),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		body := string(buf[:n])

		f.mu.Lock()
		f.posts = append(f.posts, body)
		f.mu.Unlock()

		// Notifications get an immediate 202 and no stream.
		if strings.Contains(body, `"method":"notifications/`) {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		// Emit a progress notification so the client sees the stream is live,
		// then hold it open until the peer goes away.
		_, _ = fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
		flusher.Flush()
		f.streamUp <- struct{}{}

		<-r.Context().Done()
		f.closed <- body
	}))
	t.Cleanup(server.Close)

	return f, server.URL
}

func (f *cancelFixture) postCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.posts)
}

const cancelMeta = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}`

// TestModernCancellationClosesTheStream covers the 2026-07-28 rule that
// closing a request's response stream is the cancellation signal. The
// notification must not also be posted: this revision does not define
// client-to-server notifications on this transport.
func TestModernCancellationClosesTheStream(t *testing.T) {
	fixture, endpoint := newCancelFixture(t)

	transport := NewStreamableHTTPTransport(StreamableHTTPTransportConfig{
		Endpoint:               endpoint,
		Client:                 &http.Client{},
		SkipNotificationStream: true,
	})
	transport.SetOnMessage(func(string, []byte) {})
	defer func() { _ = transport.Close() }()

	err := transport.Send(context.Background(),
		[]byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"slow","arguments":{},`+cancelMeta+`}}`))
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	select {
	case <-fixture.streamUp:
	case <-time.After(3 * time.Second):
		t.Fatal("the server never opened a response stream")
	}

	// The request is now in flight and addressable.
	if got := transport.inflightCount(); got != 1 {
		t.Fatalf("inflight = %d, want 1", got)
	}

	err = transport.Send(context.Background(),
		[]byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7,"reason":"user"}}`))
	if err != nil {
		t.Fatalf("cancellation Send failed: %v", err)
	}

	select {
	case <-fixture.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the response stream was not closed by the cancellation")
	}

	// Only the original request should have been posted.
	if got := fixture.postCount(); got != 1 {
		t.Errorf("server received %d POSTs, want 1: the cancellation must not be forwarded", got)
	}

	// The slot must be released, or the table grows for the process lifetime.
	deadline := time.Now().Add(2 * time.Second)
	for transport.inflightCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := transport.inflightCount(); got != 0 {
		t.Errorf("inflight = %d after cancellation, want 0", got)
	}
}

// TestLegacyCancellationIsForwarded covers the other era: 2025-11-25 and
// earlier did expect notifications/cancelled on the wire, so it must still go
// out rather than being swallowed.
func TestLegacyCancellationIsForwarded(t *testing.T) {
	fixture, endpoint := newCancelFixture(t)

	transport := NewStreamableHTTPTransport(StreamableHTTPTransportConfig{
		Endpoint:               endpoint,
		Client:                 &http.Client{},
		SkipNotificationStream: true,
	})
	transport.SetOnMessage(func(string, []byte) {})
	defer func() { _ = transport.Close() }()

	err := transport.Send(context.Background(),
		[]byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"slow","arguments":{}}}`))
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	select {
	case <-fixture.streamUp:
	case <-time.After(3 * time.Second):
		t.Fatal("the server never opened a response stream")
	}

	err = transport.Send(context.Background(),
		[]byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7,"reason":"user"}}`))
	if err != nil {
		t.Fatalf("cancellation Send failed: %v", err)
	}

	if got := fixture.postCount(); got != 2 {
		t.Errorf("server received %d POSTs, want 2: a legacy cancellation is sent as a message", got)
	}
}

// TestCancellationOfUnknownRequestIsHarmless checks the cases that must not
// panic or leak: a cancellation naming nothing, and one naming a request that
// has already finished.
func TestCancellationOfUnknownRequestIsHarmless(t *testing.T) {
	fixture, endpoint := newCancelFixture(t)

	transport := NewStreamableHTTPTransport(StreamableHTTPTransportConfig{
		Endpoint:               endpoint,
		Client:                 &http.Client{},
		SkipNotificationStream: true,
	})
	transport.SetOnMessage(func(string, []byte) {})
	defer func() { _ = transport.Close() }()

	// Put the transport in the modern era without leaving a request open.
	transport.observeProtocolVersion(requestMetadata{protocolVersion: ProtocolVersion20260728})

	messages := []string{
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":999}}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled"}`,
	}
	for _, message := range messages {
		if err := transport.Send(context.Background(), []byte(message)); err != nil {
			t.Errorf("Send(%s) failed: %v", message, err)
		}
	}

	if got := fixture.postCount(); got != 0 {
		t.Errorf("server received %d POSTs, want 0", got)
	}
	if got := transport.inflightCount(); got != 0 {
		t.Errorf("inflight = %d, want 0", got)
	}
}

// TestCloseCancelsInflightRequests checks shutdown closes open streams, which
// is how the server learns those requests are abandoned.
func TestCloseCancelsInflightRequests(t *testing.T) {
	fixture, endpoint := newCancelFixture(t)

	transport := NewStreamableHTTPTransport(StreamableHTTPTransportConfig{
		Endpoint:               endpoint,
		Client:                 &http.Client{},
		SkipNotificationStream: true,
	})
	transport.SetOnMessage(func(string, []byte) {})

	err := transport.Send(context.Background(),
		[]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slow","arguments":{},`+cancelMeta+`}}`))
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	select {
	case <-fixture.streamUp:
	case <-time.After(3 * time.Second):
		t.Fatal("the server never opened a response stream")
	}

	if err := transport.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	select {
	case <-fixture.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not close the in-flight response stream")
	}
}

// inflightCount reports how many response streams are currently addressable.
func (t *StreamableHTTPTransport) inflightCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.inflight)
}

// notifyFixture counts GET notification-stream attempts against a server that
// serves both eras, which is what most deployments are during the migration.
type notifyFixture struct {
	mu   sync.Mutex
	gets int
}

func newNotifyFixture(t *testing.T) (*notifyFixture, string) {
	t.Helper()

	f := &notifyFixture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			f.mu.Lock()
			f.gets++
			f.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			flusher.Flush()
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	t.Cleanup(server.Close)
	return f, server.URL
}

func (f *notifyFixture) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets
}

// TestLegacyClientGetsItsNotificationStream is the regression guard for the
// case a dual-era server creates: the server speaks 2026-07-28, but this
// client does not, and on the revision it does speak the standalone GET stream
// is the only way server-initiated messages arrive. Deciding on the server's
// revision instead of the client's silently dropped them.
func TestLegacyClientGetsItsNotificationStream(t *testing.T) {
	fixture, endpoint := newNotifyFixture(t)

	transport := NewStreamableHTTPTransport(StreamableHTTPTransportConfig{
		Endpoint: endpoint,
		Client:   &http.Client{},
		// A dual-era server can serve a revision that has the stream, so the
		// transport is not told to skip it.
		SkipNotificationStream: false,
	})
	transport.SetOnMessage(func(string, []byte) {})
	defer func() { _ = transport.Close() }()

	if err := transport.Connect(context.Background()); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	// Nothing has identified the client yet, so no stream should exist.
	if got := fixture.count(); got != 0 {
		t.Errorf("opened %d GET streams before the client said anything, want 0", got)
	}

	err := transport.Send(context.Background(),
		[]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{}}}`))
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for fixture.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := fixture.count(); got != 1 {
		t.Errorf("opened %d GET streams for a legacy client, want 1", got)
	}
}

// TestModernClientOpensNoNotificationStream is the other half: the revision
// the client declared has no such stream, so none is opened even though the
// server would serve one.
func TestModernClientOpensNoNotificationStream(t *testing.T) {
	fixture, endpoint := newNotifyFixture(t)

	transport := NewStreamableHTTPTransport(StreamableHTTPTransportConfig{
		Endpoint: endpoint,
		Client:   &http.Client{},
	})
	transport.SetOnMessage(func(string, []byte) {})
	defer func() { _ = transport.Close() }()

	if err := transport.Connect(context.Background()); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	err := transport.Send(context.Background(),
		[]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`+cancelMeta+`}}`))
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	if got := fixture.count(); got != 0 {
		t.Errorf("opened %d GET streams for a modern client, want 0", got)
	}
}
