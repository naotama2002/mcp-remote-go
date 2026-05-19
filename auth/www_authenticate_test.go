package auth

import "testing"

func TestParseWWWAuthenticate(t *testing.T) {
	tests := []struct {
		name             string
		header           string
		wantOK           bool
		wantResourceMeta string
		wantRealm        string
		wantScope        string
		wantError        string
	}{
		{
			name:             "spec example: resource_metadata only",
			header:           `Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource"`,
			wantOK:           true,
			wantResourceMeta: "https://mcp.example.com/.well-known/oauth-protected-resource",
		},
		{
			name:             "multiple params with realm and scope",
			header:           `Bearer realm="mcp", scope="read write", resource_metadata="https://as.example.com/.well-known/oauth-protected-resource"`,
			wantOK:           true,
			wantResourceMeta: "https://as.example.com/.well-known/oauth-protected-resource",
			wantRealm:        "mcp",
			wantScope:        "read write",
		},
		{
			name:      "unquoted values",
			header:    `Bearer realm=mcp, scope=read`,
			wantOK:    true,
			wantRealm: "mcp",
			wantScope: "read",
		},
		{
			name:      "error parameter",
			header:    `Bearer error="invalid_token", error_description="Token expired"`,
			wantOK:    true,
			wantError: "invalid_token",
		},
		{
			name:             "case-insensitive scheme",
			header:           `bearer resource_metadata="https://example.com/prm"`,
			wantOK:           true,
			wantResourceMeta: "https://example.com/prm",
		},
		{
			name:   "bare Bearer challenge",
			header: `Bearer`,
			wantOK: true,
		},
		{
			name:   "empty header",
			header: ``,
			wantOK: false,
		},
		{
			name:   "no Bearer challenge",
			header: `Basic realm="example"`,
			wantOK: false,
		},
		{
			name:             "Bearer preceded by another scheme",
			header:           `Basic realm="x", Bearer resource_metadata="https://example.com/prm"`,
			wantOK:           true,
			wantResourceMeta: "https://example.com/prm",
		},
		{
			name:      "quoted value with embedded escape",
			header:    `Bearer realm="quote\"inside"`,
			wantOK:    true,
			wantRealm: `quote"inside`,
		},
		{
			name:   "MyBearer should not match Bearer",
			header: `MyBearer resource_metadata="https://example.com/prm"`,
			wantOK: false,
		},
		{
			name:             "non-boundary bearer earlier in header does not mask later Bearer",
			header:           `MyBearer foo=bar, Bearer resource_metadata="https://example.com/prm"`,
			wantOK:           true,
			wantResourceMeta: "https://example.com/prm",
		},
		{
			name:             "bearer substring inside a quoted realm before a real Bearer challenge",
			header:           `Basic realm="Bearer-Realm", Bearer resource_metadata="https://example.com/prm"`,
			wantOK:           true,
			wantResourceMeta: "https://example.com/prm",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseWWWAuthenticate(tt.header)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (parsed: %+v)", ok, tt.wantOK, got)
			}
			if !ok {
				return
			}
			if got.ResourceMetadata != tt.wantResourceMeta {
				t.Errorf("ResourceMetadata = %q, want %q", got.ResourceMetadata, tt.wantResourceMeta)
			}
			if got.Realm != tt.wantRealm {
				t.Errorf("Realm = %q, want %q", got.Realm, tt.wantRealm)
			}
			if got.Scope != tt.wantScope {
				t.Errorf("Scope = %q, want %q", got.Scope, tt.wantScope)
			}
			if got.Error != tt.wantError {
				t.Errorf("Error = %q, want %q", got.Error, tt.wantError)
			}
		})
	}
}

func TestBestWWWAuthenticateHeader(t *testing.T) {
	const bearerPRM = `Bearer resource_metadata="https://example.com/prm"`
	const bearerBare = `Bearer realm="mcp"`

	tests := []struct {
		name    string
		headers []string
		want    string
	}{
		{
			name:    "prefers Bearer with resource_metadata over earlier Digest line",
			headers: []string{`Digest realm="corp"`, bearerPRM},
			want:    bearerPRM,
		},
		{
			name:    "skips non-Bearer lines",
			headers: []string{`Negotiate`, bearerPRM},
			want:    bearerPRM,
		},
		{
			name:    "falls back to Bearer without resource_metadata",
			headers: []string{`Digest realm="corp"`, bearerBare},
			want:    bearerBare,
		},
		{
			name:    "empty",
			headers: nil,
			want:    "",
		},
		{
			name:    "no Bearer falls back to first non-empty line for diagnostics",
			headers: []string{`Digest realm="corp"`, `Negotiate`},
			want:    `Digest realm="corp"`,
		},
		{
			name:    "blank lines are skipped when falling back",
			headers: []string{"", `  `, `Digest realm="corp"`},
			want:    `Digest realm="corp"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BestWWWAuthenticateHeader(tt.headers); got != tt.want {
				t.Errorf("BestWWWAuthenticateHeader() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseWWWAuthenticateHeaders(t *testing.T) {
	headers := []string{
		`Digest realm="corp"`,
		`Bearer resource_metadata="https://example.com/prm"`,
	}
	challenge, ok := ParseWWWAuthenticateHeaders(headers)
	if !ok {
		t.Fatal("expected ok")
	}
	if challenge.ResourceMetadata != "https://example.com/prm" {
		t.Errorf("ResourceMetadata = %q", challenge.ResourceMetadata)
	}
}
