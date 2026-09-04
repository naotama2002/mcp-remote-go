package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestNoBrowserForAClientThatIsGone is the regression guard for the second
// browser window.
//
// A host that starts a proxy and immediately drops it -- which Claude Desktop
// does on every launch, spawning one instance and replacing it a moment later --
// leaves the abandoned instance mid-authorization. It used to finish the flow
// anyway: register, bind a callback port, and open a browser window for a client
// that no longer existed. The user got two windows, and only one of them could be
// completed, because each carried its own `state` and its own port.
func TestNoBrowserForAClientThatIsGone(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mcp":
			w.Header().Set("WWW-Authenticate",
				fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, server.URL))
			w.WriteHeader(http.StatusUnauthorized)

		case "/.well-known/oauth-protected-resource":
			w.Header().Set("Content-Type", "application/json")
			// The resource has to be the MCP server's own URL: metadata naming
			// a different resource is rejected, and discovery then falls through
			// to invented endpoints instead of these.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"resource":              server.URL + "/mcp",
				"authorization_servers": []string{server.URL},
				"scopes_supported":      []string{"data:read"},
			})

		case "/.well-known/oauth-authorization-server":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                 server.URL,
				"authorization_endpoint": server.URL + "/auth",
				"token_endpoint":         server.URL + "/token",
				"registration_endpoint":  server.URL + "/register",
			})

		case "/register":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"client_id":                  "abandoned-client",
				"redirect_uris":              []string{"http://localhost:0/callback"},
				"token_endpoint_auth_method": "none",
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	defer func() { _ = os.Setenv("HOME", originalHome) }()
	if err := os.Setenv("HOME", tmpDir); err != nil {
		t.Fatalf("failed to set HOME: %v", err)
	}

	// Nothing may reach the browser in this test.
	opened := make([]string, 0, 1)
	originalOpen := openBrowserFunc
	defer func() { openBrowserFunc = originalOpen }()
	openBrowserFunc = func(rawURL string) error {
		opened = append(opened, rawURL)
		return nil
	}

	proxy, err := NewProxyWithTransport(server.URL+"/mcp", 0, map[string]string{}, "abandoned-auth-test", TransportModeAuto)
	if err != nil {
		t.Fatalf("failed to create the proxy: %v", err)
	}
	defer proxy.Shutdown()

	// The client is already gone by the time authorization would begin.
	proxy.cancel()

	err = proxy.handleAuthentication(
		fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, server.URL))

	if err == nil {
		t.Fatal("authorization reported success for a client that was gone")
	}
	if len(opened) != 0 {
		t.Errorf("opened a browser window for a client that was gone: %v", opened)
	}
	if !strings.Contains(err.Error(), "client is gone") {
		t.Errorf("error does not say why it stopped: %v", err)
	}
}
