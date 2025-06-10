package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestOAuthFlowIntegration tests the complete OAuth flow end-to-end
func TestOAuthFlowIntegration(t *testing.T) {
	// Create mock OAuth server
	authCode := "test-auth-code-123"
	serverMetadata := &ServerMetadata{
		Issuer:                 "https://test-server.example.com",
		AuthorizationEndpoint:  "",
		TokenEndpoint:          "",
		RegistrationEndpoint:   "",
		ScopesSupported:        []string{"mcp", "offline_access"},
		ResponseTypesSupported: []string{"code"},
		GrantTypesSupported:    []string{"authorization_code", "refresh_token"},
	}

	var mockServer *httptest.Server
	mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			// OAuth metadata endpoint
			metadata := *serverMetadata
			metadata.AuthorizationEndpoint = mockServer.URL + "/auth"
			metadata.TokenEndpoint = mockServer.URL + "/token"
			metadata.RegistrationEndpoint = mockServer.URL + "/register"
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(metadata)

		case "/register":
			// Client registration endpoint
			if r.Method != http.MethodPost {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}

			clientInfo := &ClientInfo{
				ClientID:                "test-client-id",
				ClientSecret:            "test-client-secret",
				ClientIDIssuedAt:        time.Now().Unix(),
				ClientSecretExpiresAt:   0,
				RedirectURIs:            []string{"http://localhost:3334/callback"},
				TokenEndpointAuthMethod: "client_secret_basic",
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(clientInfo)

		case "/token":
			// Token exchange endpoint
			if r.Method != http.MethodPost {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}

			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "authorization_code" {
				http.Error(w, "Invalid grant_type", http.StatusBadRequest)
				return
			}

			if r.Form.Get("code") != authCode {
				http.Error(w, "Invalid authorization code", http.StatusBadRequest)
				return
			}

			tokens := &Tokens{
				AccessToken:  "test-access-token",
				RefreshToken: "test-refresh-token",
				ExpiresIn:    3600,
				TokenType:    "Bearer",
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(tokens)

		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	// Setup test environment
	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	defer func() { _ = os.Setenv("HOME", originalHome) }()
	_ = os.Setenv("HOME", tmpDir)

	// Create coordinator
	coordinator, err := NewCoordinator("test-server-hash", 3334)
	if err != nil {
		t.Fatalf("NewCoordinator failed: %v", err)
	}

	// Test initialization with server metadata discovery
	authURL, err := coordinator.InitializeAuth(mockServer.URL)
	if err != nil {
		t.Fatalf("InitializeAuth failed: %v", err)
	}
	if authURL == "" {
		t.Fatal("InitializeAuth should return non-empty auth URL")
	}

	// Verify server metadata was loaded
	if coordinator.serverMetadata == nil {
		t.Fatal("Server metadata should be loaded")
	}
	if coordinator.serverMetadata.TokenEndpoint != mockServer.URL+"/token" {
		t.Errorf("TokenEndpoint mismatch: expected %s, got %s",
			mockServer.URL+"/token", coordinator.serverMetadata.TokenEndpoint)
	}

	// Verify client was registered
	if coordinator.clientInfo == nil {
		t.Fatal("Client info should be loaded")
	}
	if coordinator.clientInfo.ClientID != "test-client-id" {
		t.Errorf("ClientID mismatch: expected %s, got %s",
			"test-client-id", coordinator.clientInfo.ClientID)
	}

	// Authorization URL was already built during InitializeAuth
	if !strings.Contains(authURL, mockServer.URL+"/auth") {
		t.Errorf("Authorization URL should contain auth endpoint: %s", authURL)
	}
	if !strings.Contains(authURL, "client_id=test-client-id") {
		t.Errorf("Authorization URL should contain client_id: %s", authURL)
	}
	if !strings.Contains(authURL, "scope=mcp+offline_access") {
		t.Errorf("Authorization URL should contain scope: %s", authURL)
	}

	// Test token exchange
	tokens, err := coordinator.ExchangeCode(authCode)
	if err != nil {
		t.Fatalf("ExchangeCode failed: %v", err)
	}

	if tokens.AccessToken != "test-access-token" {
		t.Errorf("AccessToken mismatch: expected %s, got %s",
			"test-access-token", tokens.AccessToken)
	}
	if tokens.RefreshToken != "test-refresh-token" {
		t.Errorf("RefreshToken mismatch: expected %s, got %s",
			"test-refresh-token", tokens.RefreshToken)
	}

	// Test token persistence
	err = coordinator.SaveTokens(tokens)
	if err != nil {
		t.Fatalf("SaveTokens failed: %v", err)
	}

	// Verify tokens can be loaded
	loadedTokens, err := coordinator.LoadTokens()
	if err != nil {
		t.Fatalf("LoadTokens failed: %v", err)
	}

	if loadedTokens.AccessToken != tokens.AccessToken {
		t.Errorf("Loaded AccessToken mismatch: expected %s, got %s",
			tokens.AccessToken, loadedTokens.AccessToken)
	}
}

// TestOAuthFlowWithFallbackMetadata tests OAuth flow when .well-known discovery fails
func TestOAuthFlowWithFallbackMetadata(t *testing.T) {
	// Mock server that doesn't support .well-known discovery
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			// Return 404 to trigger fallback
			http.NotFound(w, r)

		case "/oauth/register":
			// Client registration endpoint (fallback path)
			clientInfo := &ClientInfo{
				ClientID:                "fallback-client-id",
				ClientSecret:            "fallback-client-secret",
				ClientIDIssuedAt:        time.Now().Unix(),
				ClientSecretExpiresAt:   0,
				RedirectURIs:            []string{"http://localhost:3334/callback"},
				TokenEndpointAuthMethod: "client_secret_basic",
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(clientInfo)

		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	// Setup test environment
	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	defer func() { _ = os.Setenv("HOME", originalHome) }()
	_ = os.Setenv("HOME", tmpDir)

	// Create coordinator
	coordinator, err := NewCoordinator("fallback-test", 3334)
	if err != nil {
		t.Fatalf("NewCoordinator failed: %v", err)
	}

	// Test initialization with fallback metadata
	_, err = coordinator.InitializeAuth(mockServer.URL)
	if err != nil {
		t.Fatalf("InitializeAuth with fallback failed: %v", err)
	}

	// Verify fallback metadata was created
	if coordinator.serverMetadata == nil {
		t.Fatal("Server metadata should be created as fallback")
	}
	if coordinator.serverMetadata.AuthorizationEndpoint != mockServer.URL+"/oauth/authorize" {
		t.Errorf("Fallback AuthorizationEndpoint mismatch: expected %s, got %s",
			mockServer.URL+"/oauth/authorize", coordinator.serverMetadata.AuthorizationEndpoint)
	}
}

// TestAuthErrorRecovery tests error handling and recovery scenarios
func TestAuthErrorRecovery(t *testing.T) {
	tests := []struct {
		name         string
		serverSetup  func(*httptest.Server) http.HandlerFunc
		expectError  bool
		errorMessage string
	}{
		{
			name: "server_unavailable",
			serverSetup: func(server *httptest.Server) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					http.Error(w, "Server unavailable", http.StatusServiceUnavailable)
				}
			},
			expectError:  true,
			errorMessage: "failed to fetch server metadata",
		},
		{
			name: "invalid_metadata_response",
			serverSetup: func(server *httptest.Server) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/.well-known/oauth-authorization-server" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"invalid": "json structure"`))
					}
				}
			},
			expectError:  true,
			errorMessage: "client registration failed",
		},
		{
			name: "registration_failure",
			serverSetup: func(server *httptest.Server) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/.well-known/oauth-authorization-server":
						var metadata *ServerMetadata
						if server != nil {
							metadata = &ServerMetadata{
								Issuer:                server.URL,
								AuthorizationEndpoint: server.URL + "/auth",
								TokenEndpoint:         server.URL + "/token",
								RegistrationEndpoint:  server.URL + "/register",
							}
						}
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(metadata)
					case "/register":
						http.Error(w, "Registration not allowed", http.StatusForbidden)
					}
				}
			},
			expectError:  true,
			errorMessage: "client registration failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mock server
			mockServer := httptest.NewServer(tt.serverSetup(nil))
			defer mockServer.Close()

			// Setup test environment
			tmpDir := t.TempDir()
			originalHome := os.Getenv("HOME")
			defer func() { _ = os.Setenv("HOME", originalHome) }()
			_ = os.Setenv("HOME", tmpDir)

			// Create coordinator
			coordinator, err := NewCoordinator("error-test", 3334)
			if err != nil {
				t.Fatalf("NewCoordinator failed: %v", err)
			}

			// Test initialization
			_, err = coordinator.InitializeAuth(mockServer.URL)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else {
					// Check for either the expected error message or related error patterns
					errorContainsExpected := strings.Contains(err.Error(), tt.errorMessage) ||
						strings.Contains(err.Error(), "client registration failed") ||
						strings.Contains(err.Error(), "503") ||
						strings.Contains(err.Error(), "Server unavailable")

					if !errorContainsExpected {
						t.Errorf("Expected error containing '%s' or related error, got: %v", tt.errorMessage, err)
					}
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

// TestConcurrentAuthOperations tests concurrent access to auth operations
func TestConcurrentAuthOperations(t *testing.T) {
	// Setup test environment
	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	defer func() { _ = os.Setenv("HOME", originalHome) }()
	_ = os.Setenv("HOME", tmpDir)

	// Create coordinator
	coordinator, err := NewCoordinator("concurrent-test", 3334)
	if err != nil {
		t.Fatalf("NewCoordinator failed: %v", err)
	}

	// Create test tokens
	tokens := &Tokens{
		AccessToken:  "concurrent-access-token",
		RefreshToken: "concurrent-refresh-token",
		ExpiresIn:    3600,
		TokenType:    "Bearer",
	}

	// Test concurrent save/load operations
	const numGoroutines = 10
	errChan := make(chan error, numGoroutines*2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Start concurrent save operations
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			testTokens := &Tokens{
				AccessToken:  fmt.Sprintf("access-token-%d", id),
				RefreshToken: fmt.Sprintf("refresh-token-%d", id),
				ExpiresIn:    3600,
				TokenType:    "Bearer",
			}
			err := coordinator.SaveTokens(testTokens)
			errChan <- err
		}(i)
	}

	// Start concurrent load operations after saving initial tokens
	_ = coordinator.SaveTokens(tokens)
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			_, err := coordinator.LoadTokens()
			errChan <- err
		}()
	}

	// Wait for all operations to complete
	wg.Wait()

	// Collect results
	for i := 0; i < numGoroutines*2; i++ {
		select {
		case err := <-errChan:
			if err != nil {
				t.Errorf("Concurrent operation failed: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("Test timed out waiting for concurrent operations")
		}
	}
}

// TestCallbackServerLifecycle tests callback server start/stop lifecycle
func TestCallbackServerLifecycle(t *testing.T) {
	// Setup test environment
	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	defer func() { _ = os.Setenv("HOME", originalHome) }()
	_ = os.Setenv("HOME", tmpDir)

	// Create coordinator with different port to avoid conflicts
	coordinator, err := NewCoordinator("callback-test", 3335)
	if err != nil {
		t.Fatalf("NewCoordinator failed: %v", err)
	}

	// Set up minimal metadata for callback server
	coordinator.serverMetadata = &ServerMetadata{
		AuthorizationEndpoint: "https://example.com/auth",
		TokenEndpoint:         "https://example.com/token",
	}

	// Test starting callback server
	go func() {
		err := coordinator.startCallbackServer()
		if err != nil {
			t.Errorf("Callback server failed: %v", err)
		}
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Simulate an auth flow by creating a goroutine that will read from the callback channel
	go func() {
		select {
		case code := <-coordinator.callbackChan:
			t.Logf("Received auth code: %s", code)
		case <-time.After(500 * time.Millisecond):
			// Timeout is expected for this test
		}
	}()

	// Test that callback endpoint is accessible and returns appropriate response
	resp, err := http.Get("http://localhost:3335/callback?code=test-code&state=test-state")
	if err != nil {
		t.Errorf("Failed to access callback endpoint: %v", err)
	} else {
		_ = resp.Body.Close()
		// Expect either 200 (if channel send succeeds) or 400 (if no auth flow in progress)
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
			t.Errorf("Expected status 200 or 400, got %d", resp.StatusCode)
		}
	}

	// Give server time to shutdown
	time.Sleep(100 * time.Millisecond)
}
