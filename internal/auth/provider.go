package auth

import (
	"net/url"
)

// OAuthProvider defines the interface for OAuth client providers
type OAuthProvider interface {
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
	
	// ExchangeCodeForToken exchanges an authorization code for an OAuth2 token
	ExchangeCodeForToken(code string) (*OAuthTokens, error)
}
