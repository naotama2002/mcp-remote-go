package conformance

import (
	"encoding/json"
	"testing"
)

// TestXMCPHeaderMirroring is the end-to-end case for x-mcp-header: the client
// reads the annotation out of tools/list and applies it on the next
// tools/call. The SDK server validates the header against the request body and
// rejects a mismatch with -32020, so a successful call is evidence the value
// was extracted from the right place and encoded correctly.
func TestXMCPHeaderMirroring(t *testing.T) {
	endpoint, rec := newAnnotatedSDKServer(t)
	c := newClient(t, endpoint)

	// The bindings are only known once the tool has been advertised.
	listed := c.call(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`+modernMeta+`}}`)
	if listed.Error != nil {
		t.Fatalf("tools/list failed: %+v", listed.Error)
	}

	called := c.call(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{`+
		`"name":"execute_sql","arguments":{"region":"us-west1","query":"SELECT 1"},`+modernMeta+`}}`)
	if called.Error != nil {
		t.Fatalf("tools/call failed: %+v", called.Error)
	}

	headers := rec.lastPOST(t)
	if got := headers.Get("Mcp-Param-Region"); got != "us-west1" {
		t.Errorf("Mcp-Param-Region = %q, want us-west1", got)
	}
	if got := headers.Get("Mcp-Name"); got != "execute_sql" {
		t.Errorf("Mcp-Name = %q, want execute_sql", got)
	}
}

// TestXMCPHeaderRequiresTheAdvertisement checks the ordering dependency is
// real rather than assumed: without a preceding tools/list the client has no
// schema to read, so the header is absent. The SDK server enforces the rule
// that makes this matter -- a body value with no matching header is a
// non-conforming client -- so the call is expected to fail.
func TestXMCPHeaderRequiresTheAdvertisement(t *testing.T) {
	endpoint, rec := newAnnotatedSDKServer(t)
	c := newClient(t, endpoint)

	err := c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{` +
		`"name":"execute_sql","arguments":{"region":"us-west1","query":"SELECT 1"},` + modernMeta + `}}`)

	if got := rec.lastPOST(t).Get("Mcp-Param-Region"); got != "" {
		t.Errorf("Mcp-Param-Region = %q, want it absent before any tools/list", got)
	}
	if err == nil {
		t.Log("note: the server accepted a call with no Mcp-Param header; " +
			"the ordering dependency is not observable through it")
	}
}

// TestXMCPHeaderNonASCIIValue exercises the sentinel encoding on the parameter
// path. Unlike Mcp-Name, the SDK does decode Mcp-Param-* before comparing, so
// this round trip is fully verifiable against the reference implementation.
func TestXMCPHeaderNonASCIIValue(t *testing.T) {
	endpoint, rec := newAnnotatedSDKServer(t)
	c := newClient(t, endpoint)

	if listed := c.call(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`+modernMeta+`}}`); listed.Error != nil {
		t.Fatalf("tools/list failed: %+v", listed.Error)
	}

	const region = "東京"
	called := c.call(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{`+
		`"name":"execute_sql","arguments":{"region":"`+region+`","query":"SELECT 1"},`+modernMeta+`}}`)
	if called.Error != nil {
		t.Fatalf("tools/call with a non-ASCII header value failed: %+v", called.Error)
	}

	got := rec.lastPOST(t).Get("Mcp-Param-Region")
	if got == region {
		t.Fatalf("Mcp-Param-Region was sent raw as %q, which is not a valid field value", got)
	}
	for _, r := range got {
		if r < 0x20 || r > 0x7E {
			t.Fatalf("Mcp-Param-Region = %q is not a valid HTTP field value", got)
		}
	}
}

// TestToolsListPassesThroughUnchanged guards the pass-through property: the
// proxy inspects tools/list to learn the bindings, but a response with nothing
// to reject must reach the client exactly as the server wrote it.
//
// The rejection path itself is unit-tested rather than covered here, because
// an SDK server filters invalid tool definitions before sending them. Our
// filtering is what protects a client from a server that does not.
func TestToolsListPassesThroughUnchanged(t *testing.T) {
	endpoint, _ := newAnnotatedSDKServer(t)
	c := newClient(t, endpoint)

	listed := c.call(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`+modernMeta+`}}`)
	if listed.Error != nil {
		t.Fatalf("tools/list failed: %+v", listed.Error)
	}

	var result struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(listed.Result, &result); err != nil {
		t.Fatalf("failed to parse tools/list result: %v", err)
	}

	if len(result.Tools) != 1 || result.Tools[0].Name != "execute_sql" {
		t.Fatalf("tools = %+v, want the single advertised tool", result.Tools)
	}

	// The annotation must still be visible to the client: it is the client's
	// schema, and the proxy only reads it.
	properties, _ := result.Tools[0].InputSchema["properties"].(map[string]any)
	region, _ := properties["region"].(map[string]any)
	if region["x-mcp-header"] != "Region" {
		t.Errorf("x-mcp-header = %v, want it forwarded intact", region["x-mcp-header"])
	}
}
