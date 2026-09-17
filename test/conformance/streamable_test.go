package conformance

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// modernMeta is the per-request metadata a 2026-07-28 client attaches to every
// call. From this revision on it, not a handshake, is what declares the era.
const modernMeta = `"_meta":{` +
	`"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
	`"io.modelcontextprotocol/clientInfo":{"name":"conformance","version":"0.1.0"},` +
	`"io.modelcontextprotocol/clientCapabilities":{}}`

// TestLegacyClientIsUnaffected is the regression guard for the migration: a
// client that still speaks the initialize handshake must keep working against
// a dual-era server, because that is what every current mcp-remote-go user has.
func TestLegacyClientIsUnaffected(t *testing.T) {
	endpoint, rec := newRecordedSDKServer(t)
	c := newClient(t, endpoint)

	initResp := c.call(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{`+
		`"protocolVersion":"2025-11-25",`+
		`"capabilities":{},`+
		`"clientInfo":{"name":"conformance","version":"0.1.0"}}}`)
	if initResp.Error != nil {
		t.Fatalf("initialize failed: %+v", initResp.Error)
	}

	resp := c.call(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{`+
		`"name":"echo","arguments":{"text":"hi"}}}`)
	if resp.Error != nil {
		t.Fatalf("legacy tools/call failed: %+v", resp.Error)
	}

	// A legacy request must not carry the modern routing headers: the server
	// only validates them from 2026-07-28 onwards, and sending them while
	// declaring an older version misrepresents us to intermediaries.
	headers := rec.lastPOST(t)
	if got := headers.Get("Mcp-Protocol-Version"); got != "2025-11-25" {
		t.Errorf("Mcp-Protocol-Version = %q, want the version the client negotiated (2025-11-25)", got)
	}
	if got := headers.Get("Mcp-Method"); got != "" {
		t.Errorf("Mcp-Method = %q, want it absent on a legacy request", got)
	}
}

// TestModernClientIsForwarded covers the capability this change adds: a client
// that speaks 2026-07-28 reaches a modern server through the proxy.
//
// While the transport stamped a hardcoded Mcp-Protocol-Version this was not
// merely incomplete, it was fatal — the server saw a header claiming
// 2025-11-25 over a body declaring 2026-07-28 and rejected the request with
// -32020 before it ever looked for the routing headers.
func TestModernClientIsForwarded(t *testing.T) {
	endpoint, rec := newRecordedSDKServer(t)
	c := newClient(t, endpoint)

	resp := c.call(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{`+
		`"name":"echo","arguments":{"text":"hi"},`+modernMeta+`}}`)
	if resp.Error != nil {
		t.Fatalf("modern tools/call failed: %+v", resp.Error)
	}

	headers := rec.lastPOST(t)
	if got := headers.Get("Mcp-Protocol-Version"); got != "2026-07-28" {
		t.Errorf("Mcp-Protocol-Version = %q, want 2026-07-28", got)
	}
	if got := headers.Get("Mcp-Method"); got != "tools/call" {
		t.Errorf("Mcp-Method = %q, want tools/call", got)
	}
	if got := headers.Get("Mcp-Name"); got != "echo" {
		t.Errorf("Mcp-Name = %q, want echo", got)
	}
	// Sessions are gone in this revision.
	if got := headers.Get("Mcp-Session-Id"); got != "" {
		t.Errorf("Mcp-Session-Id = %q, want it absent on a modern request", got)
	}
}

// TestModernNameSourcePerMethod checks that Mcp-Name is taken from the field
// the spec designates for each method, since a mismatch is a hard rejection.
func TestModernNameSourcePerMethod(t *testing.T) {
	tests := []struct {
		name     string
		message  string
		wantName string
	}{
		{
			name:     "tools/call uses params.name",
			message:  `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":"x"},` + modernMeta + `}}`,
			wantName: "echo",
		},
		{
			name:     "tools/list carries no name",
			message:  `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{` + modernMeta + `}}`,
			wantName: "",
		},
		{
			name:     "server/discover carries no name",
			message:  `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{` + modernMeta + `}}`,
			wantName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpoint, rec := newRecordedSDKServer(t)
			c := newClient(t, endpoint)

			resp := c.call(t, tt.message)
			if resp.Error != nil {
				t.Fatalf("request failed: %+v", resp.Error)
			}

			if got := rec.lastPOST(t).Get("Mcp-Name"); got != tt.wantName {
				t.Errorf("Mcp-Name = %q, want %q", got, tt.wantName)
			}
		})
	}
}

