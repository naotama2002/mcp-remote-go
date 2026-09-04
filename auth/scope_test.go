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
)

// TestRequestedScope covers how the scope for registration and authorization is
// chosen: from the challenge, else from what the protected resource advertises,
// else not at all. No scope name is ever invented -- a server that does not
// recognise one rejects the whole request with invalid_scope.
func TestRequestedScope(t *testing.T) {
	tests := []struct {
		name           string
		challengeScope string
		metadata       *ServerMetadata
		want           string
	}{
		{
			name:           "challenge scope wins over advertised scopes",
			challengeScope: "data:read",
			metadata: &ServerMetadata{
				ResourceScopesSupported: []string{"data:read_write"},
				ScopesSupported:         []string{"data:read_write", offlineAccessScope},
			},
			want: "data:read",
		},
		{
			name:           "challenge scope is normalized",
			challengeScope: "  data:read\tdata:delete  ",
			want:           "data:read data:delete",
		},
		{
			name: "protected resource scopes are used",
			metadata: &ServerMetadata{
				ResourceScopesSupported: []string{"data:read_write"},
			},
			want: "data:read_write",
		},
		{
			name: "offline_access is added when the authorization server offers it",
			metadata: &ServerMetadata{
				ResourceScopesSupported: []string{"data:read_write"},
				ScopesSupported:         []string{"data:read", "data:read_write", offlineAccessScope},
			},
			want: "data:read_write offline_access",
		},
		{
			name: "offline_access is not duplicated",
			metadata: &ServerMetadata{
				ResourceScopesSupported: []string{"data:read_write", offlineAccessScope},
				ScopesSupported:         []string{offlineAccessScope},
			},
			want: "data:read_write offline_access",
		},
		{
			name: "authorization server catalogue alone yields no scope",
			metadata: &ServerMetadata{
				ScopesSupported: []string{"data:read", "data:read_write", "project:delete"},
			},
			want: "",
		},
		{
			name:     "nothing advertised yields no scope",
			metadata: &ServerMetadata{},
			want:     "",
		},
		{
			name: "blank advertised entries are dropped",
			metadata: &ServerMetadata{
				ResourceScopesSupported: []string{"", "  "},
			},
			want: "",
		},
		{
			name: "no metadata yields no scope",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Coordinator{
				challengeScope: normalizeScope(tt.challengeScope),
				serverMetadata: tt.metadata,
			}
			if got := c.requestedScope(); got != tt.want {
				t.Errorf("requestedScope() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestProtectedResourceScopesUsedForRegistrationAndAuthorization is the
// regression test for the Todoist failure: its authorization server rejected
// registration with `invalid_scope: Invalid scope: mcp` because the client asked
// for a scope name nothing had advertised. The scopes published by the protected
// resource must be what is requested instead.
func TestProtectedResourceScopesUsedForRegistrationAndAuthorization(t *testing.T) {
	var registrationScope atomic.Value // string
	registrationScope.Store("")

	var authServer *httptest.Server
	authServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			metadata := ServerMetadata{
				Issuer:                authServer.URL,
				AuthorizationEndpoint: authServer.URL + "/auth",
				TokenEndpoint:         authServer.URL + "/token",
				RegistrationEndpoint:  authServer.URL + "/register",
				// Deliberately does not include "mcp"; asking for it is what
				// used to fail.
				ScopesSupported: []string{"data:read", "data:read_write", offlineAccessScope},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(metadata)

		case "/register":
			var req map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad registration request", http.StatusBadRequest)
				return
			}
			scope, _ := req["scope"].(string)
			registrationScope.Store(scope)

			for _, s := range splitScope(scope) {
				if s != "data:read_write" && s != offlineAccessScope {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]string{
						"error":             "invalid_scope",
						"error_description": "Invalid scope: " + s,
					})
					return
				}
			}

			clientInfo := ClientInfo{
				ClientID:                "scope-client",
				RedirectURIs:            []string{"http://localhost:3346/callback"},
				TokenEndpointAuthMethod: "none",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(clientInfo)

		default:
			http.NotFound(w, r)
		}
	}))
	defer authServer.Close()

	resourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-protected-resource" {
			http.NotFound(w, r)
			return
		}
		prm := ProtectedResourceMetadata{
			Resource:             prmResource(r),
			AuthorizationServers: []string{authServer.URL},
			ScopesSupported:      []string{"data:read_write"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(prm)
	}))
	defer resourceServer.Close()

	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	defer func() { _ = os.Setenv("HOME", originalHome) }()
	_ = os.Setenv("HOME", tmpDir)

	coordinator, err := NewCoordinator("scope-test-hash", 3346)
	if err != nil {
		t.Fatalf("NewCoordinator failed: %v", err)
	}

	authURL, err := coordinator.InitializeAuth(resourceServer.URL)
	if err != nil {
		t.Fatalf("InitializeAuth failed: %v", err)
	}

	const wantScope = "data:read_write offline_access"

	if got, _ := registrationScope.Load().(string); got != wantScope {
		t.Errorf("registration scope = %q, want %q", got, wantScope)
	}

	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("invalid auth URL: %v", err)
	}
	if got := parsed.Query().Get("scope"); got != wantScope {
		t.Errorf("authorization URL scope = %q, want %q (auth URL: %s)", got, wantScope, authURL)
	}
}

// TestChallengeScopeOverridesAdvertisedScopes verifies that a scope named in the
// WWW-Authenticate challenge is what gets requested, since it states what this
// particular request was refused for.
func TestChallengeScopeOverridesAdvertisedScopes(t *testing.T) {
	var authServer *httptest.Server
	authServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			metadata := ServerMetadata{
				Issuer:                authServer.URL,
				AuthorizationEndpoint: authServer.URL + "/auth",
				TokenEndpoint:         authServer.URL + "/token",
				RegistrationEndpoint:  authServer.URL + "/register",
				ScopesSupported:       []string{"data:read", "data:delete", offlineAccessScope},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(metadata)

		case "/register":
			clientInfo := ClientInfo{
				ClientID:                "challenge-scope-client",
				RedirectURIs:            []string{"http://localhost:3347/callback"},
				TokenEndpointAuthMethod: "none",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(clientInfo)

		default:
			http.NotFound(w, r)
		}
	}))
	defer authServer.Close()

	resourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-protected-resource" {
			http.NotFound(w, r)
			return
		}
		prm := ProtectedResourceMetadata{
			Resource:             prmResource(r),
			AuthorizationServers: []string{authServer.URL},
			ScopesSupported:      []string{"data:read"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(prm)
	}))
	defer resourceServer.Close()

	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	defer func() { _ = os.Setenv("HOME", originalHome) }()
	_ = os.Setenv("HOME", tmpDir)

	coordinator, err := NewCoordinator("challenge-scope-hash", 3347)
	if err != nil {
		t.Fatalf("NewCoordinator failed: %v", err)
	}

	authURL, err := coordinator.InitializeAuth(resourceServer.URL, WithChallengeScope("data:delete"))
	if err != nil {
		t.Fatalf("InitializeAuth failed: %v", err)
	}

	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("invalid auth URL: %v", err)
	}
	if got := parsed.Query().Get("scope"); got != "data:delete" {
		t.Errorf("authorization URL scope = %q, want %q (auth URL: %s)", got, "data:delete", authURL)
	}
}

// prmResource builds the `resource` value a protected resource metadata document
// must echo for the request that fetched it.
func prmResource(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// splitScope splits a scope string into its tokens.
func splitScope(scope string) []string {
	return strings.Fields(scope)
}
