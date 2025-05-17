package utils

import (
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"

	"github.com/naotama2002/mcp-remote-go/internal/auth"
	"github.com/naotama2002/mcp-remote-go/internal/transport"
)

// MCPProxy sets up a proxy between transports
type MCPProxy struct {
	transportToClient transport.Transport
	transportToServer transport.Transport
	mu                sync.Mutex
	clientClosed      bool
	serverClosed      bool
}

// NewMCPProxy creates a new MCP proxy
func NewMCPProxy(transportToClient, transportToServer transport.Transport) *MCPProxy {
	return &MCPProxy{
		transportToClient: transportToClient,
		transportToServer: transportToServer,
	}
}

// Start begins the proxy operation
func (p *MCPProxy) Start() {
	// Message handler from client to server
	p.transportToClient.SetMessageHandler(func(message interface{}) {
		log.Println("[Local→Remote]", getMessageIdentifier(message))
		
		// Modify clientInfo for initialize messages
		if msg, ok := message.(map[string]interface{}); ok {
			if method, ok := msg["method"].(string); ok && method == "initialize" {
				if params, ok := msg["params"].(map[string]interface{}); ok {
					if clientInfo, ok := params["clientInfo"].(map[string]interface{}); ok {
						if name, ok := clientInfo["name"].(string); ok {
							clientInfo["name"] = name + " (via mcp-remote-go)"
						}
					}
				}
			}
		}
		
		// Send message to server
		if err := p.transportToServer.Send(message); err != nil {
			log.Printf("Error sending message to server: %v", err)
		}
	})

	// Message handler from server to client
	p.transportToServer.SetMessageHandler(func(message interface{}) {
		log.Println("[Remote→Local]", getMessageIdentifier(message))
		
		// Send message to client
		if err := p.transportToClient.Send(message); err != nil {
			log.Printf("Error sending message to client: %v", err)
		}
	})

	// Client close handler
	p.transportToClient.SetCloseHandler(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		
		if p.serverClosed {
			return
		}
		
		p.clientClosed = true
		p.transportToServer.Close()
	})

	// Server close handler
	p.transportToServer.SetCloseHandler(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		
		if p.clientClosed {
			return
		}
		
		p.serverClosed = true
		p.transportToClient.Close()
	})

	// Client error handler
	p.transportToClient.SetErrorHandler(func(err error) {
		log.Printf("Client error: %v", err)
	})

	// Server error handler
	p.transportToServer.SetErrorHandler(func(err error) {
		log.Printf("Server error: %v", err)
	})
}

// getMessageIdentifier retrieves the identifier for a message
func getMessageIdentifier(message interface{}) string {
	if msg, ok := message.(map[string]interface{}); ok {
		if method, ok := msg["method"].(string); ok {
			return method
		}
		if id, ok := msg["id"]; ok {
			return fmt.Sprintf("id:%v", id)
		}
	}
	return "unknown"
}

