package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/pkg/browser"
	"golang.org/x/oauth2"
)

// OAuth2ClientProvider is the Go implementation of OAuth client provider using golang.org/x/oauth2
// It implements the OAuthProvider interface
type OAuth2ClientProvider struct {
	serverURLHash   string
	serverURL       string
	callbackPort    int
	callbackPath    string
	clientName      string
	clientURI       string
	softwareID      string
	softwareVersion string
	config          *oauth2.Config
	verifier        string
}

// NewOAuth2ClientProvider creates a new OAuth2 client provider
func NewOAuth2ClientProvider(options OAuthProviderOptions) *OAuth2ClientProvider {
	callbackPath := options.CallbackPath
	if callbackPath == "" {
		callbackPath = "/oauth/callback"
	}

	// OAuth2.1 standard implementation with discovery
	// Try to discover OAuth2 endpoints from .well-known/oauth-authorization-server
	var authURL, tokenURL string

	// Parse the server URL to extract the base URL
	parsedURL, err := url.Parse(options.ServerURL)
	if err != nil {
		log.Printf("Warning: failed to parse server URL: %v", err)
		authURL = options.ServerURL
		tokenURL = options.ServerURL
	} else {
		// Construct the discovery URL
		baseURL := fmt.Sprintf("%s://%s", parsedURL.Scheme, parsedURL.Host)
		discoveryURL := baseURL + "/.well-known/oauth-authorization-server"
		
		// Try to fetch the discovery document
		log.Printf("Attempting to discover OAuth2 endpoints from %s", discoveryURL)
		resp, err := http.Get(discoveryURL)
		
		if err == nil && resp.StatusCode == http.StatusOK {
			// Parse the discovery document
			var discovery struct {
				AuthorizationEndpoint string `json:"authorization_endpoint"`
				TokenEndpoint        string `json:"token_endpoint"`
			}
			
			if err := json.NewDecoder(resp.Body).Decode(&discovery); err == nil {
				// Use the discovered endpoints
				if discovery.AuthorizationEndpoint != "" {
					authURL = discovery.AuthorizationEndpoint
					log.Printf("Discovered authorization endpoint: %s", authURL)
				}
				if discovery.TokenEndpoint != "" {
					tokenURL = discovery.TokenEndpoint
					log.Printf("Discovered token endpoint: %s", tokenURL)
				}
			}
			
			resp.Body.Close()
		}
		
		// If discovery failed, try common OAuth2 endpoints
		if authURL == "" {
			// Try common paths for authorization endpoint
			commonAuthPaths := []string{
				"/api/oauth/authorize",
				"/oauth/authorize",
				"/oauth2/authorize",
				"/authorize",
			}
			
			for _, path := range commonAuthPaths {
				testURL := baseURL + path
				resp, err := http.Head(testURL)
				if err == nil && (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusFound) {
					authURL = testURL
					log.Printf("Using common authorization endpoint: %s", authURL)
					break
				}
			}
		}
		
		if tokenURL == "" {
			// Try common paths for token endpoint
			commonTokenPaths := []string{
				"/api/oauth/token",
				"/oauth/token",
				"/oauth2/token",
				"/token",
			}
			
			for _, path := range commonTokenPaths {
				testURL := baseURL + path
				resp, err := http.Head(testURL)
				if err == nil && (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusFound) {
					tokenURL = testURL
					log.Printf("Using common token endpoint: %s", tokenURL)
					break
				}
			}
		}
		
		// If still not found, fall back to server URL
		if authURL == "" {
			authURL = options.ServerURL
			log.Printf("Falling back to server URL for authorization endpoint: %s", authURL)
		}
		if tokenURL == "" {
			tokenURL = options.ServerURL
			log.Printf("Falling back to server URL for token endpoint: %s", tokenURL)
		}
	}

	redirectURL := fmt.Sprintf("http://localhost:%d%s", options.CallbackPort, callbackPath)

	// Create OAuth2 config
	config := &oauth2.Config{
		ClientID: "mcp-remote-go-client", // Default client ID
		Endpoint: oauth2.Endpoint{
			AuthURL:  authURL,
			TokenURL: tokenURL,
		},
		RedirectURL: redirectURL,
		Scopes:      []string{}, // Default empty scopes
	}

	return &OAuth2ClientProvider{
		serverURLHash:   GetServerURLHash(options.ServerURL),
		serverURL:       options.ServerURL,
		callbackPort:    options.CallbackPort,
		callbackPath:    callbackPath,
		clientName:      options.ClientName,
		clientURI:       options.ClientURI,
		softwareID:      options.SoftwareID,
		softwareVersion: options.SoftwareVersion,
		config:          config,
	}
}

