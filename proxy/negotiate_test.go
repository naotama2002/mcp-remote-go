package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
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
