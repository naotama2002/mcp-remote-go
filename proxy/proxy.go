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
	// stdin is the client's messages, read from startup so that the pipe
	// closing is noticed even while an authorization flow is in progress.
	stdin *stdinQueue
	// renewalTried guards against renewing an access token in a loop; see
	// mayRenew.
	renewalTried bool
	writerMu     sync.Mutex
	// stateMu guards transport, transportMode and serverProfile. Reconnection
	// runs on the transport's error callback goroutine and replaces all three
	// while the stdio reader is using them.
	stateMu sync.RWMutex
	wg      sync.WaitGroup
}

// currentTransport returns the active transport, or nil if none is connected.
func (p *Proxy) currentTransport() Transport {
	p.stateMu.RLock()
	defer p.stateMu.RUnlock()
	return p.transport
}

// currentTransportMode returns the transport mode in use.
func (p *Proxy) currentTransportMode() TransportMode {
	p.stateMu.RLock()
	defer p.stateMu.RUnlock()
	return p.transportMode
}

// currentProfile returns what negotiation learned about the server.
func (p *Proxy) currentProfile() serverProfile {
	p.stateMu.RLock()
	defer p.stateMu.RUnlock()
	return p.serverProfile
}

// setActiveTransport records the connected transport and the mode it speaks.
func (p *Proxy) setActiveTransport(t Transport, mode TransportMode) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	p.transport = t
	p.transportMode = mode
	// A connection stands, so whatever token it was made with was accepted.
	// The next refusal is a new question, and renewal is on the table again.
	p.renewalTried = false
}

// markRenewed records that the access token has just been renewed, so a refusal
// of the renewed token is not answered by renewing again.
func (p *Proxy) markRenewed() {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	p.renewalTried = true
}

// mayRenew reports whether renewing the access token is still worth trying, and
// records the attempt.
//
// A 401 that arrives on a token this process just renewed is not about the
// token's age, and renewing again would loop between the server and the token
// endpoint without the user ever being asked to authorize. One attempt per
// connection is enough: setActiveTransport clears this once a token has been
// accepted.
func (p *Proxy) mayRenew() bool {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.renewalTried {
		return false
	}
	p.renewalTried = true
	return true
}

// setProfile records the result of auto-negotiation.
func (p *Proxy) setProfile(profile serverProfile) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	p.serverProfile = profile
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

	// Start reading the client's end before connecting, not after. Connecting
	// can mean waiting on an authorization flow, and a client that gives up
	// during it closes this pipe; see stdinQueue for what reading late cost.
	p.stdin = newStdinQueue(p.stdioReader)
	go p.stdin.pump(p.handleStdioClosed)

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
	if t := p.currentTransport(); t != nil {
		if err := t.Close(); err != nil {
			log.Printf("Warning: failed to close transport: %v", err)
		}
	}
	p.cancel()
	p.wg.Wait()
}

// getAuthToken returns the token to present to the server, renewing it first
// when it is about to expire.
//
// Renewing here rather than after the server refuses the request costs the same
// single round trip and spares the failed request, the reconnection behind it,
// and -- for a token whose refresh has itself expired -- lets the browser flow
// start before the user is mid-request.
func (p *Proxy) getAuthToken() string {
	tokens, err := p.authCoord.LoadTokens()
	if err != nil || tokens.AccessToken == "" {
		return ""
	}
	if !tokens.DueForRenewal() {
		return tokens.AccessToken
	}

	renewed, err := p.authCoord.RenewExclusively(p.ctx, p.serverURL)
	if err != nil {
		if !errors.Is(err, auth.ErrNoRefreshToken) {
			log.Printf("Could not renew the expiring access token: %v", err)
		}
		// Present what we have. The server is the authority on whether it is
		// still good, and its refusal is handled where every other one is.
		return tokens.AccessToken
	}

	// If the renewed token is refused as well, age was not the problem, and
	// renewing a second time on its 401 would only delay asking the user.
	p.markRenewed()
	return renewed.AccessToken
}

// connectToServer establishes a connection using the configured transport
func (p *Proxy) connectToServer() error {
	mode := p.currentTransportMode()
	if mode == TransportModeAuto {
		return p.negotiateTransport()
	}

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
		return fmt.Errorf("failed to connect: %w", err)
	}

	p.setActiveTransport(t, mode)
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

	p.setProfile(profile)

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

