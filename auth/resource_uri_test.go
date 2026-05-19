package auth

import "testing"

func TestCanonicalResourceURI(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "simple https",
			input: "https://mcp.example.com",
			want:  "https://mcp.example.com",
		},
		{
			name:  "with path",
			input: "https://mcp.example.com/mcp",
			want:  "https://mcp.example.com/mcp",
		},
		{
			name:  "with port",
			input: "https://mcp.example.com:8443",
			want:  "https://mcp.example.com:8443",
		},
		{
			name:  "uppercase scheme and host normalised",
			input: "HTTPS://MCP.Example.COM/Mcp",
			want:  "https://mcp.example.com/Mcp",
		},
		{
			name:  "trailing slash on root stripped",
			input: "https://mcp.example.com/",
			want:  "https://mcp.example.com",
		},
		{
			name:  "trailing slash on subpath stripped",
			input: "https://mcp.example.com/mcp/",
			want:  "https://mcp.example.com/mcp",
		},
		{
			name:  "nested path preserved",
			input: "https://mcp.example.com/server/mcp",
			want:  "https://mcp.example.com/server/mcp",
		},
		{
			name:  "query preserved",
			input: "https://mcp.example.com/mcp?tenant=acme",
			want:  "https://mcp.example.com/mcp?tenant=acme",
		},
		{
			name:    "missing scheme",
			input:   "mcp.example.com",
			wantErr: true,
		},
		{
			name:    "with fragment",
			input:   "https://mcp.example.com#fragment",
			wantErr: true,
		},
		{
			name:    "empty",
			input:   "",
			wantErr: true,
		},
		{
			name:    "scheme only",
			input:   "https://",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalResourceURI(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got %q", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("CanonicalResourceURI(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
