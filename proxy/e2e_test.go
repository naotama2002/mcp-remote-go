package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestE2EStreamableHTTPAutoNegotiation tests the full pipeline:
// auto-negotiation → initialize → notifications/initialized → tools/list
// with mock stdin/stdout, verifying the client receives only expected messages.
func TestE2EStreamableHTTPAutoNegotiation(t *testing.T) {
	server := newMockMCPServer(t)
	defer server.Close()

	proxy, err := NewProxyWithTransport(server.URL, 0, map[string]string{}, "e2e-auto-test", TransportModeAuto)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	// Create pipes for stdin/stdout
	stdinReader, stdinWriter := io.Pipe()
	var stdoutBuf safeBuffer
	proxy.SetStdio(bufio.NewReader(stdinReader), bufio.NewWriter(&stdoutBuf))

	// Start proxy in background
	proxyErr := make(chan error, 1)
	go func() {
		proxyErr <- proxy.Start()
	}()

	// Wait for connection to be established
	time.Sleep(200 * time.Millisecond)

	// Verify auto-negotiation selected Streamable HTTP
	if proxy.transportMode != TransportModeStreamableHTTP {
		t.Fatalf("Expected auto-negotiation to select streamable-http, got '%s'", proxy.transportMode)
	}

	// Send initialize via stdin
	writeJSON(t, stdinWriter, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": MCPProtocolVersion,
			"capabilities":   map[string]interface{}{},
			"clientInfo":     map[string]string{"name": "test-client", "version": "1.0.0"},
		},
	})

	// Wait for response
	initResp := readJSONResponse(t, &stdoutBuf, 1*time.Second)
	if initResp == nil {
		t.Fatal("Expected initialize response, got nothing")
	}
	if initResp["id"] != float64(1) {
		t.Errorf("Expected initialize response id=1, got %v", initResp["id"])
	}
	result, ok := initResp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected result object in initialize response")
	}
	if result["protocolVersion"] != MCPProtocolVersion {
		t.Errorf("Expected protocol version '%s', got '%v'", MCPProtocolVersion, result["protocolVersion"])
	}

	// Send notifications/initialized (no response expected)
	writeJSON(t, stdinWriter, map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	time.Sleep(100 * time.Millisecond)

	// Send tools/list
	writeJSON(t, stdinWriter, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	})

	toolsResp := readJSONResponse(t, &stdoutBuf, 1*time.Second)
	if toolsResp == nil {
		t.Fatal("Expected tools/list response, got nothing")
	}
	if toolsResp["id"] != float64(2) {
		t.Errorf("Expected tools/list response id=2, got %v", toolsResp["id"])
	}
	toolsResult, ok := toolsResp["result"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected result object in tools/list response")
	}
	tools, ok := toolsResult["tools"].([]interface{})
	if !ok || len(tools) != 1 {
		t.Errorf("Expected 1 tool, got %v", toolsResult["tools"])
	}

	// Shutdown
	_ = stdinWriter.Close()
	time.Sleep(200 * time.Millisecond)
	proxy.Shutdown()
}

// TestE2EStreamableHTTPProbeIsolation verifies that during auto-negotiation,
// no unexpected messages (like probe responses) are delivered to the client.
func TestE2EStreamableHTTPProbeIsolation(t *testing.T) {
	var probeRequestCount int
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var msg map[string]interface{}
			_ = json.Unmarshal(body, &msg)

			mu.Lock()
			probeRequestCount++
			reqNum := probeRequestCount
			mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			w.Header().Set(HeaderMCPSessionID, "isolation-session")

			if reqNum == 1 {
				// First POST is the probe (ping with id=0)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      0,
					"result":  map[string]interface{}{},
				})
				return
			}

			// Subsequent requests are real client messages
			method, _ := msg["method"].(string)
			switch method {
			case "initialize":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      msg["id"],
					"result": map[string]interface{}{
						"protocolVersion": MCPProtocolVersion,
						"capabilities":   map[string]interface{}{},
						"serverInfo":     map[string]string{"name": "test", "version": "1.0.0"},
					},
				})
			default:
				w.WriteHeader(http.StatusAccepted)
			}
		case http.MethodGet:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	proxy, err := NewProxyWithTransport(server.URL, 0, map[string]string{}, "isolation-test", TransportModeAuto)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	stdinReader, stdinWriter := io.Pipe()
	var stdoutBuf safeBuffer
	proxy.SetStdio(bufio.NewReader(stdinReader), bufio.NewWriter(&stdoutBuf))

	go func() {
		_ = proxy.Start()
	}()

	time.Sleep(200 * time.Millisecond)

	// At this point, auto-negotiation has completed.
	// The probe response (id=0) should NOT be in stdout.
	if stdoutBuf.Len() > 0 {
		t.Errorf("Unexpected data in stdout after auto-negotiation: %s", stdoutBuf.String())
	}

	// Now send initialize (id=1) and verify only this response comes back
	writeJSON(t, stdinWriter, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": MCPProtocolVersion,
			"capabilities":   map[string]interface{}{},
		},
	})

	resp := readJSONResponse(t, &stdoutBuf, 1*time.Second)
	if resp == nil {
		t.Fatal("Expected initialize response")
	}

	// The response must be for id=1 (initialize), NOT id=0 (probe)
	if resp["id"] != float64(1) {
		t.Errorf("Expected response id=1, got %v (probe response may have leaked)", resp["id"])
	}

	_ = stdinWriter.Close()
	time.Sleep(200 * time.Millisecond)
	proxy.Shutdown()
}

