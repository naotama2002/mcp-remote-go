package auth

import (
	"fmt"
	"net/url"
	"strings"
)

// CanonicalResourceURI returns the canonical URI of an MCP server as defined by
// the MCP authorization specification and RFC 8707.
//
// Rules:
//   - Scheme and host are lowercased.
//   - Fragment is rejected (invalid per the MCP spec).
//   - Scheme and host must be present.
//   - A trailing slash on the path is removed unless the path is exactly "/".
//   - Query string is preserved verbatim (RFC 8707 does not forbid it, though
//     servers typically use path-only identifiers).
func CanonicalResourceURI(serverURL string) (string, error) {
	if serverURL == "" {
		return "", fmt.Errorf("server URL is empty")
	}

	parsed, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("invalid server URL: %w", err)
	}

	if parsed.Scheme == "" {
		return "", fmt.Errorf("server URL missing scheme: %s", serverURL)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("server URL missing host: %s", serverURL)
	}
	if parsed.Fragment != "" || strings.Contains(serverURL, "#") {
		return "", fmt.Errorf("server URL must not contain a fragment: %s", serverURL)
	}

	canonical := &url.URL{
		Scheme:   strings.ToLower(parsed.Scheme),
		Host:     strings.ToLower(parsed.Host),
		Path:     parsed.Path,
		RawQuery: parsed.RawQuery,
	}

	// Strip trailing slash unless the path is just "/".
	if len(canonical.Path) > 1 && strings.HasSuffix(canonical.Path, "/") {
		canonical.Path = strings.TrimRight(canonical.Path, "/")
	}
	// "/" alone carries no information; drop it for consistency.
	if canonical.Path == "/" {
		canonical.Path = ""
	}

	return canonical.String(), nil
}
