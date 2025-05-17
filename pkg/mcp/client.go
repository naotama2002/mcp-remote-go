package mcp

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/naotama2002/mcp-remote-go/internal/transport"
)

// MCPClient represents an MCP client
type MCPClient struct {
	name      string
	version   string
	transport transport.Transport
	closed    bool
}

// ClientOptions represents client configuration options
type ClientOptions struct {
	Capabilities map[string]interface{}
}

// NewClient creates a new MCP client
func NewClient(name, version string, options *ClientOptions) *MCPClient {
	if options == nil {
		options = &ClientOptions{
			Capabilities: make(map[string]interface{}),
		}
	}

	return &MCPClient{
		name:    name,
		version: version,
	}
}

// Connect establishes a connection to the transport
func (c *MCPClient) Connect(t transport.Transport) error {
	if c.closed {
		return fmt.Errorf("client is closed")
	}

	c.transport = t

	// Send initialization message
	initMessage := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "initialize",
		"params": map[string]interface{}{
			"clientInfo": map[string]interface{}{
				"name":    c.name,
				"version": c.version,
			},
		},
	}

	return c.transport.Send(initMessage)
}

// Close terminates the client connection
func (c *MCPClient) Close() error {
	if c.closed {
		return nil
	}

	c.closed = true

	if c.transport != nil {
		return c.transport.Close()
	}

	return nil
}

// Request sends a request to the server
func (c *MCPClient) Request(request map[string]interface{}, schema interface{}) (interface{}, error) {
	if c.closed {
		return nil, fmt.Errorf("client is closed")
	}

	if c.transport == nil {
		return nil, fmt.Errorf("transport is not configured")
	}

	// Set request ID
	request["id"] = generateRequestID()
	request["jsonrpc"] = "2.0"

	// Channel to wait for request completion
	resultCh := make(chan interface{}, 1)
	errorCh := make(chan error, 1)

	// Set temporary message handler
	
	// Set temporary message handler
	c.transport.SetMessageHandler(func(message interface{}) {
		// Encode message to JSON
		jsonData, err := json.Marshal(message)
		if err != nil {
			log.Printf("Failed to encode message: %v", err)
			return
		}

		// Decode message from JSON
		var msg map[string]interface{}
		if err := json.Unmarshal(jsonData, &msg); err != nil {
			log.Printf("Failed to decode message: %v", err)
			return
		}

		// Check if IDs match
		if msgID, ok := msg["id"]; ok && msgID == request["id"] {
			// Check for errors
			if errObj, ok := msg["error"]; ok {
				errorCh <- fmt.Errorf("server error: %v", errObj)
				return
			}

			// Get result
			if result, ok := msg["result"]; ok {
				resultCh <- result
				return
			}

			errorCh <- fmt.Errorf("response does not contain result field")
		}
	})

	// Send request
	if err := c.transport.Send(request); err != nil {
		return nil, err
	}

	// Wait for response
	select {
	case result := <-resultCh:
		return result, nil
	case err := <-errorCh:
		return nil, err
	}
}

// ListTools retrieves the list of tools from the server
func (c *MCPClient) ListTools() ([]mcp.Tool, error) {
	request := map[string]interface{}{
		"method": "tools/list",
	}

	result, err := c.Request(request, nil)
	if err != nil {
		return nil, err
	}

	// Encode result to JSON
	jsonData, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("failed to encode result: %v", err)
	}

	// Decode result to Tool struct
	var tools struct {
		Tools []mcp.Tool `json:"tools"`
	}
	if err := json.Unmarshal(jsonData, &tools); err != nil {
		return nil, fmt.Errorf("failed to decode result: %v", err)
	}

	return tools.Tools, nil
}

// ListResources retrieves the list of resources from the server
func (c *MCPClient) ListResources() ([]interface{}, error) {
	request := map[string]interface{}{
		"method": "resources/list",
	}

	result, err := c.Request(request, nil)
	if err != nil {
		return nil, err
	}

	// Encode result to JSON
	jsonData, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("failed to encode result: %v", err)
	}

	// Decode result to Resource struct
	var resources struct {
		Resources []interface{} `json:"resources"`
	}
	if err := json.Unmarshal(jsonData, &resources); err != nil {
		return nil, fmt.Errorf("failed to decode result: %v", err)
	}

	return resources.Resources, nil
}

// generateRequestID generates a request ID
func generateRequestID() string {
	return fmt.Sprintf("req_%d", time.Now().UnixNano())
}
