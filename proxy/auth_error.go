package proxy

import "fmt"

// UnauthorizedError signals that the remote MCP server returned an HTTP 401
// Unauthorized response. It carries the raw WWW-Authenticate header so the
// caller can extract the Protected Resource Metadata URL per RFC 9728 §5.1,
// as required by the MCP authorization specification:
//
//	"MCP clients MUST be able to parse WWW-Authenticate headers and respond
//	 appropriately to HTTP 401 Unauthorized responses from the MCP server."
type UnauthorizedError struct {
	StatusCode      int
	WWWAuthenticate string
	Body            string
}

func (e *UnauthorizedError) Error() string {
	if e == nil {
		return "unauthorized"
	}
	if e.WWWAuthenticate != "" {
		return fmt.Sprintf("server returned %d Unauthorized (WWW-Authenticate: %s)", e.StatusCode, e.WWWAuthenticate)
	}
	return fmt.Sprintf("server returned %d Unauthorized", e.StatusCode)
}
