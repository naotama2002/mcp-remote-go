package conformance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/naotama2002/mcp-remote-go/proxy"
)

// echoIn and echoOut are the payloads of the trivial tool every fixture server
// exposes.
type echoIn struct {
	Text string `json:"text"`
}

type echoOut struct {
	Text string `json:"text"`
}

// newSDKServer starts an MCP server built from the official Go SDK and returns
// its endpoint. Stateless mode selects the 2026-07-28 shape (no session IDs,
// 405 on GET and DELETE); the server still answers legacy clients, matching how
// real dual-era deployments such as the GitHub MCP server behave.
func newSDKServer(t *testing.T, toolNames ...string) string {
	t.Helper()
	endpoint, _ := newRecordedSDKServer(t, toolNames...)
	return endpoint
}

// sqlIn matches the annotated schema used by newAnnotatedSDKServer.
type sqlIn struct {
	Region string `json:"region"`
	Query  string `json:"query"`
}

// newAnnotatedSDKServer serves a tool whose schema asks for one of its
// arguments to be mirrored into a header, which is the case x-mcp-header
// exists for. The SDK validates the header against the body on arrival, so a
// call only succeeds if the value was extracted and encoded correctly.
func newAnnotatedSDKServer(t *testing.T) (string, *recorder) {
	t.Helper()

	server := mcp.NewServer(&mcp.Implementation{Name: "conformance", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "execute_sql",
		Description: "Execute SQL in a region",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"region": map[string]any{
					"type":         "string",
					"description":  "The region to execute the query in",
					"x-mcp-header": "Region",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "The SQL query to execute",
				},
			},
			"required": []any{"region", "query"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in sqlIn) (*mcp.CallToolResult, echoOut, error) {
		return nil, echoOut{Text: in.Region + ":" + in.Query}, nil
	})

	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)

	rec := &recorder{}
	ts := httptest.NewServer(rec.wrap(handler))
	t.Cleanup(ts.Close)
	return ts.URL, rec
}

// recorder captures the HTTP headers of every request that reaches the server,
// so tests can assert on what the transport actually put on the wire.
type recorder struct {
	mu      sync.Mutex
	records []http.Header
}

func (r *recorder) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.records = append(r.records, req.Header.Clone())
		r.mu.Unlock()
		next.ServeHTTP(w, req)
	})
}

// lastPOST returns the headers of the most recent POST, which is the request
// carrying a JSON-RPC message.
func (r *recorder) lastPOST(t *testing.T) http.Header {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()

	for i := len(r.records) - 1; i >= 0; i-- {
		if r.records[i].Get("Content-Type") == "application/json" {
			return r.records[i]
		}
	}
	t.Fatal("no POST request was recorded")
	return nil
}

// newRecordedSDKServer is newSDKServer with header capture in front of it.
func newRecordedSDKServer(t *testing.T, toolNames ...string) (string, *recorder) {
	t.Helper()

	if len(toolNames) == 0 {
		toolNames = []string{"echo"}
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "conformance", Version: "0.1.0"}, nil)
	for _, name := range toolNames {
		mcp.AddTool(server, &mcp.Tool{Name: name},
			func(ctx context.Context, req *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
				return nil, echoOut{Text: in.Text}, nil
			})
	}

	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)

	rec := &recorder{}
	ts := httptest.NewServer(rec.wrap(handler))
	t.Cleanup(ts.Close)
	return ts.URL, rec
}

// client drives a StreamableHTTPTransport and collects the messages the server
// sends back.
type client struct {
	transport *proxy.StreamableHTTPTransport
	messages  chan []byte
}

func newClient(t *testing.T, endpoint string) *client {
	t.Helper()

	c := &client{messages: make(chan []byte, 16)}
	c.transport = proxy.NewStreamableHTTPTransport(proxy.StreamableHTTPTransportConfig{
		Endpoint: endpoint,
		Client:   &http.Client{Timeout: 10 * time.Second},
	})
	c.transport.SetOnMessage(func(event string, data []byte) {
		if event != "message" && event != "" {
			return
		}
		select {
		case c.messages <- append([]byte(nil), data...):
		default:
		}
	})
	c.transport.SetOnError(func(err error) {
		t.Logf("transport error: %v", err)
	})

	if err := c.transport.Connect(context.Background()); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	t.Cleanup(func() { _ = c.transport.Close() })

	return c
}

// rawPOST sends a message straight to the endpoint, bypassing the transport,
// so tests can assert on how the server reacts to requests our client would
// never produce.
func rawPOST(t *testing.T, endpoint, body string, headers map[string]string) jsonrpcResponse {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	// An SSE reply carries the JSON-RPC message in a `data:` line.
	payload := strings.TrimSpace(string(raw))
	for _, line := range strings.Split(payload, "\n") {
		if after, ok := strings.CutPrefix(strings.TrimSpace(line), "data:"); ok {
			payload = strings.TrimSpace(after)
			break
		}
	}

	var parsed jsonrpcResponse
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("failed to parse response %q: %v", payload, err)
	}
	return parsed
}

// send delivers a message and returns any transport-level error rather than
// failing the test, for cases where the server is expected to reject it.
func (c *client) send(message string) error {
	return c.transport.Send(context.Background(), []byte(message))
}

// jsonrpcResponse is the shape tests assert against.
type jsonrpcResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// call sends a raw JSON-RPC message and waits for the matching reply.
func (c *client) call(t *testing.T, message string) jsonrpcResponse {
	t.Helper()

	if err := c.transport.Send(context.Background(), []byte(message)); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	select {
	case data := <-c.messages:
		var resp jsonrpcResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			t.Fatalf("failed to parse response %s: %v", data, err)
		}
		return resp
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a response")
		return jsonrpcResponse{}
	}
}
