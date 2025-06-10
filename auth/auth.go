package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/naotama2002/mcp-remote-go/internal/filelock"
	"github.com/naotama2002/mcp-remote-go/internal/httpclient"
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

	// 2. Start callback server if not already running to find an available port
	if c.callbackServer == nil {
		if err := c.startCallbackServer(); err != nil {
			return "", fmt.Errorf("failed to start callback server: %w", err)
		}
	}

	// 3. Register client if needed (uses the potentially updated port)
	clientInfo, err := c.loadOrRegisterClient()
	if err != nil {
		return "", fmt.Errorf("client registration failed: %w", err)
	}
	c.clientInfo = clientInfo

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

	// Prepare form data for token request
	formData := map[string]string{
		"grant_type":   "authorization_code",
		"code":         code,
		"redirect_uri": fmt.Sprintf("http://localhost:%d/callback", c.callbackPort),
		"client_id":    c.clientInfo.ClientID,
	}

	// Add client secret if available
	if c.clientInfo.ClientSecret != "" {
		formData["client_secret"] = c.clientInfo.ClientSecret
	}

	// Create HTTP client and send request
	client := httpclient.New(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.PostForm(ctx, c.serverMetadata.TokenEndpoint, formData, nil)
	if err != nil {
		return nil, fmt.Errorf("token exchange failed: %w", err)
	}
	defer func() { _ = resp.SafeClose() }()

	// Parse tokens
	var tokens Tokens
	if err := resp.JSON(&tokens); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	return &tokens, nil
}

// LoadTokens loads tokens from disk with file locking
func (c *Coordinator) LoadTokens() (*Tokens, error) {
	tokensPath := c.getTokensPath()
	lock := filelock.New(tokensPath)

	var tokens Tokens
	err := lock.WithLock(5*time.Second, func() error {
		// Read file
		data, err := os.ReadFile(tokensPath)
		if err != nil {
			return err
		}

		// Parse tokens
		if err := json.Unmarshal(data, &tokens); err != nil {
			return fmt.Errorf("failed to parse tokens file: %w", err)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return &tokens, nil
}

// SaveTokens saves tokens to disk with file locking
func (c *Coordinator) SaveTokens(tokens *Tokens) error {
	tokensPath := c.getTokensPath()
	lock := filelock.New(tokensPath)

	return lock.WithLock(5*time.Second, func() error {
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
	})
}

// discoverServerMetadata discovers OAuth server metadata using multiple strategies
func (c *Coordinator) discoverServerMetadata(serverURL string) (*ServerMetadata, error) {
	// First try to load cached metadata
	metadata, err := c.loadServerMetadata()
	if err == nil {
		return metadata, nil
	}

	// Use the discovery service to find metadata
	discoveryService := NewMetadataDiscoveryService()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	metadata, err = discoveryService.Discover(ctx, serverURL)
	if err != nil {
		return nil, fmt.Errorf("failed to discover server metadata: %w", err)
	}

	// Save discovered metadata
	if err := c.saveServerMetadata(metadata); err != nil {
		log.Printf("Warning: failed to save server metadata: %v", err)
	}

	return metadata, nil
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

	// Send registration request using httpclient
	client := httpclient.New(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.Post(ctx, c.serverMetadata.RegistrationEndpoint, regReq, nil)
	if err != nil {
		return nil, fmt.Errorf("client registration failed: %w", err)
	}
	defer func() { _ = resp.SafeClose() }()

	// Parse response
	var clientInfoResp ClientInfo
	if err := resp.JSON(&clientInfoResp); err != nil {
		return nil, fmt.Errorf("failed to parse client registration response: %w", err)
	}

	// Save client info
	if err := c.saveClientInfo(&clientInfoResp); err != nil {
		return nil, fmt.Errorf("failed to save client info: %w", err)
	}

	return &clientInfoResp, nil
}

// startCallbackServer starts the HTTP server to receive the OAuth callback.
// It tries to find an available port, starting from the one configured,
// and updates the coordinator's port to the one it successfully binds to.
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
			if _, err := w.Write([]byte(`
				<html>
				<head><title>Authorization Successful</title></head>
				<body>
					<h1>Authorization Successful</h1>
					<p>You can close this window and return to the application.</p>
					<script>window.close();</script>
				</body>
				</html>
			`)); err != nil {
				log.Printf("Warning: failed to write response: %v", err)
			}
		default:
			http.Error(w, "Authorization flow not in progress", http.StatusBadRequest)
		}
	})

	// Find an available port and start the server
	var listener net.Listener
	var err error
	basePort := c.callbackPort
	for i := 0; i < 100; i++ { // Try up to 100 ports from the base port
		port := basePort + i
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		listener, err = net.Listen("tcp", addr)
		if err == nil {
			c.callbackPort = port // Update to the successfully bound port
			log.Printf("Callback server listening on %s", addr)
			break
		}
	}

	if err != nil {
		return fmt.Errorf("could not find an available port for callback server after 100 attempts: %w", err)
	}

	// Create server
	c.callbackServer = &http.Server{
		Handler: mux,
	}

	// Start the server in a goroutine
	go func() {
		if err := c.callbackServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
