package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestClassifyProbe(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantEra       serverEra
		wantSupported []string
		wantJSONRPC   bool
	}{
		{
			name:          "DiscoverResult identifies a modern server",
			body:          `{"jsonrpc":"2.0","id":0,"result":{"supportedVersions":["2026-07-28","2025-11-25"]}}`,
			wantEra:       eraModern,
			wantSupported: []string{"2026-07-28", "2025-11-25"},
			wantJSONRPC:   true,
		},
		{
			name:        "a result without a version list is still modern",
			body:        `{"jsonrpc":"2.0","id":0,"result":{}}`,
			wantEra:     eraModern,
			wantJSONRPC: true,
		},
		{
			name:        "HeaderMismatch identifies a modern server",
			body:        `{"jsonrpc":"2.0","id":0,"error":{"code":-32020,"message":"missing required Mcp-Method header"}}`,
			wantEra:     eraModern,
			wantJSONRPC: true,
		},
		{
			name:        "MissingRequiredClientCapabilities identifies a modern server",
			body:        `{"jsonrpc":"2.0","id":0,"error":{"code":-32021,"message":"missing capability"}}`,
			wantEra:     eraModern,
			wantJSONRPC: true,
		},
		{
			name:          "UnsupportedProtocolVersion carries the fallback options",
			body:          `{"jsonrpc":"2.0","id":0,"error":{"code":-32022,"message":"Unsupported protocol version","data":{"supported":["2025-11-25"],"requested":"2026-07-28"}}}`,
			wantEra:       eraModern,
			wantSupported: []string{"2025-11-25"},
			wantJSONRPC:   true,
		},
		{
			name:        "method not found means the server has no server/discover",
			body:        `{"jsonrpc":"2.0","id":0,"error":{"code":-32601,"message":"Method not found"}}`,
			wantEra:     eraLegacy,
			wantJSONRPC: true,
		},
		{
			name:        "initialize-first is the classic legacy rejection",
			body:        `{"jsonrpc":"2.0","id":0,"error":{"code":-32600,"message":"Must send initialize first"}}`,
			wantEra:     eraLegacy,
			wantJSONRPC: true,
		},
		{
			name:        "an empty body proves nothing",
			body:        ``,
			wantEra:     eraUnknown,
			wantJSONRPC: false,
		},
		{
			name:        "an HTML error page proves nothing",
			body:        `<html><body>404 Not Found</body></html>`,
			wantEra:     eraUnknown,
			wantJSONRPC: false,
		},
		{
			name:        "valid JSON that is not JSON-RPC proves nothing",
			body:        `{"error":"bad request"}`,
			wantEra:     eraUnknown,
			wantJSONRPC: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			era, supported, isJSONRPC := classifyProbe([]byte(tt.body))

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

// TestProbePayload covers the reply framing. A server chooses per request
// whether to answer with a JSON object or an SSE stream, and real ones -- the
// GitHub MCP server and the Go SDK both -- choose SSE, so reading the raw body
// as JSON silently classified every live server as "era unknown".
func TestProbePayload(t *testing.T) {
	const message = `{"jsonrpc":"2.0","id":0,"result":{"supportedVersions":["2026-07-28"]}}`

	tests := []struct {
		name        string
		contentType string
		body        string
		want        string
	}{
		{
			name:        "a JSON reply is passed through",
			contentType: "application/json",
			body:        message,
			want:        message,
		},
		{
			name:        "an SSE reply is unwrapped",
			contentType: "text/event-stream",
			body:        "event: message\ndata: " + message + "\n\n",
			want:        message,
		},
		{
			name:        "content-type parameters do not confuse it",
			contentType: "text/event-stream; charset=utf-8",
			body:        "event: message\ndata: " + message + "\n\n",
			want:        message,
		},
		{
			name:        "only the first event is taken",
			contentType: "text/event-stream",
			body:        "event: message\ndata: " + message + "\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1}\n\n",
			want:        message,
		},
		{
			name:        "a comment keep-alive is skipped",
			contentType: "text/event-stream",
			body:        ":\n\nevent: message\ndata: " + message + "\n\n",
			want:        message,
		},
		{
			name:        "an SSE body with no event falls back to the raw bytes",
			contentType: "text/event-stream",
			body:        "",
			want:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(probePayload(tt.contentType, []byte(tt.body)))
			if got != tt.want {
				t.Errorf("probePayload() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestNegotiateClassifiesSSEProbeReply is the end-to-end guard for the same
// thing: a server that answers the probe over SSE must still be placed.
func TestNegotiateClassifiesSSEProbeReply(t *testing.T) {
	var mu sync.Mutex
	var gets int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			mu.Lock()
			gets++
			mu.Unlock()
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "event: message\ndata: "+
			`{"jsonrpc":"2.0","id":0,"result":{"supportedVersions":["2026-07-28","2025-11-25"]}}`+"\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	proxy, err := NewProxyWithTransport(server.URL, 0, map[string]string{}, "sse-probe-test", TransportModeAuto)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Shutdown()

	if err := proxy.connectToServer(); err != nil {
		t.Fatalf("connectToServer failed: %v", err)
	}

	profile := proxy.currentProfile()
	if profile.era != eraModern {
		t.Errorf("era = %v, want modern", profile.era)
	}
	if len(profile.supportedVersions) != 2 {
		t.Errorf("supportedVersions = %v, want both advertised versions", profile.supportedVersions)
	}

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if gets != 0 {
		t.Errorf("opened %d GET notification streams, want 0: the era was known before connecting", gets)
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
