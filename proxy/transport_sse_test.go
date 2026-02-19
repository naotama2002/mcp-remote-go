package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSSETransportConnect(t *testing.T) {
	messageReceived := make(chan SSEEvent, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "SSE not supported", http.StatusInternalServerError)
			return
		}

		// Send endpoint event
		fmt.Fprintf(w, "event: endpoint\ndata: /message\n\n")
		flusher.Flush()

		// Send a message
		fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1}\n\n")
		flusher.Flush()

		// Keep alive until client disconnects
		<-r.Context().Done()
	}))
	defer server.Close()

	transport := NewSSETransport(SSETransportConfig{
		ServerURL: server.URL,
		Client:    &http.Client{},
		Headers:   map[string]string{},
	})

	transport.SetOnMessage(func(event string, data []byte) {
		messageReceived <- SSEEvent{Event: event, Data: data}
	})

	if err := transport.Connect(t.Context()); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer transport.Close()

	// Wait for message
	select {
	case msg := <-messageReceived:
		if msg.Event != "message" {
			t.Errorf("Expected event 'message', got '%s'", msg.Event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for message")
	}

	// Verify command endpoint was set via the endpoint event
	endpoint := transport.getCommandEndpointValue()
	if endpoint != "/message" {
		t.Errorf("Expected command endpoint '/message', got '%s'", endpoint)
	}
}

func TestSSETransportSend(t *testing.T) {
	receivedBody := make(chan string, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 1024)
		n, _ := r.Body.Read(body)
		receivedBody <- string(body[:n])

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := NewSSETransport(SSETransportConfig{
		ServerURL: server.URL,
		Client:    &http.Client{},
		Headers:   map[string]string{"X-Custom": "test"},
	})
	transport.setCommandEndpoint(server.URL)

	err := transport.Send(t.Context(), []byte(`{"jsonrpc":"2.0","method":"ping","id":1}`))
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	select {
	case body := <-receivedBody:
		if body != `{"jsonrpc":"2.0","method":"ping","id":1}` {
			t.Errorf("Unexpected body: %s", body)
		}
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for request")
	}
}

func TestSSETransportSessionID(t *testing.T) {
	transport := NewSSETransport(SSETransportConfig{
		ServerURL: "https://example.com",
		Client:    &http.Client{},
	})

	if sid := transport.SessionID(); sid != "" {
		t.Errorf("Expected empty session ID, got '%s'", sid)
	}
}

func TestSSETransportGetCommandURL(t *testing.T) {
	tests := []struct {
		name            string
		serverURL       string
		commandEndpoint string
		expected        string
	}{
		{
			name:      "fallback to /message",
			serverURL: "https://example.com/sse",
			expected:  "https://example.com/message",
		},
		{
			name:            "relative endpoint",
			serverURL:       "https://example.com/sse",
			commandEndpoint: "/api/command",
			expected:        "https://example.com/api/command",
		},
		{
			name:            "absolute endpoint",
			serverURL:       "https://example.com/sse",
			commandEndpoint: "https://other.example.com/cmd",
			expected:        "https://other.example.com/cmd",
		},
		{
			name:            "server with port",
			serverURL:       "https://example.com:8080/sse",
			commandEndpoint: "/cmd",
			expected:        "https://example.com:8080/cmd",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := NewSSETransport(SSETransportConfig{
				ServerURL: tt.serverURL,
				Client:    &http.Client{},
			})
			if tt.commandEndpoint != "" {
				transport.setCommandEndpoint(tt.commandEndpoint)
			}

			result := transport.getCommandURL()
			if result != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestSSETransportAuthToken(t *testing.T) {
	var receivedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := NewSSETransport(SSETransportConfig{
		ServerURL: server.URL,
		Client:    &http.Client{},
		GetAuthToken: func() string {
			return "test-token"
		},
	})
	transport.setCommandEndpoint(server.URL)

	err := transport.Send(t.Context(), []byte(`{}`))
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	if receivedAuth != "Bearer test-token" {
		t.Errorf("Expected 'Bearer test-token', got '%s'", receivedAuth)
	}
}
