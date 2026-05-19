package auth

import (
	"strings"
)

// BearerChallenge represents a parsed WWW-Authenticate Bearer challenge,
// as used by MCP servers per RFC 9728 §5.1.
type BearerChallenge struct {
	// ResourceMetadata is the value of the "resource_metadata" parameter,
	// pointing at the Protected Resource Metadata document.
	ResourceMetadata string

	// Realm is the (optional) "realm" parameter.
	Realm string

	// Scope is the (optional) "scope" parameter.
	Scope string

	// Error and ErrorDescription are RFC 6750 error parameters, if present.
	Error            string
	ErrorDescription string

	// Params holds any other parameters present in the challenge.
	Params map[string]string
}

// ParseWWWAuthenticate parses a WWW-Authenticate header value looking for a
// Bearer challenge. It tolerates multiple challenges in the same header and
// returns the first Bearer one. If no Bearer challenge is found, the returned
// challenge is the zero value and ok is false.
//
// The parser is intentionally lenient: it accepts both quoted and unquoted
// parameter values and ignores parameters it cannot parse, rather than
// failing the whole header.
func ParseWWWAuthenticate(header string) (BearerChallenge, bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return BearerChallenge{}, false
	}

	// A WWW-Authenticate header may contain multiple challenges separated by
	// commas, but parameters are also comma-separated, which makes naive
	// splitting unsafe. We instead scan for "Bearer" tokens and parse from
	// there.
	const scheme = "bearer"
	lower := strings.ToLower(header)
	idx := strings.Index(lower, scheme)
	if idx < 0 {
		return BearerChallenge{}, false
	}
	// Require the match to be at a token boundary so we don't pick up e.g.
	// "MyBearer".
	if idx > 0 {
		prev := header[idx-1]
		if prev != ' ' && prev != ',' && prev != '\t' {
			return BearerChallenge{}, false
		}
	}
	rest := header[idx+len(scheme):]
	// The scheme name must be followed by whitespace or end-of-string.
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return BearerChallenge{}, false
	}
	rest = strings.TrimSpace(rest)

	params := parseAuthParams(rest)
	if len(params) == 0 {
		// A bare "Bearer" challenge is valid but uninteresting.
		return BearerChallenge{Params: params}, true
	}

	ch := BearerChallenge{Params: params}
	ch.ResourceMetadata = params["resource_metadata"]
	ch.Realm = params["realm"]
	ch.Scope = params["scope"]
	ch.Error = params["error"]
	ch.ErrorDescription = params["error_description"]
	return ch, true
}

// parseAuthParams parses a comma-separated list of key=value or key="value"
// pairs. Values containing commas must be quoted. Unrecognised tokens
// (e.g. another auth-scheme starting after a comma) terminate parsing.
func parseAuthParams(s string) map[string]string {
	params := make(map[string]string)
	i := 0
	for i < len(s) {
		// Skip leading whitespace and commas.
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == ',') {
			i++
		}
		if i >= len(s) {
			break
		}

		// Read key up to '='.
		keyStart := i
		for i < len(s) && s[i] != '=' && s[i] != ',' && s[i] != ' ' && s[i] != '\t' {
			i++
		}
		key := strings.ToLower(s[keyStart:i])
		if key == "" {
			return params
		}
		// Skip whitespace.
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) || s[i] != '=' {
			// A token without '=' likely marks the start of a new
			// challenge (e.g. "Basic realm=..."); stop parsing.
			return params
		}
		i++ // consume '='
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}

		// Read value: quoted or unquoted.
		var value string
		if i < len(s) && s[i] == '"' {
			i++ // consume opening quote
			valStart := i
			var b strings.Builder
			for i < len(s) {
				c := s[i]
				if c == '\\' && i+1 < len(s) {
					b.WriteByte(s[i+1])
					i += 2
					continue
				}
				if c == '"' {
					break
				}
				b.WriteByte(c)
				i++
			}
			_ = valStart
			value = b.String()
			if i < len(s) && s[i] == '"' {
				i++ // consume closing quote
			}
		} else {
			valStart := i
			for i < len(s) && s[i] != ',' && s[i] != ' ' && s[i] != '\t' {
				i++
			}
			value = s[valStart:i]
		}

		params[key] = value
	}
	return params
}
