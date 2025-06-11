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
		// Handle both root path and /events path for proxy connection
		if r.URL.Path == "/" || r.URL.Path == "/events" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")

			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "SSE not supported", http.StatusInternalServerError)
				return
			}

			// Send test message
			_, _ = fmt.Fprintf(w, "event: message\n")
			_, _ = fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n\n")
			flusher.Flush()
		} else {
			http.NotFound(w, r)
		}
	}))
	defer sseServer.Close()

	// Create proxy
	headers := make(map[string]string)
	proxy, err := NewProxy(sseServer.URL, 0, headers, "test-hash")
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Shutdown()

	// Test connection
	err = proxy.connectToServer()
	if err != nil {
		t.Errorf("Failed to connect to server: %v", err)
	}

	// Give connection time to establish
	time.Sleep(100 * time.Millisecond)
}

// TestProxyWithAuthentication tests proxy connection with authentication headers
func TestProxyWithAuthentication(t *testing.T) {
	accessToken := "test-access-token-123"

	// Create mock server that requires authentication
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/events" {
			// Check authorization header
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

	// Create proxy with authentication headers
	headers := map[string]string{
		"Authorization": "Bearer " + accessToken,
	}
	proxy, err := NewProxy(authServer.URL, 0, headers, "auth-test-hash")
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Shutdown()

	// Test authenticated connection
	err = proxy.connectToServer()
	if err != nil {
		t.Errorf("Failed to connect with authentication: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
}

// TestProxyCommandURL tests command URL resolution
func TestProxyCommandURL(t *testing.T) {
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
			headers := make(map[string]string)
			proxy, err := NewProxy(tt.serverURL, 0, headers, "test-hash")
			if err != nil {
				t.Fatalf("Failed to create proxy: %v", err)
			}
			defer proxy.Shutdown()

			if tt.commandEndpoint != "" {
				proxy.SetCommandEndpoint(tt.commandEndpoint)
			}

			url := proxy.getCommandURL()

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
			errorType:   "401",
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

// TestProxySendToServer tests sending messages to the server
func TestProxySendToServer(t *testing.T) {
	messageReceived := make(chan string, 1)

	// Create mock server that captures sent messages
	commandServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/command" {
			// Read the message
			body := make([]byte, 1024)
			n, _ := r.Body.Read(body)
			messageReceived <- string(body[:n])

			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":"success"}`)
		}
	}))
	defer commandServer.Close()

	headers := make(map[string]string)
	proxy, err := NewProxy(commandServer.URL, 0, headers, "send-test-hash")
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Shutdown()

	proxy.SetCommandEndpoint(commandServer.URL + "/command")

	// Send test message
	testMessage := `{"jsonrpc":"2.0","id":1,"method":"test","params":{}}`
	err = proxy.sendToServer(testMessage)
	if err != nil {
		t.Errorf("Failed to send message: %v", err)
	}

	// Verify message was received
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

	// Create server that handles multiple concurrent requests
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

	proxy.SetCommandEndpoint(concurrentServer.URL + "/command")

	// Send multiple messages concurrently
	const numMessages = 10
	var wg sync.WaitGroup
	wg.Add(numMessages)

	for i := 0; i < numMessages; i++ {
		go func(id int) {
			defer wg.Done()
			message := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"concurrent-test-%d","params":{}}`, id, id)
			err := proxy.sendToServer(message)
			if err != nil {
				t.Errorf("Failed to send message %d: %v", id, err)
			}
		}(i)
	}

	wg.Wait()

	// Verify all messages were received
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
	// Create server with delayed responses
	shutdownServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/events" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")

			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "SSE not supported", http.StatusInternalServerError)
				return
			}

			// Keep connection alive until context is cancelled
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

	// Start connection
	err = proxy.connectToServer()
	if err != nil {
		t.Errorf("Failed to connect: %v", err)
	}

	// Give connection time to establish
	time.Sleep(200 * time.Millisecond)

	// Test graceful shutdown
	shutdownStart := time.Now()
	proxy.Shutdown()

	// Verify shutdown completes quickly
	shutdownDuration := time.Since(shutdownStart)
	if shutdownDuration > 1*time.Second {
		t.Errorf("Shutdown took too long: %v", shutdownDuration)
	}
}

// TestProxyReconnectionBehavior tests connection recovery after failures
func TestProxyReconnectionBehavior(t *testing.T) {
	connectionAttempts := 0
	var serverMux sync.Mutex

	// Create server that fails first few attempts
	reconnectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/events" {
			serverMux.Lock()
			connectionAttempts++
			attempts := connectionAttempts
			serverMux.Unlock()

			// Fail first 2 connection attempts
			if attempts <= 2 {
				http.Error(w, "Server temporarily unavailable", http.StatusServiceUnavailable)
				return
			}

			// Succeed on 3rd attempt
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

	// Test multiple connection attempts
	for i := 0; i < 3; i++ {
		err = proxy.connectToServer()
		if i < 2 {
			// First 2 attempts should fail
			if err == nil {
				t.Errorf("Expected connection attempt %d to fail", i+1)
			}
		} else {
			// 3rd attempt should succeed
			if err != nil {
				t.Errorf("Expected connection attempt %d to succeed, got: %v", i+1, err)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Verify connection attempts
	serverMux.Lock()
	attempts := connectionAttempts
	serverMux.Unlock()

	if attempts != 3 {
		t.Errorf("Expected 3 connection attempts, got %d", attempts)
	}
}
