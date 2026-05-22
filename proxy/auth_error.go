package proxy

import (
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/naotama2002/mcp-remote-go/auth"
)

// HeaderWWWAuthenticate is the response header name carrying the Bearer
// challenge that points at the Protected Resource Metadata document
// (RFC 9728 §5.1).
const HeaderWWWAuthenticate = "WWW-Authenticate"

// UnauthorizedError signals an HTTP 401 from the MCP server and carries the
// WWW-Authenticate header so callers can locate the Protected Resource
// Metadata document per RFC 9728 §5.1.
type UnauthorizedError struct {
	StatusCode      int
	WWWAuthenticate string
}

func (e *UnauthorizedError) Error() string {
	if e.WWWAuthenticate != "" {
		return fmt.Sprintf("server returned %d Unauthorized (WWW-Authenticate: %s)", e.StatusCode, e.WWWAuthenticate)
	}
	return fmt.Sprintf("server returned %d Unauthorized", e.StatusCode)
}

// unauthorizedFromResponse drains and closes the response body, then returns
// an UnauthorizedError carrying the WWW-Authenticate header.
func unauthorizedFromResponse(resp *http.Response) *UnauthorizedError {
	_, _ = io.Copy(io.Discard, resp.Body)
	if err := resp.Body.Close(); err != nil {
		log.Printf("Warning: failed to close response body: %v", err)
	}
	return &UnauthorizedError{
		StatusCode:      resp.StatusCode,
		WWWAuthenticate: auth.BestWWWAuthenticateHeader(resp.Header.Values(HeaderWWWAuthenticate)),
	}
}
