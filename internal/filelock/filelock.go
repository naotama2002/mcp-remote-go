package filelock

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// FileLock provides file-based locking to prevent concurrent access
type FileLock struct {
	path     string
	file     *os.File
	acquired bool
	mu       sync.Mutex
}

// New creates a new file lock
func New(path string) *FileLock {
	return &FileLock{
		path: path + ".lock",
	}
}

// Lock acquires the file lock with a timeout
func (fl *FileLock) Lock(timeout time.Duration) error {
	fl.mu.Lock()
	defer fl.mu.Unlock()

	if fl.acquired {
		return fmt.Errorf("lock already acquired")
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		file, err := os.OpenFile(fl.path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err == nil {
			fl.file = file
			fl.acquired = true
			return nil
		}

		// If file exists, wait and retry
		if os.IsExist(err) {
			time.Sleep(10 * time.Millisecond)
			continue
		}

		// Other errors are fatal
		return fmt.Errorf("failed to acquire lock: %w", err)
	}

	return fmt.Errorf("timeout acquiring lock after %v", timeout)
}

// Unlock releases the file lock
func (fl *FileLock) Unlock() error {
	fl.mu.Lock()
	defer fl.mu.Unlock()

	if !fl.acquired {
		return nil // Already unlocked
	}

	var err error
	if fl.file != nil {
		err = fl.file.Close()
		fl.file = nil
	}

	// Remove lock file
	if removeErr := os.Remove(fl.path); removeErr != nil && !os.IsNotExist(removeErr) {
		if err == nil {
			err = fmt.Errorf("failed to remove lock file: %w", removeErr)
		}
	}

	fl.acquired = false
	return err
}

// WithLock executes a function while holding the lock
func (fl *FileLock) WithLock(timeout time.Duration, fn func() error) error {
	if err := fl.Lock(timeout); err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()

	return fn()
}

// HeldSince reports whether some process currently holds this lock, and when it
// was taken.
//
// A lock file is only ever removed by its holder, so its presence alone cannot
// distinguish a live holder from one that died still holding it. The age is what
// separates them: past any duration a holder could legitimately need, the file
// is a leftover, and a caller that keeps waiting on it waits forever.
func (fl *FileLock) HeldSince() (time.Time, bool) {
	info, err := os.Stat(fl.path)
	if err != nil {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

// Discard removes the lock file without holding it, for a caller that has
// established the holder is gone. It is a no-op when the lock is already free.
func (fl *FileLock) Discard() error {
	if err := os.Remove(fl.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to discard lock file: %w", err)
	}
	return nil
}
