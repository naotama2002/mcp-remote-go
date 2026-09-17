package auth

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
)

// TestTokenEndpointAuthMethod covers picking a client authentication method from
// the ones the authorization server publishes. Declaring "none" unconditionally
// is rejected by servers that do not offer it -- the same failure as requesting
// an unadvertised scope.
func TestTokenEndpointAuthMethod(t *testing.T) {
	tests := []struct {
		name     string
		metadata *ServerMetadata
		want     string
	}{
		{
			name:     "no metadata prefers a public client",
			metadata: nil,
			want:     authMethodNone,
		},
		{
			name:     "nothing published prefers a public client",
			metadata: &ServerMetadata{},
			want:     authMethodNone,
		},
		{
			name: "none is chosen when offered",
			metadata: &ServerMetadata{TokenEndpointAuthMethodsSupported: []string{
				authMethodSecretBasic, authMethodSecretPost, authMethodNone}},
			want: authMethodNone,
		},
		{
			name: "post is chosen when none is absent",
			metadata: &ServerMetadata{TokenEndpointAuthMethodsSupported: []string{
				authMethodSecretBasic, authMethodSecretPost}},
			want: authMethodSecretPost,
		},
		{
			name:     "basic is chosen when it is the only one we can perform",
			metadata: &ServerMetadata{TokenEndpointAuthMethodsSupported: []string{authMethodSecretBasic}},
			want:     authMethodSecretBasic,
		},
		{
			name:     "an unperformable list falls back to none",
			metadata: &ServerMetadata{TokenEndpointAuthMethodsSupported: []string{"private_key_jwt", "tls_client_auth"}},
			want:     authMethodNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Coordinator{serverMetadata: tt.metadata}
			if got := c.tokenEndpointAuthMethod(); got != tt.want {
				t.Errorf("tokenEndpointAuthMethod() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRegistrationDeclaresAdvertisedAuthMethod checks the registration request
// carries a method the server accepts, using Figma's shape: it publishes
// client_secret_basic and client_secret_post, and no "none".
func TestRegistrationDeclaresAdvertisedAuthMethod(t *testing.T) {
	var declaredMethod atomic.Value // string
	declaredMethod.Store("")

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			metadata := ServerMetadata{
				Issuer:                            server.URL,
				AuthorizationEndpoint:             server.URL + "/auth",
				TokenEndpoint:                     server.URL + "/token",
				RegistrationEndpoint:              server.URL + "/register",
				TokenEndpointAuthMethodsSupported: []string{authMethodSecretBasic, authMethodSecretPost},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(metadata)

		case "/register":
			var req map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			method, _ := req["token_endpoint_auth_method"].(string)
			declaredMethod.Store(method)

			if method == authMethodNone {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error":             "invalid_client_metadata",
					"error_description": "token_endpoint_auth_method none is not supported",
				})
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ClientInfo{
				ClientID:                "secret-client",
				ClientSecret:            "s3cret",
				RedirectURIs:            []string{"http://localhost:3348/callback"},
				TokenEndpointAuthMethod: method,
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	defer func() { _ = os.Setenv("HOME", originalHome) }()
	_ = os.Setenv("HOME", tmpDir)

	coordinator, err := NewCoordinator("auth-method-hash", 3348)
	if err != nil {
		t.Fatalf("NewCoordinator failed: %v", err)
	}

	if _, err := coordinator.InitializeAuth(server.URL); err != nil {
		t.Fatalf("InitializeAuth failed: %v", err)
	}

	if got, _ := declaredMethod.Load().(string); got != authMethodSecretPost {
		t.Errorf("declared token_endpoint_auth_method = %q, want %q", got, authMethodSecretPost)
	}
}

// TestTokenRequestUsesBasicAuthWhenRegistered verifies that a client the server
// registered as client_secret_basic authenticates in the Authorization header,
// with the secret kept out of the body.
func TestTokenRequestUsesBasicAuthWhenRegistered(t *testing.T) {
	var gotAuthHeader, gotFormSecret atomic.Value // string
	gotAuthHeader.Store("")
	gotFormSecret.Store("")

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			metadata := ServerMetadata{
				Issuer:                            server.URL,
				AuthorizationEndpoint:             server.URL + "/auth",
				TokenEndpoint:                     server.URL + "/token",
				RegistrationEndpoint:              server.URL + "/register",
				TokenEndpointAuthMethodsSupported: []string{authMethodSecretBasic},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(metadata)

		case "/register":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ClientInfo{
				ClientID:                "basic-client",
				ClientSecret:            "s3cret",
				RedirectURIs:            []string{"http://localhost:3349/callback"},
				TokenEndpointAuthMethod: authMethodSecretBasic,
			})

		case "/token":
			if err := r.ParseForm(); err != nil {
				http.Error(w, "parse form failed", http.StatusBadRequest)
				return
			}
			gotAuthHeader.Store(r.Header.Get("Authorization"))
			gotFormSecret.Store(r.Form.Get("client_secret"))

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Tokens{AccessToken: "token", TokenType: "Bearer", ExpiresIn: 3600})

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	defer func() { _ = os.Setenv("HOME", originalHome) }()
	_ = os.Setenv("HOME", tmpDir)

	coordinator, err := NewCoordinator("basic-auth-hash", 3349)
	if err != nil {
		t.Fatalf("NewCoordinator failed: %v", err)
	}
	if _, err := coordinator.InitializeAuth(server.URL); err != nil {
		t.Fatalf("InitializeAuth failed: %v", err)
	}
	if _, err := coordinator.ExchangeCode("code-123"); err != nil {
		t.Fatalf("ExchangeCode failed: %v", err)
	}

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(url.QueryEscape("basic-client")+":"+url.QueryEscape("s3cret")))
	if got, _ := gotAuthHeader.Load().(string); got != want {
		t.Errorf("Authorization header = %q, want %q", got, want)
	}
	if got, _ := gotFormSecret.Load().(string); got != "" {
		t.Errorf("client_secret was also sent in the body: %q", got)
	}
}
