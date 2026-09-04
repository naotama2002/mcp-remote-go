package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestNegotiateTransportStreamableHTTP(t *testing.T) {
	// Server that supports Streamable HTTP
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set(HeaderMCPSessionID, "negotiate-session")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      0,
				"result":  map[string]interface{}{},
			})
		case http.MethodGet:
			// Notification stream
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			flusher.Flush()
			<-r.Context().Done()
		case http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	proxy, err := NewProxyWithTransport(server.URL, 0, map[string]string{}, "negotiate-test", TransportModeAuto)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Shutdown()

	err = proxy.connectToServer()
	if err != nil {
		t.Fatalf("connectToServer failed: %v", err)
	}

	// Should have selected Streamable HTTP
	if proxy.currentTransportMode() != TransportModeStreamableHTTP {
		t.Errorf("Expected transport mode 'streamable-http', got '%s'", proxy.currentTransportMode())
	}
}

// TestNegotiateDetectsModernServer checks the probe places a server that
// answers server/discover, and that the era is acted on rather than merely
// recorded: 2026-07-28 has no GET notification stream, so a correctly
// classified server is never asked for one.
func TestNegotiateDetectsModernServer(t *testing.T) {
	var mu sync.Mutex
	var gets int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      0,
				"result": map[string]interface{}{
					"supportedVersions": []string{"2026-07-28", "2025-11-25"},
				},
			})
		case http.MethodGet:
			mu.Lock()
			gets++
			mu.Unlock()
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	proxy, err := NewProxyWithTransport(server.URL, 0, map[string]string{}, "modern-era-test", TransportModeAuto)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Shutdown()

	if err := proxy.connectToServer(); err != nil {
		t.Fatalf("connectToServer failed: %v", err)
	}

	if proxy.currentTransportMode() != TransportModeStreamableHTTP {
		t.Errorf("transport = %q, want streamable-http", proxy.currentTransportMode())
	}
	if proxy.currentProfile().era != eraModern {
		t.Errorf("era = %v, want modern", proxy.currentProfile().era)
	}
	if !proxy.currentProfile().supportsLegacy() {
		t.Error("a server listing 2025-11-25 should be reported as dual-era")
	}

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if gets != 0 {
		t.Errorf("opened %d GET notification streams against a modern server, want 0", gets)
	}
}

// TestNegotiateDetectsLegacyServer covers the other branch: a server that
// rejects server/discover but answers in JSON-RPC is legacy, and still gets
// its notification stream.
func TestNegotiateDetectsLegacyServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      0,
				"error": map[string]interface{}{
					"code":    -32601,
					"message": "Method not found",
				},
			})
		case http.MethodGet:
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			flusher.Flush()
			<-r.Context().Done()
		case http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	proxy, err := NewProxyWithTransport(server.URL, 0, map[string]string{}, "legacy-era-test", TransportModeAuto)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Shutdown()

	if err := proxy.connectToServer(); err != nil {
		t.Fatalf("connectToServer failed: %v", err)
	}

	if proxy.currentTransportMode() != TransportModeStreamableHTTP {
		t.Errorf("transport = %q, want streamable-http", proxy.currentTransportMode())
	}
	if proxy.currentProfile().era != eraLegacy {
		t.Errorf("era = %v, want legacy", proxy.currentProfile().era)
	}
}

// TestNegotiateDetectsModernOnlyServer covers the case the compatibility
// matrix marks as unreachable for a legacy client, which the proxy must be
// able to recognise in order to say so.
func TestNegotiateDetectsModernOnlyServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      0,
			"result":  map[string]interface{}{"supportedVersions": []string{"2026-07-28"}},
		})
	}))
	defer server.Close()

	proxy, err := NewProxyWithTransport(server.URL, 0, map[string]string{}, "modern-only-test", TransportModeAuto)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Shutdown()

	if err := proxy.connectToServer(); err != nil {
		t.Fatalf("connectToServer failed: %v", err)
	}

	if proxy.currentProfile().supportsLegacy() {
		t.Error("a server listing only 2026-07-28 must not be reported as answering initialize")
	}
}

