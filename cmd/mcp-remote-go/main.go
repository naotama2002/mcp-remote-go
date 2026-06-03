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

// Build-time variables set via -ldflags.
var (
	version   = "dev"
	gitCommit = "unknown"
	buildTime = "unknown"
)

func main() {
	log.Printf("mcp-remote-go version=%s commit=%s built=%s", version, gitCommit, buildTime)

	var serverURL string
	var callbackPort int
	var allowHTTP bool
	var transportMode string
	var httpProxy string
	var headers flagList

	flag.StringVar(&serverURL, "server", "", "The MCP server URL to connect to")
	flag.IntVar(&callbackPort, "port", 3334, "The callback port for OAuth")
	flag.BoolVar(&allowHTTP, "allow-http", false, "Allow HTTP connections (only for trusted networks)")
	flag.StringVar(&transportMode, "transport", "auto", "Transport mode: auto, streamable-http, sse")
	flag.StringVar(&httpProxy, "https-proxy", "", "HTTP/HTTPS proxy URL (e.g. http://proxy:8080)")
	flag.Var(&headers, "header", "Custom header to include in requests (format: 'Key:Value')")
	flag.Parse()

	// Go's flag package stops parsing at the first non-flag argument.
	// Re-parse remaining args to support flags after positional arguments.
	cfg := parseRemainingArgs(flag.Args(), cliConfig{
		serverURL:     serverURL,
		callbackPort:  callbackPort,
		allowHTTP:     allowHTTP,
		transportMode: transportMode,
		httpProxy:     httpProxy,
		headers:       []string(headers),
	})
	serverURL = cfg.serverURL
	callbackPort = cfg.callbackPort
	allowHTTP = cfg.allowHTTP
	transportMode = cfg.transportMode
	httpProxy = cfg.httpProxy
	headers = flagList(cfg.headers)

	// Environment variable overrides (used by MCPB user_config)
	applyEnvOverrides(&serverURL, &callbackPort, &allowHTTP, &transportMode, &httpProxy, &headers)

	if serverURL == "" {
		fmt.Println("Usage: mcp-remote-go -server <server-url> [-port <callback-port>] [-allow-http] [-transport auto|streamable-http|sse] [-https-proxy <proxy-url>] [-header 'Key:Value'] ...")
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
	p, err := proxy.NewProxyWithOptions(serverURL, callbackPort, headerMap, serverURLHash, mode, httpProxy)
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

// cliConfig holds parsed CLI configuration.
type cliConfig struct {
	serverURL     string
	callbackPort  int
	allowHTTP     bool
	transportMode string
	httpProxy     string
	headers       []string
}

// parseRemainingArgs re-parses remaining args after flag.Parse() to support
// flags after positional arguments (e.g.: mcp-remote-go https://server/mcp --transport streamable-http).
func parseRemainingArgs(remaining []string, defaults cliConfig) cliConfig {
	cfg := defaults
	var positionalArgs []string

	for i := 0; i < len(remaining); i++ {
		arg := remaining[i]
		switch {
		case (arg == "--transport" || arg == "-transport") && i+1 < len(remaining):
			cfg.transportMode = remaining[i+1]
			i++
		case strings.HasPrefix(arg, "--transport=") || strings.HasPrefix(arg, "-transport="):
			cfg.transportMode = strings.SplitN(arg, "=", 2)[1]
		case (arg == "--header" || arg == "-header") && i+1 < len(remaining):
			cfg.headers = append(cfg.headers, remaining[i+1])
			i++
		case strings.HasPrefix(arg, "--header=") || strings.HasPrefix(arg, "-header="):
			cfg.headers = append(cfg.headers, strings.SplitN(arg, "=", 2)[1])
		case (arg == "--https-proxy" || arg == "-https-proxy") && i+1 < len(remaining):
			cfg.httpProxy = remaining[i+1]
			i++
		case strings.HasPrefix(arg, "--https-proxy=") || strings.HasPrefix(arg, "-https-proxy="):
			cfg.httpProxy = strings.SplitN(arg, "=", 2)[1]
		case arg == "--allow-http" || arg == "-allow-http":
			cfg.allowHTTP = true
		case (arg == "--port" || arg == "-port") && i+1 < len(remaining):
			if _, err := fmt.Sscanf(remaining[i+1], "%d", &cfg.callbackPort); err != nil {
				log.Printf("Warning: failed to parse port: %v", err)
			}
			i++
		default:
			positionalArgs = append(positionalArgs, arg)
		}
	}

	// If server URL not provided as a flag, check positional arguments
	if cfg.serverURL == "" && len(positionalArgs) > 0 {
		cfg.serverURL = positionalArgs[0]
		if len(positionalArgs) > 1 {
			if _, err := fmt.Sscanf(positionalArgs[1], "%d", &cfg.callbackPort); err != nil {
				log.Printf("Warning: failed to parse callback port: %v", err)
			}
		}
	}

	return cfg
}

// applyEnvOverrides reads environment variables and applies them as overrides.
// mcpbEnv reads an environment variable populated by MCPB user_config
// substitution. When an optional user_config field is left blank, the host
// (Claude Desktop) does not substitute it and the literal "${user_config.NAME}"
// template string reaches the process instead of an empty value. Such values
// must be treated as unset, otherwise they leak into proxy URLs and header
// names (e.g. `invalid header field name "${user_config.header_2_name}"`).
func mcpbEnv(name string) string {
	v := os.Getenv(name)
	if strings.Contains(v, "${user_config.") {
		return ""
	}
	return v
}

// Environment variables are used by MCPB user_config to pass GUI-configured values.
// CLI flags take precedence; env vars only apply when the corresponding flag is at its default.
func applyEnvOverrides(serverURL *string, callbackPort *int, allowHTTP *bool, transportMode *string, httpProxy *string, headers *flagList) {
	if v := mcpbEnv("MCP_SERVER_URL"); v != "" && *serverURL == "" {
		*serverURL = v
	}
	if v := mcpbEnv("MCP_TRANSPORT"); v != "" && *transportMode == "auto" {
		*transportMode = v
	}
	if v := mcpbEnv("MCP_PORT"); v != "" && *callbackPort == 3334 {
		if _, err := fmt.Sscanf(v, "%d", callbackPort); err != nil {
			log.Printf("Warning: failed to parse MCP_PORT: %v", err)
		}
	}
	if v := mcpbEnv("MCP_HTTPS_PROXY"); v != "" && *httpProxy == "" {
		*httpProxy = v
	}
	if os.Getenv("MCP_ALLOW_HTTP") == "true" && !*allowHTTP {
		*allowHTTP = true
	}
	if v := mcpbEnv("MCP_HEADERS"); v != "" {
		for _, h := range parseHeaderLines(v) {
			*headers = append(*headers, h)
		}
	}
	// MCP_HEADER_1..MCP_HEADER_5 carry one `Name: Value` entry each. They back
	// the per-row Custom Header fields in the MCPB manifest, where the value
	// portion is marked sensitive. Unused rows arrive either empty, as a bare
	// ":" (only the value field filled), or with unsubstituted "${...}"
	// placeholders — mcpbEnv drops the last case and parseHeaderLines the rest.
	for i := 1; i <= 5; i++ {
		if v := mcpbEnv(fmt.Sprintf("MCP_HEADER_%d", i)); v != "" {
			for _, h := range parseHeaderLines(v) {
				*headers = append(*headers, h)
			}
		}
	}
	if v := mcpbEnv("MCP_AUTH_HEADER"); v != "" {
		hasAuth := false
		for _, h := range *headers {
			// Compare the parsed header name rather than a string prefix:
			// entries may carry whitespace around the colon (e.g.
			// "Authorization : Bearer ..."), which headerMap construction
			// later trims to the same key.
			name, _, found := strings.Cut(h, ":")
			if found && strings.EqualFold(strings.TrimSpace(name), "Authorization") {
				hasAuth = true
				break
			}
		}
		if !hasAuth {
			*headers = append(*headers, "Authorization:"+v)
		}
	}
}

// parseHeaderLines parses a newline-separated list of `Name: Value` header
// entries, as accepted by the MCP_HEADERS and MCP_HEADER_1..5 env vars. Empty
// lines and lines with an empty header name are ignored. Lines that do not
// contain a colon are logged and skipped so a typo in one entry does not
// prevent the others from being applied.
//
// As a convenience for contexts that cannot embed real newlines in an env
// var value (Claude Desktop's MCPB single-line text field, Windows CMD
// double quotes, etc.), a literal "\n" two-character sequence is also
// treated as a separator — but only when no real newline is present, so an
// HTTP header value legitimately containing "\n" in a real-newline payload
// is left untouched.
func parseHeaderLines(raw string) []string {
	if !strings.ContainsAny(raw, "\r\n") {
		raw = strings.ReplaceAll(raw, `\n`, "\n")
	}
	var out []string
	for _, line := range strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == '\r' }) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			log.Printf("Warning: ignoring header entry without ':' separator: %q", line)
			continue
		}
		name := strings.TrimSpace(line[:idx])
		if name == "" {
			// Empty header name, e.g. an unused MCPB row where only the
			// sensitive value field was filled in. Skip silently.
			continue
		}
		if !isValidHeaderName(name) {
			// e.g. a human-readable label like "Redash API Key" typed into the
			// header-name field. Sending it would make net/http reject the whole
			// request, so skip this entry and keep the others.
			log.Printf("Warning: ignoring header entry with invalid header name %q (HTTP header names cannot contain spaces or special characters)", name)
			continue
		}
		out = append(out, line)
	}
	return out
}

// isValidHeaderName reports whether name is a valid RFC 7230 header field name
// (a non-empty "token": ASCII letters, digits, and a limited set of symbols,
// with no spaces or control characters).
func isValidHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}
