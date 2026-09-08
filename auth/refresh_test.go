package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// TestDueForRenewal covers when a stored token is treated as worth renewing.
func TestDueForRenewal(t *testing.T) {
	tests := []struct {
		name   string
		tokens *Tokens
		want   bool
	}{
		{
			name:   "no expiry recorded is never due",
			tokens: &Tokens{AccessToken: "a"},
			want:   false,
		},
		{
			name:   "well before expiry",
			tokens: &Tokens{AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour).Unix()},
			want:   false,
		},
		{
			name:   "inside the skew",
			tokens: &Tokens{AccessToken: "a", ExpiresAt: time.Now().Add(renewalSkew / 2).Unix()},
			want:   true,
		},
		{
			name:   "already expired",
			tokens: &Tokens{AccessToken: "a", ExpiresAt: time.Now().Add(-time.Hour).Unix()},
			want:   true,
		},
		{
			name:   "nil",
			tokens: nil,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tokens.DueForRenewal(); got != tt.want {
				t.Errorf("DueForRenewal() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestStampExpiryLeavesUnknownLifetimeAlone pins that a response without
// expires_in records no expiry, rather than a made-up one.
func TestStampExpiryLeavesUnknownLifetimeAlone(t *testing.T) {
	tokens := &Tokens{AccessToken: "a"}
	tokens.stampExpiry(time.Now())
	if tokens.ExpiresAt != 0 {
		t.Errorf("ExpiresAt = %d, want 0 for a response with no expires_in", tokens.ExpiresAt)
	}

	withLifetime := &Tokens{AccessToken: "a", ExpiresIn: 3600}
	now := time.Now()
	withLifetime.stampExpiry(now)
	if want := now.Add(time.Hour).Unix(); withLifetime.ExpiresAt != want {
		t.Errorf("ExpiresAt = %d, want %d", withLifetime.ExpiresAt, want)
	}
}

// renewTestServer is a token endpoint that answers the refresh grant, recording
// what it was sent.
type renewTestServer struct {
	*httptest.Server
	form    atomic.Value // url.Values
	authHdr atomic.Value // string
	rotate  bool
	fail    string // when set, the error code to answer with
}

func newRenewTestServer(t *testing.T, rotate bool, fail string) *renewTestServer {
	t.Helper()
	rs := &renewTestServer{rotate: rotate, fail: fail}
	rs.form.Store(url.Values{})
	rs.authHdr.Store("")

	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		rs.form.Store(r.Form)
		rs.authHdr.Store(r.Header.Get("Authorization"))

		if rs.fail != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": rs.fail})
			return
		}

		reply := Tokens{AccessToken: "renewed-access", TokenType: "Bearer", ExpiresIn: 3600}
		if rs.rotate {
			reply.RefreshToken = "rotated-refresh"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(rs.Close)
	return rs
}

// seedStoredAuthorization writes the metadata, client and tokens a proxy would
// have on disk from an earlier authorization, without running a flow.
func seedStoredAuthorization(t *testing.T, hash string, rs *renewTestServer, clientInfo *ClientInfo, tokens *Tokens) *Coordinator {
	t.Helper()

	c := newTestCoordinator(t, hash)
	if err := c.saveServerMetadata(&ServerMetadata{
		Issuer:              rs.URL,
		TokenEndpoint:       rs.URL + "/token",
		GrantTypesSupported: []string{grantTypeAuthorizationCode, grantTypeRefreshToken},
	}); err != nil {
		t.Fatalf("failed to seed metadata: %v", err)
	}
	if err := c.saveClientInfo(clientInfo); err != nil {
		t.Fatalf("failed to seed client info: %v", err)
	}
	if err := c.SaveTokens(tokens); err != nil {
		t.Fatalf("failed to seed tokens: %v", err)
	}
	return c
}

// TestRenewFromStoredAuthorization is the core of the feature: a proxy that
// starts with an expired token and never runs discovery must still be able to
// renew, using only what is on disk.
func TestRenewFromStoredAuthorization(t *testing.T) {
	rs := newRenewTestServer(t, true, "")
	c := seedStoredAuthorization(t, "renew-stored", rs,
		&ClientInfo{ClientID: "stored-client", TokenEndpointAuthMethod: authMethodNone},
		&Tokens{AccessToken: "old-access", RefreshToken: "stored-refresh", ExpiresAt: time.Now().Add(-time.Minute).Unix()})

	renewed, err := c.Renew(context.Background(), "https://mcp.example.com/mcp")
	if err != nil {
		t.Fatalf("Renew failed: %v", err)
	}

	if renewed.AccessToken != "renewed-access" {
		t.Errorf("access token = %q, want the renewed one", renewed.AccessToken)
	}
	if renewed.RefreshToken != "rotated-refresh" {
		t.Errorf("refresh token = %q, want the rotated one", renewed.RefreshToken)
	}
	if renewed.ExpiresAt == 0 {
		t.Error("the renewed token recorded no expiry, so it can never be renewed proactively again")
	}

	// The rotated refresh token must reach disk: a server that retires the old
	// one on use leaves nothing to renew with if this write is lost.
	stored, err := c.LoadTokens()
	if err != nil {
		t.Fatalf("LoadTokens failed: %v", err)
	}
	if stored.RefreshToken != "rotated-refresh" || stored.AccessToken != "renewed-access" {
		t.Errorf("stored tokens = %+v, want the renewed pair", stored)
	}

	form, _ := rs.form.Load().(url.Values)
	if got := form.Get("grant_type"); got != grantTypeRefreshToken {
		t.Errorf("grant_type = %q, want %q", got, grantTypeRefreshToken)
	}
	if got := form.Get("refresh_token"); got != "stored-refresh" {
		t.Errorf("refresh_token = %q, want the stored one", got)
	}
	if got := form.Get("client_id"); got != "stored-client" {
		t.Errorf("client_id = %q, want the stored one", got)
	}
	// RFC 8707, as on every other token request.
	if got := form.Get("resource"); got != "https://mcp.example.com/mcp" {
		t.Errorf("resource = %q, want the canonical MCP server URI", got)
	}
}

// TestRenewKeepsTheRefreshTokenWhenTheServerSendsNone covers RFC 6749 §6: a
// server need not rotate, and the old refresh token stays valid. Dropping it
// would make such a server renewable exactly once.
func TestRenewKeepsTheRefreshTokenWhenTheServerSendsNone(t *testing.T) {
	rs := newRenewTestServer(t, false, "")
	c := seedStoredAuthorization(t, "renew-no-rotation", rs,
		&ClientInfo{ClientID: "c", TokenEndpointAuthMethod: authMethodNone},
		&Tokens{AccessToken: "old", RefreshToken: "keep-me"})

	renewed, err := c.Renew(context.Background(), "https://mcp.example.com/mcp")
	if err != nil {
		t.Fatalf("Renew failed: %v", err)
	}
	if renewed.RefreshToken != "keep-me" {
		t.Errorf("refresh token = %q, want the original to be kept", renewed.RefreshToken)
	}
	stored, _ := c.LoadTokens()
	if stored.RefreshToken != "keep-me" {
		t.Errorf("stored refresh token = %q, want the original to be kept", stored.RefreshToken)
	}
}

// TestRenewAuthenticatesAsRegistered checks a confidential client renews the
// way it was registered to authenticate, rather than the way a fresh flow would
// have chosen.
func TestRenewAuthenticatesAsRegistered(t *testing.T) {
	rs := newRenewTestServer(t, true, "")
	c := seedStoredAuthorization(t, "renew-basic-auth", rs,
		&ClientInfo{ClientID: "basic-client", ClientSecret: "s3cret", TokenEndpointAuthMethod: authMethodSecretBasic},
		&Tokens{AccessToken: "old", RefreshToken: "r"})

	if _, err := c.Renew(context.Background(), "https://mcp.example.com/mcp"); err != nil {
		t.Fatalf("Renew failed: %v", err)
	}

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(url.QueryEscape("basic-client")+":"+url.QueryEscape("s3cret")))
	if got, _ := rs.authHdr.Load().(string); got != want {
		t.Errorf("Authorization header = %q, want %q", got, want)
	}
	form, _ := rs.form.Load().(url.Values)
	if got := form.Get("client_secret"); got != "" {
		t.Errorf("client_secret was also sent in the body: %q", got)
	}
}

// TestRenewWithoutARefreshTokenIsNotAFailure pins the signal the proxy uses to
// decide between renewing and asking the user: a server that issues no refresh
// token is ordinary, not broken.
func TestRenewWithoutARefreshTokenIsNotAFailure(t *testing.T) {
	rs := newRenewTestServer(t, true, "")
	c := seedStoredAuthorization(t, "renew-none", rs,
		&ClientInfo{ClientID: "c"},
		&Tokens{AccessToken: "only-access"})

	_, err := c.Renew(context.Background(), "https://mcp.example.com/mcp")
	if !errors.Is(err, ErrNoRefreshToken) {
		t.Errorf("error = %v, want ErrNoRefreshToken", err)
	}
}

// TestRenewReportsARejectedRefreshToken covers the refresh token itself having
// expired or been revoked, which has to surface so the interactive flow runs.
func TestRenewReportsARejectedRefreshToken(t *testing.T) {
	rs := newRenewTestServer(t, false, "invalid_grant")
	c := seedStoredAuthorization(t, "renew-rejected", rs,
		&ClientInfo{ClientID: "c", TokenEndpointAuthMethod: authMethodNone},
		&Tokens{AccessToken: "old", RefreshToken: "revoked"})

	_, err := c.Renew(context.Background(), "https://mcp.example.com/mcp")
	if err == nil {
		t.Fatal("expected an error for a rejected refresh token")
	}
	if errors.Is(err, ErrNoRefreshToken) {
		t.Errorf("a rejected token must be distinguishable from an absent one: %v", err)
	}

	// The stored pair is left as it was; the interactive flow replaces it.
	stored, _ := c.LoadTokens()
	if stored.RefreshToken != "revoked" {
		t.Errorf("stored refresh token = %q, want it untouched after a failed renewal", stored.RefreshToken)
	}
}

// TestRenewExclusivelyUsesAnotherProcessResult covers two proxies renewing at
// once. Refresh tokens are commonly single-use, so the second must take the
// first one's result rather than spend a token the server has already retired.
func TestRenewExclusivelyUsesAnotherProcessResult(t *testing.T) {
	rs := newRenewTestServer(t, true, "")
	c := seedStoredAuthorization(t, "renew-exclusive", rs,
		&ClientInfo{ClientID: "c", TokenEndpointAuthMethod: authMethodNone},
		&Tokens{AccessToken: "old", RefreshToken: "single-use"})

	// Stand in for the other process: hold the lock, then store its result.
	other := newFileLockFor(c)
	if err := other.Lock(time.Second); err != nil {
		t.Fatalf("failed to take the lock as the other process: %v", err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = c.SaveTokens(&Tokens{AccessToken: "other-process-access", RefreshToken: "other-process-refresh"})
		_ = other.Unlock()
	}()

	renewed, err := c.RenewExclusively(context.Background(), "https://mcp.example.com/mcp")
	if err != nil {
		t.Fatalf("RenewExclusively failed: %v", err)
	}
	if renewed.AccessToken != "other-process-access" {
		t.Errorf("access token = %q, want the other process's result", renewed.AccessToken)
	}
	if form, _ := rs.form.Load().(url.Values); form.Get("refresh_token") != "" {
		t.Error("the single-use refresh token was spent a second time")
	}
}

// TestRegistrationAsksForTheRefreshGrant pins that the grant is requested where
// the server offers it -- a client not registered for it is refused when it
// tries to renew -- and left out where it is not, since declaring an unoffered
// grant is what gets a registration rejected.
func TestRegistrationAsksForTheRefreshGrant(t *testing.T) {
	tests := []struct {
		name      string
		supported []string
		want      []string
	}{
		{
			name:      "offered",
			supported: []string{grantTypeAuthorizationCode, grantTypeRefreshToken},
			want:      []string{grantTypeAuthorizationCode, grantTypeRefreshToken},
		},
		{
			name:      "not offered",
			supported: []string{grantTypeAuthorizationCode},
			want:      []string{grantTypeAuthorizationCode},
		},
		{
			name:      "nothing published",
			supported: nil,
			want:      []string{grantTypeAuthorizationCode},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Coordinator{serverMetadata: &ServerMetadata{GrantTypesSupported: tt.supported}}
			got := c.grantTypes()
			if len(got) != len(tt.want) {
				t.Fatalf("grantTypes() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("grantTypes() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// TestExchangeCodeRecordsTheExpiry checks the authorization code exchange
// stamps an absolute expiry too, so the first proactive renewal has something
// to compare against.
func TestExchangeCodeRecordsTheExpiry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Tokens{AccessToken: "a", RefreshToken: "r", ExpiresIn: 1800, TokenType: "Bearer"})
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	defer func() { _ = os.Setenv("HOME", originalHome) }()
	if err := os.Setenv("HOME", tmpDir); err != nil {
		t.Fatalf("failed to set HOME: %v", err)
	}

	c, err := NewCoordinator("exchange-expiry", 0)
	if err != nil {
		t.Fatalf("NewCoordinator failed: %v", err)
	}
	c.serverMetadata = &ServerMetadata{Issuer: server.URL, TokenEndpoint: server.URL + "/token"}
	c.clientInfo = &ClientInfo{ClientID: "c", TokenEndpointAuthMethod: authMethodNone}

	tokens, err := c.ExchangeCode("code")
	if err != nil {
		t.Fatalf("ExchangeCode failed: %v", err)
	}
	if tokens.ExpiresAt == 0 {
		t.Error("ExchangeCode recorded no expiry, so nothing can be renewed before it fails")
	}
	if tokens.DueForRenewal() {
		t.Error("a token with half an hour left was reported as due for renewal")
	}
}
