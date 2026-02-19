package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestProxyConnectionToServer tests basic connection establishment
func TestProxyConnectionToServer(t *testing.T) {
	// Create mock SSE server
	sseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/events" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")

			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "SSE not supported", http.StatusInternalServerError)
				return
			}

			_, _ = fmt.Fprintf(w, "event: message\n")
			_, _ = fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n\n")
			flusher.Flush()
		} else {
			http.NotFound(w, r)
		}
	}))
	defer sseServer.Close()

	headers := make(map[string]string)
	proxy, err := NewProxy(sseServer.URL, 0, headers, "test-hash")
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Shutdown()

	err = proxy.connectToServer()
	if err != nil {
		t.Errorf("Failed to connect to server: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
}

// TestProxyWithAuthentication tests proxy connection with authentication headers
func TestProxyWithAuthentication(t *testing.T) {
	accessToken := "test-access-token-123"

	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/events" {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer "+accessToken {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")

			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "SSE not supported", http.StatusInternalServerError)
				return
			}

			_, _ = fmt.Fprintf(w, "event: message\n")
			_, _ = fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/authenticated\"}\n\n")
			flusher.Flush()
		} else {
			http.NotFound(w, r)
		}
	}))
	defer authServer.Close()

	headers := map[string]string{
		"Authorization": "Bearer " + accessToken,
	}
	proxy, err := NewProxy(authServer.URL, 0, headers, "auth-test-hash")
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Shutdown()

	err = proxy.connectToServer()
	if err != nil {
		t.Errorf("Failed to connect with authentication: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
}

// TestSSETransportCommandURL tests command URL resolution via SSETransport
func TestSSETransportCommandURL(t *testing.T) {
	tests := []struct {
		name            string
		serverURL       string
		commandEndpoint string
		expected        string
	}{
		{
			name:            "no_command_endpoint",
			serverURL:       "https://example.com",
			commandEndpoint: "",
			expected:        "https://example.com/message",
		},
		{
			name:            "relative_command_endpoint",
			serverURL:       "https://example.com",
			commandEndpoint: "/api/command",
			expected:        "https://example.com/api/command",
		},
		{
			name:            "absolute_command_endpoint",
			serverURL:       "https://example.com",
			commandEndpoint: "https://api.example.com/command",
			expected:        "https://api.example.com/command",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := NewSSETransport(SSETransportConfig{
				ServerURL: tt.serverURL,
				Client:    &http.Client{},
				Headers:   map[string]string{},
			})

			if tt.commandEndpoint != "" {
				transport.setCommandEndpoint(tt.commandEndpoint)
			}

			url := transport.getCommandURL()

			if url != tt.expected {
				t.Errorf("Expected URL %s, got %s", tt.expected, url)
			}
		})
	}
}

// TestProxyErrorHandling tests various error scenarios
func TestProxyErrorHandling(t *testing.T) {
	tests := []struct {
		name        string
		serverSetup func() *httptest.Server
		expectError bool
		errorType   string
	}{
		{
			name: "server_not_found",
			serverSetup: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.NotFound(w, r)
				}))
			},
			expectError: true,
			errorType:   "404",
		},
		{
			name: "unauthorized_access",
			serverSetup: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
				}))
			},
			expectError: true,
			errorType:   "auth",
		},
		{
			name: "invalid_content_type",
			serverSetup: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/plain")
					_, _ = w.Write([]byte("Not an SSE stream"))
				}))
			},
			expectError: true,
			errorType:   "content-type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := tt.serverSetup()
			defer server.Close()

			headers := make(map[string]string)
			proxy, err := NewProxy(server.URL, 0, headers, "error-test-hash")
			if err != nil {
				t.Fatalf("Failed to create proxy: %v", err)
			}
			defer proxy.Shutdown()

			err = proxy.connectToServer()

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else if !strings.Contains(strings.ToLower(err.Error()), tt.errorType) {
					t.Errorf("Expected error containing '%s', got: %v", tt.errorType, err)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

// TestSSETransportSendToServer tests sending messages via SSETransport
func TestSSETransportSendToServer(t *testing.T) {
	messageReceived := make(chan string, 1)

	commandServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/command" {
			body := make([]byte, 1024)
			n, _ := r.Body.Read(body)
			messageReceived <- string(body[:n])

			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":"success"}`)
		}
	}))
	defer commandServer.Close()

	headers := make(map[string]string)

	// Create an SSE transport and set command endpoint directly
	transport := NewSSETransport(SSETransportConfig{
		ServerURL: commandServer.URL,
		Client:    &http.Client{},
		Headers:   headers,
	})
	transport.setCommandEndpoint(commandServer.URL + "/command")

	testMessage := `{"jsonrpc":"2.0","id":1,"method":"test","params":{}}`
	err := transport.Send(t.Context(), []byte(testMessage))
	if err != nil {
		t.Errorf("Failed to send message: %v", err)
	}

	select {
	case received := <-messageReceived:
		if !strings.Contains(received, "test") {
			t.Errorf("Expected message containing 'test', got: %s", received)
		}
	case <-time.After(1 * time.Second):
		t.Error("Timeout waiting for message to be received")
	}
}

