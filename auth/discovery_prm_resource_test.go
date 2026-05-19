package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/naotama2002/mcp-remote-go/internal/httpclient"
)

func TestProtectedResourceDiscoveryPathBasedWellKnown(t *testing.T) {
	const serverPath = "/mcp"

	var authServer *httptest.Server
	authServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-authorization-server" {
			meta := ServerMetadata{
				Issuer:                authServer.URL,
				AuthorizationEndpoint: authServer.URL + "/authorize",
				TokenEndpoint:         authServer.URL + "/token",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(meta)
			return
		}
		http.NotFound(w, r)
	}))
	defer authServer.Close()

	serverURL := ""
	resourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-protected-resource/mcp" {
			prm := ProtectedResourceMetadata{
				Resource:             serverURL,
				AuthorizationServers: []string{authServer.URL},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(prm)
			return
		}
		http.NotFound(w, r)
	}))
	defer resourceServer.Close()

	canonicalURL, err := CanonicalResourceURI(resourceServer.URL + serverPath)
	if err != nil {
		t.Fatalf("CanonicalResourceURI: %v", err)
	}
	serverURL = canonicalURL

	strategy := NewProtectedResourceDiscovery(*httpclient.New(nil))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	metadata, err := strategy.Discover(ctx, serverURL)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	if metadata.AuthorizationEndpoint != authServer.URL+"/authorize" {
		t.Errorf("AuthorizationEndpoint = %q", metadata.AuthorizationEndpoint)
	}
}

func TestProtectedResourceDiscoveryRejectsMismatchedResource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-protected-resource" {
			prm := ProtectedResourceMetadata{
				Resource:             "https://other.example.com",
				AuthorizationServers: []string{"https://as.example.com"},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(prm)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	strategy := NewProtectedResourceDiscovery(*httpclient.New(nil))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := strategy.Discover(ctx, server.URL)
	if err == nil {
		t.Fatal("expected error for mismatched PRM resource")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Errorf("unexpected error: %v", err)
	}
}
