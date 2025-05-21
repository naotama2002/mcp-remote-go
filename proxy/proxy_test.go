package proxy

import (
	"bufio"
	"bytes"
	"context"
	// "errors" // Removed as it's unused
	"fmt" // For TestSendToServer_DynamicURL server handler
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	// "os" // Not strictly needed if using log.Writer() for default
	"sync"
	"testing"

	"github.com/naotama2002/mcp-remote-go/auth" // Required for auth.Coordinator
)

// newTestProxy creates a Proxy instance for testing.
// It initializes fields to usable defaults for most tests.
// authCoord is initialized with the real auth.NewCoordinator. Tests should
// be aware that LoadTokens might fail (e.g. no token file), which is
// handled by sendToServer (proceeds without auth header).
func newTestProxy(tb testing.TB) *Proxy {
	var stdoutBuf bytes.Buffer // Default buffer, can be replaced by test
	ctx, cancel := context.WithCancel(context.Background())

	authCoord, err := auth.NewCoordinator("test-hash-for-proxy-test-"+tb.Name(), 12345)
	if err != nil {
		tb.Fatalf("Failed to create auth.Coordinator for test proxy: %v", err)
	}

	return &Proxy{
		stdioWriter:    bufio.NewWriter(&stdoutBuf),
		dynamicPostURL: "", // Default to empty
		postURLMutex:   sync.RWMutex{},
		client:         http.DefaultClient,
		ctx:            ctx,
		cancel:         cancel,
		headers:        make(map[string]string),
		authCoord:      authCoord,
	}
}

func TestHandleServerMessage_EndpointEvent(t *testing.T) {
	originalLogOutput := log.Writer()
	log.SetOutput(io.Discard) // Discard logs for cleaner test output
	defer log.SetOutput(originalLogOutput) // Restore log output

	tests := []struct {
		name                   string
		proxyInitialServerURL  string // p.serverURL to set before calling handleServerMessage
		event                  string
		data                   string // Raw URI from endpoint event
		initialDynamicPostURL  string // Value of dynamicPostURL before this event
		expectedDynamicPostURL string // Expected value after this event
		expectWriteToStdout    bool
		// expectedStdoutData is only relevant if expectWriteToStdout is true
		expectedStdoutData     string 
	}{
		{
			name:                   "endpoint event with absolute URL",
			proxyInitialServerURL:  "https://base.com/sse",
			event:                  "endpoint",
			data:                   "https://absolute.com/post_endpoint",
			initialDynamicPostURL:  "",
			expectedDynamicPostURL: "https://absolute.com/post_endpoint",
			expectWriteToStdout:    false,
		},
		{
			name:                   "endpoint event with root relative path",
			proxyInitialServerURL:  "https://example.com/sse_base_path/",
			event:                  "endpoint",
			data:                   "/api/post_endpoint",
			initialDynamicPostURL:  "",
			expectedDynamicPostURL: "https://example.com/api/post_endpoint",
			expectWriteToStdout:    false,
		},
		{
			name:                   "endpoint event with sub-path relative",
			proxyInitialServerURL:  "https://example.com/sse_base_path/sub/",
			event:                  "endpoint",
			data:                   "action/post",
			initialDynamicPostURL:  "",
			expectedDynamicPostURL: "https://example.com/sse_base_path/sub/action/post",
			expectWriteToStdout:    false,
		},
		{
			name:                   "endpoint event with relative path going up",
			proxyInitialServerURL:  "https://example.com/sse_base_path/sub/",
			event:                  "endpoint",
			data:                   "../another_api/post",
			initialDynamicPostURL:  "",
			expectedDynamicPostURL: "https://example.com/sse_base_path/another_api/post",
			expectWriteToStdout:    false,
		},
		{
			name:                   "endpoint event with relative path and whitespace",
			proxyInitialServerURL:  "https://example.com/foo/",
			event:                  "endpoint",
			data:                   "  /bar/baz  ",
			initialDynamicPostURL:  "",
			expectedDynamicPostURL: "https://example.com/bar/baz",
			expectWriteToStdout:    false,
		},
		{
			name:                   "endpoint event with empty data string, dynamicPostURL should not change",
			proxyInitialServerURL:  "https://example.com/",
			event:                  "endpoint",
			data:                   "  ", // Whitespace only
			initialDynamicPostURL:  "http://shouldnotchange.com",
			expectedDynamicPostURL: "http://shouldnotchange.com",
			expectWriteToStdout:    false,
		},
		{
			name:                   "endpoint event with unparseable URI, dynamicPostURL should not change",
			proxyInitialServerURL:  "https://example.com/",
			event:                  "endpoint",
			data:                   ":badscheme", // This will cause url.Parse to error
			initialDynamicPostURL:  "http://initial.com/value",
			expectedDynamicPostURL: "http://initial.com/value",
			expectWriteToStdout:    false,
		},
		{
			name:                   "message event, dynamicPostURL should not change",
			proxyInitialServerURL:  "https://example.com/", // Base URL context for this test
			event:                  "message",
			data:                   `{"id":1,"method":"test"}`,
			initialDynamicPostURL:  "https://original.com/post_path",
			expectedDynamicPostURL: "https://original.com/post_path",
			expectWriteToStdout:    true,
			expectedStdoutData:     `{"id":1,"method":"test"}` + "\n",
		},
		{
			name:                   "unknown event, dynamicPostURL should not change",
			proxyInitialServerURL:  "https://example.com/",
			event:                  "someotherstuff",
			data:                   "somedata",
			initialDynamicPostURL:  "https://anotheroriginal.com/post_path",
			expectedDynamicPostURL: "https://anotheroriginal.com/post_path",
			expectWriteToStdout:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Minimal proxy setup for this test to directly control serverURL and initialDynamicPostURL
			var stdoutBuf bytes.Buffer
			p := &Proxy{
				serverURL:      tt.proxyInitialServerURL,
				stdioWriter:    bufio.NewWriter(&stdoutBuf),
				dynamicPostURL: tt.initialDynamicPostURL,
				postURLMutex:   sync.RWMutex{}, // Initialize mutex
				// Other fields (authCoord, client, ctx, cancel, headers) are not strictly needed by handleServerMessage.
				// If handleServerMessage were to use them, they'd need to be initialized appropriately.
			}

			p.handleServerMessage(tt.event, []byte(tt.data))

			p.postURLMutex.RLock()
			if p.dynamicPostURL != tt.expectedDynamicPostURL {
				t.Errorf("For event data '%s', base URL '%s', initial dynamicURL '%s', expected dynamicPostURL to be '%s', got '%s'",
					tt.data, tt.proxyInitialServerURL, tt.initialDynamicPostURL, tt.expectedDynamicPostURL, p.dynamicPostURL)
			}
			p.postURLMutex.RUnlock()
			
			if err := p.stdioWriter.Flush(); err != nil {
				t.Fatalf("Failed to flush stdioWriter: %v", err)
			}

			if tt.expectWriteToStdout {
				if stdoutBuf.Len() == 0 {
					t.Errorf("Expected data to be written to stdout, but buffer is empty")
				}
				if tt.expectedStdoutData != "" && stdoutBuf.String() != tt.expectedStdoutData {
					t.Errorf("Expected stdout data '%s', got '%s'", tt.expectedStdoutData, stdoutBuf.String())
				}
			} else {
				if stdoutBuf.Len() > 0 {
					t.Errorf("Expected no data to be written to stdout for event '%s', but got: %s", tt.event, stdoutBuf.String())
				}
			}
		})
	}
}

