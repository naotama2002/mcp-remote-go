package auth

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/naotama2002/mcp-remote-go/internal/filelock"
)

// newTestCoordinator returns a coordinator with its own config directory.
func newTestCoordinator(t *testing.T, hash string) *Coordinator {
	t.Helper()

	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	t.Cleanup(func() { _ = os.Setenv("HOME", originalHome) })
	if err := os.Setenv("HOME", tmpDir); err != nil {
		t.Fatalf("failed to set HOME: %v", err)
	}

	c, err := NewCoordinator(hash, 0)
	if err != nil {
		t.Fatalf("NewCoordinator failed: %v", err)
	}
	return c
}

// TestAuthorizeExclusivelyRunsWhenUncontested is the ordinary case: nothing else
// is authorizing, so the flow runs here.
func TestAuthorizeExclusivelyRunsWhenUncontested(t *testing.T) {
	c := newTestCoordinator(t, "uncontested")

	ran := false
	if err := c.AuthorizeExclusively(context.Background(), func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("AuthorizeExclusively failed: %v", err)
	}

	if !ran {
		t.Error("the authorization was skipped with nothing holding the lock")
	}

	// The lock has to be released, or the next flow waits on this one forever.
	if _, held := filelock.New(c.getAuthLockPath()).HeldSince(); held {
		t.Error("the authorization lock was left behind")
	}
}

// TestAuthorizeExclusivelyWaitsForAnotherProcess is the fix for the duplicate
// browser windows across processes: while another process is authorizing, this
// one must use the token that process stores rather than start its own flow.
func TestAuthorizeExclusivelyWaitsForAnotherProcess(t *testing.T) {
	c := newTestCoordinator(t, "contested")

	if err := c.SaveTokens(&Tokens{AccessToken: "stale-token"}); err != nil {
		t.Fatalf("SaveTokens failed: %v", err)
	}

	// Stand in for the other process: hold the lock, then store a token.
	other := filelock.New(c.getAuthLockPath())
	if err := other.Lock(time.Second); err != nil {
		t.Fatalf("failed to take the lock as the other process: %v", err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = c.SaveTokens(&Tokens{AccessToken: "fresh-token"})
		_ = other.Unlock()
	}()

	ran := false
	if err := c.AuthorizeExclusively(context.Background(), func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("AuthorizeExclusively failed: %v", err)
	}

	if ran {
		t.Error("a second authorization ran while another process was already authorizing")
	}
	if got := c.cachedAccessToken(); got != "fresh-token" {
		t.Errorf("stored token = %q, want the other process's token", got)
	}
}

// TestAuthorizeExclusivelyProceedsWhenTheOtherProcessFails covers the other
// process giving up: it releases the lock without storing anything, and waiting
// any longer would strand this one.
func TestAuthorizeExclusivelyProceedsWhenTheOtherProcessFails(t *testing.T) {
	c := newTestCoordinator(t, "other-failed")

	other := filelock.New(c.getAuthLockPath())
	if err := other.Lock(time.Second); err != nil {
		t.Fatalf("failed to take the lock as the other process: %v", err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = other.Unlock()
	}()

	ran := false
	if err := c.AuthorizeExclusively(context.Background(), func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("AuthorizeExclusively failed: %v", err)
	}

	if !ran {
		t.Error("the authorization never ran after the other process gave up")
	}
}

// TestAuthorizeExclusivelyDiscardsAbandonedLock covers a process killed while
// holding the lock. Nothing removes the file for it, so without this every later
// flow would wait out the full timeout -- a permanent failure from one crash.
func TestAuthorizeExclusivelyDiscardsAbandonedLock(t *testing.T) {
	c := newTestCoordinator(t, "abandoned-lock")

	abandoned := filelock.New(c.getAuthLockPath())
	if err := abandoned.Lock(time.Second); err != nil {
		t.Fatalf("failed to take the lock: %v", err)
	}
	// Age it past any flow that could still be running.
	lockFile := c.getAuthLockPath() + ".lock"
	old := time.Now().Add(-2 * authFlowMaxAge)
	if err := os.Chtimes(lockFile, old, old); err != nil {
		t.Fatalf("failed to age the lock file: %v", err)
	}

	ran := false
	start := time.Now()
	if err := c.AuthorizeExclusively(context.Background(), func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("AuthorizeExclusively failed: %v", err)
	}

	if !ran {
		t.Error("the authorization never ran despite the lock being abandoned")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("waited %v on an abandoned lock", elapsed)
	}
}

// TestAuthorizeExclusivelyStopsWhenTheClientGoesAway pins that waiting on
// another process ends the moment this proxy's own client disappears -- the
// point of the context being here at all.
func TestAuthorizeExclusivelyStopsWhenTheClientGoesAway(t *testing.T) {
	c := newTestCoordinator(t, "client-gone")

	other := filelock.New(c.getAuthLockPath())
	if err := other.Lock(time.Second); err != nil {
		t.Fatalf("failed to take the lock as the other process: %v", err)
	}
	defer func() { _ = other.Unlock() }()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	ran := false
	err := c.AuthorizeExclusively(ctx, func() error {
		ran = true
		return nil
	})

	if err == nil {
		t.Fatal("expected an error once the client went away")
	}
	if ran {
		t.Error("an authorization started for a client that was already gone")
	}
}

// TestWaitForAuthCodeStopsWhenTheClientGoesAway pins the other half of the
// abandoned-flow fix: the five-minute wait for a browser is not something to sit
// through once the client that wanted the connection has gone. Sitting through it
// held the callback port, so the next process bound a different one and its
// authorization URL pointed somewhere the first window could never reach.
func TestWaitForAuthCodeStopsWhenTheClientGoesAway(t *testing.T) {
	c := newTestCoordinator(t, "wait-cancelled")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	code, err := c.WaitForAuthCode(ctx)

	if err == nil {
		t.Fatalf("expected an error, got code %q", code)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("waited %v after the client went away", elapsed)
	}
}
