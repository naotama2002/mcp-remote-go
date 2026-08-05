package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// On the stdio transport, stdout carries the JSON-RPC stream and nothing else.
// Anything else written there reaches the client's parser as a message, and
// the connection fails on it -- which is what "Unexpected token 'S',
// "Shutting down..." is not valid JSON" was.
//
// The proxy is a whole process, so this is checked by running one: the tests
// below re-execute this binary, which runs main instead of the suite when the
// marker is set.
const childMarker = "MCP_REMOTE_GO_TEST_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(childMarker) == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

// syncBuffer collects a child's output. os/exec writes into it from its own
// goroutine while the test reads, so the buffer needs a lock of its own.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startChild runs the proxy as a subprocess with stdout and stderr captured.
//
// Its stdin is left open: the proxy treats EOF there as the client going away
// and shuts itself down, which would end the process before a signal could
// reach it.
func startChild(t *testing.T, args ...string) (*exec.Cmd, io.WriteCloser, *syncBuffer, *syncBuffer) {
	t.Helper()

	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), childMarker+"=1")

	stdout, stderr := &syncBuffer{}, &syncBuffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("failed to open the proxy's stdin: %v", err)
	}
	t.Cleanup(func() { _ = stdin.Close() })

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start the proxy: %v", err)
	}
	return cmd, stdin, stdout, stderr
}

// assertStdoutIsJSONRPC fails if stdout holds anything that is not a JSON-RPC
// message, naming the offending line.
func assertStdoutIsJSONRPC(t *testing.T, stdout string) {
	t.Helper()

	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var message map[string]any
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Errorf("stdout carries a line that is not JSON: %q", line)
			continue
		}
		if _, ok := message["jsonrpc"]; !ok {
			t.Errorf("stdout carries JSON that is not a JSON-RPC message: %q", line)
		}
	}
}

// TestShutdownWritesNothingToStdout is the regression guard for the reported
// failure: the signal handler announced itself on stdout, so every restart by
// the host application corrupted the stream.
func TestShutdownWritesNothingToStdout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":0,"result":{}}`)
	}))
	defer server.Close()

	// No stdin input, so the proxy stays connected and waits.
	cmd, _, stdout, stderr := startChild(t, "-server", server.URL, "-allow-http", "-port", "0")

	// Give it time to negotiate and settle before interrupting.
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(stderr.String(), "Connected") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(stderr.String(), "Connected") {
		t.Fatalf("the proxy never connected; stderr: %s", stderr.String())
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("failed to signal the proxy: %v", err)
	}

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the proxy did not exit after SIGTERM")
	}

	if got := stdout.String(); got != "" {
		t.Errorf("shutdown wrote %q to stdout; it must go to stderr", got)
	}
	assertStdoutIsJSONRPC(t, stdout.String())

	// The message should still be reported, just on the other stream.
	if !strings.Contains(stderr.String(), "Shutting down") {
		t.Errorf("shutdown was not reported on stderr; got: %s", stderr.String())
	}
}

// TestUsageGoesToStderr covers the other write: a misconfigured launch would
// otherwise put usage text into the stream too.
func TestUsageGoesToStderr(t *testing.T) {
	cmd, _, stdout, stderr := startChild(t)
	_ = cmd.Wait()

	if got := stdout.String(); got != "" {
		t.Errorf("usage wrote %q to stdout; it must go to stderr", got)
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("usage was not written to stderr; got: %s", stderr.String())
	}
}

// TestSignalTerminatesWithStdinIdle is the regression guard for the shutdown
// hang. stdin is left open and quiet, which is the state a blocking read parks
// in: Shutdown waited on that reader, and with the context only checked
// between reads it never came back.
//
// It goes unnoticed in normal use because the host closes the pipe on teardown
// and the resulting EOF is what actually ends the process. A signal on its own
// has to work too -- that is what SIGTERM is.
func TestSignalTerminatesWithStdinIdle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":0,"result":{}}`)
	}))
	defer server.Close()

	cmd, stdin, _, stderr := startChild(t, "-server", server.URL, "-allow-http", "-port", "0")

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(stderr.String(), "Connected") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(stderr.String(), "Connected") {
		t.Fatalf("the proxy never connected; stderr: %s", stderr.String())
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("failed to signal the proxy: %v", err)
	}

	// stdin stays open for the whole wait: the point is that the signal alone
	// is enough. Closing it would provide the EOF that masks the defect.
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the proxy did not exit on SIGTERM while stdin was open and idle")
	}

	_ = stdin.Close()
}
