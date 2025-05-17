package transport

import (
	"errors"
)

// TransportStrategy defines the type of transport to use
type TransportStrategy string

const (
	// SSEOnly uses only SSE transport
	SSEOnly TransportStrategy = "sse-only"
	// HTTPOnly uses only HTTP transport
	HTTPOnly TransportStrategy = "http-only"
	// SSEFirst tries SSE transport first, falls back to HTTP transport if it fails
	SSEFirst TransportStrategy = "sse-first"
	// HTTPFirst tries HTTP transport first, falls back to SSE transport if it fails
	HTTPFirst TransportStrategy = "http-first"
)

// Connection related constants
const (
	ReasonAuthNeeded         = "authentication-needed"
	ReasonTransportFallback  = "falling-back-to-alternate-transport"
)

// Error definitions
var (
	ErrTransportClosed = errors.New("transport is closed")
	ErrAuthRequired    = errors.New("authentication required")
)

// MessageHandler is a handler function called when a message is received
type MessageHandler func(message interface{})

// ErrorHandler is a handler function called when an error occurs
type ErrorHandler func(err error)

// CloseHandler is a handler function called when the connection is closed
type CloseHandler func()

// Transport is an interface for transporting MCP messages
type Transport interface {
	// Start initiates the transport
	Start() error

	// Close terminates the transport
	Close() error

	// Send transmits a message
	Send(message interface{}) error

	// SetMessageHandler sets the handler to be called when a message is received
	SetMessageHandler(handler MessageHandler)

	// SetErrorHandler sets the handler to be called when an error occurs
	SetErrorHandler(handler ErrorHandler)

	// SetCloseHandler sets the handler to be called when the connection is closed
	SetCloseHandler(handler CloseHandler)
}

// BaseTransport provides the basic implementation of a transport
type BaseTransport struct {
	onMessage MessageHandler
	onError   ErrorHandler
	onClose   CloseHandler
	closed    bool
}

// SetMessageHandler sets the handler to be called when a message is received
func (t *BaseTransport) SetMessageHandler(handler MessageHandler) {
	t.onMessage = handler
}

// SetErrorHandler sets the handler to be called when an error occurs
func (t *BaseTransport) SetErrorHandler(handler ErrorHandler) {
	t.onError = handler
}

// SetCloseHandler sets the handler to be called when the connection is closed
func (t *BaseTransport) SetCloseHandler(handler CloseHandler) {
	t.onClose = handler
}

// IsClosed returns whether the transport is closed
func (t *BaseTransport) IsClosed() bool {
	return t.closed
}

// MarkClosed marks the transport as closed
func (t *BaseTransport) MarkClosed() {
	t.closed = true
}

// HandleMessage processes a message
func (t *BaseTransport) HandleMessage(message interface{}) {
	if t.onMessage != nil {
		t.onMessage(message)
	}
}

// HandleError processes an error
func (t *BaseTransport) HandleError(err error) {
	if t.onError != nil {
		t.onError(err)
	}
}

// HandleClose processes the connection termination
func (t *BaseTransport) HandleClose() {
	if t.onClose != nil {
		t.onClose()
	}
}
