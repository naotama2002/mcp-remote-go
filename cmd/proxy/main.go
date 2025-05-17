package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/naotama2002/mcp-remote-go/internal/auth"
	"github.com/naotama2002/mcp-remote-go/internal/transport"
	"github.com/naotama2002/mcp-remote-go/internal/utils"
)

// Version of MCP Remote Proxy
const MCP_REMOTE_VERSION = "0.1.0"

// Main function
func main() {
	// Parse command line arguments
	args, err := utils.ParseCommandLineArgs(os.Args[1:], "Usage: mcp-remote-go <https://server-url> [callback-port]")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Get server URL and callback port
	serverURL := args.ServerURL
	callbackPort := args.CallbackPort
	headers := args.Headers
	transportStrategy := args.TransportStrategy

	// Run proxy
	if err := runProxy(serverURL, callbackPort, headers, transportStrategy); err != nil {
		fmt.Fprintf(os.Stderr, "Fatal error: %v\n", err)
		os.Exit(1)
	}
}

// Function to run the proxy
func runProxy(serverURL string, callbackPort int, headers map[string]string, transportStrategy transport.TransportStrategy) error {
	// Get hash of the server URL
	serverURLHash := auth.GetServerURLHash(serverURL)

	// Create OAuth2 client provider using golang.org/x/oauth2 package
	authProvider := auth.NewOAuth2ClientProvider(auth.OAuthProviderOptions{
		ServerURL:       serverURL,
		CallbackPort:    callbackPort,
		ClientName:      "MCP CLI Proxy",
		ClientURI:       "https://github.com/naotama2002/mcp-remote-go",
		SoftwareID:      "2e6dc280-f3c3-4e01-99a7-8181dbd1d23d",
		SoftwareVersion: MCP_REMOTE_VERSION,
	})

	// Create lazy authentication coordinator
	authCoordinator := auth.NewLazyAuthCoordinator(serverURLHash, callbackPort)

	// Create local transport
	localTransport := transport.NewStdioServerTransport()

	// Track server instance
	var server interface{}

	// Define authentication initializer function
	authInitializer := func() (*auth.AuthState, error) {
		authState, err := authCoordinator.InitializeAuth()
		if err != nil {
			return nil, err
		}

		// Save server to outer scope
		server = authState.Server

		// If authentication was completed by another instance
		if authState.SkipBrowserAuth {
			log.Println("Authentication completed by another instance - using tokens from disk")
			// Wait a bit as callback might happen before tokens are exchanged
			// TODO: Remove this
			// time.Sleep(1 * time.Second)
		}

		return authState, nil
	}

	// Connect to remote server
	remoteTransport, err := utils.ConnectToRemoteServer(serverURL, authProvider, headers, authInitializer, transportStrategy)
	if err != nil {
		return fmt.Errorf("failed to connect to remote server: %w", err)
	}

	// Start local transport
	if err := localTransport.Start(); err != nil {
		return fmt.Errorf("failed to start local transport: %w", err)
	}
	log.Println("Local STDIO server is running")

	// Set up bidirectional proxy
	proxy := utils.NewMCPProxy(localTransport, remoteTransport)
	proxy.Start()

	log.Printf("Proxy between local STDIO and remote server successfully established")
	log.Println("Press Ctrl+C to exit")

	// Set up cleanup handler
	cleanup := func() error {
		if err := remoteTransport.Close(); err != nil {
			return fmt.Errorf("failed to close remote transport: %w", err)
		}
		if err := localTransport.Close(); err != nil {
			return fmt.Errorf("failed to close local transport: %w", err)
		}
		// Close auth server if initialized
		if server != nil {
			if httpServer, ok := server.(*http.Server); ok {
				if err := httpServer.Close(); err != nil {
					return fmt.Errorf("failed to close auth server: %w", err)
				}
			}
		}
		return nil
	}

	// Set up signal handler
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
	log.Println("Signal received. Performing cleanup...")
	if err := cleanup(); err != nil {
		return fmt.Errorf("cleanup error: %v", err)
	}

	return nil
}
