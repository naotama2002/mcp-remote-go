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

	// Go's flag package stops parsing at the first non-flag argument.
	// Re-parse remaining args to support flags after positional arguments,
	// e.g.: mcp-remote-go https://server/mcp --transport streamable-http
	remaining := flag.Args()
	var positionalArgs []string
	for i := 0; i < len(remaining); i++ {
		arg := remaining[i]
		switch {
		case (arg == "--transport" || arg == "-transport") && i+1 < len(remaining):
			transportMode = remaining[i+1]
			i++
		case strings.HasPrefix(arg, "--transport=") || strings.HasPrefix(arg, "-transport="):
			transportMode = strings.SplitN(arg, "=", 2)[1]
		case (arg == "--header" || arg == "-header") && i+1 < len(remaining):
			headers = append(headers, remaining[i+1])
			i++
		case strings.HasPrefix(arg, "--header=") || strings.HasPrefix(arg, "-header="):
			headers = append(headers, strings.SplitN(arg, "=", 2)[1])
		case arg == "--allow-http" || arg == "-allow-http":
			allowHTTP = true
		case (arg == "--port" || arg == "-port") && i+1 < len(remaining):
			if _, err := fmt.Sscanf(remaining[i+1], "%d", &callbackPort); err != nil {
				log.Printf("Warning: failed to parse port: %v", err)
			}
			i++
		default:
			positionalArgs = append(positionalArgs, arg)
		}
	}

	// If server URL not provided as a flag, check positional arguments
	if serverURL == "" && len(positionalArgs) > 0 {
		serverURL = positionalArgs[0]

		// If port is provided as second positional argument
		if len(positionalArgs) > 1 {
			if _, err := fmt.Sscanf(positionalArgs[1], "%d", &callbackPort); err != nil {
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
