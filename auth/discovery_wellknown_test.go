package auth

import (
	"reflect"
	"testing"
)

// TestWellKnownCandidates covers issuers that carry a path. RFC 8414 §3.1
// inserts the well-known segment between the host and the path; OpenID Connect
// Discovery appends it. Both are tried, and neither is the naive "replace the
// path" that this used to do.
func TestWellKnownCandidates(t *testing.T) {
	tests := []struct {
		name     string
		issuer   string
		endpoint string
		want     []string
	}{
		{
			name:     "an issuer with no path has a single location",
			issuer:   "https://auth.example.com",
			endpoint: "oauth-authorization-server",
			want:     []string{"https://auth.example.com/.well-known/oauth-authorization-server"},
		},
		{
			name:     "a trailing slash is not a path",
			issuer:   "https://auth.example.com/",
			endpoint: "oauth-authorization-server",
			want:     []string{"https://auth.example.com/.well-known/oauth-authorization-server"},
		},
		{
			name:     "GitHub's issuer keeps its path",
			issuer:   "https://github.com/login/oauth",
			endpoint: "oauth-authorization-server",
			want: []string{
				"https://github.com/.well-known/oauth-authorization-server/login/oauth",
				"https://github.com/login/oauth/.well-known/oauth-authorization-server",
			},
		},
		{
			name:     "the OIDC endpoint gets the same treatment",
			issuer:   "https://github.com/login/oauth",
			endpoint: "openid-configuration",
			want: []string{
				"https://github.com/.well-known/openid-configuration/login/oauth",
				"https://github.com/login/oauth/.well-known/openid-configuration",
			},
		},
		{
			name:     "a port is preserved",
			issuer:   "https://auth.example.com:8443/tenant-a",
			endpoint: "oauth-authorization-server",
			want: []string{
				"https://auth.example.com:8443/.well-known/oauth-authorization-server/tenant-a",
				"https://auth.example.com:8443/tenant-a/.well-known/oauth-authorization-server",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := wellKnownCandidates(tt.issuer, tt.endpoint)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("candidates =\n  %v\nwant\n  %v", got, tt.want)
			}
		})
	}
}

func TestWellKnownCandidatesRejectsUnusableIssuers(t *testing.T) {
	for _, issuer := range []string{"", "not-a-url", "/relative/path", "auth.example.com"} {
		if _, err := wellKnownCandidates(issuer, "oauth-authorization-server"); err == nil {
			t.Errorf("wellKnownCandidates(%q) succeeded, want an error", issuer)
		}
	}
}
