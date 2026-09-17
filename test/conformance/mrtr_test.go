package conformance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// newElicitingSDKServer serves a tool that needs input from the user before it
// can finish, which is what MRTR exists to express.
func newElicitingSDKServer(t *testing.T) string {
	t.Helper()

	server := mcp.NewServer(&mcp.Implementation{Name: "conformance", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "confirm"},
		func(ctx context.Context, req *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
			// On 2026-07-28 a handler that needs input returns an
			// InputRequests map rather than sending a request of its own.
			if in.Text != "confirmed" {
				return &mcp.CallToolResult{
					InputRequests: mcp.InputRequestMap{
						"confirm": &mcp.ElicitParams{
							Message: "Are you sure?",
							RequestedSchema: json.RawMessage(`{"type":"object","properties":{` +
								`"confirmed":{"type":"boolean","description":"confirm the action"}}}`),
						},
					},
				}, nil, nil
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "done"}},
			}, nil, nil
		})

	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL
}

// TestMRTRPassesThrough checks that a multi-round-trip exchange needs nothing
// from the proxy.
//
// Under MRTR a server that needs input does not send a request of its own; it
// answers with an InputRequiredResult, and the client answers by reissuing the
// original call. Both legs are ordinary request/response traffic, so the proxy
// carries them the way it carries anything else. This test proves that rather
// than assuming it.
//
// It is also the evidence that the proxy needs no MRTR translation of its own,
// which is not obvious -- the proxy sits between two parties that may be on
// different revisions, and translating between them is exactly its job
// elsewhere. It is unnecessary here because no reachable pairing requires it:
//
//   - modern client, modern server: the client runs the retry loop itself, as
//     below.
//   - legacy client, dual-era server: the *server* translates. The SDK's
//     serverMultiRoundTripMiddleware "transparently handles multi-round-trip
//     for clients on older protocol versions", fulfilling the input requests
//     by calling the client directly. The client never sees MRTR.
//   - legacy client, modern-only server: the connection fails at initialize,
//     so MRTR never arises. negotiateTransport reports this at startup.
//
// If that ever stops holding -- a server that drops the compatibility
// middleware while still answering initialize -- this test is where to start.
func TestMRTRPassesThrough(t *testing.T) {
	endpoint := newElicitingSDKServer(t)
	c := newClient(t, endpoint)

	resp := c.call(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{`+
		`"name":"confirm","arguments":{"text":"x"},`+modernMeta+`}}`)
	if resp.Error != nil {
		t.Fatalf("tools/call failed: %+v", resp.Error)
	}

	var result struct {
		ResultType    string          `json:"resultType"`
		InputRequests json.RawMessage `json:"inputRequests"`
		RequestState  string          `json:"requestState"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("failed to parse the result: %v", err)
	}

	if result.ResultType != "input_required" {
		t.Fatalf("resultType = %q, want input_required; the server did not use MRTR", result.ResultType)
	}

	// The input request must survive the trip intact, since answering it is
	// the client's job and the proxy has no business editing it.
	var requests map[string]struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(result.InputRequests, &requests); err != nil {
		t.Fatalf("failed to parse inputRequests: %v", err)
	}
	if got := requests["confirm"].Method; got != "elicitation/create" {
		t.Errorf("inputRequests[confirm].method = %q, want elicitation/create", got)
	}

	// Second leg: the client answers and reissues the original call. Any
	// server instance can pick it up, which is the point of MRTR. This handler
	// keeps no state between the legs, so it returns no requestState to echo;
	// the field is optional and only sent when the server needs it back.
	requestState := ""
	if result.RequestState != "" {
		encoded, err := json.Marshal(result.RequestState)
		if err != nil {
			t.Fatalf("failed to re-encode requestState: %v", err)
		}
		requestState = `,"requestState":` + string(encoded)
	}

	resumed := c.call(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{`+
		`"name":"confirm","arguments":{"text":"confirmed"},`+
		`"inputResponses":{"confirm":{"action":"accept","content":{"confirmed":true}}}`+
		requestState+`,`+modernMeta+`}}`)
	if resumed.Error != nil {
		t.Fatalf("the resumed call failed: %+v", resumed.Error)
	}

	var final struct {
		ResultType string `json:"resultType"`
		IsError    bool   `json:"isError"`
	}
	if err := json.Unmarshal(resumed.Result, &final); err != nil {
		t.Fatalf("failed to parse the resumed result: %v", err)
	}
	if final.IsError {
		t.Fatalf("the resumed call returned a tool error: %s", resumed.Result)
	}
	if final.ResultType != "complete" {
		t.Errorf("resultType = %q, want complete after the input was supplied", final.ResultType)
	}
}
