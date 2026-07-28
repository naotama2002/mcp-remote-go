package proxy

import (
	"strings"
	"testing"
)

func TestParseRequestMetadata(t *testing.T) {
	tests := []struct {
		name            string
		message         string
		wantMethod      string
		wantName        string
		wantHasName     bool
		wantVersion     string
		wantInitVersion string
	}{
		{
			name:        "tools/call mirrors params.name",
			message:     `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_weather","arguments":{}}}`,
			wantMethod:  "tools/call",
			wantName:    "get_weather",
			wantHasName: true,
		},
		{
			name:        "prompts/get mirrors params.name",
			message:     `{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"summarize"}}`,
			wantMethod:  "prompts/get",
			wantName:    "summarize",
			wantHasName: true,
		},
		{
			name:        "resources/read mirrors params.uri",
			message:     `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"file:///a.json"}}`,
			wantMethod:  "resources/read",
			wantName:    "file:///a.json",
			wantHasName: true,
		},
		{
			name:       "resources/read ignores a stray name field",
			message:    `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"name":"nope"}}`,
			wantMethod: "resources/read",
		},
		{
			name:       "tools/list carries no name",
			message:    `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
			wantMethod: "tools/list",
		},
		{
			name:       "method without params",
			message:    `{"jsonrpc":"2.0","id":1,"method":"ping"}`,
			wantMethod: "ping",
		},
		{
			name:        "modern _meta declares the protocol version",
			message:     `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`,
			wantMethod:  "tools/call",
			wantName:    "echo",
			wantHasName: true,
			wantVersion: "2026-07-28",
		},
		{
			name:            "legacy initialize declares its version separately",
			message:         `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
			wantMethod:      "initialize",
			wantInitVersion: "2025-11-25",
		},
		{
			name:    "response carries no method",
			message: `{"jsonrpc":"2.0","id":1,"result":{}}`,
		},
		{
			name:    "malformed JSON yields the zero value",
			message: `{not json`,
		},
		{
			name:       "array params are tolerated",
			message:    `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":[1,2]}`,
			wantMethod: "tools/call",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md := parseRequestMetadata([]byte(tt.message))

			if md.method != tt.wantMethod {
				t.Errorf("method = %q, want %q", md.method, tt.wantMethod)
			}
			if md.name != tt.wantName {
				t.Errorf("name = %q, want %q", md.name, tt.wantName)
			}
			if md.hasName != tt.wantHasName {
				t.Errorf("hasName = %v, want %v", md.hasName, tt.wantHasName)
			}
			if md.protocolVersion != tt.wantVersion {
				t.Errorf("protocolVersion = %q, want %q", md.protocolVersion, tt.wantVersion)
			}
			if md.initializeVersion != tt.wantInitVersion {
				t.Errorf("initializeVersion = %q, want %q", md.initializeVersion, tt.wantInitVersion)
			}
		})
	}
}

func TestRequestMetadataIsModern(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"", false},
		{"2024-11-05", false},
		{"2025-03-26", false},
		{"2025-11-25", false},
		{"2026-07-28", true},
		{"2027-01-01", true},
	}

	for _, tt := range tests {
		md := requestMetadata{protocolVersion: tt.version}
		if got := md.isModern(); got != tt.want {
			t.Errorf("isModern(%q) = %v, want %v", tt.version, got, tt.want)
		}
	}
}

func TestEncodeHeaderValue(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"plain ascii is untouched", "get_weather", "get_weather"},
		{"resource uri is untouched", "file:///projects/app/config.json", "file:///projects/app/config.json"},
		{"empty stays empty", "", ""},
		{"inner space is fine", "hello world", "hello world"},
		{"non-ascii is encoded", "Hello, 世界", "=?base64?SGVsbG8sIOS4lueVjA==?="},
		{"leading space is encoded", " padded ", "=?base64?IHBhZGRlZCA=?="},
		{"newline is encoded", "line1\nline2", "=?base64?bGluZTEKbGluZTI=?="},
		{"sentinel-looking value is encoded", "=?base64?literal?=", "=?base64?PT9iYXNlNjQ/bGl0ZXJhbD89?="},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := encodeHeaderValue(tt.value); got != tt.want {
				t.Errorf("encodeHeaderValue(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

// TestEncodeHeaderValueIsHeaderSafe guards the property the encoding exists
// for: whatever comes out must survive as an HTTP field value.
func TestEncodeHeaderValueIsHeaderSafe(t *testing.T) {
	inputs := []string{
		"plain",
		"日本語ツール",
		"\ttabbed\t",
		"with\r\ninjection: attempt",
		strings.Repeat("é", 10),
	}

	for _, in := range inputs {
		got := encodeHeaderValue(in)
		for i, c := range got {
			if c < 0x20 || c > 0x7E {
				t.Errorf("encodeHeaderValue(%q) = %q: byte %d is not a safe field value", in, got, i)
				break
			}
		}
		if strings.TrimSpace(got) != got {
			t.Errorf("encodeHeaderValue(%q) = %q: has edge whitespace", in, got)
		}
	}
}
