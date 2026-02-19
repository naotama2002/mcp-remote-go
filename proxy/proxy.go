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
	TransportModeAuto          TransportMode = "auto"
	TransportModeStreamableHTTP TransportMode = "streamable-http"
	TransportModeSSE           TransportMode = "sse"
)

// Proxy handles the bidirectional communication between stdio (MCP client) and the remote server
type Proxy struct {
	serverURL     string
	callbackPort  int
	headers       map[string]string
	serverURLHash string
	transportMode TransportMode
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
	ctx, cancel := context.WithCancel(context.Background())

	// Create auth coordinator
	authCoord, err := auth.NewCoordinator(serverURLHash, callbackPort)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create auth coordinator: %w", err)
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
		client:        &http.Client{},
		stdioReader:   bufio.NewReader(os.Stdin),
		stdioWriter:   bufio.NewWriter(os.Stdout),
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
		if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "Unauthorized") {
			log.Println("Authentication required")
			return p.handleAuthentication()
		}
		return fmt.Errorf("failed to connect: %w", err)
	}

	p.transport = t
	log.Println("Connected to server successfully")
	return nil
}

// negotiateTransport attempts Streamable HTTP first, then falls back to SSE.
func (p *Proxy) negotiateTransport() error {
	log.Println("Auto-detecting transport...")

	// Try Streamable HTTP first: send a POST probe to the server URL
	probeReq, err := http.NewRequestWithContext(p.ctx, http.MethodPost, p.serverURL, strings.NewReader(`{"jsonrpc":"2.0","method":"ping","id":0}`))
	if err != nil {
		// If we can't even create the request, fall back to SSE
		log.Printf("Failed to create probe request: %v, falling back to SSE", err)
		return p.connectWithMode(TransportModeSSE)
	}

	for k, v := range p.headers {
		probeReq.Header.Set(k, v)
	}
	if token := p.getAuthToken(); token != "" {
		probeReq.Header.Set("Authorization", "Bearer "+token)
	}
	probeReq.Header.Set("Content-Type", "application/json")
	probeReq.Header.Set("Accept", "application/json, text/event-stream")
	probeReq.Header.Set(HeaderMCPProtocolVersion, MCPProtocolVersion)

	resp, err := p.client.Do(probeReq)
	if err != nil {
		log.Printf("Streamable HTTP probe failed: %v, falling back to SSE", err)
		return p.connectWithMode(TransportModeSSE)
	}

	body, _ := io.ReadAll(resp.Body)
	if closeErr := resp.Body.Close(); closeErr != nil {
		log.Printf("Warning: failed to close probe response body: %v", closeErr)
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted:
		// Server supports Streamable HTTP
		log.Println("Server supports Streamable HTTP transport")
		if err := p.connectWithMode(TransportModeStreamableHTTP); err != nil {
			return err
		}
		// Forward the probe response if it was a valid JSON response
		if len(body) > 0 && json.Valid(body) {
			p.writeToStdout(body)
		}
		return nil

	case http.StatusUnauthorized:
		log.Println("Authentication required")
		return p.handleAuthentication()

	case http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed:
		// Server does not support Streamable HTTP, fall back to SSE
		log.Printf("Server returned %d, falling back to SSE transport", resp.StatusCode)
		return p.connectWithMode(TransportModeSSE)

	default:
		log.Printf("Unexpected status %d from probe, falling back to SSE", resp.StatusCode)
		return p.connectWithMode(TransportModeSSE)
	}
}

// connectWithMode connects using a specific transport mode.
func (p *Proxy) connectWithMode(mode TransportMode) error {
	t := p.createTransport(mode)
	t.SetOnMessage(p.handleServerMessage)
	t.SetOnError(p.handleServerError)

	err := t.Connect(p.ctx)
	if err != nil {
		if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "Unauthorized") {
			log.Println("Authentication required")
			return p.handleAuthentication()
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

// handleAuthentication handles the OAuth flow
func (p *Proxy) handleAuthentication() error {
	authURL, err := p.authCoord.InitializeAuth(p.serverURL)
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
func (p *Proxy) processStdioInput() {
	defer p.wg.Done()

	for {
		select {
		case <-p.ctx.Done():
			return
		default:
			line, err := p.stdioReader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					log.Println("STDIO input closed")
					p.Shutdown()
					return
				}
				log.Printf("Error reading from STDIO: %v", err)
				continue
			}

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
				continue
			}
			if err := p.transport.Send(p.ctx, []byte(line)); err != nil {
				log.Printf("Error sending to server: %v", err)
			}
		}
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

	if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "Unauthorized") {
		log.Println("Authentication error, trying to re-authenticate...")
		if err := p.handleAuthentication(); err != nil {
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