// TestNegotiateKeepsStreamableHTTPOnPlainTextRejection covers the server that
// exposed this: a Streamable HTTP endpoint on a revision that predates
// server/discover, which rejects the modern probe with a plain-text 400 rather
// than a JSON-RPC error.
//
// Reading that as "not Streamable HTTP" sent it to the deprecated transport,
// where the opening GET is answered "GET requires an Mcp-Session-Id header" and
// the connection is lost. What places it is that it answers a request from its
// own era, which is what the fallback probe asks.
func TestNegotiateKeepsStreamableHTTPOnPlainTextRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			if probeMethod(t, r) == "ping" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":0,"result":{}}`)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, "Bad Request: unknown method")
		case http.MethodGet:
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, "Bad Request: GET requires an Mcp-Session-Id header")
		}
	}))
	defer server.Close()

	proxy, err := NewProxyWithTransport(server.URL, 0, map[string]string{}, "plaintext-400-test", TransportModeAuto)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Shutdown()

	if err := proxy.connectToServer(); err != nil {
		t.Fatalf("connectToServer failed: %v", err)
	}

	if got := proxy.currentTransportMode(); got != TransportModeStreamableHTTP {
		t.Errorf("transport = %q, want streamable-http", got)
	}
	if got := proxy.currentProfile().era; got != eraLegacy {
		t.Errorf("era = %v, want legacy", got)
	}
}

// TestNegotiateFallsBackToSSEWhenNoJSONRPCIsServed is the other half of the
// same rule, and a regression guard for real breakage: a server hosting only
// the deprecated transport, whose POST endpoint answers a plain-text 400 rather
// than 404 or 405 -- the shape an SDK server produces when the POST handler
// wants a sessionId query parameter.
//
// Treating any rejection as proof of a Streamable HTTP endpoint sent this server
// to the wrong transport, and every message the client sent failed. The endpoint
// refusing a request from its own era is what rules it out.
func TestNegotiateFallsBackToSSEWhenNoJSONRPCIsServed(t *testing.T) {
	var postBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			postBodies = append(postBodies, probeMethod(t, r))
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, "Missing sessionId")
		case http.MethodGet:
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			_, _ = fmt.Fprint(w, "event: endpoint\ndata: /message\n\n")
			flusher.Flush()
			<-r.Context().Done()
		}
	}))
	defer server.Close()

	proxy, err := NewProxyWithTransport(server.URL, 0, map[string]string{}, "sse-only-400-test", TransportModeAuto)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Shutdown()

	if err := proxy.connectToServer(); err != nil {
		t.Fatalf("connectToServer failed: %v", err)
	}

	if got := proxy.currentTransportMode(); got != TransportModeSSE {
		t.Errorf("transport = %q, want sse", got)
	}
	// Both shapes must have been tried before ruling the endpoint out.
	if len(postBodies) != 2 || postBodies[0] != "server/discover" || postBodies[1] != "ping" {
		t.Errorf("probe methods = %v, want [server/discover ping]", postBodies)
	}
}

// probeMethod returns the JSON-RPC method of a probe request body.
func probeMethod(t *testing.T, r *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("failed to read probe body: %v", err)
		return ""
	}
	var envelope struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Errorf("probe body is not JSON: %v", err)
		return ""
	}
	return envelope.Method
}

// TestNegotiateFallsBackToSSEOnlyWhenPostIsUnserved pins the other side of the
// rule: the deprecated transport is chosen when nothing answers POST here, not
// merely because a request was rejected.
func TestNegotiateFallsBackToSSEOnlyWhenPostIsUnserved(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					w.WriteHeader(status)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				flusher, _ := w.(http.Flusher)
				_, _ = fmt.Fprintf(w, "event: endpoint\ndata: /message\n\n")
				flusher.Flush()
				<-r.Context().Done()
			}))
			defer server.Close()

			proxy, err := NewProxyWithTransport(server.URL, 0, map[string]string{}, "unserved-post-test", TransportModeAuto)
			if err != nil {
				t.Fatalf("Failed to create proxy: %v", err)
			}
			defer proxy.Shutdown()

			if err := proxy.connectToServer(); err != nil {
				t.Fatalf("connectToServer failed: %v", err)
			}
			if got := proxy.currentTransportMode(); got != TransportModeSSE {
				t.Errorf("transport = %q, want sse", got)
			}
		})
	}
}

