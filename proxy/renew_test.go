package proxy

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/naotama2002/mcp-remote-go/auth"
)

// seedAuthCache writes the files an earlier authorization would have left on
// disk: the discovered metadata, the registered client, and the tokens. This is
// the state a freshly started proxy is in, and the one a renewal has to work
// from -- nothing in it has run discovery.
func seedAuthCache(t *testing.T, hash, tokenEndpoint string, tokens map[string]any) {
	t.Helper()

	dir := filepath.Join(seedHome(t), ".mcp-remote-go-auth", hash)
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

// readStoredAccessToken returns the access token currently on disk, through the
// same API the proxy reads it with.
func readStoredAccessToken(t *testing.T, hash string) string {
	t.Helper()
	coordinator, err := auth.NewCoordinator(hash, 0)
	if err != nil {
		t.Fatalf("failed to open the auth store: %v", err)
	}
	tokens, err := coordinator.LoadTokens()
	if err != nil {
		t.Fatalf("failed to read the stored tokens: %v", err)
	}
	return tokens.AccessToken
}

// seedHome points the auth store at a directory of this test's own, and returns
// it. Seeding writes the cache files directly because the functions that write
// metadata and client registrations are not exported.
func seedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// TestRenewsOn401WithoutAskingTheUser is the point of the feature: an access
// token that has aged out is replaced with one request, and the user is not
// sent to a browser for something no human input can settle.
func TestRenewsOn401WithoutAskingTheUser(t *testing.T) {
	var renewals int64

	server := newOAuthMockServer(t, "renewed-access", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&renewals, 1)
		writeJSONBody(w, map[string]any{
			"access_token":  "renewed-access",
			"refresh_token": "rotated-refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	})

	const hash = "renew-on-401"
	seedAuthCache(t, hash, server.URL+"/token", map[string]any{
		"access_token":  "expired-access",
		"refresh_token": "stored-refresh",
	})

	opened := captureBrowserOpens(t, nil)

	proxy, err := NewProxyWithTransport(server.MCPURL(), 0, map[string]string{}, hash, TransportModeAuto)
	if err != nil {
		t.Fatalf("failed to create the proxy: %v", err)
	}
	defer proxy.Shutdown()

	if err := proxy.connectToServer(); err != nil {
		t.Fatalf("connectToServer failed: %v", err)
	}

	if got := opened(); len(got) != 0 {
		t.Errorf("opened a browser window for a token that only needed renewing: %v", got)
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

	server := newOAuthMockServer(t, "", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&renewals, 1)
		writeJSONBody(w, map[string]any{
			"access_token": "renewed-but-still-refused",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	})

	const hash = "renew-then-refused"
	seedAuthCache(t, hash, server.URL+"/token", map[string]any{
		"access_token":  "expired-access",
		"refresh_token": "stored-refresh",
	})

	proxy, err := NewProxyWithTransport(server.MCPURL(), 0, map[string]string{}, hash, TransportModeAuto)
	if err != nil {
		t.Fatalf("failed to create the proxy: %v", err)
	}
	defer proxy.Shutdown()

	// Reaching the browser is the correct outcome here; cancelling from it ends
	// the wait for a code that no test is going to supply.
	opened := captureBrowserOpens(t, proxy.cancel)

	// The server refuses everything, so this cannot succeed. What matters is
	// how it fails.
	_ = proxy.connectToServer()

	if got := len(opened()); got != 1 {
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

	server := newOAuthMockServer(t, "", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&renewals, 1)
		writeJSONBody(w, map[string]any{
			"access_token":  "fresh-access",
			"refresh_token": "rotated",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	})

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

// TestAFailedRenewalIsNotRetriedPerMessage guards the request path. Renewal is
// reached from the header-building callback, which runs once per outgoing
// message, so a refresh token the server has revoked used to cost a round trip
// on every single message for the rest of the process's life.
func TestAFailedRenewalIsNotRetriedPerMessage(t *testing.T) {
	var attempts int64

	server := newOAuthMockServer(t, "", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&attempts, 1)
		w.WriteHeader(http.StatusBadRequest)
		writeJSONBody(w, map[string]any{"error": "invalid_grant"})
	})

	const hash = "renew-failure-backoff"
	seedAuthCache(t, hash, server.URL+"/token", map[string]any{
		"access_token":  "expiring-access",
		"refresh_token": "revoked-refresh",
		"expires_at":    time.Now().Add(-time.Minute).Unix(),
	})

	proxy, err := NewProxyWithTransport(server.MCPURL(), 0, map[string]string{}, hash, TransportModeAuto)
	if err != nil {
		t.Fatalf("failed to create the proxy: %v", err)
	}
	defer proxy.Shutdown()

	for i := 0; i < 3; i++ {
		if got := proxy.getAuthToken(); got != "expiring-access" {
			t.Fatalf("token presented = %q, want the stored one to be used while renewal is failing", got)
		}
	}

	if got := atomic.LoadInt64(&attempts); got != 1 {
		t.Errorf("token endpoint was called %d times over 3 messages, want 1", got)
	}
}

// TestRenewalIsNotAttemptedWhileAnotherProcessHoldsTheLock pins that the
// request path does not wait on the authorization lock. The holder may be a
// process sitting at a browser prompt, and stalling every message behind it for
// minutes is worse than presenting a token the server can judge for itself.
func TestRenewalIsNotAttemptedWhileAnotherProcessHoldsTheLock(t *testing.T) {
	var attempts int64

	server := newOAuthMockServer(t, "", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&attempts, 1)
		writeJSONBody(w, map[string]any{"access_token": "renewed", "token_type": "Bearer", "expires_in": 3600})
	})

	const hash = "renew-lock-held"
	seedAuthCache(t, hash, server.URL+"/token", map[string]any{
		"access_token":  "expiring-access",
		"refresh_token": "stored-refresh",
		"expires_at":    time.Now().Add(-time.Minute).Unix(),
	})

	// Stand in for the process that holds the authorization lock.
	lockFile := filepath.Join(os.Getenv("HOME"), ".mcp-remote-go-auth", hash, "authorize.lock")
	held, err := os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("failed to take the lock: %v", err)
	}
	defer func() { _ = held.Close(); _ = os.Remove(lockFile) }()

	proxy, err := NewProxyWithTransport(server.MCPURL(), 0, map[string]string{}, hash, TransportModeAuto)
	if err != nil {
		t.Fatalf("failed to create the proxy: %v", err)
	}
	defer proxy.Shutdown()

	start := time.Now()
	if got := proxy.getAuthToken(); got != "expiring-access" {
		t.Errorf("token presented = %q, want the stored one", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("a message waited %v on another process's authorization lock", elapsed)
	}
	if got := atomic.LoadInt64(&attempts); got != 0 {
		t.Errorf("renewed %d times while another process held the lock, want 0", got)
	}
}
