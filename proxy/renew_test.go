package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// seedAuthCache writes the files an earlier authorization would have left on
// disk: the discovered metadata, the registered client, and the tokens. This is
// the state a freshly started proxy is in, and the one a renewal has to work
// from -- nothing in it has run discovery.
func seedAuthCache(t *testing.T, hash, tokenEndpoint string, tokens map[string]any) {
	t.Helper()

	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	t.Cleanup(func() { _ = os.Setenv("HOME", originalHome) })
	if err := os.Setenv("HOME", tmpDir); err != nil {
		t.Fatalf("failed to set HOME: %v", err)
	}

	dir := filepath.Join(tmpDir, ".mcp-remote-go-auth", hash)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("failed to create the auth directory: %v", err)
	}

	write := func(name string, content any) {
		data, err := json.Marshal(content)
		if err != nil {
			t.Fatalf("failed to marshal %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
	}

	write("server_metadata.json", map[string]any{
		"issuer":                tokenEndpoint,
		"token_endpoint":        tokenEndpoint,
		"grant_types_supported": []string{"authorization_code", "refresh_token"},
	})
	write("client_info.json", map[string]any{
		"client_id":                  "seeded-client",
		"token_endpoint_auth_method": "none",
	})
	write("tokens.json", tokens)
}

// readStoredAccessToken returns the access token currently on disk.
func readStoredAccessToken(t *testing.T, hash string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".mcp-remote-go-auth", hash, "tokens.json"))
	if err != nil {
		t.Fatalf("failed to read the stored tokens: %v", err)
	}
	var tokens struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(data, &tokens); err != nil {
		t.Fatalf("failed to parse the stored tokens: %v", err)
	}
	return tokens.AccessToken
}

// TestRenewsOn401WithoutAskingTheUser is the point of the feature: an access
// token that has aged out is replaced with one request, and the user is not
// sent to a browser for something no human input can settle.
func TestRenewsOn401WithoutAskingTheUser(t *testing.T) {
	var renewals int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			atomic.AddInt64(&renewals, 1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "renewed-access",
				"refresh_token": "rotated-refresh",
				"token_type":    "Bearer",
				"expires_in":    3600,
			})

		case "/mcp":
			// Only the renewed token is accepted, so connecting at all proves
			// the renewal happened and was used.
			if r.Header.Get("Authorization") != "Bearer renewed-access" {
				w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":0,"result":{}}`)

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	// No recorded expiry, which is what a token stored before this proxy knew
	// to record one looks like. Nothing can be renewed ahead of time, so the
	// server's refusal is what has to trigger it.
	const hash = "renew-on-401"
	seedAuthCache(t, hash, server.URL+"/token", map[string]any{
		"access_token":  "expired-access",
		"refresh_token": "stored-refresh",
	})

	opened := make([]string, 0, 1)
	originalOpen := openBrowserFunc
	defer func() { openBrowserFunc = originalOpen }()
	openBrowserFunc = func(rawURL string) error {
		opened = append(opened, rawURL)
		return nil
	}

	proxy, err := NewProxyWithTransport(server.URL+"/mcp", 0, map[string]string{}, hash, TransportModeAuto)
	if err != nil {
		t.Fatalf("failed to create the proxy: %v", err)
	}
	defer proxy.Shutdown()

	if err := proxy.connectToServer(); err != nil {
		t.Fatalf("connectToServer failed: %v", err)
	}

	if len(opened) != 0 {
		t.Errorf("opened a browser window for a token that only needed renewing: %v", opened)
	}
	if got := atomic.LoadInt64(&renewals); got != 1 {
		t.Errorf("token endpoint was called %d times, want 1", got)
	}
	if got := readStoredAccessToken(t, hash); got != "renewed-access" {
		t.Errorf("stored access token = %q, want the renewed one", got)
	}
}

// TestStopsRenewingWhenTheServerStillRefuses guards against a loop between the
// server and the token endpoint. A 401 on a token that was just renewed is not
// about the token's age, so the user has to be asked -- renewing again would
// spin forever without anyone being told.
func TestStopsRenewingWhenTheServerStillRefuses(t *testing.T) {
	var renewals int64

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			atomic.AddInt64(&renewals, 1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "renewed-but-still-refused",
				"token_type":   "Bearer",
				"expires_in":   3600,
			})

		case "/mcp":
			// Refuses everything, however new.
			w.Header().Set("WWW-Authenticate",
				fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, server.URL))
			w.WriteHeader(http.StatusUnauthorized)

		case "/.well-known/oauth-protected-resource":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"resource":              server.URL + "/mcp",
				"authorization_servers": []string{server.URL},
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
				"client_id":                  "reregistered",
				"token_endpoint_auth_method": "none",
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	const hash = "renew-then-refused"
	seedAuthCache(t, hash, server.URL+"/token", map[string]any{
		"access_token":  "expired-access",
		"refresh_token": "stored-refresh",
	})

	proxy, err := NewProxyWithTransport(server.URL+"/mcp", 0, map[string]string{}, hash, TransportModeAuto)
	if err != nil {
		t.Fatalf("failed to create the proxy: %v", err)
	}
	defer proxy.Shutdown()

	// Reaching the browser is the correct outcome here; cancelling from it ends
	// the wait for a code that no test is going to supply.
	var opened int64
	originalOpen := openBrowserFunc
	defer func() { openBrowserFunc = originalOpen }()
	openBrowserFunc = func(string) error {
		atomic.AddInt64(&opened, 1)
		proxy.cancel()
		return nil
	}

	// The server refuses everything, so this cannot succeed. What matters is
	// how it fails.
	_ = proxy.connectToServer()

	if got := atomic.LoadInt64(&opened); got != 1 {
		t.Errorf("browser opened %d times, want 1: the user must be asked once renewal has not helped", got)
	}
	if got := atomic.LoadInt64(&renewals); got != 1 {
		t.Errorf("token endpoint was called %d times, want 1: renewal must not be retried in a loop", got)
	}
}

// TestRenewsBeforeUsingAnExpiringToken covers the proactive half: the renewal
// happens as the token is fetched for a request, so the request itself never
// has to fail first.
func TestRenewsBeforeUsingAnExpiringToken(t *testing.T) {
	var renewals int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt64(&renewals, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "fresh-access",
			"refresh_token": "rotated",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	const hash = "renew-proactive"
	seedAuthCache(t, hash, server.URL+"/token", map[string]any{
		"access_token":  "about-to-expire",
		"refresh_token": "stored-refresh",
		"expires_at":    time.Now().Add(10 * time.Second).Unix(),
	})

	proxy, err := NewProxyWithTransport("https://mcp.example.com/mcp", 0, map[string]string{}, hash, TransportModeAuto)
	if err != nil {
		t.Fatalf("failed to create the proxy: %v", err)
	}
	defer proxy.Shutdown()

	if got := proxy.getAuthToken(); got != "fresh-access" {
		t.Errorf("token presented = %q, want the renewed one", got)
	}
	if got := atomic.LoadInt64(&renewals); got != 1 {
		t.Errorf("token endpoint was called %d times, want 1", got)
	}

	// A token with plenty of life left is used as it is.
	if got := proxy.getAuthToken(); got != "fresh-access" {
		t.Errorf("token presented = %q, want the stored one", got)
	}
	if got := atomic.LoadInt64(&renewals); got != 1 {
		t.Errorf("token endpoint was called %d times, want 1: a fresh token needs no renewal", got)
	}
}