func TestSendToServer_DynamicURL(t *testing.T) {
	originalLogOutput := log.Writer()
	log.SetOutput(io.Discard) 
	defer log.SetOutput(originalLogOutput)

	t.Run("dynamicPostURL not set", func(t *testing.T) {
		requestReceived := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestReceived = true 
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		p := newTestProxy(t) 
		defer p.cancel()

		p.eventSource = &EventSource{} 

		p.postURLMutex.RLock()
		if p.dynamicPostURL != "" {
			// tb.Fatalf() is not available in t.Run, use t.Fatalf()
			p.postURLMutex.RUnlock() // Release lock before Fatalf
			t.Fatalf("Initial dynamicPostURL should be empty for this test case, but was '%s'", p.dynamicPostURL)
		}
		p.postURLMutex.RUnlock()

		err := p.sendToServer("test message for no dynamic URL")
		if err == nil {
			t.Fatalf("Expected an error when dynamicPostURL is not set, but got nil")
		}
		expectedErr := "POST endpoint not yet known from server"
		if err.Error() != expectedErr {
			t.Errorf("Expected error message '%s', got '%s'", expectedErr, err.Error())
		}

		if requestReceived {
			t.Errorf("Server should not have received a request when dynamicPostURL is not set, but it did.")
		}
	})

	t.Run("dynamicPostURL is set", func(t *testing.T) {
		testMessageBody := "test message for dynamic URL"
		requestReceivedAtServer := false

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestReceivedAtServer = true
			if r.Method != http.MethodPost {
				t.Errorf("Expected POST request, got %s", r.Method)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				// t.Fatalf inside an HTTP handler might not behave as expected,
				// but t.Errorf is fine. For fatal errors, better to panic or signal failure.
				// For test simplicity, t.Errorf is okay.
				http.Error(w, "Failed to read body", http.StatusInternalServerError)
				t.Errorf("Error reading request body on mock server: %v", err)
				return
			}
			if string(body) != testMessageBody {
				t.Errorf("Expected body '%s', got '%s'", testMessageBody, string(body))
			}
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "OK") 
		}))
		defer server.Close()

		p := newTestProxy(t) 
		defer p.cancel()

		p.eventSource = &EventSource{} 

		p.postURLMutex.Lock()
		p.dynamicPostURL = server.URL 
		p.postURLMutex.Unlock()

		err := p.sendToServer(testMessageBody)
		if err != nil {
			t.Errorf("Expected no error when dynamicPostURL is set, but got: %v", err)
		}

		if !requestReceivedAtServer {
			t.Errorf("Server did not receive the request, but it should have.")
		}
	})
}