// probeServer settles the transport and, where it can, the era.
//
// The modern probe asks a question only a 2026-07-28 server can answer, so its
// rejection is ambiguous: it may mean "wrong transport" or merely "I do not
// speak that revision". Nothing in a plain-text rejection separates the two,
// and guessing either way strands real servers -- guessing SSE breaks legacy
// Streamable HTTP endpoints, guessing Streamable HTTP breaks endpoints that
// host only the deprecated transport. So when the answer is ambiguous the
// question is asked again in a shape every revision understands, and that reply
// decides the transport.
func (p *Proxy) probeServer() (serverProfile, error) {
	modern, err := p.sendProbe(probeBody(), map[string]string{
		HeaderMCPProtocolVersion: ProtocolVersion20260728,
		HeaderMCPMethod:          "server/discover",
	}, classifyProbe)
	if err != nil {
		return serverProfile{}, err
	}

	switch {
	case modern.status == http.StatusOK || modern.status == http.StatusAccepted:
		log.Println("Server supports Streamable HTTP transport")
		return serverProfile{transport: TransportModeStreamableHTTP, era: modern.era, supportedVersions: modern.supported}, nil

	case modern.isJSONRPC:
		// A JSON-RPC body means the server speaks the protocol on this
		// endpoint even though it rejected this particular request, so the
		// endpoint is a Streamable HTTP one regardless of the status.
		log.Printf("Server returned %d with a JSON-RPC body, using Streamable HTTP transport", modern.status)
		return serverProfile{transport: TransportModeStreamableHTTP, era: modern.era, supportedVersions: modern.supported}, nil

	case modern.status == http.StatusNotFound || modern.status == http.StatusMethodNotAllowed:
		// Nothing here answers POST at all: the signature of a server hosting
		// only the deprecated transport, whose POST endpoint is another URL.
		log.Printf("Server returned %d without a JSON-RPC body, falling back to SSE transport", modern.status)
		return serverProfile{transport: TransportModeSSE, era: eraLegacy}, nil
	}

	// Ambiguous. Ask in a pre-2026-07-28 shape, which any Streamable HTTP
	// endpoint answers and a deprecated-transport endpoint still refuses.
	log.Printf("Server returned %d without a JSON-RPC body; re-probing with a pre-%s request",
		modern.status, ProtocolVersion20260728)

	legacy, err := p.sendProbe(legacyProbeBody(), map[string]string{
		HeaderMCPProtocolVersion: MCPProtocolVersion,
	}, classifyLegacyProbe)
	if err != nil {
		var unauth *UnauthorizedError
		if errors.As(err, &unauth) {
			return serverProfile{}, err
		}
		log.Printf("Legacy probe failed: %v, falling back to SSE transport", err)
		return serverProfile{transport: TransportModeSSE, era: eraLegacy}, nil
	}

	switch {
	case legacy.status == http.StatusOK || legacy.status == http.StatusAccepted || legacy.isJSONRPC:
		log.Printf("Server answered the pre-%s probe with %d, using Streamable HTTP transport",
			ProtocolVersion20260728, legacy.status)
		return serverProfile{transport: TransportModeStreamableHTTP, era: legacy.era, supportedVersions: legacy.supported}, nil

	default:
		// Neither shape of JSON-RPC request was answered on this endpoint, so
		// it is not a Streamable HTTP one.
		log.Printf("Server rejected the pre-%s probe with %d as well, falling back to SSE transport",
			ProtocolVersion20260728, legacy.status)
		return serverProfile{transport: TransportModeSSE, era: eraLegacy}, nil
	}
}

// probeOutcome is what a single probe request revealed.
type probeOutcome struct {
	status    int
	era       serverEra
	supported []string
	isJSONRPC bool
}

