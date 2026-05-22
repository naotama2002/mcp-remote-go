package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/naotama2002/mcp-remote-go/internal/httpclient"
)

// TestProtectedResourceDiscoveryRejectsInvalidPRMURL verifies that an
// attacker-supplied WWW-Authenticate `resource_metadata` value pointing at a
// non-absolute or non-http(s) URL is rejected before any fetch happens.
func TestProtectedResourceDiscoveryRejectsInvalidPRMURL(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		errSub string
	}{
		{
			name:   "relative URL",
			url:    "/some/path",
			errSub: "scheme",
		},
		{
			name:   "ftp scheme",
			url:    "ftp://example.com/prm",
			errSub: "scheme",
		},
		{
			name:   "file scheme",
			url:    "file:///etc/passwd",
			errSub: "scheme",
		},
		{
			name:   "javascript scheme",
			url:    "javascript:alert(1)",
			errSub: "scheme",
		},
		{
			name:   "scheme without host",
			url:    "https://",
			errSub: "host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := httpclient.New(nil)
			disco := NewProtectedResourceDiscoveryFromURL(*client, tt.url)
			_, err := disco.Discover(context.Background(), "https://mcp.example.com")
			if err == nil {
				t.Fatalf("expected error for %q, got nil", tt.url)
			}
			if !strings.Contains(err.Error(), tt.errSub) {
				t.Errorf("error %q does not mention %q", err.Error(), tt.errSub)
			}
		})
	}
}