// ClientInformation retrieves client information
func (p *OAuth2ClientProvider) ClientInformation() (*OAuthClientInformation, error) {
	var clientInfo OAuthClientInformation
	err := ReadJSONFile(p.serverURLHash, clientInfoFileName, &clientInfo)
	if err != nil {
		return nil, err
	}

	// Return nil if client information does not exist
	if clientInfo.ClientID == "" {
		return nil, nil
	}

	// Update OAuth2 config with client ID
	p.config.ClientID = clientInfo.ClientID

	return &clientInfo, nil
}

// SaveClientInformation saves client information
func (p *OAuth2ClientProvider) SaveClientInformation(clientInfo *OAuthClientInformation) error {
	// Update OAuth2 config with client ID
	p.config.ClientID = clientInfo.ClientID
	
	return WriteJSONFile(p.serverURLHash, clientInfoFileName, clientInfo)
}

// Tokens retrieves tokens
func (p *OAuth2ClientProvider) Tokens() (*OAuthTokens, error) {
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
func (p *OAuth2ClientProvider) SaveTokens(tokens *OAuthTokens) error {
	return WriteJSONFile(p.serverURLHash, tokensFileName, tokens)
}

// RedirectToAuthorization redirects to the authorization URL
func (p *OAuth2ClientProvider) RedirectToAuthorization(authorizationURL *url.URL) error {
	// Generate verifier for PKCE
	verifier := oauth2.GenerateVerifier()
	p.verifier = verifier
	
	// Save verifier for later use
	if err := p.SaveCodeVerifier(verifier); err != nil {
		return fmt.Errorf("failed to save code verifier: %w", err)
	}

	// Generate authorization URL with PKCE
	authURL := p.config.AuthCodeURL(
		"state",
		oauth2.AccessTypeOffline,
		oauth2.S256ChallengeOption(verifier),
	)

	log.Printf("\nPlease access the following URL for authentication:\n%s\n", authURL)

	// Open browser with multiple attempts
	var openErr error
	for i := 0; i < 3; i++ {
		openErr = browser.OpenURL(authURL)
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
func (p *OAuth2ClientProvider) SaveCodeVerifier(codeVerifier string) error {
	return WriteTextFile(p.serverURLHash, codeVerifierFileName, codeVerifier)
}

// CodeVerifier retrieves the code verifier
func (p *OAuth2ClientProvider) CodeVerifier() (string, error) {
	return ReadTextFile(p.serverURLHash, codeVerifierFileName)
}

// RedirectURL returns the redirect URL
func (p *OAuth2ClientProvider) RedirectURL() string {
	return p.config.RedirectURL
}

// ClientMetadata returns the client metadata
func (p *OAuth2ClientProvider) ClientMetadata() map[string]interface{} {
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

// ExchangeCodeForToken exchanges an authorization code for an OAuth2 token
func (p *OAuth2ClientProvider) ExchangeCodeForToken(code string) (*OAuthTokens, error) {
	// Get code verifier
	verifier, err := p.CodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("failed to get code verifier: %w", err)
	}

	// Exchange code for token
	token, err := p.config.Exchange(
		context.Background(),
		code,
		oauth2.VerifierOption(verifier),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code for token: %w", err)
	}

	// Create OAuth tokens
	oauthTokens := &OAuthTokens{
		AccessToken:  token.AccessToken,
		TokenType:    token.TokenType,
		RefreshToken: token.RefreshToken,
		ExpiresIn:    int(token.Expiry.Sub(time.Now()).Seconds()),
		ExpiresAt:    token.Expiry,
	}

	return oauthTokens, nil
}

// RefreshToken refreshes an OAuth2 token
func (p *OAuth2ClientProvider) RefreshToken(refreshToken string) (*OAuthTokens, error) {
	// Create token source
	token := &oauth2.Token{
		RefreshToken: refreshToken,
	}
	
	tokenSource := p.config.TokenSource(context.Background(), token)
	
	// Refresh token
	newToken, err := tokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}
	
	// Create OAuth tokens
	oauthTokens := &OAuthTokens{
		AccessToken:  newToken.AccessToken,
		TokenType:    newToken.TokenType,
		RefreshToken: newToken.RefreshToken,
		ExpiresIn:    int(newToken.Expiry.Sub(time.Now()).Seconds()),
		ExpiresAt:    newToken.Expiry,
	}
	
	return oauthTokens, nil
}
