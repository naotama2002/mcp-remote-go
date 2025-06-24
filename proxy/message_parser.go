package proxy

import (
	"encoding/json"
	"fmt"
)

// MessageType represents the type of JSON-RPC message
type MessageType int

const (
	SingleMessage MessageType = iota
	BatchMessage
	InvalidMessage
)

// ParsedMessage contains the parsed message information
type ParsedMessage struct {
	Type     MessageType
	Single   map[string]interface{}
	Batch    []map[string]interface{}
	Methods  []string // Extracted method names for logging
	IDs      []interface{} // Extracted IDs for logging
}

// ParseMessage parses a JSON-RPC message and determines if it's single or batch
func ParseMessage(data []byte) (*ParsedMessage, error) {
	if len(data) == 0 {
		return &ParsedMessage{Type: InvalidMessage}, fmt.Errorf("empty message")
	}
	
	// Trim whitespace
	data = trimWhitespace(data)
	
	// Check if it starts with '[' (batch) or '{' (single)
	if data[0] == '[' {
		return parseBatchMessage(data)
	} else if data[0] == '{' {
		return parseSingleMessage(data)
	}
	
	return &ParsedMessage{Type: InvalidMessage}, fmt.Errorf("invalid JSON-RPC message format")
}

// parseSingleMessage parses a single JSON-RPC message
func parseSingleMessage(data []byte) (*ParsedMessage, error) {
	var msg map[string]interface{}
	if err := json.Unmarshal(data, &msg); err != nil {
		return &ParsedMessage{Type: InvalidMessage}, fmt.Errorf("failed to parse single message: %w", err)
	}
	
	parsed := &ParsedMessage{
		Type:   SingleMessage,
		Single: msg,
	}
	
	// Extract method and ID for logging
	if method, ok := msg["method"].(string); ok {
		parsed.Methods = []string{method}
	}
	if id, ok := msg["id"]; ok {
		parsed.IDs = []interface{}{id}
	}
	
	return parsed, nil
}

// parseBatchMessage parses a batch JSON-RPC message
func parseBatchMessage(data []byte) (*ParsedMessage, error) {
	var batch []map[string]interface{}
	if err := json.Unmarshal(data, &batch); err != nil {
		return &ParsedMessage{Type: InvalidMessage}, fmt.Errorf("failed to parse batch message: %w", err)
	}
	
	if len(batch) == 0 {
		return &ParsedMessage{Type: InvalidMessage}, fmt.Errorf("empty batch message")
	}
	
	parsed := &ParsedMessage{
		Type:  BatchMessage,
		Batch: batch,
	}
	
	// Extract methods and IDs for logging
	for _, msg := range batch {
		if method, ok := msg["method"].(string); ok {
			parsed.Methods = append(parsed.Methods, method)
		}
		if id, ok := msg["id"]; ok {
			parsed.IDs = append(parsed.IDs, id)
		}
	}
	
	return parsed, nil
}

// trimWhitespace trims leading and trailing whitespace from byte slice
func trimWhitespace(data []byte) []byte {
	start := 0
	end := len(data)
	
	// Trim leading whitespace
	for start < end && isWhitespace(data[start]) {
		start++
	}
	
	// Trim trailing whitespace
	for end > start && isWhitespace(data[end-1]) {
		end--
	}
	
	return data[start:end]
}

// isWhitespace checks if a byte is a whitespace character
func isWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// GetMethodsString returns a formatted string of methods for logging
func (p *ParsedMessage) GetMethodsString() string {
	if len(p.Methods) == 0 {
		return ""
	}
	
	if len(p.Methods) == 1 {
		return p.Methods[0]
	}
	
	result := fmt.Sprintf("%s", p.Methods[0])
	for i := 1; i < len(p.Methods); i++ {
		result += fmt.Sprintf(", %s", p.Methods[i])
	}
	
	return fmt.Sprintf("[%s]", result)
}

// GetIDsString returns a formatted string of IDs for logging
func (p *ParsedMessage) GetIDsString() string {
	if len(p.IDs) == 0 {
		return ""
	}
	
	if len(p.IDs) == 1 {
		return fmt.Sprintf("%v", p.IDs[0])
	}
	
	result := fmt.Sprintf("%v", p.IDs[0])
	for i := 1; i < len(p.IDs); i++ {
		result += fmt.Sprintf(", %v", p.IDs[i])
	}
	
	return fmt.Sprintf("[%s]", result)
}