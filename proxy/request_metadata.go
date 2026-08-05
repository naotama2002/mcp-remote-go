package proxy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
)

// Request metadata mirroring for the Streamable HTTP transport.
//
// Since MCP 2026-07-28 the transport mirrors selected JSON-RPC body fields
// into HTTP headers so intermediaries can route without parsing the body.
// Servers MUST reject a request whose headers disagree with its body, so the
// values here are always derived from the message actually being sent rather
// than from transport-level state.
//
// https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http#request-metadata
const (
	// HeaderMCPMethod mirrors the JSON-RPC `method` field. Required on all
	// requests from 2026-07-28 onwards.
	HeaderMCPMethod = "Mcp-Method"

	// HeaderMCPName mirrors `params.name` or `params.uri`. Required for
	// tools/call, prompts/get and resources/read.
	HeaderMCPName = "Mcp-Name"

	// MetaKeyProtocolVersion is the _meta key carrying the protocol version on
	// every request in the modern (per-request metadata) era.
	MetaKeyProtocolVersion = "io.modelcontextprotocol/protocolVersion"

	// ProtocolVersion20260728 is the first revision that drops the initialize
	// handshake and requires the standard request metadata headers.
	ProtocolVersion20260728 = "2026-07-28"

	// base64Prefix and base64Suffix delimit the sentinel encoding used for
	// header values that cannot be represented as plain ASCII.
	base64Prefix = "=?base64?"
	base64Suffix = "?="

	// methodToolsList and methodToolsCall are the two methods whose traffic the
	// transport has to look inside: one advertises the header bindings, the
	// other needs them applied.
	methodToolsList = "tools/list"
	methodToolsCall = "tools/call"

	// methodCancelled is the notification a client sends to abandon a request.
	// From 2026-07-28 it is a stdio-only message: on Streamable HTTP the
	// cancellation signal is closing the request's response stream.
	methodCancelled = "notifications/cancelled"
)

// requestMetadata is the subset of an outgoing JSON-RPC message that the
// Streamable HTTP transport mirrors into HTTP headers.
type requestMetadata struct {
	// method is the JSON-RPC method, empty for responses.
	method string

	// name is the value for Mcp-Name, set only for the methods that require it.
	name    string
	hasName bool

	// protocolVersion is the version declared in `params._meta`. Non-empty
	// only for modern-era messages.
	protocolVersion string

	// initializeVersion is `params.protocolVersion` on a legacy `initialize`
	// request, i.e. the version the local client is asking the server for.
	initializeVersion string

	// id identifies this request among those in flight, in the canonical form
	// produced by requestKey. Empty for notifications.
	id string

	// cancelTarget is the id named by `params.requestId` on a
	// notifications/cancelled message, in the same canonical form.
	cancelTarget string

	// params is the raw params object, kept so x-mcp-header mirroring can read
	// the call arguments without parsing the message a second time.
	params json.RawMessage
}

// requestKey renders a JSON-RPC id as a comparable key. JSON-RPC ids may be
// numbers or strings and the two are distinct, so the type is part of the key:
// otherwise cancelling id "1" would also cancel id 1.
func requestKey(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// Decode numbers as their literal text rather than through float64, which
	// holds only integers up to 2^53: past that, 9007199254740993 and
	// 9007199254740992 become the same key, and a cancellation aimed at one
	// request would close the other one's stream.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return ""
	}
	// Anything left over means the id was not a single well-formed JSON value.
	// Without this, `007` decodes as 0 and silently keys to another id.
	if decoder.More() {
		return ""
	}

	switch v := value.(type) {
	case string:
		return "s:" + v
	case json.Number:
		// Plain decimal integers are keyed by value, so -0 and 0 agree. Any
		// other numeric form -- an exponent or a fractional part -- keeps its
		// own literal, so 1e2 and 100 are treated as different ids. JSON-RPC
		// says ids should not have fractional parts, and a lookup that misses
		// is a better failure than one that hits the wrong request.
		if i, err := strconv.ParseInt(v.String(), 10, 64); err == nil {
			return "n:" + strconv.FormatInt(i, 10)
		}
		return "n:" + v.String()
	default:
		return ""
	}
}

// isModern reports whether the message declares a revision that requires the
// standard request metadata headers.
func (m requestMetadata) isModern() bool {
	return m.protocolVersion >= ProtocolVersion20260728
}

// parseRequestMetadata extracts the header-relevant fields from a JSON-RPC
// message. A message that cannot be parsed yields a zero value, which mirrors
// nothing and leaves the request untouched: forwarding it unchanged keeps the
// proxy transparent and lets the server produce the error.
func parseRequestMetadata(message []byte) requestMetadata {
	var envelope struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(message, &envelope); err != nil {
		return requestMetadata{}
	}

	md := requestMetadata{method: envelope.Method, id: requestKey(envelope.ID), params: envelope.Params}
	if len(envelope.Params) == 0 {
		return md
	}

	var params struct {
		Name            string          `json:"name"`
		URI             string          `json:"uri"`
		ProtocolVersion string          `json:"protocolVersion"`
		RequestID       json.RawMessage `json:"requestId"`
		Meta            json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(envelope.Params, &params); err != nil {
		// params is present but not an object; nothing to mirror.
		return md
	}

	md.initializeVersion = params.ProtocolVersion
	if envelope.Method == methodCancelled {
		md.cancelTarget = requestKey(params.RequestID)
	}

	// The spec fixes the source field per method rather than falling back
	// between them, so an unexpected `name` on resources/read is not mirrored.
	switch envelope.Method {
	case "tools/call", "prompts/get":
		md.name, md.hasName = params.Name, params.Name != ""
	case "resources/read":
		md.name, md.hasName = params.URI, params.URI != ""
	}

	if len(params.Meta) > 0 {
		var meta map[string]json.RawMessage
		if json.Unmarshal(params.Meta, &meta) == nil {
			if raw, ok := meta[MetaKeyProtocolVersion]; ok {
				var version string
				if json.Unmarshal(raw, &version) == nil {
					md.protocolVersion = version
				}
			}
		}
	}

	return md
}

// encodeHeaderValue renders a value for Mcp-Name using the base64 sentinel
// format when it cannot be carried as a plain ASCII header value.
func encodeHeaderValue(value string) string {
	if requiresBase64Encoding(value) {
		return base64Prefix + base64.StdEncoding.EncodeToString([]byte(value)) + base64Suffix
	}
	return value
}

// requiresBase64Encoding reports whether a value must use the sentinel
// encoding: RFC 9110 restricts field values to visible ASCII plus space and
// horizontal tab, and neither may sit at the edges of the value.
func requiresBase64Encoding(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == ' ' || s[0] == '\t' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t' {
		return true
	}
	for _, c := range s {
		if c < 0x20 || c > 0x7E {
			return true
		}
	}
	// A plain-ASCII value that looks like the sentinel is encoded too, so a
	// server can never mistake it for an already-encoded value.
	return strings.HasPrefix(s, base64Prefix) && strings.HasSuffix(s, base64Suffix)
}
