// Package lock is an exclusive flock on a file (ARCHITECTURE §6.3, ADR-45).
// The kernel releases it when the process dies, so a crash cannot leave it
// stale. What the lock guards is the caller's business.
package lock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// pollInterval is how often Acquire retries a held lock.
const pollInterval = 500 * time.Millisecond

// ErrHeld means another process holds the lock.
var ErrHeld = errors.New("lock held by another process")

// Lock is a held render lock.
type Lock struct{ f *os.File }

// TryAcquire takes the lock at path or returns ErrHeld at once. The
// file and its directory are created when missing.
func TryAcquire(path string) (*Lock, error) {
	f, err := open(path)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return &Lock{f: f}, nil
}

// Acquire waits for the lock at path until ctx ends. Polling
// keeps the wait cancellable; a blocking flock would not be.
func Acquire(ctx context.Context, path string) (*Lock, error) {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		l, err := TryAcquire(path)
		if !errors.Is(err, ErrHeld) {
			return l, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

// Release unlocks and closes the file. The file itself stays: deleting it
// would let two processes lock two different inodes.
func (l *Lock) Release() error {
	return l.f.Close() // closing the last fd drops the flock
}

func open(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	return f, nil
}