// sendProbe POSTs one probe body and classifies the reply with the classifier
// matching the shape of the request that was sent.
func (p *Proxy) sendProbe(body string, headers map[string]string, classify func([]byte) (serverEra, []string, bool)) (probeOutcome, error) {
	req, err := http.NewRequestWithContext(p.ctx, http.MethodPost, p.serverURL, strings.NewReader(body))
	if err != nil {
		return probeOutcome{}, fmt.Errorf("failed to create probe request: %w", err)
	}

	for k, v := range p.headers {
		req.Header.Set(k, v)
	}
	if token := p.getAuthToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return probeOutcome{}, fmt.Errorf("streamable HTTP probe failed: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return probeOutcome{}, unauthorizedFromResponse(resp)
	}

	payload, _ := io.ReadAll(resp.Body)
	if closeErr := resp.Body.Close(); closeErr != nil {
		log.Printf("Warning: failed to close probe response body: %v", closeErr)
	}

	era, supported, isJSONRPC := classify(probePayload(resp.Header.Get("Content-Type"), payload))
	return probeOutcome{status: resp.StatusCode, era: era, supported: supported, isJSONRPC: isJSONRPC}, nil
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

	p.setActiveTransport(t, mode)
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
			// A server that cannot serve any revision older than 2026-07-28
			// has no GET notification stream under any circumstances. For
			// every other server the transport decides, once the local client
			// has said which revision it speaks.
			SkipNotificationStream: !p.currentProfile().supportsLegacy(),
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

// openBrowserFunc is indirected so tests can assert whether the proxy would put
// a browser window in front of the user.
var openBrowserFunc = openBrowser

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

// handleAuthentication obtains a token for the server and reconnects.
//
// The interactive part runs under a per-server lock, so a second proxy for the
// same server waits for this one's token instead of sending the user a second
// browser window for the same account. When the triggering 401 carried a
// WWW-Authenticate Bearer challenge, its resource_metadata URL (RFC 9728 §5.1)
// is forwarded to discovery and its scope (RFC 6750 §3.1) to the authorization
// request.
func (p *Proxy) handleAuthentication(wwwAuthenticate string) error {
	var initOpts []auth.InitOption
	if wwwAuthenticate != "" {
		if challenge, ok := auth.ParseWWWAuthenticate(wwwAuthenticate); ok {
			if challenge.ResourceMetadata != "" {
				log.Printf("Using resource_metadata URL from WWW-Authenticate: %s", challenge.ResourceMetadata)
				initOpts = append(initOpts, auth.WithResourceMetadataURL(challenge.ResourceMetadata))
			}
			if challenge.Scope != "" {
				log.Printf("Requesting scope from WWW-Authenticate: %s", challenge.Scope)
				initOpts = append(initOpts, auth.WithChallengeScope(challenge.Scope))
			}
		}
	}

	mayRenew := p.mayRenew()

	err := p.authCoord.AuthorizeExclusively(p.ctx, func() error {
		// A 401 often means nothing more than an access token that aged out.
		// Renewing costs one request and no user interaction, so it is tried
		// before a browser window is put in front of anyone.
		if mayRenew {
			switch _, err := p.authCoord.Renew(p.ctx, p.serverURL); {
			case err == nil:
				log.Println("Renewed the access token; no authorization needed")
				return nil
			case errors.Is(err, auth.ErrNoRefreshToken):
				// Nothing stored to renew with, which is ordinary.
			default:
				log.Printf("Could not renew the access token, authorizing instead: %v", err)
			}
		}
		return p.authorize(initOpts)
	})
	if err != nil {
		return fmt.Errorf("failed to initialize auth: %w", err)
	}

	// Reached whether this process authorized or another one did, since either
	// way there is now a token to connect with.
	return p.connectToServer()
}

// authorize sends the user through the authorization server and stores the
// tokens the returned code buys.
func (p *Proxy) authorize(initOpts []auth.InitOption) error {
	authURL, err := p.authCoord.InitializeAuth(p.serverURL, initOpts...)
	if err != nil {
		return err
	}

	// Discovery and registration take requests of their own, and the client may
	// have closed its pipe during them. Opening a browser now would put a window
	// in front of the user that authorizes a connection nobody is waiting for.
	if err := p.ctx.Err(); err != nil {
		return fmt.Errorf("abandoning authorization, the client is gone: %w", err)
	}

	log.Println("Please authorize access in your browser at:", authURL)

	if err := openBrowserFunc(authURL); err != nil {
		log.Printf("Failed to open browser automatically: %v", err)
		log.Println("Please open the URL manually in your browser.")
	} else {
		log.Println("Opening browser...")
	}

	code, err := p.authCoord.WaitForAuthCode(p.ctx)
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

	return nil
}

// processStdioInput forwards the client's messages to the server, in order, from
// the queue the reader has been filling since the proxy started.
func (p *Proxy) processStdioInput() {
	defer p.wg.Done()

	for {
		line, ok := p.stdin.next(p.ctx)
		if !ok {
			return
		}
		p.forwardToServer(line)
	}
}

// handleStdioClosed reacts to the client closing its end of the pipe, which is
// how an MCP host says it is finished with this server.
//
// The transport is closed and the context cancelled directly rather than through
// Shutdown, which waits on the WaitGroup that the forwarding goroutine is in.
func (p *Proxy) handleStdioClosed() {
	log.Println("STDIO input closed")

	if t := p.currentTransport(); t != nil {
		if err := t.Close(); err != nil {
			log.Printf("Warning: failed to close transport: %v", err)
		}
	}
	p.cancel()
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

	transport := p.currentTransport()
	if transport == nil {
		log.Printf("Error sending to server: not connected")
		return
	}
	if err := transport.Send(p.ctx, []byte(line)); err != nil {
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
	if sseTransport, ok := p.currentTransport().(*SSETransport); ok {
		sseTransport.setCommandEndpoint(endpoint)
	}
}

// GetCommandEndpoint returns the command endpoint URL (for backward compatibility in tests).
func (p *Proxy) GetCommandEndpoint() string {
	if sseTransport, ok := p.currentTransport().(*SSETransport); ok {
		return sseTransport.getCommandEndpointValue()
	}
	return ""
}
