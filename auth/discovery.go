package auth

import (
	"context"
	"fmt"
	"log"
	"net/url"

	"github.com/naotama2002/mcp-remote-go/internal/httpclient"
)

// MetadataDiscoveryService handles OAuth server metadata discovery
type MetadataDiscoveryService struct {
	client httpclient.Client
}

// NewMetadataDiscoveryService creates a new metadata discovery service
func NewMetadataDiscoveryService() *MetadataDiscoveryService {
	return &MetadataDiscoveryService{
		client: *httpclient.New(nil),
	}
}

// DiscoveryStrategy represents a discovery method
type DiscoveryStrategy interface {
	Discover(ctx context.Context, serverURL string) (*ServerMetadata, error)
	Name() string
}

// StandardOAuthDiscovery implements RFC 8414 OAuth authorization server metadata discovery
type StandardOAuthDiscovery struct {
	client httpclient.Client
}

// NewStandardOAuthDiscovery creates a new standard OAuth discovery strategy
func NewStandardOAuthDiscovery(client httpclient.Client) *StandardOAuthDiscovery {
	return &StandardOAuthDiscovery{client: client}
}

func (s *StandardOAuthDiscovery) Name() string {
	return "Standard OAuth 2.0 Discovery (RFC 8414)"
}

func (s *StandardOAuthDiscovery) Discover(ctx context.Context, serverURL string) (*ServerMetadata, error) {
	wellKnownURL, err := s.buildWellKnownURL(serverURL, "oauth-authorization-server")
	if err != nil {
		return nil, err
	}

	return s.fetchMetadata(ctx, wellKnownURL)
}

func (s *StandardOAuthDiscovery) buildWellKnownURL(serverURL, endpoint string) (string, error) {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("invalid server URL: %w", err)
	}
	return fmt.Sprintf("%s://%s/.well-known/%s", parsed.Scheme, parsed.Host, endpoint), nil
}

func (s *StandardOAuthDiscovery) fetchMetadata(ctx context.Context, metadataURL string) (*ServerMetadata, error) {
	resp, err := s.client.Get(ctx, metadataURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metadata from %s: %w", metadataURL, err)
	}
	defer func() { _ = resp.SafeClose() }()

	var metadata ServerMetadata
	if err := resp.JSON(&metadata); err != nil {
		return nil, fmt.Errorf("failed to parse metadata from %s: %w", metadataURL, err)
	}

	return &metadata, nil
}

// OpenIDConnectDiscovery implements OpenID Connect discovery
type OpenIDConnectDiscovery struct {
	client httpclient.Client
}

// NewOpenIDConnectDiscovery creates a new OpenID Connect discovery strategy
func NewOpenIDConnectDiscovery(client httpclient.Client) *OpenIDConnectDiscovery {
	return &OpenIDConnectDiscovery{client: client}
}

func (o *OpenIDConnectDiscovery) Name() string {
	return "OpenID Connect Discovery"
}

func (o *OpenIDConnectDiscovery) Discover(ctx context.Context, serverURL string) (*ServerMetadata, error) {
	wellKnownURL, err := o.buildWellKnownURL(serverURL)
	if err != nil {
		return nil, err
	}

	return o.fetchMetadata(ctx, wellKnownURL)
}

func (o *OpenIDConnectDiscovery) buildWellKnownURL(serverURL string) (string, error) {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("invalid server URL: %w", err)
	}
	return fmt.Sprintf("%s://%s/.well-known/openid-configuration", parsed.Scheme, parsed.Host), nil
}

func (o *OpenIDConnectDiscovery) fetchMetadata(ctx context.Context, metadataURL string) (*ServerMetadata, error) {
	resp, err := o.client.Get(ctx, metadataURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch OIDC metadata from %s: %w", metadataURL, err)
	}
	defer func() { _ = resp.SafeClose() }()

	var metadata ServerMetadata
	if err := resp.JSON(&metadata); err != nil {
		return nil, fmt.Errorf("failed to parse OIDC metadata from %s: %w", metadataURL, err)
	}

	return &metadata, nil
}

// FallbackDiscovery creates metadata based on common endpoint patterns
type FallbackDiscovery struct{}

// NewFallbackDiscovery creates a new fallback discovery strategy
func NewFallbackDiscovery() *FallbackDiscovery {
	return &FallbackDiscovery{}
}

func (f *FallbackDiscovery) Name() string {
	return "Fallback Discovery (Common Patterns)"
}

func (f *FallbackDiscovery) Discover(ctx context.Context, serverURL string) (*ServerMetadata, error) {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("invalid server URL for fallback discovery: %w", err)
	}

	// Validate that we have a proper scheme and host
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("server URL must have valid scheme and host for fallback discovery: %s", serverURL)
	}

	baseURL := fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)
	return &ServerMetadata{
		Issuer:                 baseURL,
		AuthorizationEndpoint:  baseURL + "/oauth/authorize",
		TokenEndpoint:          baseURL + "/oauth/token",
		RegistrationEndpoint:   baseURL + "/oauth/register",
		ScopesSupported:        []string{"mcp", "offline_access"},
		ResponseTypesSupported: []string{"code"},
		GrantTypesSupported:    []string{"authorization_code", "refresh_token"},
	}, nil
}

// Discover tries multiple discovery strategies in order
func (m *MetadataDiscoveryService) Discover(ctx context.Context, serverURL string) (*ServerMetadata, error) {
	strategies := []DiscoveryStrategy{
		NewStandardOAuthDiscovery(m.client),
		NewOpenIDConnectDiscovery(m.client),
		NewFallbackDiscovery(),
	}

	var lastErr error
	for _, strategy := range strategies {
		log.Printf("Trying %s for %s", strategy.Name(), serverURL)

		// Check if context is already cancelled before trying each strategy
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("discovery cancelled due to context: %w", ctx.Err())
		default:
		}

		metadata, err := strategy.Discover(ctx, serverURL)
		if err == nil {
			log.Printf("Successfully discovered metadata using %s", strategy.Name())
			return metadata, nil
		}

		log.Printf("%s failed: %v", strategy.Name(), err)
		lastErr = err

		// If context is cancelled, return immediately instead of trying next strategy
		if ctx.Err() != nil {
			return nil, fmt.Errorf("discovery cancelled due to context: %w", ctx.Err())
		}
	}

	return nil, fmt.Errorf("all discovery methods failed, last error: %w", lastErr)
}
