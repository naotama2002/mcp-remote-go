package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestResourceParameterSentInAuthAndTokenRequests verifies that the MCP-required
// `resource` parameter (RFC 8707) is included in both the authorization request
// and the token exchange, and that it carries the canonical URI of the MCP server.
func TestResourceParameterSentInAuthAndTokenRequests(t *testing.T) {
	authCode := "code-resource-test"

	var tokenFormResource atomic.Value // string
	tokenFormResource.Store("")

	var mockServer *httptest.Server
	mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			metadata := ServerMetadata{
				Issuer:                mockServer.URL,
				AuthorizationEndpoint: mockServer.URL + "/auth",
				TokenEndpoint:         mockServer.URL + "/token",
				RegistrationEndpoint:  mockServer.URL + "/register",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(metadata)

		case "/register":
			clientInfo := ClientInfo{
				ClientID:                "resource-client",
				RedirectURIs:            []string{"http://localhost:3344/callback"},
				TokenEndpointAuthMethod: "none",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(clientInfo)

		case "/token":
			if err := r.ParseForm(); err != nil {
				http.Error(w, "parse form failed", http.StatusBadRequest)
				return
			}
			tokenFormResource.Store(r.Form.Get("resource"))

			tokens := Tokens{
				AccessToken: "access-token",
				TokenType:   "Bearer",
				ExpiresIn:   3600,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(tokens)

		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	// Isolate config directory.
	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	defer func() { _ = os.Setenv("HOME", originalHome) }()
	_ = os.Setenv("HOME", tmpDir)

	coordinator, err := NewCoordinator("resource-test-hash", 3344)
	if err != nil {
		t.Fatalf("NewCoordinator failed: %v", err)
	}

	// Use uppercase / trailing slash to also exercise canonicalization.
	rawServerURL := mockServer.URL + "/"
	expectedResource, err := CanonicalResourceURI(rawServerURL)
	if err != nil {
		t.Fatalf("CanonicalResourceURI failed: %v", err)
	}

	authURL, err := coordinator.InitializeAuth(rawServerURL)
	if err != nil {
		t.Fatalf("InitializeAuth failed: %v", err)
	}

	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("invalid auth URL: %v", err)
	}
	if got := parsed.Query().Get("resource"); got != expectedResource {
		t.Errorf("authorization URL resource param = %q, want %q (auth URL: %s)", got, expectedResource, authURL)
	}

	// Exchange the code and verify the token request carried the same resource.
	if _, err := coordinator.ExchangeCode(authCode); err != nil {
		t.Fatalf("ExchangeCode failed: %v", err)
	}

	// Allow a brief moment in case net/http handlers are not yet flushed
	// (in practice the response is already returned before we get here).
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if v, _ := tokenFormResource.Load().(string); v != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	got, _ := tokenFormResource.Load().(string)
	if got != expectedResource {
		t.Errorf("token request resource param = %q, want %q", got, expectedResource)
	}
}

// TestResourceParameterRejectsInvalidServerURL ensures InitializeAuth fails
// fast on URLs that cannot produce a canonical resource identifier.
func TestResourceParameterRejectsInvalidServerURL(t *testing.T) {
	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	defer func() { _ = os.Setenv("HOME", originalHome) }()
	_ = os.Setenv("HOME", tmpDir)

	coordinator, err := NewCoordinator("invalid-url-test", 3345)
	if err != nil {
		t.Fatalf("NewCoordinator failed: %v", err)
	}

	_, err = coordinator.InitializeAuth("https://example.com#frag")
	if err == nil {
		t.Fatal("expected error for URL containing fragment")
	}
	if !strings.Contains(err.Error(), "canonical resource URI") {
		t.Errorf("expected canonical-URI error, got: %v", err)
	}
}
