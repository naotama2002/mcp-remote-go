package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
)

// SSEEvent represents a single Server-Sent Event
type SSEEvent struct {
	Event string
	Data  []byte
	ID    string
}

// ReadSSEEvents reads SSE events from a reader and calls the handler for each complete event.
// It returns when the reader is exhausted or ctx is cancelled.
func ReadSSEEvents(ctx context.Context, reader io.Reader, handler func(SSEEvent)) error {
	scanner := bufio.NewReader(reader)

	var event string
	var data bytes.Buffer
	var id string

	parseLine := func(line []byte) {
		line = bytes.TrimSpace(line)

		if len(line) == 0 {
			// Empty line marks the end of an event
			if data.Len() > 0 {
				handler(SSEEvent{Event: event, Data: bytes.Clone(data.Bytes()), ID: id})
				event = ""
				data.Reset()
				id = ""
			}
			return
		}

		// Skip comments
		if bytes.HasPrefix(line, []byte(":")) {
			return
		}

		// Parse field
		if bytes.HasPrefix(line, []byte("event:")) {
			event = string(bytes.TrimSpace(line[6:]))
		} else if bytes.HasPrefix(line, []byte("data:")) {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.Write(bytes.TrimSpace(line[5:]))
		} else if bytes.HasPrefix(line, []byte("id:")) {
			id = string(bytes.TrimSpace(line[3:]))
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line, err := scanner.ReadBytes('\n')
		if len(line) > 0 {
			parseLine(line)
		}
		if err != nil {
			// Dispatch any pending event before returning
			if data.Len() > 0 {
				handler(SSEEvent{Event: event, Data: bytes.Clone(data.Bytes()), ID: id})
			}
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
	}
}
