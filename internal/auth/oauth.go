package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log"
	"net/url"
	"time"

	"github.com/pkg/browser"
)

// GoOAuthClientProvider is the Go implementation of OAuth client provider
type GoOAuthClientProvider struct {
	serverURLHash   string
	serverURL       string
	callbackPort    int
	callbackPath    string
	clientName      string
	clientURI       string
	softwareID      string
	softwareVersion string
}

// NewOAuthClientProvider creates a new OAuth client provider
func NewOAuthClientProvider(options OAuthProviderOptions) *GoOAuthClientProvider {
	callbackPath := options.CallbackPath
	if callbackPath == "" {
		callbackPath = "/oauth/callback"
	}

	return &GoOAuthClientProvider{
		serverURLHash:   GetServerURLHash(options.ServerURL),
		serverURL:       options.ServerURL,
		callbackPort:    options.CallbackPort,
		callbackPath:    callbackPath,
		clientName:      options.ClientName,
		clientURI:       options.ClientURI,
		softwareID:      options.SoftwareID,
		softwareVersion: options.SoftwareVersion,
	}
}

// ClientInformation retrieves client information
func (p *GoOAuthClientProvider) ClientInformation() (*OAuthClientInformation, error) {
	var clientInfo OAuthClientInformation
	err := ReadJSONFile(p.serverURLHash, clientInfoFileName, &clientInfo)
	if err != nil {
		return nil, err
	}

	// Return nil if client information does not exist
	if clientInfo.ClientID == "" {
		return nil, nil
	}

	return &clientInfo, nil
}

// SaveClientInformation saves client information
func (p *GoOAuthClientProvider) SaveClientInformation(clientInfo *OAuthClientInformation) error {
	return WriteJSONFile(p.serverURLHash, clientInfoFileName, clientInfo)
}

// Tokens retrieves tokens
func (p *GoOAuthClientProvider) Tokens() (*OAuthTokens, error) {
	var tokens OAuthTokens
	err := ReadJSONFile(p.serverURLHash, tokensFileName, &tokens)
	if err != nil {
		return nil, err
	}

	// Return nil if tokens do not exist
	if tokens.AccessToken == "" {
		return nil, nil
	}

	return &tokens, nil
}

// SaveTokens saves tokens
func (p *GoOAuthClientProvider) SaveTokens(tokens *OAuthTokens) error {
	return WriteJSONFile(p.serverURLHash, tokensFileName, tokens)
}

// RedirectToAuthorization redirects to the authorization URL
func (p *GoOAuthClientProvider) RedirectToAuthorization(authorizationURL *url.URL) error {
	// Ensure code_challenge and code_challenge_method are in the URL
	q := authorizationURL.Query()
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") == "" {
		// Generate code verifier if not already present
		codeVerifier, err := p.CodeVerifier()
		if err != nil || codeVerifier == "" {
			codeVerifier, err = GenerateCodeVerifier()
			if err != nil {
				return fmt.Errorf("failed to generate code verifier: %w", err)
			}
			
			// Save code verifier for later use
			if err := p.SaveCodeVerifier(codeVerifier); err != nil {
				return fmt.Errorf("failed to save code verifier: %w", err)
			}
		}
		
		// Generate code challenge
		codeChallenge := GenerateCodeChallenge(codeVerifier)
		
		// Add PKCE parameters to URL
		q.Set("code_challenge", codeChallenge)
		q.Set("code_challenge_method", "S256")
		authorizationURL.RawQuery = q.Encode()
	}

	log.Printf("\nPlease access the following URL for authentication:\n%s\n", authorizationURL.String())

	// Open browser with multiple attempts
	var openErr error
	for i := 0; i < 3; i++ {
		openErr = browser.OpenURL(authorizationURL.String())
		if openErr == nil {
			log.Println("Browser has been opened automatically.")
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	
	if openErr != nil {
		log.Println("Could not open browser automatically. Please copy and paste the above URL into your browser.")
	}

	return nil
}

// SaveCodeVerifier saves the code verifier
func (p *GoOAuthClientProvider) SaveCodeVerifier(codeVerifier string) error {
	return WriteTextFile(p.serverURLHash, codeVerifierFileName, codeVerifier)
}

// CodeVerifier retrieves the code verifier
func (p *GoOAuthClientProvider) CodeVerifier() (string, error) {
	return ReadTextFile(p.serverURLHash, codeVerifierFileName)
}

// RedirectURL returns the redirect URL
func (p *GoOAuthClientProvider) RedirectURL() string {
	return fmt.Sprintf("http://localhost:%d%s", p.callbackPort, p.callbackPath)
}

// ClientMetadata returns the client metadata
func (p *GoOAuthClientProvider) ClientMetadata() map[string]interface{} {
	return map[string]interface{}{
		"redirect_uris":              []string{p.RedirectURL()},
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"client_name":                p.clientName,
		"client_uri":                 p.clientURI,
		"software_id":                p.softwareID,
		"software_version":           p.softwareVersion,
	}
}

// GenerateCodeVerifier generates a PKCE code verifier
func GenerateCodeVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// GenerateCodeChallenge generates a PKCE code challenge using S256 method
func GenerateCodeChallenge(codeVerifier string) string {
	// Generate code challenge using S256 method (SHA-256 hash and base64url encode)
	hash := sha256.Sum256([]byte(codeVerifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}
