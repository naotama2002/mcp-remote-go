package proxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"
	"sync"
)

// stdinQueue reads the client's end of the pipe from the moment the proxy
// starts, rather than from the moment it is connected to the remote server.
//
// Those two moments can be minutes apart: connecting may require an
// authorization flow that waits on a browser. A client that gives up in between
// says so by closing this pipe, and with nothing reading it that went unnoticed
// -- the flow ran to completion for a client already gone, opening a browser
// window nobody was waiting for and holding the callback port until a signal
// arrived. Two hosts starting a proxy each, as they do, then produced two
// windows for one account.
//
// Lines are queued rather than handed straight to a transport, because there may
// not be one yet, and dropping the client's initialize would leave it waiting
// for a reply that never comes. The queue is unbounded deliberately: any bound
// parks the reader once it fills, which is exactly the blindness being fixed.
type stdinQueue struct {
	reader *bufio.Reader

	mu     sync.Mutex
	lines  []string
	closed bool // the client closed its end; no further lines will arrive

	// ready is pinged whenever lines or closed change, so a waiting consumer
	// wakes without polling. Buffered, so a ping neither blocks the reader nor
	// gets lost.
	ready chan struct{}
}

func newStdinQueue(reader *bufio.Reader) *stdinQueue {
	return &stdinQueue{reader: reader, ready: make(chan struct{}, 1)}
}

// pump reads until the client closes its end, then calls onClose once.
//
// It is deliberately not part of the proxy's WaitGroup: a blocking read cannot
// be interrupted, so this may stay parked on a read that never returns. It holds
// nothing and ends with the process.
func (q *stdinQueue) pump(onClose func()) {
	for {
		line, err := q.reader.ReadString('\n')
		if err == nil {
			q.add(line)
			continue
		}
		if errors.Is(err, io.EOF) {
			q.markClosed()
			onClose()
			return
		}
		// Any other read error is reported and retried, as it always has been:
		// it says nothing about whether the client is still there.
		log.Printf("Error reading from STDIO: %v", err)
	}
}

// next returns the oldest queued line, waiting for one to arrive. ok is false
// once ctx is done, or the client has closed its end and the queue is drained.
func (q *stdinQueue) next(ctx context.Context) (line string, ok bool) {
	for {
		q.mu.Lock()
		if len(q.lines) > 0 {
			line, q.lines = q.lines[0], q.lines[1:]
			q.mu.Unlock()
			return line, true
		}
		closed := q.closed
		q.mu.Unlock()

		if closed {
			return "", false
		}

		select {
		case <-ctx.Done():
			return "", false
		case <-q.ready:
		}
	}
}

func (q *stdinQueue) add(line string) {
	q.mu.Lock()
	q.lines = append(q.lines, line)
	q.mu.Unlock()
	q.wake()
}

func (q *stdinQueue) markClosed() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.wake()
}

func (q *stdinQueue) wake() {
	select {
	case q.ready <- struct{}{}:
	default:
	}
}
