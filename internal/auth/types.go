package auth

import (
	"net/url"
	"time"
)

// OAuthTokens holds OAuth token information
type OAuthTokens struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresIn    int       `json:"expires_in,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
}

// OAuthClientInformation holds OAuth client information
type OAuthClientInformation struct {
	ClientID                string   `json:"client_id"`
	ClientSecret            string   `json:"client_secret,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	ClientName              string   `json:"client_name"`
	ClientURI               string   `json:"client_uri,omitempty"`
	SoftwareID              string   `json:"software_id,omitempty"`
	SoftwareVersion         string   `json:"software_version,omitempty"`
}

// LockfileData holds lockfile data
type LockfileData struct {
	PID       int   `json:"pid"`
	Port      int   `json:"port"`
	Timestamp int64 `json:"timestamp"`
}

// OAuthProviderOptions holds configuration options for the OAuth provider
type OAuthProviderOptions struct {
	ServerURL        string
	CallbackPort     int
	CallbackPath     string
	ClientName       string
	ClientURI        string
	SoftwareID       string
	SoftwareVersion  string
}

// AuthState holds authentication state
type AuthState struct {
	Server         interface{}
	WaitForAuthCode func() (string, error)
	SkipBrowserAuth bool
}

// OAuthClientProvider is the interface for OAuth client providers
type OAuthClientProvider interface {
	// ClientInformation retrieves client information
	ClientInformation() (*OAuthClientInformation, error)
	
	// SaveClientInformation saves client information
	SaveClientInformation(clientInfo *OAuthClientInformation) error
	
	// Tokens retrieves tokens
	Tokens() (*OAuthTokens, error)
	
	// SaveTokens saves tokens
	SaveTokens(tokens *OAuthTokens) error
	
	// RedirectToAuthorization redirects to the authorization URL
	RedirectToAuthorization(authorizationURL *url.URL) error
	
	// SaveCodeVerifier saves the code verifier
	SaveCodeVerifier(codeVerifier string) error
	
	// CodeVerifier retrieves the code verifier
	CodeVerifier() (string, error)
	
	// RedirectURL returns the redirect URL
	RedirectURL() string
	
	// ClientMetadata returns the client metadata
	ClientMetadata() map[string]interface{}
}

// AuthCoordinator is the interface for authentication coordinators
type AuthCoordinator interface {
	// InitializeAuth initializes authentication
	InitializeAuth() (*AuthState, error)
}
