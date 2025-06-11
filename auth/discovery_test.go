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

func TestStandardOAuthDiscovery(t *testing.T) {
	// Create test server with OAuth metadata
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-authorization-server" {
			metadata := &ServerMetadata{
				Issuer:                "https://example.com",
				AuthorizationEndpoint: "https://example.com/oauth/authorize",
				TokenEndpoint:         "https://example.com/oauth/token",
				RegistrationEndpoint:  "https://example.com/oauth/register",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(metadata)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service := NewMetadataDiscoveryService()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	metadata, err := service.Discover(ctx, server.URL)
	if err != nil {
		t.Fatalf("Discovery failed: %v", err)
	}

	if metadata.Issuer != "https://example.com" {
		t.Errorf("Expected issuer 'https://example.com', got %s", metadata.Issuer)
	}

	if metadata.AuthorizationEndpoint != "https://example.com/oauth/authorize" {
		t.Errorf("Expected auth endpoint 'https://example.com/oauth/authorize', got %s", metadata.AuthorizationEndpoint)
	}
}

func TestOpenIDConnectDiscovery(t *testing.T) {
	// Create test server that only responds to OIDC discovery
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			// Standard OAuth discovery fails
			http.NotFound(w, r)
		case "/.well-known/openid-configuration":
			// OIDC discovery succeeds
			metadata := &ServerMetadata{
				Issuer:                "https://oidc-provider.com",
				AuthorizationEndpoint: "https://oidc-provider.com/auth",
				TokenEndpoint:         "https://oidc-provider.com/token",
				RegistrationEndpoint:  "https://oidc-provider.com/register",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(metadata)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service := NewMetadataDiscoveryService()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	metadata, err := service.Discover(ctx, server.URL)
	if err != nil {
		t.Fatalf("OIDC discovery failed: %v", err)
	}

	if metadata.Issuer != "https://oidc-provider.com" {
		t.Errorf("Expected issuer 'https://oidc-provider.com', got %s", metadata.Issuer)
	}
}

func TestFallbackDiscovery(t *testing.T) {
	// Create test server that doesn't respond to any well-known endpoints
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	service := NewMetadataDiscoveryService()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	metadata, err := service.Discover(ctx, server.URL)
	if err != nil {
		t.Fatalf("Fallback discovery failed: %v", err)
	}

	expectedAuthEndpoint := server.URL + "/oauth/authorize"
	if metadata.AuthorizationEndpoint != expectedAuthEndpoint {
		t.Errorf("Expected auth endpoint '%s', got %s", expectedAuthEndpoint, metadata.AuthorizationEndpoint)
	}

	expectedTokenEndpoint := server.URL + "/oauth/token"
	if metadata.TokenEndpoint != expectedTokenEndpoint {
		t.Errorf("Expected token endpoint '%s', got %s", expectedTokenEndpoint, metadata.TokenEndpoint)
	}
}

func TestDiscoveryWithInvalidURL(t *testing.T) {
	service := NewMetadataDiscoveryService()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := service.Discover(ctx, "invalid-url")
	if err == nil {
		t.Error("Expected error for invalid URL")
	}
}

func TestDiscoveryTimeout(t *testing.T) {
	// Create server with delay
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		http.NotFound(w, r)
	}))
	defer server.Close()

	service := NewMetadataDiscoveryService()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := service.Discover(ctx, server.URL)
	if err == nil {
		t.Error("Expected timeout error")
	}

	// Check that it's actually a timeout, not a fallback success
	if !strings.Contains(err.Error(), "context deadline exceeded") &&
		!strings.Contains(err.Error(), "timeout") &&
		!strings.Contains(err.Error(), "deadline") {
		t.Errorf("Expected timeout-related error, got: %v", err)
	}
}

func TestIndividualDiscoveryStrategies(t *testing.T) {
	tests := []struct {
		name     string
		strategy DiscoveryStrategy
		path     string
	}{
		{
			name:     "StandardOAuth",
			strategy: NewStandardOAuthDiscovery(*httpclient.New(nil)),
			path:     "/.well-known/oauth-authorization-server",
		},
		{
			name:     "OpenIDConnect",
			strategy: NewOpenIDConnectDiscovery(*httpclient.New(nil)),
			path:     "/.well-known/openid-configuration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create server that responds to specific path
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tt.path {
					metadata := &ServerMetadata{
						Issuer:                "https://test.com",
						AuthorizationEndpoint: "https://test.com/auth",
						TokenEndpoint:         "https://test.com/token",
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(metadata)
				} else {
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			metadata, err := tt.strategy.Discover(ctx, server.URL)
			if err != nil {
				t.Fatalf("%s discovery failed: %v", tt.name, err)
			}

			if metadata.Issuer != "https://test.com" {
				t.Errorf("Expected issuer 'https://test.com', got %s", metadata.Issuer)
			}
		})
	}
}

func TestFallbackDiscoveryAlwaysSucceeds(t *testing.T) {
	fallback := NewFallbackDiscovery()
	ctx := context.Background()

	testURLs := []string{
		"https://example.com",
		"http://localhost:8080",
		"https://api.service.com:9443",
	}

	for _, testURL := range testURLs {
		metadata, err := fallback.Discover(ctx, testURL)
		if err != nil {
			t.Errorf("Fallback discovery failed for %s: %v", testURL, err)
		}

		if metadata == nil {
			t.Errorf("Expected metadata for %s, got nil", testURL)
			continue
		}

		if len(metadata.ScopesSupported) == 0 {
			t.Errorf("Expected default scopes for %s", testURL)
		}
	}
}
