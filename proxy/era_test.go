package proxy

import (
	"net/http"
	"reflect"
	"testing"
)

func TestClassifyProbe(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		body          string
		wantEra       serverEra
		wantSupported []string
		wantJSONRPC   bool
	}{
		{
			name:          "DiscoverResult identifies a modern server",
			status:        http.StatusOK,
			body:          `{"jsonrpc":"2.0","id":0,"result":{"supportedVersions":["2026-07-28","2025-11-25"]}}`,
			wantEra:       eraModern,
			wantSupported: []string{"2026-07-28", "2025-11-25"},
			wantJSONRPC:   true,
		},
		{
			name:        "a result without a version list is still modern",
			status:      http.StatusOK,
			body:        `{"jsonrpc":"2.0","id":0,"result":{}}`,
			wantEra:     eraModern,
			wantJSONRPC: true,
		},
		{
			name:        "HeaderMismatch identifies a modern server",
			status:      http.StatusBadRequest,
			body:        `{"jsonrpc":"2.0","id":0,"error":{"code":-32020,"message":"missing required Mcp-Method header"}}`,
			wantEra:     eraModern,
			wantJSONRPC: true,
		},
		{
			name:        "MissingRequiredClientCapabilities identifies a modern server",
			status:      http.StatusBadRequest,
			body:        `{"jsonrpc":"2.0","id":0,"error":{"code":-32021,"message":"missing capability"}}`,
			wantEra:     eraModern,
			wantJSONRPC: true,
		},
		{
			name:          "UnsupportedProtocolVersion carries the fallback options",
			status:        http.StatusBadRequest,
			body:          `{"jsonrpc":"2.0","id":0,"error":{"code":-32022,"message":"Unsupported protocol version","data":{"supported":["2025-11-25"],"requested":"2026-07-28"}}}`,
			wantEra:       eraModern,
			wantSupported: []string{"2025-11-25"},
			wantJSONRPC:   true,
		},
		{
			name:        "method not found means the server has no server/discover",
			status:      http.StatusNotFound,
			body:        `{"jsonrpc":"2.0","id":0,"error":{"code":-32601,"message":"Method not found"}}`,
			wantEra:     eraLegacy,
			wantJSONRPC: true,
		},
		{
			name:        "initialize-first is the classic legacy rejection",
			status:      http.StatusBadRequest,
			body:        `{"jsonrpc":"2.0","id":0,"error":{"code":-32600,"message":"Must send initialize first"}}`,
			wantEra:     eraLegacy,
			wantJSONRPC: true,
		},
		{
			name:        "an empty body proves nothing",
			status:      http.StatusNotFound,
			body:        ``,
			wantEra:     eraUnknown,
			wantJSONRPC: false,
		},
		{
			name:        "an HTML error page proves nothing",
			status:      http.StatusNotFound,
			body:        `<html><body>404 Not Found</body></html>`,
			wantEra:     eraUnknown,
			wantJSONRPC: false,
		},
		{
			name:        "valid JSON that is not JSON-RPC proves nothing",
			status:      http.StatusBadRequest,
			body:        `{"error":"bad request"}`,
			wantEra:     eraUnknown,
			wantJSONRPC: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			era, supported, isJSONRPC := classifyProbe(tt.status, []byte(tt.body))

			if era != tt.wantEra {
				t.Errorf("era = %v, want %v", era, tt.wantEra)
			}
			if isJSONRPC != tt.wantJSONRPC {
				t.Errorf("isJSONRPC = %v, want %v", isJSONRPC, tt.wantJSONRPC)
			}
			if len(supported) != 0 || len(tt.wantSupported) != 0 {
				if !reflect.DeepEqual(supported, tt.wantSupported) {
					t.Errorf("supported = %v, want %v", supported, tt.wantSupported)
				}
			}
		})
	}
}

func TestServerProfileSupportsLegacy(t *testing.T) {
	tests := []struct {
		name    string
		profile serverProfile
		want    bool
	}{
		{
			name:    "a dual-era server lists a legacy version",
			profile: serverProfile{era: eraModern, supportedVersions: []string{"2026-07-28", "2025-11-25"}},
			want:    true,
		},
		{
			name:    "a modern-only server does not",
			profile: serverProfile{era: eraModern, supportedVersions: []string{"2026-07-28"}},
			want:    false,
		},
		{
			name:    "a legacy server obviously does",
			profile: serverProfile{era: eraLegacy},
			want:    true,
		},
		{
			name:    "silence is not evidence, so traffic is not refused",
			profile: serverProfile{era: eraModern},
			want:    true,
		},
		{
			name:    "an unknown era is not evidence either",
			profile: serverProfile{era: eraUnknown},
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.profile.supportsLegacy(); got != tt.want {
				t.Errorf("supportsLegacy() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestServerEraString(t *testing.T) {
	tests := []struct {
		era  serverEra
		want string
	}{
		{eraModern, "modern"},
		{eraLegacy, "legacy"},
		{eraUnknown, "unknown"},
	}

	for _, tt := range tests {
		if got := tt.era.String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}

// TestProbeBodyIsAModernRequest checks the probe declares the era it is
// testing for: a probe that omits _meta would be read as legacy traffic and
// the server would answer as a legacy server, defeating the detection.
func TestProbeBodyIsAModernRequest(t *testing.T) {
	md := parseRequestMetadata([]byte(probeBody()))

	if md.method != "server/discover" {
		t.Errorf("probe method = %q, want server/discover", md.method)
	}
	if md.protocolVersion != ProtocolVersion20260728 {
		t.Errorf("probe declares version %q, want %q", md.protocolVersion, ProtocolVersion20260728)
	}
	if !md.isModern() {
		t.Error("probe is not recognised as a modern request")
	}
}