// TestModernNonASCIINameIsEncoded covers the base64 sentinel encoding, which
// the spec requires whenever a tool name or resource URI is not header-safe:
//
//	The same encoding rule applies to the Mcp-Name header value. Tool and
//	prompt names are only SHOULD-constrained to header-safe characters, so a
//	name (or resource URI) outside the safe set is carried as:
//	Mcp-Name: =?base64?{Base64EncodedValue}?=
//
// KNOWN DIVERGENCE (go-sdk v1.7.0-pre.3): the SDK does not implement this for
// Mcp-Name in either direction — its client sets the raw value, and its server
// compares the header to the body without calling decodeHeaderValue. It does
// decode for Mcp-Param-*, so this reads as an oversight rather than a
// deliberate reading of the spec. Until it is fixed, a spec-correct client is
// rejected with -32020, which is why this test asserts on what we put on the
// wire rather than on the server's verdict.
func TestModernNonASCIINameIsEncoded(t *testing.T) {
	const toolName = "天気予報"

	endpoint, rec := newRecordedSDKServer(t, toolName)
	c := newClient(t, endpoint)

	// The server's verdict is not asserted; see the divergence note above.
	_ = c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{` +
		`"name":"` + toolName + `","arguments":{"text":"hi"},` + modernMeta + `}}`)

	got := rec.lastPOST(t).Get("Mcp-Name")
	want := "=?base64?" + base64.StdEncoding.EncodeToString([]byte(toolName)) + "?="
	if got != want {
		t.Errorf("Mcp-Name = %q, want %q", got, want)
	}
	for _, r := range got {
		if r < 0x20 || r > 0x7E {
			t.Fatalf("Mcp-Name = %q contains a byte that is not a valid HTTP field value", got)
		}
	}
}

// TestSDKRejectsEncodedName pins the divergence itself, so the day the SDK
// starts decoding Mcp-Name this test fails and tells us the workaround note in
// TestModernNonASCIINameIsEncoded can be removed.
func TestSDKRejectsEncodedName(t *testing.T) {
	const toolName = "天気予報"

	endpoint := newSDKServer(t, toolName)
	encoded := "=?base64?" + base64.StdEncoding.EncodeToString([]byte(toolName)) + "?="

	resp := rawPOST(t, endpoint,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{`+
			`"name":"`+toolName+`","arguments":{"text":"hi"},`+modernMeta+`}}`,
		map[string]string{
			"Mcp-Protocol-Version": "2026-07-28",
			"Mcp-Method":           "tools/call",
			"Mcp-Name":             encoded,
		})

	if resp.Error == nil {
		t.Fatal("go-sdk now accepts a sentinel-encoded Mcp-Name: " +
			"drop the divergence note in TestModernNonASCIINameIsEncoded and assert the round trip")
	}
	if !strings.Contains(resp.Error.Message, "does not match body value") {
		t.Errorf("unexpected rejection reason %q; the divergence may have changed shape", resp.Error.Message)
	}
}

// TestModernServerDropsSessionMechanics pins the transport removals in
// 2026-07-28 so a future change cannot quietly reintroduce them.
func TestModernServerDropsSessionMechanics(t *testing.T) {
	endpoint, rec := newRecordedSDKServer(t)
	c := newClient(t, endpoint)

	resp := c.call(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`+modernMeta+`}}`)
	if resp.Error != nil {
		t.Fatalf("tools/list failed: %+v", resp.Error)
	}

	// The server must not have minted a session for us.
	if sid := c.transport.SessionID(); sid != "" {
		t.Errorf("SessionID() = %q, want empty against a stateless server", sid)
	}

	// Connect opens a GET notification stream, which this revision removed.
	// The server answers 405 and the transport must stop retrying rather than
	// spin. Reaching this point at all means it did not block startup.
	if got := rec.lastPOST(t).Get("Last-Event-ID"); got != "" {
		t.Errorf("Last-Event-ID = %q, want it absent: streams are not resumable", got)
	}
}

// TestDiscoverProbeContract checks the assumption era detection rests on:
// that a real modern server answers server/discover with the list of versions
// it speaks. Our classification of that answer is unit-tested in the proxy
// package; what cannot be verified there is whether a genuine server replies
// in this shape at all, which is what this pins.
func TestDiscoverProbeContract(t *testing.T) {
	endpoint := newSDKServer(t)

	resp := rawPOST(t, endpoint,
		`{"jsonrpc":"2.0","id":0,"method":"server/discover","params":{`+modernMeta+`}}`,
		map[string]string{
			"Mcp-Protocol-Version": "2026-07-28",
			"Mcp-Method":           "server/discover",
		})

	if resp.Error != nil {
		t.Fatalf("server/discover failed: %+v", resp.Error)
	}

	var result struct {
		SupportedVersions []string `json:"supportedVersions"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("failed to parse DiscoverResult: %v", err)
	}

	if !slices.Contains(result.SupportedVersions, "2026-07-28") {
		t.Errorf("supportedVersions = %v, want it to include 2026-07-28", result.SupportedVersions)
	}

	// The SDK serves both eras, which is what makes the "does this server
	// still answer initialize" question answerable from the version list.
	if !slices.Contains(result.SupportedVersions, "2025-11-25") {
		t.Errorf("supportedVersions = %v, want a dual-era server to list 2025-11-25", result.SupportedVersions)
	}
}

// TestModernMissingHeadersAreRejected documents the failure mode this work
// exists to avoid, and proves the fixture server really does enforce the rule
// rather than accepting anything we send.
func TestModernMissingHeadersAreRejected(t *testing.T) {
	endpoint := newSDKServer(t)

	// Bypass the transport and post a modern body with no routing headers.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"},` + modernMeta + `}}`
	resp := rawPOST(t, endpoint, body, map[string]string{
		"Mcp-Protocol-Version": "2026-07-28",
	})

	if resp.Error == nil {
		t.Fatal("expected the server to reject a modern request without Mcp-Method")
	}
	if resp.Error.Code != -32020 {
		t.Errorf("error code = %d, want -32020 (HeaderMismatch)", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "Mcp-Method") {
		t.Errorf("error message = %q, want it to name the missing header", resp.Error.Message)
	}
}