// TestConcurrentOperations tests concurrent proxy operations
func TestConcurrentOperations(t *testing.T) {
	messagesReceived := make(chan string, 20)
	var receivedMux sync.Mutex

	concurrentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/command" {
			body := make([]byte, 1024)
			n, _ := r.Body.Read(body)

			receivedMux.Lock()
			messagesReceived <- string(body[:n])
			receivedMux.Unlock()

			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":"processed"}`)
		}
	}))
	defer concurrentServer.Close()

	headers := make(map[string]string)
	proxy, err := NewProxy(concurrentServer.URL, 0, headers, "concurrent-test-hash")
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Shutdown()

	// Create an SSE transport with command endpoint set
	transport := NewSSETransport(SSETransportConfig{
		ServerURL: concurrentServer.URL,
		Client:    proxy.client,
		Headers:   headers,
	})
	transport.setCommandEndpoint(concurrentServer.URL + "/command")

	const numMessages = 10
	var wg sync.WaitGroup
	wg.Add(numMessages)

	for i := 0; i < numMessages; i++ {
		go func(id int) {
			defer wg.Done()
			message := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"concurrent-test-%d","params":{}}`, id, id)
			err := transport.Send(proxy.ctx, []byte(message))
			if err != nil {
				t.Errorf("Failed to send message %d: %v", id, err)
			}
		}(i)
	}

	wg.Wait()

	receivedCount := 0
	timeout := time.After(2 * time.Second)
	for {
		select {
		case <-messagesReceived:
			receivedCount++
			if receivedCount >= numMessages {
				goto done
			}
		case <-timeout:
			goto done
		}
	}

done:
	if receivedCount != numMessages {
		t.Errorf("Expected %d messages to be received, got %d", numMessages, receivedCount)
	}
}

// TestGracefulShutdown tests proper resource cleanup during shutdown
func TestGracefulShutdown(t *testing.T) {
	shutdownServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")

			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "SSE not supported", http.StatusInternalServerError)
				return
			}

			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()

			for {
				select {
				case <-ticker.C:
					_, _ = fmt.Fprintf(w, "event: message\n")
					_, _ = fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"keepalive\"}\n\n")
					flusher.Flush()
				case <-r.Context().Done():
					return
				}
			}
		}
	}))
	defer shutdownServer.Close()

	headers := make(map[string]string)
	proxy, err := NewProxy(shutdownServer.URL, 0, headers, "shutdown-test-hash")
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	err = proxy.connectToServer()
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	shutdownStart := time.Now()
	proxy.Shutdown()

	shutdownDuration := time.Since(shutdownStart)
	if shutdownDuration > 1*time.Second {
		t.Errorf("Shutdown took too long: %v", shutdownDuration)
	}
}

// TestProxyReconnectionBehavior tests connection recovery after failures
func TestProxyReconnectionBehavior(t *testing.T) {
	connectionAttempts := 0
	var serverMux sync.Mutex

	reconnectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			serverMux.Lock()
			connectionAttempts++
			attempts := connectionAttempts
			serverMux.Unlock()

			if attempts <= 2 {
				http.Error(w, "Server temporarily unavailable", http.StatusServiceUnavailable)
				return
			}

			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")

			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "SSE not supported", http.StatusInternalServerError)
				return
			}

			_, _ = fmt.Fprintf(w, "event: message\n")
			_, _ = fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/reconnected\"}\n\n")
			flusher.Flush()
		}
	}))
	defer reconnectServer.Close()

	headers := make(map[string]string)
	proxy, err := NewProxy(reconnectServer.URL, 0, headers, "reconnect-test-hash")
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Shutdown()

	for i := 0; i < 3; i++ {
		err = proxy.connectToServer()
		if i < 2 {
			if err == nil {
				t.Errorf("Expected connection attempt %d to fail", i+1)
			}
		} else {
			if err != nil {
				t.Errorf("Expected connection attempt %d to succeed, got: %v", i+1, err)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	serverMux.Lock()
	attempts := connectionAttempts
	serverMux.Unlock()

	if attempts != 3 {
		t.Errorf("Expected 3 connection attempts, got %d", attempts)
	}
}
