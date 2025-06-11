package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewEventSource(t *testing.T) {
	req, err := http.NewRequest("GET", "http://example.com/events", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	client := &http.Client{}
	es := NewEventSource(req, client)

	if es == nil {
		t.Fatal("EventSource should not be nil")
		return
	}

	if es.request == nil {
		t.Error("Request should not be nil")
	}

	if es.client != client {
		t.Error("Client should be set correctly")
	}

	if es.ctx == nil {
		t.Error("Context should not be nil")
	}

	if es.cancel == nil {
		t.Error("Cancel function should not be nil")
	}

	if !es.reconnect {
		t.Error("Reconnect should be true by default")
	}

	if es.connected {
		t.Error("Should not be connected initially")
	}

	// Clean up
	es.Close()
}

func TestEventSourceConnect(t *testing.T) {
	// Create a test server that sends SSE data
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Server doesn't support streaming", http.StatusInternalServerError)
			return
		}

		// Send a simple event
		if _, err := w.Write([]byte("data: test message\n\n")); err != nil {
			http.Error(w, "Failed to write data", http.StatusInternalServerError)
			return
		}
		flusher.Flush()

		// Keep connection open for a short time
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	req, err := http.NewRequest("GET", server.URL, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	client := &http.Client{}
	es := NewEventSource(req, client)
	defer es.Close()

	// Test connection
	err = es.Connect()
	if err != nil {
		t.Errorf("Connect should not fail: %v", err)
	}

	if !es.connected {
		t.Error("Should be connected after Connect()")
	}

	// Test that calling Connect again doesn't fail
	err = es.Connect()
	if err != nil {
		t.Errorf("Second Connect should not fail: %v", err)
	}
}

func TestEventSourceConnectError(t *testing.T) {
	tests := []struct {
		name               string
		handler            http.HandlerFunc
		expectedErrorMatch string
	}{
		{
			name: "404 error",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "Not Found", http.StatusNotFound)
			}),
			expectedErrorMatch: "server returned error status: 404",
		},
		{
			name: "wrong content type",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
			}),
			expectedErrorMatch: "expected content-type text/event-stream",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			req, err := http.NewRequest("GET", server.URL, nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			client := &http.Client{}
			es := NewEventSource(req, client)
			defer es.Close()

			err = es.Connect()
			if err == nil {
				t.Error("Connect should fail")
			}

			if !strings.Contains(err.Error(), tt.expectedErrorMatch) {
				t.Errorf("Error should contain '%s', got: %v", tt.expectedErrorMatch, err)
			}

			if es.connected {
				t.Error("Should not be connected after failed Connect()")
			}
		})
	}
}

func TestEventSourceMessageHandling(t *testing.T) {
	// Create test server that sends different types of SSE events
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Server doesn't support streaming", http.StatusInternalServerError)
			return
		}

		// Send different types of events
		events := []string{
			"data: simple message\n\n",
			"event: custom\ndata: custom event message\n\n",
			"id: 123\ndata: message with id\n\n",
			"data: line 1\ndata: line 2\n\n",
		}

		for _, event := range events {
			if _, err := w.Write([]byte(event)); err != nil {
				http.Error(w, "Failed to write event", http.StatusInternalServerError)
				return
			}
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer server.Close()

	req, err := http.NewRequest("GET", server.URL, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	var receivedEvents []struct {
		event string
		data  string
	}

	client := &http.Client{}
	es := NewEventSource(req, client)
	defer es.Close()

	// Set up callbacks
	es.OnMessage = func(event string, data []byte) {
		receivedEvents = append(receivedEvents, struct {
			event string
			data  string
		}{
			event: event,
			data:  string(data),
		})
	}

	// Connect and wait for events
	err = es.Connect()
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	// Wait for events to be processed
	time.Sleep(200 * time.Millisecond)

	// Verify received events
	if len(receivedEvents) < 3 {
		t.Errorf("Expected at least 3 events, got %d", len(receivedEvents))
	}

	// Check first event (simple message)
	if len(receivedEvents) > 0 {
		if receivedEvents[0].event != "" {
			t.Errorf("First event should have empty event type, got '%s'", receivedEvents[0].event)
		}
		if receivedEvents[0].data != "simple message" {
			t.Errorf("First event data should be 'simple message', got '%s'", receivedEvents[0].data)
		}
	}

	// Check second event (custom event)
	if len(receivedEvents) > 1 {
		if receivedEvents[1].event != "custom" {
			t.Errorf("Second event should be 'custom', got '%s'", receivedEvents[1].event)
		}
		if receivedEvents[1].data != "custom event message" {
			t.Errorf("Second event data should be 'custom event message', got '%s'", receivedEvents[1].data)
		}
	}
}

func TestEventSourceClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		// Keep connection open
		select {
		case <-r.Context().Done():
			return
		case <-time.After(1 * time.Second):
			return
		}
	}))
	defer server.Close()

	req, err := http.NewRequest("GET", server.URL, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	client := &http.Client{}
	es := NewEventSource(req, client)

	// Connect
	err = es.Connect()
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	if !es.connected {
		t.Error("Should be connected")
	}

	// Close
	es.Close()

	if es.connected {
		t.Error("Should not be connected after Close()")
	}

	// Check context is cancelled
	select {
	case <-es.ctx.Done():
		if es.ctx.Err() != context.Canceled {
			t.Errorf("Expected context.Canceled, got %v", es.ctx.Err())
		}
	case <-time.After(time.Second):
		t.Error("Context should be cancelled after Close()")
	}

	// Calling Close again should not panic
	es.Close()
}

func TestEventSourceCallbacks(t *testing.T) {
	var onOpenCalled bool
	var onErrorCalled bool
	var errorReceived error

	// Test OnOpen callback
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("data: test\n\n")); err != nil {
			http.Error(w, "Failed to write data", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	req, err := http.NewRequest("GET", server.URL, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	client := &http.Client{}
	es := NewEventSource(req, client)
	defer es.Close()

	es.OnOpen = func() {
		onOpenCalled = true
	}

	es.OnError = func(err error) {
		onErrorCalled = true
		errorReceived = err
	}

	err = es.Connect()
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	// Give some time for callbacks
	time.Sleep(100 * time.Millisecond)

	if !onOpenCalled {
		t.Error("OnOpen callback should have been called")
	}

	if onErrorCalled {
		t.Errorf("OnError should not have been called, but got: %v", errorReceived)
	}
}
