package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/naotama2002/mcp-remote-go/auth"
	"github.com/pkg/browser"
)

// TransportMode specifies which transport to use.
type TransportMode string

const (
	TransportModeAuto           TransportMode = "auto"
	TransportModeStreamableHTTP TransportMode = "streamable-http"
	TransportModeSSE            TransportMode = "sse"
)

// Proxy handles the bidirectional communication between stdio (MCP client) and the remote server
type Proxy struct {
	serverURL     string
	callbackPort  int
	headers       map[string]string
	serverURLHash string
	transportMode TransportMode
	// serverProfile holds what auto-negotiation learned about the remote. The
	// spec asks clients to cache the era for the lifetime of the origin; this
	// process is scoped to exactly one origin, so the field is that cache.
	serverProfile serverProfile
	authCoord     *auth.Coordinator
	ctx           context.Context
	cancel        context.CancelFunc
	client        *http.Client
	transport     Transport
	stdioReader   *bufio.Reader
	stdioWriter   *bufio.Writer
	writerMu      sync.Mutex
	wg            sync.WaitGroup
}

// NewProxy creates a new MCP proxy
func NewProxy(serverURL string, callbackPort int, headers map[string]string, serverURLHash string) (*Proxy, error) {
	return NewProxyWithTransport(serverURL, callbackPort, headers, serverURLHash, TransportModeSSE)
}

// NewProxyWithTransport creates a new MCP proxy with a specified transport mode
func NewProxyWithTransport(serverURL string, callbackPort int, headers map[string]string, serverURLHash string, mode TransportMode) (*Proxy, error) {
	return NewProxyWithOptions(serverURL, callbackPort, headers, serverURLHash, mode, "")
}

// NewProxyWithOptions creates a new MCP proxy with full configuration including HTTP proxy support
func NewProxyWithOptions(serverURL string, callbackPort int, headers map[string]string, serverURLHash string, mode TransportMode, httpProxyURL string) (*Proxy, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// Create auth coordinator
	authCoord, err := auth.NewCoordinator(serverURLHash, callbackPort)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create auth coordinator: %w", err)
	}

	// Build HTTP client with optional proxy
	httpClient, err := buildHTTPClient(httpProxyURL)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to configure HTTP proxy: %w", err)
	}

	return &Proxy{
		serverURL:     serverURL,
		callbackPort:  callbackPort,
		headers:       headers,
		serverURLHash: serverURLHash,
		transportMode: mode,
		authCoord:     authCoord,
		ctx:           ctx,
		cancel:        cancel,
		client:        httpClient,
		stdioReader:   bufio.NewReader(os.Stdin),
		stdioWriter:   bufio.NewWriter(os.Stdout),
	}, nil
}

// buildHTTPClient creates an http.Client with optional proxy configuration.
// When a proxy is configured, it clones http.DefaultTransport to preserve
// HTTP/2, timeouts, and connection pooling defaults.
func buildHTTPClient(proxyURL string) (*http.Client, error) {
	if proxyURL == "" {
		return &http.Client{}, nil
	}

	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL %q: %w", proxyURL, err)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("invalid proxy URL %q: scheme must be http or https", proxyURL)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("invalid proxy URL %q: missing host", proxyURL)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(parsed)

	return &http.Client{
		Transport: transport,
	}, nil
}

// Start initializes the proxy and begins bidirectional communication
func (p *Proxy) Start() error {
	log.Println("Starting MCP proxy")
	log.Println("Connecting to remote server:", p.serverURL)

	if err := p.connectToServer(); err != nil {
		return fmt.Errorf("failed to connect to server: %w", err)
	}

	p.wg.Add(1)
	go p.processStdioInput()

	p.wg.Wait()
	return nil
}

// Shutdown gracefully stops the proxy
func (p *Proxy) Shutdown() {
	log.Println("Shutting down proxy")
	if p.transport != nil {
		if err := p.transport.Close(); err != nil {
			log.Printf("Warning: failed to close transport: %v", err)
		}
	}
	p.cancel()
	p.wg.Wait()
}

// getAuthToken returns the current auth token if available.
func (p *Proxy) getAuthToken() string {
	tokens, err := p.authCoord.LoadTokens()
	if err == nil && tokens.AccessToken != "" {
		return tokens.AccessToken
	}
	return ""
}

