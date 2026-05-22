package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

// TestInitializeAuthUsesResourceMetadataURL verifies that when a Protected
// Resource Metadata URL is supplied (typically obtained from a 401's
// WWW-Authenticate header per RFC 9728 §5.1), discovery fetches that exact
// URL instead of deriving /.well-known/oauth-protected-resource from the
// MCP server host.
func TestInitializeAuthUsesResourceMetadataURL(t *testing.T) {
	var (
		prmHits          atomic.Int32
		wellKnownPRMHits atomic.Int32
		oauthHits        atomic.Int32
	)

	// Resource server: returns 404 on the conventional well-known PRM path.
	// If discovery falls back to host-derived PRM lookup we will see hits
	// here instead of the explicit metadataURL handler.
	resourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource":
			wellKnownPRMHits.Add(1)
			http.NotFound(w, r)
		case "/.well-known/oauth-authorization-server":
			// Fallback path; should not be hit when explicit PRM URL works.
			oauthHits.Add(1)
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer resourceServer.Close()

	// Authorization server: hosts both an unconventional PRM URL (the one
	// advertised via WWW-Authenticate) and its own RFC 8414 metadata.
	var authServer *httptest.Server
	authServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/custom/protected-resource.json":
			prmHits.Add(1)
			prm := ProtectedResourceMetadata{
				Resource:             resourceServer.URL,
				AuthorizationServers: []string{authServer.URL},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(prm)

		case "/.well-known/oauth-authorization-server":
			meta := ServerMetadata{
				Issuer:                authServer.URL,
				AuthorizationEndpoint: authServer.URL + "/auth",
				TokenEndpoint:         authServer.URL + "/token",
				RegistrationEndpoint:  authServer.URL + "/register",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(meta)

		case "/register":
			info := ClientInfo{
				ClientID:                "via-www-authenticate",
				RedirectURIs:            []string{"http://localhost:3346/callback"},
				TokenEndpointAuthMethod: "none",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(info)

		default:
			http.NotFound(w, r)
		}
	}))
	defer authServer.Close()

	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	defer func() { _ = os.Setenv("HOME", originalHome) }()
	_ = os.Setenv("HOME", tmpDir)

	coordinator, err := NewCoordinator("prm-url-hint-test", 3346)
	if err != nil {
		t.Fatalf("NewCoordinator failed: %v", err)
	}

	prmURL := authServer.URL + "/custom/protected-resource.json"
	authURL, err := coordinator.InitializeAuth(resourceServer.URL, WithResourceMetadataURL(prmURL))
	if err != nil {
		t.Fatalf("InitializeAuth failed: %v", err)
	}
	if authURL == "" {
		t.Fatal("expected non-empty authorization URL")
	}

	if prmHits.Load() == 0 {
		t.Errorf("expected the supplied PRM URL %q to be fetched, but it was never hit", prmURL)
	}
	if wellKnownPRMHits.Load() != 0 {
		t.Errorf("did not expect host-derived PRM lookup, but got %d hit(s) on resource server's .well-known", wellKnownPRMHits.Load())
	}

	// The authorization endpoint must come from the AS metadata we
	// reached via the supplied PRM URL.
	if coordinator.serverMetadata == nil || coordinator.serverMetadata.AuthorizationEndpoint != authServer.URL+"/auth" {
		t.Errorf("AuthorizationEndpoint = %q, want %q", coordinator.serverMetadata.AuthorizationEndpoint, authServer.URL+"/auth")
	}
}
