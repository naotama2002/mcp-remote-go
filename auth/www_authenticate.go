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
	// splitting unsafe. Scan all "bearer" occurrences and accept the first
	// one on a proper token boundary, so a non-boundary substring (e.g.
	// "MyBearer") does not mask a real Bearer challenge later in the header.
	const scheme = "bearer"
	lower := strings.ToLower(header)

	for offset := 0; offset < len(lower); {
		idx := strings.Index(lower[offset:], scheme)
		if idx < 0 {
			return BearerChallenge{}, false
		}
		pos := offset + idx
		offset = pos + len(scheme)

		if pos > 0 {
			prev := header[pos-1]
			if prev != ' ' && prev != ',' && prev != '\t' {
				continue
			}
		}
		rest := header[pos+len(scheme):]
		if rest != "" && rest[0] != ' ' && rest[0] != '\t' && rest[0] != ',' {
			continue
		}

		params := parseAuthParams(strings.TrimSpace(rest))
		return BearerChallenge{
			ResourceMetadata: params["resource_metadata"],
			Realm:            params["realm"],
			Scope:            params["scope"],
			Error:            params["error"],
			ErrorDescription: params["error_description"],
		}, true
	}
	return BearerChallenge{}, false
}

// BestWWWAuthenticateHeader selects the most useful WWW-Authenticate field value
// when a response carries multiple header lines (RFC 9110). Preference order:
// Bearer challenge with resource_metadata, any Bearer challenge, then the first
// non-empty line.
func BestWWWAuthenticateHeader(headers []string) string {
	var bearerFallback string
	for _, h := range headers {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		challenge, ok := ParseWWWAuthenticate(h)
		if !ok {
			continue
		}
		if challenge.ResourceMetadata != "" {
			return h
		}
		if bearerFallback == "" {
			bearerFallback = h
		}
	}
	return bearerFallback
}

// ParseWWWAuthenticateHeaders parses Bearer challenges across multiple
// WWW-Authenticate header field values.
func ParseWWWAuthenticateHeaders(headers []string) (BearerChallenge, bool) {
	h := BestWWWAuthenticateHeader(headers)
	if h == "" {
		return BearerChallenge{}, false
	}
	return ParseWWWAuthenticate(h)
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
