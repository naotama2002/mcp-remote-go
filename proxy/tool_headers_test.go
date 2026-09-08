package proxy

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// schemaOf parses a JSON schema literal for the table tests.
func schemaOf(t *testing.T, raw string) map[string]any {
	t.Helper()
	var schema map[string]any
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		t.Fatalf("bad test schema: %v", err)
	}
	return schema
}

func TestSchemaAnnotationsAccepted(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		want   []paramBinding
	}{
		{
			name:   "no annotations",
			schema: `{"type":"object","properties":{"query":{"type":"string"}}}`,
		},
		{
			name:   "a string parameter",
			schema: `{"type":"object","properties":{"region":{"type":"string","x-mcp-header":"Region"}}}`,
			want:   []paramBinding{{Path: []string{"region"}, Header: "Region"}},
		},
		{
			name:   "integer and boolean are permitted",
			schema: `{"type":"object","properties":{"n":{"type":"integer","x-mcp-header":"N"},"flag":{"type":"boolean","x-mcp-header":"Flag"}}}`,
			want: []paramBinding{
				{Path: []string{"flag"}, Header: "Flag"},
				{Path: []string{"n"}, Header: "N"},
			},
		},
		{
			name:   "nested properties stay reachable",
			schema: `{"type":"object","properties":{"outer":{"type":"object","properties":{"inner":{"type":"string","x-mcp-header":"Inner"}}}}}`,
			want:   []paramBinding{{Path: []string{"outer", "inner"}, Header: "Inner"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := schemaAnnotations(schemaOf(t, tt.schema))
			if err != nil {
				t.Fatalf("unexpected rejection: %v", err)
			}

			// Map iteration makes sibling order arbitrary; compare as sets.
			sortBindings(got)
			sortBindings(tt.want)
			if len(got) != len(tt.want) || (len(got) > 0 && !reflect.DeepEqual(got, tt.want)) {
				t.Errorf("bindings = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSchemaAnnotationsRejected(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		wantErr string
	}{
		{
			name:    "number is not a permitted type",
			schema:  `{"type":"object","properties":{"x":{"type":"number","x-mcp-header":"X"}}}`,
			wantErr: "want string, integer or boolean",
		},
		{
			name:    "object is not a permitted type",
			schema:  `{"type":"object","properties":{"x":{"type":"object","x-mcp-header":"X"}}}`,
			wantErr: "want string, integer or boolean",
		},
		{
			name:    "an empty header name",
			schema:  `{"type":"object","properties":{"x":{"type":"string","x-mcp-header":""}}}`,
			wantErr: "must be a non-empty string",
		},
		{
			name:    "a non-string header name",
			schema:  `{"type":"object","properties":{"x":{"type":"string","x-mcp-header":42}}}`,
			wantErr: "must be a non-empty string",
		},
		{
			name:    "a header name with a space",
			schema:  `{"type":"object","properties":{"x":{"type":"string","x-mcp-header":"My Header"}}}`,
			wantErr: "not a valid HTTP field name",
		},
		{
			name:    "a header name carrying CRLF",
			schema:  `{"type":"object","properties":{"x":{"type":"string","x-mcp-header":"X\r\nInjected: 1"}}}`,
			wantErr: "not a valid HTTP field name",
		},
		{
			name:    "names colliding case-insensitively",
			schema:  `{"type":"object","properties":{"a":{"type":"string","x-mcp-header":"Region"},"b":{"type":"string","x-mcp-header":"region"}}}`,
			wantErr: "duplicates",
		},
		{
			name:    "an annotation under items",
			schema:  `{"type":"object","properties":{"list":{"type":"array","items":{"type":"string","x-mcp-header":"Item"}}}}`,
			wantErr: "not statically reachable",
		},
		{
			name:    "an annotation under anyOf",
			schema:  `{"type":"object","properties":{"x":{"anyOf":[{"type":"string","x-mcp-header":"X"}]}}}`,
			wantErr: "not statically reachable",
		},
		{
			name:    "an annotation under if/then",
			schema:  `{"type":"object","if":{"type":"string","x-mcp-header":"X"}}`,
			wantErr: "not statically reachable",
		},
		{
			name:    "an annotation under $defs",
			schema:  `{"type":"object","$defs":{"shared":{"type":"string","x-mcp-header":"X"}}}`,
			wantErr: "not statically reachable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := schemaAnnotations(schemaOf(t, tt.schema))
			if err == nil {
				t.Fatalf("expected the tool definition to be rejected")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestIsHeaderNameToken(t *testing.T) {
	valid := []string{"Region", "X-Tenant", "a", "A1", "x_y", "a.b", "a~b", "!#$%&'*+-.^_`|~"}
	invalid := []string{"", "a b", "a:b", "a\nb", "a\rb", "a\tb", "a/b", "a(b)", "日本語", "a@b"}

	for _, s := range valid {
		if !isHeaderNameToken(s) {
			t.Errorf("isHeaderNameToken(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if isHeaderNameToken(s) {
			t.Errorf("isHeaderNameToken(%q) = true, want false", s)
		}
	}
}

func TestParamHeaders(t *testing.T) {
	bindings := []paramBinding{
		{Path: []string{"region"}, Header: "Region"},
		{Path: []string{"count"}, Header: "Count"},
		{Path: []string{"dry"}, Header: "Dry"},
		{Path: []string{"outer", "inner"}, Header: "Inner"},
	}

	tests := []struct {
		name   string
		params string
		want   map[string]string
	}{
		{
			name:   "a string value",
			params: `{"name":"t","arguments":{"region":"us-west1"}}`,
			want:   map[string]string{"Mcp-Param-Region": "us-west1"},
		},
		{
			name:   "an integer renders without a decimal point",
			params: `{"name":"t","arguments":{"count":42}}`,
			want:   map[string]string{"Mcp-Param-Count": "42"},
		},
		{
			name:   "a boolean renders lowercase",
			params: `{"name":"t","arguments":{"dry":true}}`,
			want:   map[string]string{"Mcp-Param-Dry": "true"},
		},
		{
			name:   "a nested value is found at its exact path",
			params: `{"name":"t","arguments":{"outer":{"inner":"v"}}}`,
			want:   map[string]string{"Mcp-Param-Inner": "v"},
		},
		{
			name:   "a null argument carries no header",
			params: `{"name":"t","arguments":{"region":null}}`,
			want:   nil,
		},
		{
			name:   "an absent argument carries no header",
			params: `{"name":"t","arguments":{}}`,
			want:   nil,
		},
		{
			name:   "a fractional number is not sent",
			params: `{"name":"t","arguments":{"count":1.5}}`,
			want:   nil,
		},
		{
			name:   "an integer beyond the safe range is not sent",
			params: `{"name":"t","arguments":{"count":9007199254740993}}`,
			want:   nil,
		},
		{
			name:   "a non-ascii value uses the sentinel encoding",
			params: `{"name":"t","arguments":{"region":"東京"}}`,
			want:   map[string]string{"Mcp-Param-Region": "=?base64?5p2x5Lqs?="},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := paramHeaders(bindings, json.RawMessage(tt.params))
			if len(got) != len(tt.want) {
				t.Fatalf("headers = %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("headers[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}

	// A tool with no bindings must not be decorated at all.
	if got := paramHeaders(nil, json.RawMessage(`{"arguments":{"region":"x"}}`)); got != nil {
		t.Errorf("headers = %v, want nil for a tool with no annotations", got)
	}
}

func TestFilterToolsList(t *testing.T) {
	registry := newToolHeaderRegistry()

	message := []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[
		{"name":"good","inputSchema":{"type":"object","properties":{"region":{"type":"string","x-mcp-header":"Region"}}}},
		{"name":"bad","inputSchema":{"type":"object","properties":{"n":{"type":"number","x-mcp-header":"N"}}}},
		{"name":"plain","inputSchema":{"type":"object","properties":{"q":{"type":"string"}}}}
	]}}`)

	filtered := filterToolsList(registry, message)

	var parsed struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(filtered, &parsed); err != nil {
		t.Fatalf("failed to parse the filtered result: %v", err)
	}

	var names []string
	for _, tool := range parsed.Result.Tools {
		names = append(names, tool.Name)
	}
	want := []string{"good", "plain"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("tools = %v, want %v: the invalid one must be excluded and the others kept", names, want)
	}

	if got := registry.get("good"); len(got) != 1 || got[0].Header != "Region" {
		t.Errorf("registry for %q = %+v, want the Region binding", "good", got)
	}
	if got := registry.get("bad"); got != nil {
		t.Errorf("registry for a rejected tool = %+v, want nothing", got)
	}
	if got := registry.get("plain"); got != nil {
		t.Errorf("registry for an unannotated tool = %+v, want nothing", got)
	}
}

// TestFilterToolsListLeavesValidResponsesAlone checks the proxy stays a
// pass-through when it has nothing to remove: rewriting every response would
// churn key order and formatting for no reason.
func TestFilterToolsListLeavesValidResponsesAlone(t *testing.T) {
	registry := newToolHeaderRegistry()

	for _, message := range []string{
		`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"a","inputSchema":{"type":"object"}}]}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"boom"}}`,
		`{"jsonrpc":"2.0","id":1,"result":{}}`,
		`not json at all`,
	} {
		if got := filterToolsList(registry, []byte(message)); string(got) != message {
			t.Errorf("filterToolsList(%s) rewrote the message to %s", message, got)
		}
	}
}

func sortBindings(bindings []paramBinding) {
	for i := 1; i < len(bindings); i++ {
		for j := i; j > 0 && bindings[j].Header < bindings[j-1].Header; j-- {
			bindings[j], bindings[j-1] = bindings[j-1], bindings[j]
		}
	}
}
