package proxy

import (
	"context"
	"strings"
	"testing"
)

func TestReadSSEEvents(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []SSEEvent
	}{
		{
			name:  "single event with data only",
			input: "data: hello\n\n",
			expected: []SSEEvent{
				{Event: "", Data: []byte("hello")},
			},
		},
		{
			name:  "event with type and data",
			input: "event: message\ndata: {\"test\":true}\n\n",
			expected: []SSEEvent{
				{Event: "message", Data: []byte(`{"test":true}`)},
			},
		},
		{
			name:  "event with id",
			input: "id: 42\nevent: message\ndata: hello\n\n",
			expected: []SSEEvent{
				{Event: "message", Data: []byte("hello"), ID: "42"},
			},
		},
		{
			name:  "multiple events",
			input: "event: endpoint\ndata: /mcp\n\nevent: message\ndata: {\"id\":1}\n\n",
			expected: []SSEEvent{
				{Event: "endpoint", Data: []byte("/mcp")},
				{Event: "message", Data: []byte(`{"id":1}`)},
			},
		},
		{
			name:  "multiline data",
			input: "data: line1\ndata: line2\n\n",
			expected: []SSEEvent{
				{Event: "", Data: []byte("line1\nline2")},
			},
		},
		{
			name:     "comment lines are skipped",
			input:    ": this is a comment\ndata: hello\n\n",
			expected: []SSEEvent{{Data: []byte("hello")}},
		},
		{
			name:     "empty lines before event are ignored",
			input:    "\n\ndata: hello\n\n",
			expected: []SSEEvent{{Data: []byte("hello")}},
		},
		{
			name:  "event without trailing newline dispatches on EOF",
			input: "data: final",
			expected: []SSEEvent{
				{Data: []byte("final")},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var received []SSEEvent

			err := ReadSSEEvents(t.Context(), strings.NewReader(tt.input), func(evt SSEEvent) {
				received = append(received, evt)
			})
			if err != nil {
				t.Fatalf("ReadSSEEvents returned error: %v", err)
			}

			if len(received) != len(tt.expected) {
				t.Fatalf("Expected %d events, got %d", len(tt.expected), len(received))
			}

			for i, exp := range tt.expected {
				got := received[i]
				if got.Event != exp.Event {
					t.Errorf("Event[%d]: expected event=%q, got=%q", i, exp.Event, got.Event)
				}
				if string(got.Data) != string(exp.Data) {
					t.Errorf("Event[%d]: expected data=%q, got=%q", i, string(exp.Data), string(got.Data))
				}
				if got.ID != exp.ID {
					t.Errorf("Event[%d]: expected id=%q, got=%q", i, exp.ID, got.ID)
				}
			}
		})
	}
}

func TestReadSSEEventsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // Cancel immediately

	err := ReadSSEEvents(ctx, strings.NewReader("data: hello\n\n"), func(evt SSEEvent) {
		t.Error("Handler should not be called when context is cancelled")
	})

	if err != context.Canceled {
		t.Errorf("Expected context.Canceled, got %v", err)
	}
}
