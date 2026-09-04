package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
	// RegisteredIssuer is the authorization server issuer this client_id was
	// registered with (RFC 8414 issuer). Used to invalidate stale cache when the
	// discovered AS changes.
	RegisteredIssuer string `json:"registered_issuer,omitempty"`
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
	// TokenEndpointAuthMethodsSupported lists how a client may authenticate at
	// the token endpoint (RFC 8414 §2). Declaring a method absent from this
	// list is what registration is rejected for.
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported,omitempty"`
	// ResourceScopesSupported carries the `scopes_supported` list published by
	// the protected resource itself (RFC 9728 §2), not by the authorization
	// server. Those are the scopes that grant access to this MCP server, so
	// they -- and not the authorization server's full catalogue -- are what an
	// authorization request should ask for.
	ResourceScopesSupported []string `json:"resource_scopes_supported,omitempty"`
	GrantTypesSupported     []string `json:"grant_types_supported,omitempty"`
	// AuthorizationResponseIssParameterSupported reports whether the server
	// returns the `iss` parameter on authorization responses (RFC 9207 §3).
	// When true, a response without `iss` is rejected.
	AuthorizationResponseIssParameterSupported bool `json:"authorization_response_iss_parameter_supported,omitempty"`
}

// callbackResult carries the outcome of the OAuth callback to the goroutine
// waiting in WaitForAuthCode.
type callbackResult struct {
	code string
	err  error
}

// Coordinator handles the OAuth flow
type Coordinator struct {
	serverURLHash  string
	callbackPort   int
	callbackServer *http.Server
	clientInfo     *ClientInfo
	serverMetadata *ServerMetadata
	resource       string // RFC 8707 canonical resource URI, reused across the flow
	challengeScope string // `scope` from the WWW-Authenticate challenge that triggered this flow
	codeVerifier   string
	state          string // CSRF binding between the authorization request and its callback
	authMutex      sync.Mutex
	callbackChan   chan callbackResult
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
		callbackChan:  make(chan callbackResult),
	}, nil
}

type InitOption func(*initConfig)

type initConfig struct {
	resourceMetadataURL string
	challengeScope      string
}

// WithResourceMetadataURL passes a Protected Resource Metadata URL extracted
// from a WWW-Authenticate header (RFC 9728 §5.1); when set, discovery fetches
// it directly instead of deriving a URL from the MCP server host.
func WithResourceMetadataURL(url string) InitOption {
	return func(c *initConfig) {
		c.resourceMetadataURL = url
	}
}

// WithChallengeScope passes the `scope` parameter of the WWW-Authenticate
// challenge that triggered the flow (RFC 6750 §3.1). A resource server that
// names the scopes it wants is stating them for this exact request, so the
// value takes precedence over anything discovery advertises.
func WithChallengeScope(scope string) InitOption {
	return func(c *initConfig) {
		c.challengeScope = scope
	}
}

