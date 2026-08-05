package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
)

// Era detection.
//
// 2026-07-28 removed the initialize handshake, so there is no longer a moment
// where the two sides announce themselves to each other:
//
//	There is no negotiation handshake. Every request carries its protocol
//	version, and the server accepts or rejects each request independently.
//
// A client that must interoperate with both eras therefore attempts a modern
// request and reads the failure:
//
//	On 400 Bad Request, the client SHOULD inspect the response body before
//	falling back: modern servers also use 400 for
//	UnsupportedProtocolVersionError, MissingRequiredClientCapabilityError, and
//	header-validation failures.
//
// https://modelcontextprotocol.io/specification/2026-07-28/basic/versioning#backward-compatibility-with-initialization-based-versions

// serverEra records which protocol generation the remote server implements.
type serverEra int

const (
	// eraUnknown means the probe could not tell, so nothing may be assumed.
	eraUnknown serverEra = iota

	// eraLegacy servers establish a session with an initialize handshake
	// (2025-11-25 and earlier).
	eraLegacy

	// eraModern servers carry version, identity and capabilities as
	// per-request metadata (2026-07-28 and later).
	eraModern
)

func (e serverEra) String() string {
	switch e {
	case eraLegacy:
		return "legacy"
	case eraModern:
		return "modern"
	default:
		return "unknown"
	}
}

// Modern JSON-RPC error codes. Their presence in a 4xx body is what identifies
// a modern server: the status alone cannot, because legacy servers use the
// same statuses for entirely different reasons.
const (
	// codeHeaderMismatch means the headers and body disagreed, or a required
	// header was missing or malformed.
	codeHeaderMismatch = -32020

	// codeMissingRequiredClientCapabilities means the server needs a
	// capability the request did not declare.
	codeMissingRequiredClientCapabilities = -32021

	// codeUnsupportedProtocolVersion carries the server's supported versions,
	// so this error advances the negotiation rather than ending it.
	codeUnsupportedProtocolVersion = -32022
)

// modernErrorCodes are the codes that identify the responder as modern.
// -32601 (method not found) is deliberately absent: a modern server MUST
// implement server/discover, so a server that does not recognise it is telling
// us it is legacy.
var modernErrorCodes = []int{
	codeHeaderMismatch,
	codeMissingRequiredClientCapabilities,
	codeUnsupportedProtocolVersion,
}

// serverProfile is what the negotiation probe learned about the remote.
type serverProfile struct {
	transport TransportMode
	era       serverEra

	// supportedVersions is the server's advertised version list, from either a
	// DiscoverResult or an UnsupportedProtocolVersionError. Empty when the
	// server did not say.
	supportedVersions []string
}

// supportsLegacy reports whether the server still answers the initialize
// handshake. A dual-era server lists legacy revisions alongside modern ones;
// a modern-only server does not, and a legacy client will never reach it.
//
// An empty version list is not evidence either way, so it reports true: the
// proxy should not refuse traffic on a guess.
func (p serverProfile) supportsLegacy() bool {
	if p.era != eraModern || len(p.supportedVersions) == 0 {
		return true
	}
	for _, v := range p.supportedVersions {
		if v < ProtocolVersion20260728 {
			return true
		}
	}
	return false
}

// probeBody is the request the era probe sends. server/discover is the natural
// probe: modern servers MUST implement it, and its result names every version
// the server speaks, so one round trip settles both the era and the fallback
// options.
func probeBody() string {
	return `{"jsonrpc":"2.0","id":0,"method":"server/discover","params":{"_meta":{` +
		`"io.modelcontextprotocol/protocolVersion":"` + ProtocolVersion20260728 + `",` +
		`"io.modelcontextprotocol/clientInfo":{"name":"mcp-remote-go","version":"1"},` +
		`"io.modelcontextprotocol/clientCapabilities":{}}}}`
}

// probeResponse is the parsed shape of a probe reply.
type probeResponse struct {
	JSONRPC string `json:"jsonrpc"`
	Result  *struct {
		SupportedVersions []string `json:"supportedVersions"`
	} `json:"result"`
	Error *struct {
		Code int `json:"code"`
		Data *struct {
			Supported []string `json:"supported"`
		} `json:"data"`
	} `json:"error"`
}

// probePayload returns the JSON-RPC message carried by a probe reply.
//
// A server answers a request with either a single JSON object or an SSE
// stream, its choice, and the classification needs the message either way.
// Reading the raw body as JSON works only for the first, and silently yields
// "era unknown" for the second -- which is what real servers send.
func probePayload(contentType string, body []byte) []byte {
	if !strings.HasPrefix(contentType, "text/event-stream") {
		return body
	}

	var payload []byte
	_ = ReadSSEEvents(context.Background(), bytes.NewReader(body), func(evt SSEEvent) {
		if payload == nil {
			payload = evt.Data
		}
	})
	if payload == nil {
		return body
	}
	return payload
}

// classifyProbe reads a probe reply and reports what it proves about the
// server. isJSONRPC distinguishes a server that speaks the protocol from one
// that merely returned a status code.
func classifyProbe(body []byte) (era serverEra, supported []string, isJSONRPC bool) {
	var parsed probeResponse
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.JSONRPC == "" {
		return eraUnknown, nil, false
	}

	switch {
	case parsed.Result != nil:
		// Only a modern server answers server/discover with a result.
		return eraModern, parsed.Result.SupportedVersions, true

	case parsed.Error != nil && slices.Contains(modernErrorCodes, parsed.Error.Code):
		if parsed.Error.Code == codeUnsupportedProtocolVersion && parsed.Error.Data != nil {
			supported = parsed.Error.Data.Supported
		}
		return eraModern, supported, true

	default:
		// A JSON-RPC error that is not one of the modern codes: the server
		// speaks the protocol over this endpoint but not this revision.
		return eraLegacy, nil, true
	}
}
