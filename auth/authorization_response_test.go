package auth

import (
	"net/url"
	"strings"
	"testing"
)

func TestGenerateState(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		state, err := GenerateState()
		if err != nil {
			t.Fatalf("GenerateState failed: %v", err)
		}
		if len(state) < 32 {
			t.Errorf("state %q is too short to be unguessable", state)
		}
		if seen[state] {
			t.Fatalf("GenerateState returned a duplicate value %q", state)
		}
		seen[state] = true

		// Must survive a round trip through a query string unescaped.
		if url.QueryEscape(state) != state {
			t.Errorf("state %q is not URL-safe", state)
		}
	}
}

func TestValidateAuthorizationResponse(t *testing.T) {
	const (
		issuer = "https://as.example.com"
		state  = "the-expected-state"
	)

	plainMetadata := &ServerMetadata{Issuer: issuer}
	issAwareMetadata := &ServerMetadata{
		Issuer: issuer,
		AuthorizationResponseIssParameterSupported: true,
	}

	tests := []struct {
		name          string
		query         url.Values
		expectedState string
		metadata      *ServerMetadata
		wantErr       string
	}{
		{
			name:          "valid response without iss",
			query:         url.Values{"code": {"abc"}, "state": {state}},
			expectedState: state,
			metadata:      plainMetadata,
		},
		{
			name:          "valid response with matching iss",
			query:         url.Values{"code": {"abc"}, "state": {state}, "iss": {issuer}},
			expectedState: state,
			metadata:      plainMetadata,
		},
		{
			name:          "iss mismatch is rejected",
			query:         url.Values{"code": {"abc"}, "state": {state}, "iss": {"https://evil.example.com"}},
			expectedState: state,
			metadata:      plainMetadata,
			wantErr:       "issuer mismatch",
		},
		{
			name:          "missing iss is rejected when the server advertises it",
			query:         url.Values{"code": {"abc"}, "state": {state}},
			expectedState: state,
			metadata:      issAwareMetadata,
			wantErr:       "omitted it from the response",
		},
		{
			name:          "state mismatch is rejected",
			query:         url.Values{"code": {"abc"}, "state": {"forged"}},
			expectedState: state,
			metadata:      plainMetadata,
			wantErr:       "state mismatch",
		},
		{
			name:          "missing state is rejected",
			query:         url.Values{"code": {"abc"}},
			expectedState: state,
			metadata:      plainMetadata,
			wantErr:       "state mismatch",
		},
		{
			name:          "callback with no flight in progress is rejected",
			query:         url.Values{"code": {"abc"}, "state": {state}},
			expectedState: "",
			metadata:      plainMetadata,
			wantErr:       "no authorization request in flight",
		},
		{
			name:          "missing code is rejected",
			query:         url.Values{"state": {state}},
			expectedState: state,
			metadata:      plainMetadata,
			wantErr:       "authorization code not found",
		},
		{
			name:          "server-reported error is surfaced",
			query:         url.Values{"error": {"access_denied"}, "error_description": {"user said no"}},
			expectedState: state,
			metadata:      plainMetadata,
			wantErr:       "user said no",
		},
		{
			name:          "server-reported error without description",
			query:         url.Values{"error": {"invalid_scope"}},
			expectedState: state,
			metadata:      plainMetadata,
			wantErr:       "invalid_scope",
		},
		{
			name:          "missing metadata is rejected",
			query:         url.Values{"code": {"abc"}, "state": {state}},
			expectedState: state,
			metadata:      nil,
			wantErr:       "no authorization server metadata",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAuthorizationResponse(tt.query, tt.expectedState, tt.metadata)

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestBuildAuthorizationURLIncludesState checks the request side of the CSRF
// binding: the state that lands in the URL is the one the callback will demand.
func TestBuildAuthorizationURLIncludesState(t *testing.T) {
	c := &Coordinator{
		callbackPort:   3334,
		serverMetadata: &ServerMetadata{AuthorizationEndpoint: "https://as.example.com/authorize"},
		clientInfo:     &ClientInfo{ClientID: "client-123"},
	}

	rawURL, err := c.buildAuthorizationURL()
	if err != nil {
		t.Fatalf("buildAuthorizationURL failed: %v", err)
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("failed to parse authorization URL: %v", err)
	}

	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatal("authorization URL has no state parameter")
	}
	if state != c.state {
		t.Errorf("URL state %q does not match stored state %q", state, c.state)
	}

	// A second call must not reuse the previous state.
	first := c.state
	if _, err := c.buildAuthorizationURL(); err != nil {
		t.Fatalf("second buildAuthorizationURL failed: %v", err)
	}
	if c.state == first {
		t.Error("state was reused across authorization requests")
	}
}
