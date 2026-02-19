package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/naotama2002/mcp-remote-go/proxy"
)

func main() {
	var serverURL string
	var callbackPort int
	var allowHTTP bool
	var transportMode string
	var headers flagList

	flag.StringVar(&serverURL, "server", "", "The MCP server URL to connect to")
	flag.IntVar(&callbackPort, "port", 3334, "The callback port for OAuth")
	flag.BoolVar(&allowHTTP, "allow-http", false, "Allow HTTP connections (only for trusted networks)")
	flag.StringVar(&transportMode, "transport", "auto", "Transport mode: auto, streamable-http, sse")
	flag.Var(&headers, "header", "Custom header to include in requests (format: 'Key:Value')")
	flag.Parse()

	// If server URL not provided as a flag, check if it's the first argument
	if serverURL == "" && len(flag.Args()) > 0 {
		serverURL = flag.Arg(0)

		// If port is provided as second argument
		if len(flag.Args()) > 1 {
			if _, err := fmt.Sscanf(flag.Arg(1), "%d", &callbackPort); err != nil {
				log.Printf("Warning: failed to parse callback port: %v", err)
			}
		}
	}

	if serverURL == "" {
		fmt.Println("Usage: mcp-remote-go -server <server-url> [-port <callback-port>] [-allow-http] [-transport auto|streamable-http|sse] [-header 'Key:Value'] ...")
		os.Exit(1)
	}

	// Validate URL scheme
	if !allowHTTP && !strings.HasPrefix(serverURL, "https://") {
		log.Fatal("Error: Only HTTPS URLs are allowed. Use -allow-http for insecure connections.")
	}

	// Validate transport mode
	mode := proxy.TransportMode(transportMode)
	switch mode {
	case proxy.TransportModeAuto, proxy.TransportModeStreamableHTTP, proxy.TransportModeSSE:
		// valid
	default:
		log.Fatalf("Error: Invalid transport mode '%s'. Must be one of: auto, streamable-http, sse", transportMode)
	}

	// Convert headers to a map
	headerMap := make(map[string]string)
	for _, h := range headers {
		parts := strings.SplitN(h, ":", 2)
		if len(parts) == 2 {
			headerMap[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}

	// Get server URL hash for storage
	serverURLHash := getServerURLHash(serverURL)

	// Create and start the proxy
	p, err := proxy.NewProxyWithTransport(serverURL, callbackPort, headerMap, serverURLHash, mode)
	if err != nil {
		log.Fatalf("Failed to create proxy: %v", err)
	}

	// Set up graceful shutdown
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-signals
		fmt.Println("Shutting down...")
		p.Shutdown()
		os.Exit(0)
	}()

	// Start the proxy
	if err := p.Start(); err != nil {
		log.Fatalf("Proxy error: %v", err)
	}
}

// getServerURLHash creates a unique hash based on the server URL
func getServerURLHash(serverURL string) string {
	hash := sha256.Sum256([]byte(serverURL))
	return hex.EncodeToString(hash[:])
}

// flagList is a custom flag type to handle multiple header entries
type flagList []string

func (f *flagList) String() string {
	return fmt.Sprint(*f)
}

func (f *flagList) Set(value string) error {
	*f = append(*f, value)
	return nil
}
