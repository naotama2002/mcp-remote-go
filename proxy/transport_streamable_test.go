package proxy

import (
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

func TestStreamableHTTPTransportSendJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers
		if r.Header.Get(HeaderMCPProtocolVersion) != MCPProtocolVersion {
			t.Errorf("Missing or wrong protocol version header")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}
		accept := r.Header.Get("Accept")
		if !strings.Contains(accept, "application/json") || !strings.Contains(accept, "text/event-stream") {
			t.Errorf("Expected Accept to contain both application/json and text/event-stream, got %s", accept)
		}

		// Return JSON response
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(HeaderMCPSessionID, "test-session-123")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  map[string]string{"status": "ok"},
		})
	}))
	defer server.Close()

	var received []byte
	transport := NewStreamableHTTPTransport(StreamableHTTPTransportConfig{
		Endpoint: server.URL,
		Client:   &http.Client{},
		Headers:  map[string]string{},
	})
	transport.SetOnMessage(func(event string, data []byte) {
		received = data
	})

	err := transport.Send(t.Context(), []byte(`{"jsonrpc":"2.0","method":"initialize","id":1}`))
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Wait for message to be dispatched
	time.Sleep(50 * time.Millisecond)

	if received == nil {
		t.Fatal("Expected to receive a message")
	}

	var msg map[string]interface{}
	if err := json.Unmarshal(received, &msg); err != nil {
		t.Fatalf("Failed to parse received message: %v", err)
	}
	if msg["id"] != float64(1) {
		t.Errorf("Expected id=1, got %v", msg["id"])
	}

	// Verify session ID was extracted
	if sid := transport.SessionID(); sid != "test-session-123" {
		t.Errorf("Expected session ID 'test-session-123', got '%s'", sid)
	}
}

func TestStreamableHTTPTransportSendSSEResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "SSE not supported", http.StatusInternalServerError)
			return
		}

		fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":\"ok\"}\n\n")
		flusher.Flush()

		fmt.Fprintf(w, "id: evt-42\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notify\"}\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	var mu sync.Mutex
	var received []SSEEvent

	transport := NewStreamableHTTPTransport(StreamableHTTPTransportConfig{
		Endpoint: server.URL,
		Client:   &http.Client{},
	})
	transport.SetOnMessage(func(event string, data []byte) {
		mu.Lock()
		received = append(received, SSEEvent{Event: event, Data: append([]byte{}, data...)})
		mu.Unlock()
	})

	err := transport.Send(t.Context(), []byte(`{"jsonrpc":"2.0","method":"test","id":1}`))
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Wait for SSE events to be processed
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	count := len(received)
	mu.Unlock()

	if count != 2 {
		t.Fatalf("Expected 2 events, got %d", count)
	}

	// Verify last event ID was updated
	transport.mu.Lock()
	lastID := transport.lastEventID
	transport.mu.Unlock()

	if lastID != "evt-42" {
		t.Errorf("Expected last event ID 'evt-42', got '%s'", lastID)
	}
}

func TestStreamableHTTPTransportSend202Accepted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	transport := NewStreamableHTTPTransport(StreamableHTTPTransportConfig{
		Endpoint: server.URL,
		Client:   &http.Client{},
	})

	err := transport.Send(t.Context(), []byte(`{"jsonrpc":"2.0","method":"notify"}`))
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
}

func TestStreamableHTTPTransportSessionPersistence(t *testing.T) {
	var requestsMu sync.Mutex
	var sessionIDsReceived []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestsMu.Lock()
		sessionIDsReceived = append(sessionIDsReceived, r.Header.Get(HeaderMCPSessionID))
		requestsMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(HeaderMCPSessionID, "session-abc")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer server.Close()

	transport := NewStreamableHTTPTransport(StreamableHTTPTransportConfig{
		Endpoint: server.URL,
		Client:   &http.Client{},
	})
	transport.SetOnMessage(func(event string, data []byte) {})

	// First request should have no session ID
	transport.Send(t.Context(), []byte(`{"jsonrpc":"2.0","id":1}`))
	time.Sleep(50 * time.Millisecond)

	// Second request should include the session ID
	transport.Send(t.Context(), []byte(`{"jsonrpc":"2.0","id":2}`))
	time.Sleep(50 * time.Millisecond)

	requestsMu.Lock()
	defer requestsMu.Unlock()

	if len(sessionIDsReceived) < 2 {
		t.Fatalf("Expected at least 2 requests, got %d", len(sessionIDsReceived))
	}

	if sessionIDsReceived[0] != "" {
		t.Errorf("First request should have empty session ID, got '%s'", sessionIDsReceived[0])
	}
	if sessionIDsReceived[1] != "session-abc" {
		t.Errorf("Second request should have session ID 'session-abc', got '%s'", sessionIDsReceived[1])
	}
}

