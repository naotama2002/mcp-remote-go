package proxy

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
)

// x-mcp-header support.
//
// A server may ask for particular tool arguments to be mirrored into HTTP
// headers, so a gateway can route on them without reading the body:
//
//	While the use of x-mcp-header is optional for servers, clients MUST
//	support this feature. When a server's tool definition includes
//	x-mcp-header annotations, conforming clients MUST mirror the designated
//	parameter values into HTTP headers.
//
// This is the one place the proxy cannot stay a pass-through. Mirroring needs
// the tool's inputSchema, which arrives in a tools/list response long before
// the tools/call that needs it, so the schemas have to be remembered. And a
// tool whose annotations are malformed MUST be withheld from the client
// entirely, which means editing the response on its way past.
//
// https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http#custom-headers-from-tool-parameters

// paramHeaderPrefix is prepended to the name given in an x-mcp-header value.
const paramHeaderPrefix = "Mcp-Param-"

// safe integer bounds. The spec restricts annotated integers to the range that
// survives a round trip through an IEEE-754 double, because JSON numbers do.
const (
	maxSafeInteger = 1<<53 - 1
	minSafeInteger = -(1<<53 - 1)
)

// paramBinding ties a property inside a tool's inputSchema to the header that
// carries it. Path is the chain of property names from the schema root.
type paramBinding struct {
	Path   []string
	Header string
}

// toolHeaderRegistry remembers the header bindings of the tools a server has
// advertised, so a later tools/call can be decorated with them.
type toolHeaderRegistry struct {
	mu       sync.RWMutex
	bindings map[string][]paramBinding
}

func newToolHeaderRegistry() *toolHeaderRegistry {
	return &toolHeaderRegistry{bindings: make(map[string][]paramBinding)}
}

func (r *toolHeaderRegistry) set(tool string, bindings []paramBinding) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(bindings) == 0 {
		delete(r.bindings, tool)
		return
	}
	r.bindings[tool] = bindings
}

func (r *toolHeaderRegistry) get(tool string) []paramBinding {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.bindings[tool]
}

// isHeaderNameToken reports whether s is a valid HTTP field name, which is
// what an x-mcp-header value has to become. RFC 9110 §5.1 defines it as
// 1*tchar; anything outside that set -- CR and LF above all -- would let a
// tool definition inject headers of its own.
func isHeaderNameToken(s string) bool {
	if s == "" {
		return false
	}
	const tcharSymbols = "!#$%&'*+-.^_`|~"
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.ContainsRune(tcharSymbols, c):
		default:
			return false
		}
	}
	return true
}

// schemaAnnotations extracts the x-mcp-header bindings of one inputSchema.
//
// It returns an error when the tool definition must be rejected. Two things
// make it invalid: an annotation that breaks the rules for its own value, and
// an annotation sitting anywhere the spec does not allow one. The second is
// why the whole schema is walked rather than only its reachable part -- an
// annotation under `items`, a composition keyword or a `$ref` invalidates the
// definition instead of being quietly ignored.
func schemaAnnotations(schema map[string]any) ([]paramBinding, error) {
	var bindings []paramBinding
	seen := make(map[string]string)
	if err := walkSchema(schema, nil, true, &bindings, seen); err != nil {
		return nil, err
	}
	return bindings, nil
}

func walkSchema(node map[string]any, path []string, reachable bool, out *[]paramBinding, seen map[string]string) error {
	if raw, ok := node["x-mcp-header"]; ok {
		where := "the schema root"
		if len(path) > 0 {
			where = strconv.Quote(strings.Join(path, "."))
		}

		if !reachable {
			return fmt.Errorf("x-mcp-header at %s is not statically reachable through properties", where)
		}

		name, ok := raw.(string)
		if !ok || name == "" {
			return fmt.Errorf("x-mcp-header at %s must be a non-empty string", where)
		}
		if !isHeaderNameToken(name) {
			return fmt.Errorf("x-mcp-header %q at %s is not a valid HTTP field name", name, where)
		}
		if prev, dup := seen[strings.ToLower(name)]; dup {
			return fmt.Errorf("x-mcp-header %q at %s duplicates the one at %s", name, where, prev)
		}
		seen[strings.ToLower(name)] = where

		// `number` is excluded on purpose: only types that survive the trip
		// through a header value unchanged are allowed.
		switch node["type"] {
		case "string", "integer", "boolean":
		default:
			return fmt.Errorf("x-mcp-header at %s is on type %v, want string, integer or boolean", where, node["type"])
		}

		*out = append(*out, paramBinding{Path: append([]string(nil), path...), Header: name})
	}

	for key, value := range node {
		if key == "properties" {
			props, ok := value.(map[string]any)
			if !ok {
				continue
			}
			for propName, propSchema := range props {
				child, ok := propSchema.(map[string]any)
				if !ok {
					continue
				}
				// Descending through `properties` is the only step that keeps
				// a property statically reachable from the root.
				if err := walkSchema(child, append(path, propName), reachable, out, seen); err != nil {
					return err
				}
			}
			continue
		}

		// Everything else -- items, oneOf, $defs, if/then/else -- is still
		// searched, but anything found there is out of bounds.
		if err := walkAny(value, path, out, seen); err != nil {
			return err
		}
	}

	return nil
}

