package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
)

// EventSource provides a client for Server-Sent Events (SSE)
type EventSource struct {
	request   *http.Request
	client    *http.Client
	lastID    string
	reconnect bool
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc

	// Callbacks
	OnOpen    func()
	OnMessage func(event string, data []byte)
	OnError   func(err error)

	// State
	connected bool
	response  *http.Response
	reader    *bufio.Reader
}

// NewEventSource creates a new SSE client
func NewEventSource(request *http.Request, client *http.Client) *EventSource {
	ctx, cancel := context.WithCancel(request.Context())

	return &EventSource{
		request:   request.WithContext(ctx),
		client:    client,
		ctx:       ctx,
		cancel:    cancel,
		reconnect: true,
	}
}

// Connect establishes the SSE connection
func (es *EventSource) Connect() error {
	es.mu.Lock()
	defer es.mu.Unlock()

	if es.connected {
		return nil
	}

	// Add Last-Event-ID header if we have one from previous connection
	if es.lastID != "" {
		es.request.Header.Set("Last-Event-ID", es.lastID)
	}

	resp, err := es.client.Do(es.request)
	if err != nil {
		return fmt.Errorf("SSE connection failed: %w", err)
	}

	// Check response code
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if err := resp.Body.Close(); err != nil {
			log.Printf("Warning: failed to close response body: %v", err)
		}
		return fmt.Errorf("server returned error status: %d - %s", resp.StatusCode, string(body))
	}

	// Verify that the content type is text/event-stream
	contentType := resp.Header.Get("Content-Type")
	if contentType != "text/event-stream" {
		if err := resp.Body.Close(); err != nil {
			log.Printf("Warning: failed to close response body: %v", err)
		}
		return fmt.Errorf("expected content-type text/event-stream, got %s", contentType)
	}

	es.response = resp
	es.reader = bufio.NewReader(resp.Body)
	es.connected = true

	// Notify that we're connected
	if es.OnOpen != nil {
		es.OnOpen()
	}

	// Start reading events
	go es.readEvents()

	return nil
}

// Close closes the SSE connection
func (es *EventSource) Close() {
	es.mu.Lock()
	defer es.mu.Unlock()

	if !es.connected {
		return
	}

	// Cancel the context to stop all operations
	es.cancel()

	// Close the response body
	if es.response != nil && es.response.Body != nil {
		if err := es.response.Body.Close(); err != nil {
			log.Printf("Warning: failed to close response body: %v", err)
		}
	}

	es.connected = false
}

// readEvents continuously reads SSE events
func (es *EventSource) readEvents() {
	defer func() {
		es.mu.Lock()
		es.connected = false
		if es.response != nil && es.response.Body != nil {
			if err := es.response.Body.Close(); err != nil {
				log.Printf("Warning: failed to close response body: %v", err)
			}
		}
		es.mu.Unlock()
	}()

	var event string
	var data bytes.Buffer

	for {
		// Check if context is done
		select {
		case <-es.ctx.Done():
			return
		default:
		}

		// Read a line
		line, err := es.reader.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return
			}

			if es.OnError != nil {
				es.OnError(err)
			}
			return
		}

		// Trim the line
		line = bytes.TrimSpace(line)

		// Empty line marks the end of an event
		if len(line) == 0 {
			if data.Len() > 0 {
				if es.OnMessage != nil {
					es.OnMessage(event, data.Bytes())
				}

				// Reset for next event
				event = ""
				data.Reset()
			}
			continue
		}

		// Parse the line
		if bytes.HasPrefix(line, []byte("event:")) {
			event = string(bytes.TrimSpace(line[6:]))
		} else if bytes.HasPrefix(line, []byte("data:")) {
			dataLine := bytes.TrimSpace(line[5:])
			data.Write(dataLine)
		} else if bytes.HasPrefix(line, []byte("id:")) {
			es.lastID = string(bytes.TrimSpace(line[3:]))
		}
		// Note: retry field is not currently implemented
	}
}