// InitializeAuth starts the OAuth flow
func (c *Coordinator) InitializeAuth(serverURL string, opts ...InitOption) (string, error) {
	c.authMutex.Lock()
	defer c.authMutex.Unlock()

	cfg := &initConfig{}
	for _, o := range opts {
		o(cfg)
	}

	resource, err := CanonicalResourceURI(serverURL)
	if err != nil {
		return "", fmt.Errorf("failed to derive canonical resource URI: %w", err)
	}
	c.resource = resource
	c.challengeScope = normalizeScope(cfg.challengeScope)

	metadata, err := c.discoverServerMetadata(serverURL, cfg.resourceMetadataURL)
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
	case result := <-c.callbackChan:
		if result.err != nil {
			return "", result.err
		}
		return result.code, nil
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

	// RFC 8707 resource indicator (required by the MCP authorization spec).
	if c.resource != "" {
		formData["resource"] = c.resource
	}

	// Add PKCE code_verifier
	if c.codeVerifier != "" {
		formData["code_verifier"] = c.codeVerifier
	}

	// Authenticate the request the way this client is registered to.
	headers := make(map[string]string)
	c.applyClientAuthentication(formData, headers)

	// Create HTTP client and send request
	client := httpclient.New(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.PostForm(ctx, c.serverMetadata.TokenEndpoint, formData, headers)
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

// Client authentication methods for the token endpoint (RFC 7591 §2 /
// RFC 6749 §2.3.1).
const (
	// authMethodNone is a public client: no secret, PKCE alone. This is what an
	// installed CLI should be, so it is preferred wherever a server allows it.
	authMethodNone = "none"

	// authMethodSecretPost carries the credentials in the request body.
	authMethodSecretPost = "client_secret_post"

	// authMethodSecretBasic carries them in an Authorization header. RFC 8414
	// makes it the default when a server publishes no list at all.
	authMethodSecretBasic = "client_secret_basic"
)

// tokenEndpointAuthMethod picks how this client will authenticate at the token
// endpoint, from the methods the authorization server says it accepts.
//
// Declaring "none" unconditionally is the same mistake as requesting a scope
// nobody advertised: a server that does not offer it rejects the registration.
// A public client is still preferred -- there is no secret to store and PKCE
// already binds the exchange -- but only where the server allows it.
func (c *Coordinator) tokenEndpointAuthMethod() string {
	if c.serverMetadata == nil || len(c.serverMetadata.TokenEndpointAuthMethodsSupported) == 0 {
		// Nothing published. "none" is what has always been sent here, and a
		// server with no metadata to contradict it has nothing to reject.
		return authMethodNone
	}

	for _, preferred := range []string{authMethodNone, authMethodSecretPost, authMethodSecretBasic} {
		if containsTrimmed(c.serverMetadata.TokenEndpointAuthMethodsSupported, preferred) {
			return preferred
		}
	}

	// Only methods this client cannot perform (private_key_jwt and the like).
	// Ask for the simplest one and let the server explain itself, which is more
	// useful than failing before the request is made.
	log.Printf("Authorization server accepts none of the supported client authentication methods (%v); requesting %s",
		c.serverMetadata.TokenEndpointAuthMethodsSupported, authMethodNone)
	return authMethodNone
}

// applyClientAuthentication authenticates the token request as the method
// assigned to this client requires.
//
// The method comes from the registration response when the server stated one:
// that is the server's own record of how this client must authenticate, which
// outranks any preference of ours.
func (c *Coordinator) applyClientAuthentication(formData map[string]string, headers map[string]string) {
	method := c.clientInfo.TokenEndpointAuthMethod
	if method == "" {
		method = c.tokenEndpointAuthMethod()
	}

	if c.clientInfo.ClientSecret == "" {
		// Nothing to authenticate with; client_id in the body identifies the
		// client, as a public client does.
		return
	}

	if method == authMethodSecretBasic {
		// RFC 6749 §2.3.1: both parts are form-urlencoded before being joined
		// and base64-encoded, and the id must not also appear in the body.
		credentials := url.QueryEscape(c.clientInfo.ClientID) + ":" + url.QueryEscape(c.clientInfo.ClientSecret)
		headers["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(credentials))
		return
	}

	formData["client_secret"] = c.clientInfo.ClientSecret
}

// offlineAccessScope is the scope that asks for a refresh token. The proxy
// outlives a single access token, so it is worth requesting -- but only where a
// server says it exists.
const offlineAccessScope = "offline_access"

// requestedScope returns the value for the `scope` parameter of the
// registration and authorization requests, or "" to send no scope at all.
//
// Every scope here is one a server advertised. Inventing one is not harmless:
// an authorization server that does not recognise it rejects the whole request
// with invalid_scope, which is how a hardcoded "mcp" -- a name no RFC or MCP
// specification defines -- shut out every server using its own scope
// vocabulary. When nothing advertises a scope, omitting the parameter lets the
// authorization server apply its default, which is a request it can answer.
func (c *Coordinator) requestedScope() string {
	// A challenge names what this particular request was refused for, which is
	// more specific than any published catalogue.
	if c.challengeScope != "" {
		return c.challengeScope
	}
	if c.serverMetadata == nil {
		return ""
	}

	scopes := nonEmptyScopes(c.serverMetadata.ResourceScopesSupported)
	if len(scopes) == 0 {
		// The authorization server's own scopes_supported is deliberately not
		// used as a fallback: it lists everything the server can issue for any
		// resource, so asking for all of it would request far more than access
		// to this MCP server needs.
		return ""
	}

	// offline_access is not a resource scope, so a protected resource has no
	// reason to list it; take it from the authorization server when offered.
	if !containsTrimmed(scopes, offlineAccessScope) && containsTrimmed(c.serverMetadata.ScopesSupported, offlineAccessScope) {
		scopes = append(scopes, offlineAccessScope)
	}

	return strings.Join(scopes, " ")
}

// normalizeScope collapses a scope string to single-space-separated tokens.
func normalizeScope(scope string) string {
	return strings.Join(strings.Fields(scope), " ")
}

// nonEmptyScopes copies scopes, dropping blank entries a server may have
// published.
func nonEmptyScopes(scopes []string) []string {
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func containsTrimmed(scopes []string, want string) bool {
	for _, s := range scopes {
		if strings.TrimSpace(s) == want {
			return true
		}
	}
	return false
}

func (c *Coordinator) discoverServerMetadata(serverURL, resourceMetadataURL string) (*ServerMetadata, error) {
	// Skip cache when the caller supplied an explicit PRM URL: the cached
	// entry may have come from a different (less authoritative) path.
	if resourceMetadataURL == "" {
		if metadata, err := c.loadServerMetadata(); err == nil {
			return metadata, nil
		}
	}

	// Use the discovery service to find metadata
	discoveryService := NewMetadataDiscoveryService()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	metadata, err := discoveryService.Discover(ctx, serverURL, WithProtectedResourceMetadataURL(resourceMetadataURL))
	if err != nil {
		return nil, fmt.Errorf("failed to discover server metadata: %w", err)
	}

	// Save discovered metadata
	if err := c.saveServerMetadata(metadata); err != nil {
		log.Printf("Warning: failed to save server metadata: %v", err)
	}

	return metadata, nil
}

// loadOrRegisterClient returns cached ClientInfo when it still matches the
// currently-discovered authorization server (RFC 8414 issuer); otherwise it
// performs RFC 7591 dynamic client registration. Issuer comparison covers the
// WWW-Authenticate-driven discovery case where the AS may have changed without
// changing the resource server URL, while still letting AS-with-no-DCR
// configurations succeed via the cached static client_id.
func (c *Coordinator) loadOrRegisterClient() (*ClientInfo, error) {
	clientInfo, err := c.loadClientInfo()
	if err == nil && c.clientInfoMatchesServer(clientInfo) {
		// Record which authorization server the credentials were accepted
		// for, so an entry is only ever unbound once.
		if clientInfo.RegisteredIssuer == "" && c.serverMetadata != nil && c.serverMetadata.Issuer != "" {
			clientInfo.RegisteredIssuer = c.serverMetadata.Issuer
			if saveErr := c.saveClientInfo(clientInfo); saveErr != nil {
				log.Printf("Warning: failed to record the issuer for cached client info: %v", saveErr)
			}
		}
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
		"token_endpoint_auth_method": c.tokenEndpointAuthMethod(),
		"grant_types":                []string{"authorization_code"},
		// A locally-installed CLI redirecting to loopback is a native client;
		// declaring it lets the authorization server apply the right redirect
		// URI rules instead of guessing (OpenID Connect Registration §2,
		// required by the MCP authorization spec since 2026-07-28).
		"application_type": "native",
	}

	// RFC 7591 §2: `scope` is optional, and omitting it leaves the scopes to
	// the authorization server rather than claiming ones it may not know.
	if scope := c.requestedScope(); scope != "" {
		regReq["scope"] = scope
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

	if c.serverMetadata != nil {
		clientInfoResp.RegisteredIssuer = c.serverMetadata.Issuer
	}

	// Save client info
	if err := c.saveClientInfo(&clientInfoResp); err != nil {
		return nil, fmt.Errorf("failed to save client info: %w", err)
	}

	return &clientInfoResp, nil
}

// clientInfoMatchesServer reports whether cached credentials may be presented
// to the authorization server currently discovered.
//
// Credentials belong to the server that issued them. Protected Resource
// Metadata can name a different authorization server than it did last time, so
// reuse without checking would send a client_id -- and any secret alongside it
// -- to a server it was never registered with.
//
// Entries written before the issuer was recorded cannot be checked that way,
// and simply rejecting them is not free: falling through to registration is
// what fails when the server offers none, and that cached credential is then
// the only one available. So the answer depends on what is at stake and on
// whether there is any alternative.
func (c *Coordinator) clientInfoMatchesServer(clientInfo *ClientInfo) bool {
	if c.serverMetadata == nil || clientInfo == nil {
		return true
	}

	if clientInfo.RegisteredIssuer != "" {
		return clientInfo.RegisteredIssuer == c.serverMetadata.Issuer
	}

	if clientInfo.ClientSecret != "" {
		// A secret is never worth presenting to a server that cannot be shown
		// to be the one holding it.
		log.Println("Discarding cached client credentials: they carry a secret but no recorded issuer")
		return false
	}

	if c.serverMetadata.RegistrationEndpoint != "" {
		// Registering again costs one request and settles the question.
		log.Println("Re-registering: the cached client_id has no recorded issuer")
		return false
	}

	// Nothing to re-register with, and a public client_id carries no secret to
	// leak. Using it is better than failing outright, and loadOrRegisterClient
	// binds it to this issuer so the question is not reopened.
	log.Printf("Using a cached client_id with no recorded issuer against %s: "+
		"the server offers no dynamic registration, so there is no alternative", c.serverMetadata.Issuer)
	return true
}

// startCallbackServer starts the HTTP server to receive the OAuth callback.
// It tries to find an available port, starting from the one configured,
// and updates the coordinator's port to the one it successfully binds to.
func (c *Coordinator) startCallbackServer() error {
	mux := http.NewServeMux()

	// Callback handler
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()

		c.authMutex.Lock()
		expectedState := c.state
		metadata := c.serverMetadata
		c.authMutex.Unlock()

		if err := validateAuthorizationResponse(query, expectedState, metadata); err != nil {
			// Report the failure to the waiting flow rather than letting it
			// sit until the five-minute timeout.
			select {
			case c.callbackChan <- callbackResult{err: err}:
			default:
			}
			log.Printf("Rejected OAuth callback: %v", err)
			http.Error(w, "Authorization failed: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Send the code to the waiting goroutine
		select {
		case c.callbackChan <- callbackResult{code: query.Get("code")}:
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

// buildAuthorizationURL builds the authorization URL with PKCE (S256)
func (c *Coordinator) buildAuthorizationURL() (string, error) {
	if c.serverMetadata == nil || c.clientInfo == nil {
		return "", errors.New("auth not initialized")
	}

	// Generate PKCE code verifier
	verifier, err := GenerateCodeVerifier()
	if err != nil {
		return "", fmt.Errorf("failed to generate PKCE code verifier: %w", err)
	}
	c.codeVerifier = verifier

	// Generate the CSRF state bound to this authorization request. Callers
	// hold authMutex, so this is safe to assign directly.
	state, err := GenerateState()
	if err != nil {
		return "", err
	}
	c.state = state

	// Build params
	params := url.Values{}
	params.Set("client_id", c.clientInfo.ClientID)
	params.Set("redirect_uri", fmt.Sprintf("http://localhost:%d/callback", c.callbackPort))
	params.Set("response_type", "code")
	params.Set("state", state)
	params.Set("code_challenge", ComputeCodeChallenge(verifier))
	params.Set("code_challenge_method", "S256")

	// Omitted rather than guessed when no server advertised a scope; see
	// requestedScope.
	if scope := c.requestedScope(); scope != "" {
		params.Set("scope", scope)
	}

	// RFC 8707 resource indicator (required by the MCP authorization spec).
	if c.resource != "" {
		params.Set("resource", c.resource)
	}

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

	// Default to ~/.mcp-remote-go-auth
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory if home dir can't be determined
		return ".mcp-remote-go-auth"
	}

	return filepath.Join(homeDir, ".mcp-remote-go-auth")
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