// walkAny searches a non-properties branch of the schema for annotations that
// should not be there.
func walkAny(value any, path []string, out *[]paramBinding, seen map[string]string) error {
	switch v := value.(type) {
	case map[string]any:
		return walkSchema(v, path, false, out, seen)
	case []any:
		for _, item := range v {
			if err := walkAny(item, path, out, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

// lookupArgument follows a binding's property path through the call arguments
// and returns the value found there.
func lookupArgument(args map[string]any, path []string) (any, bool) {
	if len(path) == 0 {
		return nil, false
	}
	var current any = args
	for _, step := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[step]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// primitiveToHeaderValue renders an argument for a header. It reports false
// for values the annotation rules do not permit, which are skipped rather than
// sent: a header the server cannot match against the body would be rejected.
func primitiveToHeaderValue(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	case bool:
		return strconv.FormatBool(v), true
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) {
			return "", false
		}
		if v < minSafeInteger || v > maxSafeInteger {
			return "", false
		}
		return strconv.FormatInt(int64(v), 10), true
	default:
		return "", false
	}
}

// paramHeaders builds the Mcp-Param-* headers for a tools/call, given the
// bindings of the tool being called and the raw params of the message.
func paramHeaders(bindings []paramBinding, params json.RawMessage) map[string]string {
	if len(bindings) == 0 || len(params) == 0 {
		return nil
	}

	var parsed struct {
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &parsed); err != nil || parsed.Arguments == nil {
		return nil
	}

	headers := make(map[string]string)
	for _, binding := range bindings {
		value, ok := lookupArgument(parsed.Arguments, binding.Path)
		if !ok || value == nil {
			// An absent or null argument carries no header, and the server
			// is required not to expect one.
			continue
		}
		rendered, ok := primitiveToHeaderValue(value)
		if !ok {
			continue
		}
		headers[paramHeaderPrefix+binding.Header] = encodeHeaderValue(rendered)
	}

	if len(headers) == 0 {
		return nil
	}
	return headers
}

// filterToolsList records the header bindings of each tool in a tools/list
// result and removes the tools whose annotations are invalid.
//
//	Rejection means the client MUST exclude the invalid tool from the result
//	of tools/list. Clients SHOULD log a warning when rejecting a tool
//	definition, including the tool name and the reason for rejection. This
//	ensures that a single malformed tool definition does not prevent other
//	valid tools from being used.
//
// It returns the message to forward, which is the original bytes when nothing
// had to be removed.
func filterToolsList(registry *toolHeaderRegistry, message []byte) []byte {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(message, &envelope); err != nil {
		return message
	}

	rawResult, ok := envelope["result"]
	if !ok {
		return message
	}

	var result map[string]json.RawMessage
	if err := json.Unmarshal(rawResult, &result); err != nil {
		return message
	}

	var tools []json.RawMessage
	if err := json.Unmarshal(result["tools"], &tools); err != nil {
		return message
	}

	kept := make([]json.RawMessage, 0, len(tools))
	rejected := 0

	for _, rawTool := range tools {
		var tool struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		}
		if err := json.Unmarshal(rawTool, &tool); err != nil {
			// Not something we can reason about; leave it for the client.
			kept = append(kept, rawTool)
			continue
		}

		bindings, err := schemaAnnotations(tool.InputSchema)
		if err != nil {
			log.Printf("Excluding tool %q from tools/list: %v", tool.Name, err)
			registry.set(tool.Name, nil)
			rejected++
			continue
		}

		registry.set(tool.Name, bindings)
		kept = append(kept, rawTool)
	}

	if rejected == 0 {
		return message
	}

	encodedTools, err := json.Marshal(kept)
	if err != nil {
		return message
	}
	result["tools"] = encodedTools

	encodedResult, err := json.Marshal(result)
	if err != nil {
		return message
	}
	envelope["result"] = encodedResult

	rewritten, err := json.Marshal(envelope)
	if err != nil {
		return message
	}
	return rewritten
}
