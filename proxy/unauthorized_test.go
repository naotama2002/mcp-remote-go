package proxy

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStreamableHTTPSendReturnsUnauthorizedError verifies that a 401 response
// surfaces an *UnauthorizedError carrying the WWW-Authenticate header, instead
// of being lost in a freeform error string.
func TestStreamableHTTPSendReturnsUnauthorizedError(t *testing.T) {
	const wwwAuth = `Bearer resource_metadata="https://as.example.com/.well-known/oauth-protected-resource"`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", wwwAuth)
		http.Error(w, "unauthorised", http.StatusUnauthorized)
	}))
	defer srv.Close()

	transport := NewStreamableHTTPTransport(StreamableHTTPTransportConfig{
		Endpoint: srv.URL,
		Client:   srv.Client(),
	})

	err := transport.Send(context.Background(), []byte(`{"jsonrpc":"2.0","method":"ping","id":1}`))
	if err == nil {
		t.Fatal("expected error from Send, got nil")
	}

	var unauth *UnauthorizedError
	if !errors.As(err, &unauth) {
		t.Fatalf("expected *UnauthorizedError, got %T: %v", err, err)
	}
	if unauth.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want %d", unauth.StatusCode, http.StatusUnauthorized)
	}
	if unauth.WWWAuthenticate != wwwAuth {
		t.Errorf("WWWAuthenticate = %q, want %q", unauth.WWWAuthenticate, wwwAuth)
	}
}

// TestSSEConnectReturnsUnauthorizedError verifies the SSE transport surfaces
// 401 with WWW-Authenticate during Connect (the GET to the event stream).
func TestSSEConnectReturnsUnauthorizedError(t *testing.T) {
	const wwwAuth = `Bearer resource_metadata="https://example.com/prm", realm="mcp"`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", wwwAuth)
		http.Error(w, "unauthorised", http.StatusUnauthorized)
	}))
	defer srv.Close()

	transport := NewSSETransport(SSETransportConfig{
		ServerURL: srv.URL,
		Client:    srv.Client(),
	})

	err := transport.Connect(context.Background())
	if err == nil {
		t.Fatal("expected error from Connect, got nil")
	}

	var unauth *UnauthorizedError
	if !errors.As(err, &unauth) {
		t.Fatalf("expected *UnauthorizedError, got %T: %v", err, err)
	}
	if unauth.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want %d", unauth.StatusCode, http.StatusUnauthorized)
	}
	if unauth.WWWAuthenticate != wwwAuth {
		t.Errorf("WWWAuthenticate = %q, want %q", unauth.WWWAuthenticate, wwwAuth)
	}
}

// TestSSESendUnauthorizedDoesNotDoubleCloseBody guards against the
// `unauthorizedFromResponse` helper closing the response body while a deferred
// Close in the caller still tries to close it again — that produced a noisy
// "failed to close response body" log line on every 401.
func TestSSESendUnauthorizedDoesNotDoubleCloseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="https://example.com/prm"`)
		http.Error(w, "unauthorised", http.StatusUnauthorized)
	}))
	defer srv.Close()

	var buf bytes.Buffer
	origOutput := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(origOutput)

	transport := NewSSETransport(SSETransportConfig{
		ServerURL: srv.URL,
		Client:    srv.Client(),
	})
	transport.setCommandEndpoint(srv.URL)

	if err := transport.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)); err == nil {
		t.Fatal("expected 401 error from Send, got nil")
	}

	if strings.Contains(buf.String(), "failed to close response body") {
		t.Errorf("unexpected double-close warning in log output:\n%s", buf.String())
	}
}

// TestSSESendReturnsUnauthorizedError verifies the SSE transport surfaces 401
// on the POST command endpoint.
func TestSSESendReturnsUnauthorizedError(t *testing.T) {
	const wwwAuth = `Bearer resource_metadata="https://example.com/prm"`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", wwwAuth)
		http.Error(w, "unauthorised", http.StatusUnauthorized)
	}))
	defer srv.Close()

	transport := NewSSETransport(SSETransportConfig{
		ServerURL: srv.URL,
		Client:    srv.Client(),
	})
	// Pretend the SSE handshake already happened by setting an explicit
	// command endpoint; Send() short-circuits the "not connected" check
	// when this is set.
	transport.setCommandEndpoint(srv.URL)

	err := transport.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err == nil {
		t.Fatal("expected error from Send, got nil")
	}

	var unauth *UnauthorizedError
	if !errors.As(err, &unauth) {
		t.Fatalf("expected *UnauthorizedError, got %T: %v", err, err)
	}
	if unauth.WWWAuthenticate != wwwAuth {
		t.Errorf("WWWAuthenticate = %q, want %q", unauth.WWWAuthenticate, wwwAuth)
	}
}