func TestNegotiateTransportFallbackToSSE(t *testing.T) {
	// Server that rejects POST (no Streamable HTTP) but serves SSE on GET
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			// Reject POST - server doesn't support Streamable HTTP
			w.WriteHeader(http.StatusNotFound)
		case http.MethodGet:
			// Serve SSE
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")

			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "SSE not supported", http.StatusInternalServerError)
				return
			}

			_, _ = fmt.Fprintf(w, "event: endpoint\ndata: /message\n\n")
			flusher.Flush()

			_, _ = fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"ready\"}\n\n")
			flusher.Flush()

			<-r.Context().Done()
		}
	}))
	defer server.Close()

	proxy, err := NewProxyWithTransport(server.URL, 0, map[string]string{}, "fallback-test", TransportModeAuto)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Shutdown()

	err = proxy.connectToServer()
	if err != nil {
		t.Fatalf("connectToServer failed: %v", err)
	}

	// Should have fallen back to SSE
	if proxy.currentTransportMode() != TransportModeSSE {
		t.Errorf("Expected transport mode 'sse', got '%s'", proxy.currentTransportMode())
	}

	time.Sleep(100 * time.Millisecond)
}

func TestNegotiateTransportFallbackOn405(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodGet:
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			_, _ = fmt.Fprintf(w, "event: endpoint\ndata: /msg\n\n")
			flusher.Flush()
			<-r.Context().Done()
		}
	}))
	defer server.Close()

	proxy, err := NewProxyWithTransport(server.URL, 0, map[string]string{}, "405-test", TransportModeAuto)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Shutdown()

	err = proxy.connectToServer()
	if err != nil {
		t.Fatalf("connectToServer failed: %v", err)
	}

	if proxy.currentTransportMode() != TransportModeSSE {
		t.Errorf("Expected SSE fallback on 405, got '%s'", proxy.currentTransportMode())
	}
}

func TestTransportModeSSEDirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\"}\n\n")
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	proxy, err := NewProxyWithTransport(server.URL, 0, map[string]string{}, "sse-direct-test", TransportModeSSE)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Shutdown()

	err = proxy.connectToServer()
	if err != nil {
		t.Fatalf("connectToServer failed: %v", err)
	}

	// Should remain SSE
	if proxy.currentTransportMode() != TransportModeSSE {
		t.Errorf("Expected SSE mode, got '%s'", proxy.currentTransportMode())
	}
}

func TestNegotiateTransportJSONRPCErrorBody(t *testing.T) {
	// Server returns 400 with JSON-RPC error body — should detect as Streamable HTTP
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      0,
				"error": map[string]interface{}{
					"code":    -32600,
					"message": "Must send initialize first",
				},
			})
		case http.MethodGet:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	proxy, err := NewProxyWithTransport(server.URL, 0, map[string]string{}, "jsonrpc-error-test", TransportModeAuto)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Shutdown()

	err = proxy.connectToServer()
	if err != nil {
		t.Fatalf("connectToServer failed: %v", err)
	}

	if proxy.currentTransportMode() != TransportModeStreamableHTTP {
		t.Errorf("Expected transport mode 'streamable-http' for JSON-RPC error body, got '%s'", proxy.currentTransportMode())
	}
}

func TestNegotiateTransportProbeResponseNotForwarded(t *testing.T) {
	// Verify that the probe response is NOT written to stdout during auto-negotiation
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      0,
				"result":  map[string]interface{}{},
			})
		case http.MethodGet:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	proxy, err := NewProxyWithTransport(server.URL, 0, map[string]string{}, "probe-isolation-test", TransportModeAuto)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Shutdown()

	// Replace stdout with a buffer to capture any output
	var stdoutBuf bytes.Buffer
	proxy.SetStdio(bufio.NewReader(&bytes.Buffer{}), bufio.NewWriter(&stdoutBuf))

	err = proxy.connectToServer()
	if err != nil {
		t.Fatalf("connectToServer failed: %v", err)
	}

	// Wait for any async writes
	time.Sleep(100 * time.Millisecond)

	// The probe response should NOT have been written to stdout
	if stdoutBuf.Len() > 0 {
		t.Errorf("Probe response was forwarded to stdout (got %d bytes: %s), expected no output",
			stdoutBuf.Len(), stdoutBuf.String())
	}

	if proxy.currentTransportMode() != TransportModeStreamableHTTP {
		t.Errorf("Expected transport mode 'streamable-http', got '%s'", proxy.currentTransportMode())
	}
}

func TestTransportModeStreamableHTTPDirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
		case http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	proxy, err := NewProxyWithTransport(server.URL, 0, map[string]string{}, "streamable-direct-test", TransportModeStreamableHTTP)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Shutdown()

	err = proxy.connectToServer()
	if err != nil {
		t.Fatalf("connectToServer failed: %v", err)
	}

	if proxy.currentTransportMode() != TransportModeStreamableHTTP {
		t.Errorf("Expected streamable-http mode, got '%s'", proxy.currentTransportMode())
	}
}
