package proxy

import (
	"context"
	"testing"
	"time"
)

func TestNewProxy(t *testing.T) {
	tests := []struct {
		name          string
		serverURL     string
		callbackPort  int
		headers       map[string]string
		serverURLHash string
		expectError   bool
	}{
		{
			name:          "valid proxy creation",
			serverURL:     "https://example.com",
			callbackPort:  3334,
			headers:       map[string]string{"Authorization": "Bearer token"},
			serverURLHash: "test-hash",
			expectError:   false,
		},
		{
			name:          "empty server URL",
			serverURL:     "",
			callbackPort:  3334,
			headers:       map[string]string{},
			serverURLHash: "test-hash",
			expectError:   false, // NewProxy doesn't validate server URL
		},
		{
			name:          "invalid port",
			serverURL:     "https://example.com",
			callbackPort:  -1,
			headers:       map[string]string{},
			serverURLHash: "test-hash",
			expectError:   false, // NewProxy doesn't validate port
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy, err := NewProxy(tt.serverURL, tt.callbackPort, tt.headers, tt.serverURLHash)

			if tt.expectError && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if err == nil {
				// Verify proxy is properly initialized
				if proxy.serverURL != tt.serverURL {
					t.Errorf("Expected serverURL %s, got %s", tt.serverURL, proxy.serverURL)
				}
				if proxy.callbackPort != tt.callbackPort {
					t.Errorf("Expected callbackPort %d, got %d", tt.callbackPort, proxy.callbackPort)
				}
				if proxy.serverURLHash != tt.serverURLHash {
					t.Errorf("Expected serverURLHash %s, got %s", tt.serverURLHash, proxy.serverURLHash)
				}
				if proxy.ctx == nil {
					t.Error("Context should not be nil")
				}
				if proxy.cancel == nil {
					t.Error("Cancel function should not be nil")
				}
				if proxy.authCoord == nil {
					t.Error("Auth coordinator should not be nil")
				}
				if proxy.client == nil {
					t.Error("HTTP client should not be nil")
				}

				// Verify headers
				for k, v := range tt.headers {
					if proxy.headers[k] != v {
						t.Errorf("Expected header %s: %s, got %s", k, v, proxy.headers[k])
					}
				}

				// Clean up resources
				proxy.cancel()
			}
		})
	}
}

func TestShutdown(t *testing.T) {
	proxy, err := NewProxy("https://example.com", 3334, map[string]string{}, "test-hash")
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	// Verify context is not cancelled initially
	select {
	case <-proxy.ctx.Done():
		t.Error("Context should not be cancelled initially")
	default:
		// Expected
	}

	// Call Shutdown
	proxy.Shutdown()

	// Verify context is cancelled
	select {
	case <-proxy.ctx.Done():
		// Expected
	case <-time.After(time.Second):
		t.Error("Context should be cancelled after shutdown")
	}
}

func TestProxyContext(t *testing.T) {
	proxy, err := NewProxy("https://example.com", 3334, map[string]string{}, "test-hash")
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.cancel()

	// Verify context is properly set
	if proxy.ctx == nil {
		t.Error("Context should not be nil")
	}

	// Verify cancel function is properly set
	if proxy.cancel == nil {
		t.Error("Cancel function should not be nil")
	}

	// Verify context is valid
	if proxy.ctx.Err() != nil {
		t.Errorf("Context should be valid, got error: %v", proxy.ctx.Err())
	}

	// Execute cancel
	proxy.cancel()

	// Verify context is cancelled
	select {
	case <-proxy.ctx.Done():
		if proxy.ctx.Err() != context.Canceled {
			t.Errorf("Expected context.Canceled, got %v", proxy.ctx.Err())
		}
	case <-time.After(time.Second):
		t.Error("Context should be cancelled")
	}
}
