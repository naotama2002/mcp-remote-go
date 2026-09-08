package auth

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/naotama2002/mcp-remote-go/internal/filelock"
)

const (
	// authCodeTimeout bounds how long an authorization flow may stay open
	// waiting for the user to finish in their browser.
	authCodeTimeout = 5 * time.Minute

	// authLockProbe is how long acquiring the authorization lock may take
	// before concluding that another process holds it. Long enough to lose a
	// race against a process that is in the middle of releasing it, short
	// enough that it never reads as a stall.
	authLockProbe = 250 * time.Millisecond

	// authLockPoll is how often a waiting process looks for the holder's result.
	authLockPoll = 250 * time.Millisecond

	// authFlowMaxAge is the longest a flow can legitimately hold the lock: the
	// browser wait, plus room for discovery, registration and the exchange
	// around it. A lock older than this was left behind.
	authFlowMaxAge = authCodeTimeout + time.Minute
)

// waitOutcome is what waiting on another process's authorization produced.
type waitOutcome int

const (
	// waitNoToken means that flow ended without storing a token, so this
	// process has to authorize after all.
	waitNoToken waitOutcome = iota

	// waitGotToken means the other process stored a token this one can use.
	waitGotToken

	// waitCancelled means this process's own client went away while waiting.
	waitCancelled
)

// AuthorizeExclusively runs authorize unless another process is already
// authorizing the same server, in which case it waits for that process's tokens
// and does not call authorize at all.
//
// Two proxies for one server happen in ordinary use: a host may start a second
// instance while the first is still authorizing, and each would otherwise run
// the whole flow. That sends the user two browser windows for one account, and
// only the window whose callback port and `state` belong to the process still
// listening can be completed -- the other is a dead end that fails with a state
// mismatch or a closed port. Waiting is both fewer windows and the one that
// works.
func (c *Coordinator) AuthorizeExclusively(ctx context.Context, authorize func() error) error {
	lock := filelock.New(c.getAuthLockPath())

	// Clear a lock left behind by a process that died holding it, or nothing
	// will ever acquire it again.
	if takenAt, held := lock.HeldSince(); held && time.Since(takenAt) > authFlowMaxAge {
		log.Println("Discarding an authorization lock left behind by a process that did not finish")
		if err := lock.Discard(); err != nil {
			log.Printf("Warning: %v", err)
		}
	}

	// The token this process starts from. Anything different appearing in the
	// store came from the other process, and is what waiting is for.
	before := c.cachedAccessToken()

	if err := lock.Lock(authLockProbe); err != nil {
		log.Println("Another process is authorizing this server; waiting for its result " +
			"rather than opening a second browser window")

		switch c.waitForOtherProcess(ctx, before) {
		case waitGotToken:
			log.Println("The other process finished authorizing; using the token it stored")
			return nil
		case waitCancelled:
			return fmt.Errorf("authorization abandoned: %w", ctx.Err())
		default:
			log.Println("The other process stored no token; authorizing here instead")
			// Take the lock if it is free now, so a third process waits for
			// this flow rather than starting its own.
			if lockErr := lock.Lock(authLockProbe); lockErr == nil {
				defer func() { _ = lock.Unlock() }()
			}
			return authorize()
		}
	}
	defer func() { _ = lock.Unlock() }()

	// The other process may have finished between the read above and the
	// acquisition, which would make this flow redundant.
	if token := c.cachedAccessToken(); token != "" && token != before {
		log.Println("Another process stored a token while this one was starting; using it")
		return nil
	}

	return authorize()
}

// waitForOtherProcess watches for the authorizing process to store a token.
//
// It stops as soon as that process releases the lock without having stored one
// -- its flow failed, or it was killed -- so a failure there does not strand
// this process for the full timeout.
func (c *Coordinator) waitForOtherProcess(ctx context.Context, before string) waitOutcome {
	lock := filelock.New(c.getAuthLockPath())
	deadline := time.Now().Add(authFlowMaxAge)

	ticker := time.NewTicker(authLockPoll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return waitCancelled
		case <-ticker.C:
		}

		if token := c.cachedAccessToken(); token != "" && token != before {
			return waitGotToken
		}

		takenAt, held := lock.HeldSince()
		if !held {
			return waitNoToken
		}
		if time.Since(takenAt) > authFlowMaxAge || time.Now().After(deadline) {
			return waitNoToken
		}
	}
}

// cachedAccessToken returns the stored access token, or "" when there is none to
// read.
func (c *Coordinator) cachedAccessToken() string {
	tokens, err := c.LoadTokens()
	if err != nil || tokens == nil {
		return ""
	}
	return tokens.AccessToken
}

// getAuthLockPath is the base path for the per-server authorization lock. It is
// deliberately not the token file: LoadTokens locks that one, and a flow holding
// the authorization lock still has to read and write tokens while it runs.
func (c *Coordinator) getAuthLockPath() string {
	return filepath.Join(getConfigDir(), c.serverURLHash, "authorize")
}
