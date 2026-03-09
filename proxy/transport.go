package proxy

import "context"

// Transport defines the interface for MCP transport implementations.
// Both the legacy SSE transport and the new Streamable HTTP transport
// implement this interface.
type Transport interface {
	// Connect establishes the connection to the remote MCP server.
	Connect(ctx context.Context) error

	// Send sends a JSON-RPC message to the remote server.
	Send(ctx context.Context, message []byte) error

	// SetOnMessage sets the callback for messages received from the server.
	SetOnMessage(handler func(event string, data []byte))

	// SetOnError sets the callback for transport-level errors.
	SetOnError(handler func(err error))

	// Close terminates the transport connection.
	Close() error

	// SessionID returns the current session ID, if any.
	SessionID() string
}
