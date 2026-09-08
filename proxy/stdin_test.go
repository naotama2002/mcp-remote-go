package proxy

import (
	"bufio"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// TestStdinQueueNoticesCloseWithNobodyConsuming is the regression guard for the
// duplicate browser windows: the client closing its pipe has to be noticed while
// the proxy is still connecting, which is when nothing is consuming messages yet.
//
// Reading only after the connection was established meant an authorization flow
// ran on for a client that had already gone, opening a browser window for it and
// holding the callback port until a signal arrived.
func TestStdinQueueNoticesCloseWithNobodyConsuming(t *testing.T) {
	pipeR, pipeW := io.Pipe()
	q := newStdinQueue(bufio.NewReader(pipeR))

	closed := make(chan struct{})
	go q.pump(func() { close(closed) })

	// What a host does: send initialize, then give up and close the pipe. Nobody
	// is calling next() -- the proxy is still busy connecting.
	if _, err := pipeW.Write([]byte("{\"method\":\"initialize\"}\n")); err != nil {
		t.Fatalf("failed to write to the pipe: %v", err)
	}
	if err := pipeW.Close(); err != nil {
		t.Fatalf("failed to close the pipe: %v", err)
	}

	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("the client closing its end went unnoticed")
	}
}

// TestStdinQueueKeepsMessagesUntilConsumed checks that reading early does not
// cost messages: lines that arrive before the transport exists must still be
// forwarded, in order, once it does. Dropping the client's initialize would
// leave it waiting for a reply forever.
func TestStdinQueueKeepsMessagesUntilConsumed(t *testing.T) {
	const first = "{\"id\":1}\n"
	const second = "{\"id\":2}\n"

	q := newStdinQueue(bufio.NewReader(strings.NewReader(first + second)))

	done := make(chan struct{})
	go q.pump(func() { close(done) })

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the reader did not reach the end of the input")
	}

	ctx := context.Background()
	for i, want := range []string{first, second} {
		got, ok := q.next(ctx)
		if !ok {
			t.Fatalf("line %d was dropped", i+1)
		}
		if got != want {
			t.Errorf("line %d = %q, want %q", i+1, got, want)
		}
	}

	// Drained and closed: no more lines, and no waiting for them.
	if _, ok := q.next(ctx); ok {
		t.Error("next returned a line after the queue was drained and closed")
	}
}

// TestStdinQueueNextStopsOnContextCancel pins that a consumer waiting for input
// lets go when the proxy is shutting down.
func TestStdinQueueNextStopsOnContextCancel(t *testing.T) {
	pipeR, pipeW := io.Pipe()
	defer func() { _ = pipeW.Close() }()

	q := newStdinQueue(bufio.NewReader(pipeR))
	go q.pump(func() {})

	ctx, cancel := context.WithCancel(context.Background())

	returned := make(chan bool, 1)
	go func() {
		_, ok := q.next(ctx)
		returned <- ok
	}()

	cancel()

	select {
	case ok := <-returned:
		if ok {
			t.Error("next reported a line after the context was cancelled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("next did not return after the context was cancelled")
	}
}