// ConnectToRemoteServer connects to a remote server
func ConnectToRemoteServer(
	serverURL string,
	authProvider auth.OAuthProvider,
	headers map[string]string,
	authInitializer func() (*auth.AuthState, error),
	transportStrategy transport.TransportStrategy,
	recursionReasons ...string,
) (transport.Transport, error) {
	log.Printf("Connecting to remote server: %s", serverURL)
	
	// Parse URL
	url, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse URL: %w", err)
	}
	
	// Track reasons for recursive calls
	reasonSet := make(map[string]bool)
	for _, reason := range recursionReasons {
		reasonSet[reason] = true
	}
	
	log.Printf("Transport strategy: %s", transportStrategy)
	
	// Whether to attempt fallback
	shouldAttemptFallback := transportStrategy == transport.HTTPFirst || transportStrategy == transport.SSEFirst
	
	// Create transport based on strategy
	useSSE := transportStrategy == transport.SSEOnly || transportStrategy == transport.SSEFirst
	var t transport.Transport
	
	if useSSE {
		// Create SSE transport
		t = transport.NewSSEClientTransport(url, transport.SSEClientOptions{
			AuthProvider: authProvider,
			Headers:      headers,
		})
	} else {
		// Create HTTP transport
		t = transport.NewHTTPClientTransport(url, transport.HTTPClientOptions{
			AuthProvider: authProvider,
			Headers:      headers,
		})
	}
	
	// Start transport
	err = t.Start()
	
	// Always run authentication flow
	// Uncomment the following line to only authenticate when required
	// if err == transport.ErrAuthRequired {
	if true {
		// Error if authentication has already been attempted
		if reasonSet[transport.ReasonAuthNeeded] {
			return nil, fmt.Errorf("authentication has already been attempted and failed")
		}
		
		log.Println("Authentication required. Starting authentication flow...")
		
		// Parse server URL if needed for debugging
		_, err = url.Parse(serverURL)
		if err != nil {
			return nil, fmt.Errorf("failed to parse server URL: %w", err)
		}
		
		// Get client information or create default if not available
		clientInfo, err := authProvider.ClientInformation()
		if err != nil || clientInfo == nil || clientInfo.ClientID == "" {
			log.Println("No client information found, creating default client information...")
			
			// Generate client metadata according to OAuth 2.1 specifications
			// This follows the OAuth 2.1 client registration protocol
			// See: https://oauth.net/2.1/
			
			// Use default client ID
			clientInfo = &auth.OAuthClientInformation{
				ClientID: "mcp-remote-go-client", // Default client ID
				RedirectURIs: []string{authProvider.RedirectURL()},
				TokenEndpointAuthMethod: "none", // Public client
				GrantTypes: []string{"authorization_code", "refresh_token"},
				ResponseTypes: []string{"code"},
			}
			
			// Save client information
			if err := authProvider.SaveClientInformation(clientInfo); err != nil {
				log.Printf("Warning: failed to save client information: %v", err)
			}
		}
		
		// Initialize authentication
		authState, err := authInitializer()
		if err != nil {
			return nil, fmt.Errorf("failed to initialize authentication: %w", err)
		}
		
		// Wait for authentication code and exchange for tokens
		if !authState.SkipBrowserAuth {
			// Let the OAuth2 provider handle the authorization URL construction
			// This follows the OAuth2.1 standard and avoids any hardcoded paths
			
			// The OAuth2 provider (golang.org/x/oauth2) will handle the proper URL construction
			// including all necessary parameters and PKCE challenge
			log.Println("Starting OAuth2.1 authorization flow...")
			
			// Create a dummy URL just to trigger the RedirectToAuthorization method
			// The actual URL will be constructed by the OAuth2 provider
			authURLParsed, _ := url.Parse(serverURL)
			
			// Redirect to authorization URL
			log.Println("Redirecting to authorization URL...")
			if err := authProvider.RedirectToAuthorization(authURLParsed); err != nil {
				return nil, fmt.Errorf("failed to redirect to authorization URL: %w", err)
			}
			
			log.Println("Waiting for authentication code...")
			authCode, err := authState.WaitForAuthCode()
			if err != nil {
				return nil, fmt.Errorf("failed to get authentication code: %w", err)
			}
			
			// Exchange code for token using the OAuth provider
			log.Println("Exchanging authorization code for token")
			tokens, err := authProvider.ExchangeCodeForToken(authCode)
			if err != nil {
				return nil, fmt.Errorf("failed to exchange code for token: %w", err)
			}
			
			// Save tokens
			if err := authProvider.SaveTokens(tokens); err != nil {
				return nil, fmt.Errorf("failed to save tokens: %w", err)
			}
			
			log.Println("Successfully obtained and saved access tokens")
		}
		
		// Try to reconnect recursively
		return ConnectToRemoteServer(
			serverURL,
			authProvider,
			headers,
			authInitializer,
			transportStrategy,
			append(recursionReasons, transport.ReasonAuthNeeded)...,
		)
	}
	
	// In case of protocol error, try fallback
	if err != nil && shouldAttemptFallback && isProtocolError(err) {
		log.Printf("Received error: %v", err)
		
		// Error if fallback has already been attempted
		if reasonSet[transport.ReasonTransportFallback] {
			return nil, fmt.Errorf("transport fallback has already been attempted. Giving up.")
		}
		
		log.Printf("Reconnecting for reason: %s", transport.ReasonTransportFallback)
		
		// Determine fallback strategy
		fallbackStrategy := transport.SSEOnly
		if useSSE {
			fallbackStrategy = transport.HTTPOnly
		}
		
		// Try to reconnect recursively
		return ConnectToRemoteServer(
			serverURL,
			authProvider,
			headers,
			authInitializer,
			fallbackStrategy,
			append(recursionReasons, transport.ReasonTransportFallback)...,
		)
	}
	
	// Other errors
	if err != nil {
		return nil, err
	}
	
	log.Printf("Connected to remote server")
	return t, nil
}

// isProtocolError determines if an error is a protocol error
func isProtocolError(err error) bool {
	errStr := err.Error()
	return strings.Contains(errStr, "405") ||
		strings.Contains(errStr, "Method Not Allowed") ||
		strings.Contains(errStr, "404") ||
		strings.Contains(errStr, "Not Found")
}
