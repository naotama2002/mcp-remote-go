package transport

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
)

// StdioServerTransport is a server transport that uses standard input/output
type StdioServerTransport struct {
	BaseTransport
	reader *bufio.Reader
	writer *bufio.Writer
	mu     sync.Mutex
}

// NewStdioServerTransport creates a new standard input/output server transport
func NewStdioServerTransport() *StdioServerTransport {
	return &StdioServerTransport{
		reader: bufio.NewReader(os.Stdin),
		writer: bufio.NewWriter(os.Stdout),
	}
}

// Start initiates the transport
func (t *StdioServerTransport) Start() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.IsClosed() {
		return ErrTransportClosed
	}

	// Start reading from standard input
	go t.readMessages()

	return nil
}

// readMessages reads messages from standard input
func (t *StdioServerTransport) readMessages() {
	for {
		if t.IsClosed() {
			return
		}

		// Read one line
		line, err := t.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				// Close transport on EOF
				t.Close()
				return
			}
			t.HandleError(fmt.Errorf("failed to read from standard input: %w", err))
			continue
		}

		// Decode JSON message
		var message map[string]interface{}
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.HandleError(fmt.Errorf("failed to decode message: %w", err))
			continue
		}

		// Process message
		t.HandleMessage(message)
	}
}

// Close terminates the transport
func (t *StdioServerTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.IsClosed() {
		return nil
	}

	t.MarkClosed()
	t.HandleClose()
	return nil
}

// Send transmits a message
func (t *StdioServerTransport) Send(message interface{}) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.IsClosed() {
		return ErrTransportClosed
	}

	// Encode message to JSON
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to encode message: %w", err)
	}

	// Write to standard output
	if _, err := t.writer.Write(data); err != nil {
		return fmt.Errorf("failed to write to standard output: %w", err)
	}
	if _, err := t.writer.Write([]byte("\n")); err != nil {
		return fmt.Errorf("failed to write to standard output: %w", err)
	}
	if err := t.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush standard output: %w", err)
	}

	return nil
}
