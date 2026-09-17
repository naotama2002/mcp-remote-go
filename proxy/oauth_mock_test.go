package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// oauthMockServer is an MCP endpoint that refuses everything with a
// WWW-Authenticate challenge, plus the discovery, registration and token
// endpoints the resulting flow walks through.
type oauthMockServer struct {
	*httptest.Server

	// Challenge is the WWW-Authenticate value the /mcp endpoint answers with,
	// and what a test hands to handleAuthentication directly.
	Challenge string
}

// newOAuthMockServer starts a server whose /mcp endpoint accepts only
// acceptToken, answering anything else with a challenge that leads to the flow.
// tokenHandler serves /token, so each test decides what renewal returns.
func newOAuthMockServer(t *testing.T, acceptToken string, tokenHandler http.HandlerFunc) *oauthMockServer {
	t.Helper()

	mock := &oauthMockServer{}
	mock.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mcp":
			if acceptToken != "" && r.Header.Get("Authorization") == "Bearer "+acceptToken {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":0,"result":{}}`)
				return
			}
			w.Header().Set("WWW-Authenticate", mock.Challenge)
			w.WriteHeader(http.StatusUnauthorized)

		case "/.well-known/oauth-protected-resource":
			writeJSONBody(w, map[string]any{
				"resource":              mock.URL + "/mcp",
				"authorization_servers": []string{mock.URL},
				"scopes_supported":      []string{"data:read"},
			})

		case "/.well-known/oauth-authorization-server":
			writeJSONBody(w, map[string]any{
				"issuer":                 mock.URL,
				"authorization_endpoint": mock.URL + "/auth",
				"token_endpoint":         mock.URL + "/token",
				"registration_endpoint":  mock.URL + "/register",
			})

		case "/register":
			writeJSONBody(w, map[string]any{
				"client_id":                  "mock-client",
				"token_endpoint_auth_method": "none",
			})

		case "/token":
			if tokenHandler == nil {
				http.NotFound(w, r)
				return
			}
			tokenHandler(w, r)

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(mock.Close)

	mock.Challenge = fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, mock.URL)
	return mock
}

// MCPURL is the endpoint a proxy is pointed at.
func (m *oauthMockServer) MCPURL() string { return m.URL + "/mcp" }

func writeJSONBody(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// captureBrowserOpens replaces the browser with a recorder for the duration of
// the test, and returns the URLs it was asked to open.
func captureBrowserOpens(t *testing.T, onOpen func()) func() []string {
	t.Helper()

	var mu sync.Mutex
	var opened []string

	original := openBrowserFunc
	t.Cleanup(func() { openBrowserFunc = original })
	openBrowserFunc = func(rawURL string) error {
		mu.Lock()
		opened = append(opened, rawURL)
		mu.Unlock()
		if onOpen != nil {
			onOpen()
		}
		return nil
	}

	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), opened...)
	}
}
