package auth

import (
	"strings"
)

// BearerChallenge is a parsed WWW-Authenticate Bearer challenge (RFC 9728 §5.1).
type BearerChallenge struct {
	ResourceMetadata string
	Realm            string
	Scope            string
	Error            string
	ErrorDescription string
}

// ParseWWWAuthenticate returns the first Bearer challenge found in a
// WWW-Authenticate header, tolerating multiple challenges in the same header.
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
	return BearerChallenge{
		ResourceMetadata: params["resource_metadata"],
		Realm:            params["realm"],
		Scope:            params["scope"],
		Error:            params["error"],
		ErrorDescription: params["error_description"],
	}, true
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
