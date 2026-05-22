package auth

import (
	"fmt"
	"net/url"
	"strings"
)

// canonicalResourceURL returns the canonical *url.URL of an MCP server per the
// MCP authorization specification and RFC 8707 (lowercased scheme/host, no
// fragment, no trailing slash on the path, query preserved).
func canonicalResourceURL(serverURL string) (*url.URL, error) {
	if serverURL == "" {
		return nil, fmt.Errorf("server URL is empty")
	}

	parsed, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("invalid server URL: %w", err)
	}

	if parsed.Scheme == "" {
		return nil, fmt.Errorf("server URL missing scheme: %s", serverURL)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("server URL missing host: %s", serverURL)
	}
	if parsed.Fragment != "" || strings.Contains(serverURL, "#") {
		return nil, fmt.Errorf("server URL must not contain a fragment: %s", serverURL)
	}

	canonical := &url.URL{
		Scheme:   strings.ToLower(parsed.Scheme),
		Host:     strings.ToLower(parsed.Host),
		Path:     parsed.Path,
		RawQuery: parsed.RawQuery,
	}
	if len(canonical.Path) > 1 && strings.HasSuffix(canonical.Path, "/") {
		canonical.Path = strings.TrimRight(canonical.Path, "/")
	}
	if canonical.Path == "/" {
		canonical.Path = ""
	}
	return canonical, nil
}

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
	u, err := canonicalResourceURL(serverURL)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

const protectedResourceWellKnownSuffix = "/.well-known/oauth-protected-resource"

// ProtectedResourceWellKnownURL builds the RFC 9728 §3.1 metadata URL for a
// resource identifier (the MCP server URL).
func ProtectedResourceWellKnownURL(serverURL string) (string, error) {
	u, err := canonicalResourceURL(serverURL)
	if err != nil {
		return "", err
	}

	out := u.Scheme + "://" + u.Host + protectedResourceWellKnownSuffix + u.Path
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out, nil
}

// ValidatePRMResource ensures PRM resource matches the MCP server per RFC 9728 §3.3.
func ValidatePRMResource(prmResource, serverURL string) error {
	if strings.TrimSpace(prmResource) == "" {
		return fmt.Errorf("protected resource metadata missing required resource field")
	}

	expected, err := CanonicalResourceURI(serverURL)
	if err != nil {
		return fmt.Errorf("invalid server URL for PRM validation: %w", err)
	}

	// Common fast path: the server already advertised the canonical form.
	if prmResource == expected {
		return nil
	}

	actual, err := CanonicalResourceURI(prmResource)
	if err != nil {
		return fmt.Errorf("protected resource metadata resource %q does not match MCP server %q", prmResource, expected)
	}
	if actual != expected {
		return fmt.Errorf("protected resource metadata resource %q does not match MCP server %q", actual, expected)
	}
	return nil
}
