package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newCoordinatorWithCachedClient(t *testing.T, hash string, port int, info ClientInfo) *Coordinator {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	coordinator, err := NewCoordinator(hash, port)
	if err != nil {
		t.Fatalf("NewCoordinator failed: %v", err)
	}
	if err := coordinator.saveClientInfo(&info); err != nil {
		t.Fatalf("saveClientInfo failed: %v", err)
	}
	return coordinator
}

func TestLoadOrRegisterClientSkipsStaleCacheForDifferentIssuer(t *testing.T) {
	coordinator := newCoordinatorWithCachedClient(t, "client-cache-issuer-test", 3350, ClientInfo{
		ClientID:         "stale-client-id",
		RegisteredIssuer: "https://old-as.example.com",
		RedirectURIs:     []string{"http://localhost:3350/callback"},
	})

	var registerHits int
	as := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/register" {
			registerHits++
			info := ClientInfo{
				ClientID:                "fresh-client-id",
				RedirectURIs:            []string{"http://localhost:3350/callback"},
				TokenEndpointAuthMethod: "none",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(info)
			return
		}
		http.NotFound(w, r)
	}))
	defer as.Close()

	coordinator.serverMetadata = &ServerMetadata{
		Issuer:               as.URL,
		RegistrationEndpoint: as.URL + "/register",
	}

	clientInfo, err := coordinator.loadOrRegisterClient()
	if err != nil {
		t.Fatalf("loadOrRegisterClient failed: %v", err)
	}
	if registerHits != 1 {
		t.Fatalf("expected dynamic registration once, got %d hits", registerHits)
	}
	if clientInfo.ClientID != "fresh-client-id" {
		t.Errorf("ClientID = %q, want fresh-client-id", clientInfo.ClientID)
	}
	if clientInfo.RegisteredIssuer != as.URL {
		t.Errorf("RegisteredIssuer = %q, want %q", clientInfo.RegisteredIssuer, as.URL)
	}
}

// TestLoadOrRegisterClientReusesCacheWhenIssuerMatches guards against the
// regression where 401 re-auth on an authorization server without RFC 7591
// dynamic client registration (no registration_endpoint) would fail because
// the cache was skipped despite the cached client_id still being valid.
func TestLoadOrRegisterClientReusesCacheWhenIssuerMatches(t *testing.T) {
	const issuer = "https://as.example.com"
	coordinator := newCoordinatorWithCachedClient(t, "client-cache-reuse-test", 3351, ClientInfo{
		ClientID:         "preregistered-static",
		RegisteredIssuer: issuer,
		RedirectURIs:     []string{"http://localhost:3351/callback"},
	})

	// No registration_endpoint: if the cache is incorrectly bypassed, the
	// call must fail with "server does not support dynamic registration".
	coordinator.serverMetadata = &ServerMetadata{
		Issuer:                issuer,
		AuthorizationEndpoint: issuer + "/authorize",
		TokenEndpoint:         issuer + "/token",
	}

	clientInfo, err := coordinator.loadOrRegisterClient()
	if err != nil {
		t.Fatalf("loadOrRegisterClient failed: %v", err)
	}
	if clientInfo.ClientID != "preregistered-static" {
		t.Errorf("ClientID = %q, want preregistered-static", clientInfo.ClientID)
	}
}

// TestLoadOrRegisterClientReusesLegacyCacheWithoutIssuer guards backwards
// compatibility: pre-existing cache files without RegisteredIssuer are still
// usable so a tool upgrade does not force a re-registration on every server.
func TestLoadOrRegisterClientReusesLegacyCacheWithoutIssuer(t *testing.T) {
	coordinator := newCoordinatorWithCachedClient(t, "client-cache-legacy-test", 3352, ClientInfo{
		ClientID:     "legacy-client",
		RedirectURIs: []string{"http://localhost:3352/callback"},
	})

	coordinator.serverMetadata = &ServerMetadata{
		Issuer: "https://as.example.com",
	}

	clientInfo, err := coordinator.loadOrRegisterClient()
	if err != nil {
		t.Fatalf("loadOrRegisterClient failed: %v", err)
	}
	if clientInfo.ClientID != "legacy-client" {
		t.Errorf("ClientID = %q, want legacy-client", clientInfo.ClientID)
	}
}
