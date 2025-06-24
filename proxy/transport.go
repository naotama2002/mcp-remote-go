package proxy

import (
	"context"
)

// Transport defines the interface for different MCP transport mechanisms
type Transport interface {
	// Connect establishes the connection to the remote server
	Connect(ctx context.Context) error

	// SendMessage sends a message to the remote server
	SendMessage(message []byte) error

	// Close closes the connection
	Close() error

	// SetMessageHandler sets the callback for receiving messages from the server
	SetMessageHandler(handler func(data []byte))

	// SetErrorHandler sets the callback for handling connection errors
	SetErrorHandler(handler func(err error))

	// IsConnected returns true if the transport is currently connected
	IsConnected() bool
}

// TransportType represents the type of transport
type TransportType string

const (
	// SSETransportType represents Server-Sent Events transport
	SSETransportType TransportType = "sse"
	// StreamableHTTPTransportType represents Streamable HTTP transport
	StreamableHTTPTransportType TransportType = "streamable-http"
)

// TransportConfig holds configuration for transport creation
type TransportConfig struct {
	Type          TransportType
	ServerURL     string
	Headers       map[string]string
	SessionID     string // For Streamable HTTP
	CallbackPort  int    // For OAuth
	ServerURLHash string // For auth storage
}