package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Tokens holds OAuth tokens
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
}

// ClientInfo holds the OAuth client registration information
type ClientInfo struct {
	ClientID                string   `json:"client_id"`
	ClientSecret            string   `json:"client_secret"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at,omitempty"`
	ClientSecretExpiresAt   int64    `json:"client_secret_expires_at,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
}

// ServerMetadata holds the OAuth server metadata
type ServerMetadata struct {
	Issuer                 string   `json:"issuer"`
	AuthorizationEndpoint  string   `json:"authorization_endpoint"`
	TokenEndpoint          string   `json:"token_endpoint"`
	RegistrationEndpoint   string   `json:"registration_endpoint"`
	JWKSUri                string   `json:"jwks_uri,omitempty"`
	ScopesSupported        []string `json:"scopes_supported,omitempty"`
	ResponseTypesSupported []string `json:"response_types_supported,omitempty"`
	GrantTypesSupported    []string `json:"grant_types_supported,omitempty"`
}

// Coordinator handles the OAuth flow
type Coordinator struct {
	serverURLHash  string
	callbackPort   int
	callbackServer *http.Server
	clientInfo     *ClientInfo
	serverMetadata *ServerMetadata
	authMutex      sync.Mutex
	callbackChan   chan string
}

// NewCoordinator creates a new authentication coordinator
func NewCoordinator(serverURLHash string, callbackPort int) (*Coordinator, error) {
	// Ensure config directory exists
	configDir := getConfigDir()
	serverDir := filepath.Join(configDir, serverURLHash)

	if err := os.MkdirAll(serverDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create config directory: %w", err)
	}

	return &Coordinator{
		serverURLHash: serverURLHash,
		callbackPort:  callbackPort,
		callbackChan:  make(chan string),
	}, nil
}

// InitializeAuth starts the OAuth flow
func (c *Coordinator) InitializeAuth(serverURL string) (string, error) {
	c.authMutex.Lock()
	defer c.authMutex.Unlock()

	// 1. Discover server metadata
	metadata, err := c.discoverServerMetadata(serverURL)
	if err != nil {
		return "", fmt.Errorf("failed to discover server metadata: %w", err)
	}
	c.serverMetadata = metadata

	// 2. Register client if needed
	clientInfo, err := c.loadOrRegisterClient()
	if err != nil {
		return "", fmt.Errorf("client registration failed: %w", err)
	}
	c.clientInfo = clientInfo

	// 3. Start callback server if not already running
	if c.callbackServer == nil {
		if err := c.startCallbackServer(); err != nil {
			return "", fmt.Errorf("failed to start callback server: %w", err)
		}
	}

	// 4. Generate authorization URL
	authURL, err := c.buildAuthorizationURL()
	if err != nil {
		return "", fmt.Errorf("failed to build authorization URL: %w", err)
	}

	return authURL, nil
}

// WaitForAuthCode waits for the authorization code from the callback
func (c *Coordinator) WaitForAuthCode() (string, error) {
	// Wait for the code from the callback
	select {
	case code := <-c.callbackChan:
		return code, nil
	case <-time.After(5 * time.Minute):
		return "", errors.New("timeout waiting for authorization code")
	}
}

// ExchangeCode exchanges the authorization code for tokens
func (c *Coordinator) ExchangeCode(code string) (*Tokens, error) {
	if c.serverMetadata == nil || c.clientInfo == nil {
		return nil, errors.New("auth not initialized")
	}

	// Prepare request
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("code", code)
	data.Set("redirect_uri", fmt.Sprintf("http://localhost:%d/callback", c.callbackPort))
	data.Set("client_id", c.clientInfo.ClientID)

	// Add client secret if available
	if c.clientInfo.ClientSecret != "" {
		data.Set("client_secret", c.clientInfo.ClientSecret)
	}

	// Send token request
	req, err := http.NewRequest(http.MethodPost, c.serverMetadata.TokenEndpoint, bytes.NewBufferString(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Send request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read token response: %w", err)
	}

	// Check status code
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed: HTTP %d - %s", resp.StatusCode, string(body))
	}

	// Parse tokens
	var tokens Tokens
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	return &tokens, nil
}

// LoadTokens loads tokens from disk
func (c *Coordinator) LoadTokens() (*Tokens, error) {
	tokensPath := c.getTokensPath()

	// Read file
	data, err := os.ReadFile(tokensPath)
	if err != nil {
		return nil, err
	}

	// Parse tokens
	var tokens Tokens
	if err := json.Unmarshal(data, &tokens); err != nil {
		return nil, fmt.Errorf("failed to parse tokens file: %w", err)
	}

	return &tokens, nil
}

// SaveTokens saves tokens to disk
func (c *Coordinator) SaveTokens(tokens *Tokens) error {
	tokensPath := c.getTokensPath()

	// Marshal tokens
	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal tokens: %w", err)
	}

	// Write to file
	if err := os.WriteFile(tokensPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write tokens file: %w", err)
	}

	return nil
}

// discoverServerMetadata discovers OAuth server metadata
func (c *Coordinator) discoverServerMetadata(serverURL string) (*ServerMetadata, error) {
	// First try to load cached metadata
	metadata, err := c.loadServerMetadata()
	if err == nil {
		return metadata, nil
	}

	// Construct the well-known URL
	serverURLParsed, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("invalid server URL: %w", err)
	}

	// Try to discover using standard OAuth 2.0 metadata endpoint
	wellKnownURL := fmt.Sprintf("%s://%s/.well-known/oauth-authorization-server", serverURLParsed.Scheme, serverURLParsed.Host)
	metadata, err = c.fetchServerMetadata(wellKnownURL)
	if err == nil {
		// Save metadata
		if err := c.saveServerMetadata(metadata); err != nil {
			log.Printf("Warning: failed to save server metadata: %v", err)
		}
		return metadata, nil
	}

	// Fallback to OpenID Connect discovery
	wellKnownURL = fmt.Sprintf("%s://%s/.well-known/openid-configuration", serverURLParsed.Scheme, serverURLParsed.Host)
	metadata, err = c.fetchServerMetadata(wellKnownURL)
	if err == nil {
		// Save metadata
		if err := c.saveServerMetadata(metadata); err != nil {
			log.Printf("Warning: failed to save server metadata: %v", err)
		}
		return metadata, nil
	}

	// If all discovery methods fail, fallback to a common structure
	fallbackMetadata := &ServerMetadata{
		Issuer:                fmt.Sprintf("%s://%s", serverURLParsed.Scheme, serverURLParsed.Host),
		AuthorizationEndpoint: fmt.Sprintf("%s://%s/oauth/authorize", serverURLParsed.Scheme, serverURLParsed.Host),
		TokenEndpoint:         fmt.Sprintf("%s://%s/oauth/token", serverURLParsed.Scheme, serverURLParsed.Host),
		RegistrationEndpoint:  fmt.Sprintf("%s://%s/oauth/register", serverURLParsed.Scheme, serverURLParsed.Host),
	}

	// Save this fallback metadata
	if err := c.saveServerMetadata(fallbackMetadata); err != nil {
		log.Printf("Warning: failed to save fallback server metadata: %v", err)
	}

	return fallbackMetadata, nil
}

// fetchServerMetadata fetches OAuth server metadata from a URL
func (c *Coordinator) fetchServerMetadata(metadataURL string) (*ServerMetadata, error) {
	resp, err := http.Get(metadataURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metadata endpoint returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata response: %w", err)
	}

	var metadata ServerMetadata
	if err := json.Unmarshal(body, &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse metadata: %w", err)
	}

	return &metadata, nil
}

// loadOrRegisterClient loads or registers a client
func (c *Coordinator) loadOrRegisterClient() (*ClientInfo, error) {
	// First try to load existing client info
	clientInfo, err := c.loadClientInfo()
	if err == nil {
		return clientInfo, nil
	}

	// Check if registration endpoint is available
	if c.serverMetadata.RegistrationEndpoint == "" {
		return nil, errors.New("server does not support dynamic registration")
	}

	// Register a new client
	redirectURI := fmt.Sprintf("http://localhost:%d/callback", c.callbackPort)

	// Prepare registration request
	regReq := map[string]interface{}{
		"client_name":                "MCP Remote Go Client",
		"redirect_uris":              []string{redirectURI},
		"token_endpoint_auth_method": "none",
		"scope":                      "mcp offline_access",
		"grant_types":                []string{"authorization_code"},
	}

	reqBody, err := json.Marshal(regReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal client registration request: %w", err)
	}

	// Send registration request
	req, err := http.NewRequest(http.MethodPost, c.serverMetadata.RegistrationEndpoint, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create registration request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("client registration request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read registration response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("client registration failed: HTTP %d - %s", resp.StatusCode, string(body))
	}

	// Parse response
	var clientInfoResp ClientInfo
	if err := json.Unmarshal(body, &clientInfoResp); err != nil {
		return nil, fmt.Errorf("failed to parse client registration response: %w", err)
	}

	// Save client info
	if err := c.saveClientInfo(&clientInfoResp); err != nil {
		return nil, fmt.Errorf("failed to save client info: %w", err)
	}

	return &clientInfoResp, nil
}

// startCallbackServer starts the HTTP server to receive the OAuth callback
func (c *Coordinator) startCallbackServer() error {
	mux := http.NewServeMux()

	// Callback handler
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Authorization code not found", http.StatusBadRequest)
			return
		}

		// Send the code to the waiting goroutine
		select {
		case c.callbackChan <- code:
			// Send success response
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`
				<html>
				<head><title>Authorization Successful</title></head>
				<body>
					<h1>Authorization Successful</h1>
					<p>You can close this window and return to the application.</p>
					<script>window.close();</script>
				</body>
				</html>
			`))
		default:
			http.Error(w, "Authorization flow not in progress", http.StatusBadRequest)
		}
	})

	// Create server
	c.callbackServer = &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", c.callbackPort),
		Handler: mux,
	}

	// Start the server
	go func() {
		if err := c.callbackServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("Callback server error: %v", err)
		}
	}()

	return nil
}

// buildAuthorizationURL builds the authorization URL
func (c *Coordinator) buildAuthorizationURL() (string, error) {
	if c.serverMetadata == nil || c.clientInfo == nil {
		return "", errors.New("auth not initialized")
	}

	// Build params
	params := url.Values{}
	params.Set("client_id", c.clientInfo.ClientID)
	params.Set("redirect_uri", fmt.Sprintf("http://localhost:%d/callback", c.callbackPort))
	params.Set("response_type", "code")
	params.Set("scope", "mcp offline_access")

	// Combine URL
	baseURL, err := url.Parse(c.serverMetadata.AuthorizationEndpoint)
	if err != nil {
		return "", fmt.Errorf("invalid authorization endpoint: %w", err)
	}

	baseURL.RawQuery = params.Encode()
	return baseURL.String(), nil
}

// loadServerMetadata loads server metadata from disk
func (c *Coordinator) loadServerMetadata() (*ServerMetadata, error) {
	metadataPath := c.getMetadataPath()

	// Read file
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, err
	}

	// Parse metadata
	var metadata ServerMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse metadata file: %w", err)
	}

	return &metadata, nil
}

// saveServerMetadata saves server metadata to disk
func (c *Coordinator) saveServerMetadata(metadata *ServerMetadata) error {
	metadataPath := c.getMetadataPath()

	// Marshal metadata
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	// Write to file
	if err := os.WriteFile(metadataPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write metadata file: %w", err)
	}

	return nil
}

// loadClientInfo loads client info from disk
func (c *Coordinator) loadClientInfo() (*ClientInfo, error) {
	clientInfoPath := c.getClientInfoPath()

	// Read file
	data, err := os.ReadFile(clientInfoPath)
	if err != nil {
		return nil, err
	}

	// Parse client info
	var clientInfo ClientInfo
	if err := json.Unmarshal(data, &clientInfo); err != nil {
		return nil, fmt.Errorf("failed to parse client info file: %w", err)
	}

	return &clientInfo, nil
}

// saveClientInfo saves client info to disk
func (c *Coordinator) saveClientInfo(clientInfo *ClientInfo) error {
	clientInfoPath := c.getClientInfoPath()

	// Marshal client info
	data, err := json.MarshalIndent(clientInfo, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal client info: %w", err)
	}

	// Write to file
	if err := os.WriteFile(clientInfoPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write client info file: %w", err)
	}

	return nil
}

// getConfigDir gets the base directory for configuration files
func getConfigDir() string {
	// Check for custom config dir from env var
	if dir := os.Getenv("MCP_REMOTE_CONFIG_DIR"); dir != "" {
		return dir
	}

	// Default to ~/.mcp-auth
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory if home dir can't be determined
		return ".mcp-auth"
	}

	return filepath.Join(homeDir, ".mcp-auth")
}

// getMetadataPath gets the path for server metadata
func (c *Coordinator) getMetadataPath() string {
	return filepath.Join(getConfigDir(), c.serverURLHash, "server_metadata.json")
}

// getClientInfoPath gets the path for client info
func (c *Coordinator) getClientInfoPath() string {
	return filepath.Join(getConfigDir(), c.serverURLHash, "client_info.json")
}

// getTokensPath gets the path for tokens
func (c *Coordinator) getTokensPath() string {
	return filepath.Join(getConfigDir(), c.serverURLHash, "tokens.json")
}