func TestStreamableHTTPTransportClose(t *testing.T) {
	deleteCalled := make(chan bool, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			if r.Header.Get(HeaderMCPSessionID) == "session-xyz" {
				deleteCalled <- true
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(HeaderMCPSessionID, "session-xyz")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer server.Close()

	transport := NewStreamableHTTPTransport(StreamableHTTPTransportConfig{
		Endpoint: server.URL,
		Client:   &http.Client{},
	})
	transport.SetOnMessage(func(event string, data []byte) {})

	// Send to establish session
	transport.Send(t.Context(), []byte(`{"jsonrpc":"2.0","id":1}`))
	time.Sleep(50 * time.Millisecond)

	// Close should send DELETE
	err := transport.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	select {
	case <-deleteCalled:
		// success
	case <-time.After(time.Second):
		t.Error("Expected DELETE request for session termination")
	}
}

func TestStreamableHTTPTransportAuthToken(t *testing.T) {
	var receivedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer server.Close()

	transport := NewStreamableHTTPTransport(StreamableHTTPTransportConfig{
		Endpoint: server.URL,
		Client:   &http.Client{},
		GetAuthToken: func() string {
			return "my-secret-token"
		},
	})
	transport.SetOnMessage(func(event string, data []byte) {})

	transport.Send(t.Context(), []byte(`{"jsonrpc":"2.0","id":1}`))
	time.Sleep(50 * time.Millisecond)

	if receivedAuth != "Bearer my-secret-token" {
		t.Errorf("Expected 'Bearer my-secret-token', got '%s'", receivedAuth)
	}
}

func TestStreamableHTTPTransportErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":"internal"}`)
	}))
	defer server.Close()

	transport := NewStreamableHTTPTransport(StreamableHTTPTransportConfig{
		Endpoint: server.URL,
		Client:   &http.Client{},
	})

	err := transport.Send(t.Context(), []byte(`{"jsonrpc":"2.0","id":1}`))
	if err == nil {
		t.Fatal("Expected error for 500 status")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("Expected error containing '500', got: %v", err)
	}
}

func TestStreamableHTTPTransportCustomHeaders(t *testing.T) {
	var receivedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer server.Close()

	transport := NewStreamableHTTPTransport(StreamableHTTPTransportConfig{
		Endpoint: server.URL,
		Client:   &http.Client{},
		Headers: map[string]string{
			"X-Custom-Header": "custom-value",
		},
	})
	transport.SetOnMessage(func(event string, data []byte) {})

	transport.Send(t.Context(), []byte(`{"jsonrpc":"2.0","id":1}`))
	time.Sleep(50 * time.Millisecond)

	if receivedHeaders.Get("X-Custom-Header") != "custom-value" {
		t.Errorf("Expected custom header 'custom-value', got '%s'", receivedHeaders.Get("X-Custom-Header"))
	}
}

func TestStreamableHTTPTransportNotificationStream405(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// Server does not support GET notification stream
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer server.Close()

	transport := NewStreamableHTTPTransport(StreamableHTTPTransportConfig{
		Endpoint: server.URL,
		Client:   &http.Client{},
	})
	transport.SetOnMessage(func(event string, data []byte) {})

	// Connect should succeed even if GET returns 405
	err := transport.Connect(t.Context())
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	// Send should still work
	err = transport.Send(t.Context(), []byte(`{"jsonrpc":"2.0","id":1}`))
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	transport.Close()
}

func TestStreamableHTTPTransportE2E(t *testing.T) {
	// Simulate a full MCP Streamable HTTP server
	var sessionID string
	var sessionMu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var msg map[string]interface{}
			json.Unmarshal(body, &msg)

			sessionMu.Lock()
			if sessionID == "" {
				sessionID = "e2e-session-001"
			}
			w.Header().Set(HeaderMCPSessionID, sessionID)
			sessionMu.Unlock()

			method, _ := msg["method"].(string)
			switch method {
			case "initialize":
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      msg["id"],
					"result": map[string]interface{}{
						"protocolVersion": MCPProtocolVersion,
						"capabilities":   map[string]interface{}{},
						"serverInfo": map[string]string{
							"name":    "test-server",
							"version": "1.0.0",
						},
					},
				})
			case "tools/list":
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      msg["id"],
					"result": map[string]interface{}{
						"tools": []map[string]interface{}{
							{"name": "echo", "description": "Echo input"},
						},
					},
				})
			default:
				w.WriteHeader(http.StatusAccepted)
			}

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

	var mu sync.Mutex
	var messages []map[string]interface{}

	transport := NewStreamableHTTPTransport(StreamableHTTPTransportConfig{
		Endpoint: server.URL,
		Client:   &http.Client{},
	})
	transport.SetOnMessage(func(event string, data []byte) {
		var msg map[string]interface{}
		if err := json.Unmarshal(data, &msg); err == nil {
			mu.Lock()
			messages = append(messages, msg)
			mu.Unlock()
		}
	})

	ctx := t.Context()

	// Connect
	err := transport.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	// Send initialize
	err = transport.Send(ctx, []byte(`{"jsonrpc":"2.0","method":"initialize","id":1,"params":{"protocolVersion":"2025-11-25","capabilities":{}}}`))
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Verify session ID
	if sid := transport.SessionID(); sid != "e2e-session-001" {
		t.Errorf("Expected session ID 'e2e-session-001', got '%s'", sid)
	}

	// Send tools/list
	err = transport.Send(ctx, []byte(`{"jsonrpc":"2.0","method":"tools/list","id":2}`))
	if err != nil {
		t.Fatalf("tools/list failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Verify messages received
	mu.Lock()
	defer mu.Unlock()

	if len(messages) < 2 {
		t.Fatalf("Expected at least 2 messages, got %d", len(messages))
	}

	// Verify initialize response
	if messages[0]["id"] != float64(1) {
		t.Errorf("First message should have id=1, got %v", messages[0]["id"])
	}

	// Verify tools/list response
	if messages[1]["id"] != float64(2) {
		t.Errorf("Second message should have id=2, got %v", messages[1]["id"])
	}

	// Close (should send DELETE)
	err = transport.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}