// connectToServer establishes a connection using the configured transport
func (p *Proxy) connectToServer() error {
	if p.transportMode == TransportModeAuto {
		return p.negotiateTransport()
	}

	t := p.createTransport(p.transportMode)
	t.SetOnMessage(p.handleServerMessage)
	t.SetOnError(p.handleServerError)

	err := t.Connect(p.ctx)
	if err != nil {
		var unauth *UnauthorizedError
		if errors.As(err, &unauth) {
			log.Println("Authentication required")
			return p.handleAuthentication(unauth.WWWAuthenticate)
		}
		return fmt.Errorf("failed to connect: %w", err)
	}

	p.transport = t
	log.Println("Connected to server successfully")
	return nil
}

// negotiateTransport probes the server to settle two questions at once: which
// transport shape it speaks, and which protocol era it implements.
//
// The probe is a modern server/discover request. A modern server answers it
// with the list of versions it supports; a legacy one rejects it but still
// replies in JSON-RPC over the same endpoint, which is enough to place it. A
// server that does not answer POST at all is left to the deprecated HTTP+SSE
// transport.
func (p *Proxy) negotiateTransport() error {
	log.Println("Auto-detecting transport...")

	profile, err := p.probeServer()
	if err != nil {
		var unauth *UnauthorizedError
		if errors.As(err, &unauth) {
			log.Println("Authentication required")
			return p.handleAuthentication(unauth.WWWAuthenticate)
		}
		log.Printf("Probe failed: %v, falling back to SSE", err)
		return p.connectWithMode(TransportModeSSE)
	}

	p.serverProfile = profile

	if profile.era != eraUnknown {
		log.Printf("Server implements the %s protocol era", profile.era)
	}
	if len(profile.supportedVersions) > 0 {
		log.Printf("Server supports protocol versions: %s", strings.Join(profile.supportedVersions, ", "))
	}
	if !profile.supportsLegacy() {
		// The compatibility matrix has no path from a legacy client to a
		// modern-only server, so say so now rather than let the user debug a
		// -32020 on their first tool call.
		log.Println("Warning: this server no longer answers the initialize handshake. " +
			"An MCP client older than 2026-07-28 cannot connect through this proxy.")
	}

	return p.connectWithMode(profile.transport)
}

// probeServer sends the era probe and classifies the reply.
func (p *Proxy) probeServer() (serverProfile, error) {
	probeReq, err := http.NewRequestWithContext(p.ctx, http.MethodPost, p.serverURL, strings.NewReader(probeBody()))
	if err != nil {
		return serverProfile{}, fmt.Errorf("failed to create probe request: %w", err)
	}

	for k, v := range p.headers {
		probeReq.Header.Set(k, v)
	}
	if token := p.getAuthToken(); token != "" {
		probeReq.Header.Set("Authorization", "Bearer "+token)
	}
	probeReq.Header.Set("Content-Type", "application/json")
	probeReq.Header.Set("Accept", "application/json, text/event-stream")
	probeReq.Header.Set(HeaderMCPProtocolVersion, ProtocolVersion20260728)
	probeReq.Header.Set(HeaderMCPMethod, "server/discover")

	resp, err := p.client.Do(probeReq)
	if err != nil {
		return serverProfile{}, fmt.Errorf("streamable HTTP probe failed: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return serverProfile{}, unauthorizedFromResponse(resp)
	}

	body, _ := io.ReadAll(resp.Body)
	if closeErr := resp.Body.Close(); closeErr != nil {
		log.Printf("Warning: failed to close probe response body: %v", closeErr)
	}

	era, supported, isJSONRPC := classifyProbe(resp.StatusCode, body)

	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted:
		log.Println("Server supports Streamable HTTP transport")
		return serverProfile{transport: TransportModeStreamableHTTP, era: era, supportedVersions: supported}, nil

	case isJSONRPC:
		// A JSON-RPC body means the server speaks the protocol on this
		// endpoint even though it rejected this particular request, so the
		// endpoint is a Streamable HTTP one regardless of the status.
		log.Printf("Server returned %d with a JSON-RPC body, using Streamable HTTP transport", resp.StatusCode)
		return serverProfile{transport: TransportModeStreamableHTTP, era: era, supportedVersions: supported}, nil

	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		// No MCP endpoint here, and no JSON-RPC error to say otherwise: this
		// is the signature of a server hosting only the deprecated transport.
		log.Printf("Server returned %d without a JSON-RPC body, falling back to SSE transport", resp.StatusCode)
		return serverProfile{transport: TransportModeSSE, era: eraLegacy}, nil

	default:
		log.Printf("Unexpected status %d from probe, falling back to SSE", resp.StatusCode)
		return serverProfile{transport: TransportModeSSE, era: eraUnknown}, nil
	}
}