// TestE2ESSEFallbackPipeline tests the full pipeline with SSE fallback.
// In SSE transport, responses are delivered via the SSE stream, not via POST response body.
func TestE2ESSEFallbackPipeline(t *testing.T) {
	// Channel to send SSE responses from POST handler to GET handler
	sseResponses := make(chan []byte, 10)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			if r.Header.Get(HeaderMCPProtocolVersion) != "" {
				// This is the auto-negotiation probe — reject it
				w.WriteHeader(http.StatusNotFound)
				return
			}
			// Regular POST for SSE message sending
			body, _ := io.ReadAll(r.Body)
			var msg map[string]interface{}
			_ = json.Unmarshal(body, &msg)

			method, _ := msg["method"].(string)
			var resp map[string]interface{}
			switch method {
			case "initialize":
				resp = map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      msg["id"],
					"result": map[string]interface{}{
						"protocolVersion": "2024-11-05",
						"capabilities":   map[string]interface{}{},
						"serverInfo":     map[string]string{"name": "sse-server", "version": "1.0.0"},
					},
				}
			case "tools/list":
				resp = map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      msg["id"],
					"result": map[string]interface{}{
						"tools": []map[string]interface{}{
							{"name": "sse-tool", "description": "A tool via SSE"},
						},
					},
				}
			}

			if resp != nil {
				respBytes, _ := json.Marshal(resp)
				sseResponses <- respBytes
			}

			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			// Serve SSE stream
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "SSE not supported", http.StatusInternalServerError)
				return
			}

			// Send endpoint event
			_, _ = fmt.Fprintf(w, "event: endpoint\ndata: /message\n\n")
			flusher.Flush()

			// Forward responses from POST handler via SSE
			for {
				select {
				case respData := <-sseResponses:
					_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(respData))
					flusher.Flush()
				case <-r.Context().Done():
					return
				}
			}
		}
	}))
	defer server.Close()

	proxy, err := NewProxyWithTransport(server.URL, 0, map[string]string{}, "e2e-sse-test", TransportModeAuto)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	stdinReader, stdinWriter := io.Pipe()
	var stdoutBuf safeBuffer
	proxy.SetStdio(bufio.NewReader(stdinReader), bufio.NewWriter(&stdoutBuf))

	go func() {
		_ = proxy.Start()
	}()

	time.Sleep(300 * time.Millisecond)

	// Verify SSE was selected
	if proxy.transportMode != TransportModeSSE {
		t.Fatalf("Expected auto-negotiation to fall back to SSE, got '%s'", proxy.transportMode)
	}

	// Send initialize
	writeJSON(t, stdinWriter, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":   map[string]interface{}{},
		},
	})

	initResp := readJSONResponse(t, &stdoutBuf, 2*time.Second)
	if initResp == nil {
		t.Fatal("Expected initialize response via SSE transport")
	}
	if initResp["id"] != float64(1) {
		t.Errorf("Expected response id=1, got %v", initResp["id"])
	}

	_ = stdinWriter.Close()
	time.Sleep(200 * time.Millisecond)
	proxy.cancel()
}

// --- Helpers ---

// safeBuffer is a goroutine-safe bytes.Buffer.
type safeBuffer struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func (sb *safeBuffer) Write(p []byte) (n int, err error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.Write(p)
}

func (sb *safeBuffer) Len() int {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.Len()
}

func (sb *safeBuffer) String() string {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.String()
}

func (sb *safeBuffer) ReadLine() (string, bool) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	line, err := sb.buf.ReadString('\n')
	if err != nil {
		return "", false
	}
	return strings.TrimRight(line, "\n"), true
}

func newMockMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var msg map[string]interface{}
			_ = json.Unmarshal(body, &msg)

			w.Header().Set("Content-Type", "application/json")
			w.Header().Set(HeaderMCPSessionID, "e2e-session")

			method, _ := msg["method"].(string)
			switch method {
			case "ping":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      msg["id"],
					"result":  map[string]interface{}{},
				})
			case "initialize":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      msg["id"],
					"result": map[string]interface{}{
						"protocolVersion": MCPProtocolVersion,
						"capabilities": map[string]interface{}{
							"tools": map[string]interface{}{"listChanged": true},
						},
						"serverInfo": map[string]string{
							"name":    "mock-mcp-server",
							"version": "1.0.0",
						},
					},
				})
			case "tools/list":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      msg["id"],
					"result": map[string]interface{}{
						"tools": []map[string]interface{}{
							{
								"name":        "echo",
								"description": "Echoes the input",
								"inputSchema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"message": map[string]string{"type": "string"},
									},
								},
							},
						},
					},
				})
			default:
				// notifications — 202 Accepted
				w.WriteHeader(http.StatusAccepted)
			}
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
}

func writeJSON(t *testing.T, w io.Writer, msg map[string]interface{}) {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Failed to marshal JSON: %v", err)
	}
	data = append(data, '\n')
	if _, err := w.Write(data); err != nil {
		t.Fatalf("Failed to write to stdin: %v", err)
	}
}

func readJSONResponse(t *testing.T, sb *safeBuffer, timeout time.Duration) map[string]interface{} {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			t.Logf("Timeout waiting for JSON response. Buffer contents: %q", sb.String())
			return nil
		default:
			line, ok := sb.ReadLine()
			if ok && line != "" {
				var msg map[string]interface{}
				if err := json.Unmarshal([]byte(line), &msg); err == nil {
					return msg
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}