// connectWithMode connects using a specific transport mode.
func (p *Proxy) connectWithMode(mode TransportMode) error {
	t := p.createTransport(mode)
	t.SetOnMessage(p.handleServerMessage)
	t.SetOnError(p.handleServerError)

	err := t.Connect(p.ctx)
	if err != nil {
		var unauth *UnauthorizedError
		if errors.As(err, &unauth) {
			log.Println("Authentication required")
			return p.handleAuthentication(unauth.WWWAuthenticate)
		}
		return fmt.Errorf("failed to connect with %s transport: %w", mode, err)
	}

	p.transport = t
	p.transportMode = mode
	log.Printf("Connected using %s transport", mode)
	return nil
}

// createTransport creates the appropriate Transport for the given mode.
func (p *Proxy) createTransport(mode TransportMode) Transport {
	switch mode {
	case TransportModeStreamableHTTP:
		return NewStreamableHTTPTransport(StreamableHTTPTransportConfig{
			Endpoint:     p.serverURL,
			Client:       p.client,
			Headers:      p.headers,
			GetAuthToken: p.getAuthToken,
			// 2026-07-28 removed the GET notification stream. Knowing the era
			// up front saves opening a request that can only be answered 405.
			SkipNotificationStream: p.serverProfile.era == eraModern,
		})
	default: // SSE
		return NewSSETransport(SSETransportConfig{
			ServerURL:    p.serverURL,
			Client:       p.client,
			Headers:      p.headers,
			GetAuthToken: p.getAuthToken,
		})
	}
}

// openBrowser opens the specified URL in the default browser
func openBrowser(rawURL string) error {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return errors.New("only http and https URLs are allowed")
	}

	return browser.OpenURL(rawURL)
}

// handleAuthentication runs the OAuth flow. When the triggering 401 carried a
// WWW-Authenticate Bearer challenge, its resource_metadata URL (RFC 9728 §5.1)
// is forwarded to discovery.
func (p *Proxy) handleAuthentication(wwwAuthenticate string) error {
	var initOpts []auth.InitOption
	if wwwAuthenticate != "" {
		if challenge, ok := auth.ParseWWWAuthenticate(wwwAuthenticate); ok && challenge.ResourceMetadata != "" {
			log.Printf("Using resource_metadata URL from WWW-Authenticate: %s", challenge.ResourceMetadata)
			initOpts = append(initOpts, auth.WithResourceMetadataURL(challenge.ResourceMetadata))
		}
	}

	authURL, err := p.authCoord.InitializeAuth(p.serverURL, initOpts...)
	if err != nil {
		return fmt.Errorf("failed to initialize auth: %w", err)
	}

	log.Println("Please authorize access in your browser at:", authURL)

	if err := openBrowser(authURL); err != nil {
		log.Printf("Failed to open browser automatically: %v", err)
		log.Println("Please open the URL manually in your browser.")
	} else {
		log.Println("Opening browser...")
	}

	code, err := p.authCoord.WaitForAuthCode()
	if err != nil {
		return fmt.Errorf("auth code retrieval failed: %w", err)
	}

	log.Println("Auth code received, exchanging for tokens...")

	tokens, err := p.authCoord.ExchangeCode(code)
	if err != nil {
		return fmt.Errorf("token exchange failed: %w", err)
	}

	if err := p.authCoord.SaveTokens(tokens); err != nil {
		return fmt.Errorf("failed to save tokens: %w", err)
	}

	return p.connectToServer()
}

// processStdioInput reads messages from stdin and forwards them to the server
// stdioRead is one outcome of reading a line from stdin.
type stdioRead struct {
	line string
	err  error
}

// processStdioInput reads messages from stdin and forwards them to the server.
//
// The read runs on a goroutine of its own because it cannot be interrupted:
// selecting on the context around a blocking ReadString only checks it between
// reads, so a cancelled context went unnoticed while stdin sat idle and
// Shutdown waited here forever. That goroutine is deliberately not part of the
// WaitGroup -- it may stay parked on a read that never returns, it holds
// nothing, and it ends with the process.
func (p *Proxy) processStdioInput() {
	defer p.wg.Done()

	reads := make(chan stdioRead)
	go func() {
		for {
			line, err := p.stdioReader.ReadString('\n')
			select {
			case reads <- stdioRead{line: line, err: err}:
			case <-p.ctx.Done():
				return
			}
			if err != nil && errors.Is(err, io.EOF) {
				return
			}
		}
	}()

	for {
		select {
		case <-p.ctx.Done():
			return
		case read := <-reads:
			if read.err != nil {
				if errors.Is(read.err, io.EOF) {
					log.Println("STDIO input closed")
					// Close the transport and cancel directly rather than
					// calling Shutdown, which waits on the WaitGroup this
					// goroutine has not yet released.
					if p.transport != nil {
						if closeErr := p.transport.Close(); closeErr != nil {
							log.Printf("Warning: failed to close transport: %v", closeErr)
						}
					}
					p.cancel()
					return
				}
				log.Printf("Error reading from STDIO: %v", read.err)
				continue
			}

			p.forwardToServer(read.line)
		}
	}
}

// forwardToServer logs a client message and hands it to the transport.
func (p *Proxy) forwardToServer(line string) {
	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(line), &msg); err == nil {
		if method, ok := msg["method"].(string); ok {
			log.Printf("[Local→Remote] %s", method)
		} else if id, ok := msg["id"].(float64); ok {
			log.Printf("[Local→Remote] Response ID: %v", id)
		}
	}

	if p.transport == nil {
		log.Printf("Error sending to server: not connected")
		return
	}
	if err := p.transport.Send(p.ctx, []byte(line)); err != nil {
		log.Printf("Error sending to server: %v", err)
	}
}

// handleServerMessage processes messages received from the server
func (p *Proxy) handleServerMessage(event string, data []byte) {
	if event != "message" && event != "" {
		return
	}

	var msg map[string]interface{}
	if err := json.Unmarshal(data, &msg); err == nil {
		if method, ok := msg["method"].(string); ok {
			log.Printf("[Remote→Local] %s", method)
		} else if id, ok := msg["id"].(float64); ok {
			log.Printf("[Remote→Local] Response ID: %v", id)
		}
	}

	p.writeToStdout(data)
}

// writeToStdout safely writes data to stdout with a newline.
func (p *Proxy) writeToStdout(data []byte) {
	p.writerMu.Lock()
	defer p.writerMu.Unlock()

	data = append(data, '\n')
	if _, err := p.stdioWriter.Write(data); err != nil {
		log.Printf("Error writing to STDIO: %v", err)
		return
	}
	if err := p.stdioWriter.Flush(); err != nil {
		log.Printf("Error flushing STDIO: %v", err)
	}
}

// handleServerError handles errors from the transport
func (p *Proxy) handleServerError(err error) {
	log.Printf("Transport error: %v", err)

	if errors.Is(err, context.Canceled) {
		return
	}

	var unauth *UnauthorizedError
	if errors.As(err, &unauth) {
		log.Println("Authentication error, trying to re-authenticate...")
		if err := p.handleAuthentication(unauth.WWWAuthenticate); err != nil {
			log.Printf("Re-authentication failed: %v", err)
			p.Shutdown()
		}
		return
	}

	time.Sleep(5 * time.Second)
	log.Println("Attempting to reconnect...")
	if err := p.connectToServer(); err != nil {
		log.Printf("Reconnection failed: %v", err)
		p.Shutdown()
	}
}

// SetStdio replaces the stdio reader and writer (for testing).
func (p *Proxy) SetStdio(reader *bufio.Reader, writer *bufio.Writer) {
	p.stdioReader = reader
	p.stdioWriter = writer
}

// SetCommandEndpoint sets the command endpoint URL (for backward compatibility in tests).
func (p *Proxy) SetCommandEndpoint(endpoint string) {
	if sseTransport, ok := p.transport.(*SSETransport); ok {
		sseTransport.setCommandEndpoint(endpoint)
	}
}

// GetCommandEndpoint returns the command endpoint URL (for backward compatibility in tests).
func (p *Proxy) GetCommandEndpoint() string {
	if sseTransport, ok := p.transport.(*SSETransport); ok {
		return sseTransport.getCommandEndpointValue()
	}
	return ""
}
