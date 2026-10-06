package lock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// flock is per open file description, so two TryAcquire calls in one process
// behave like two processes (each opens the file anew).

func TestTryAcquire_secondCallerIsRefused(t *testing.T) { // ADR-45, CLI fails fast
	path := filepath.Join(t.TempDir(), ".screencaster", "render.lock")
	l, err := TryAcquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Release() }()

	if _, err := TryAcquire(path); !errors.Is(err, ErrHeld) {
		t.Errorf("second TryAcquire err = %v, want ErrHeld", err)
	}
}

func TestRelease_freesLockAndKeepsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "render.lock")
	l, err := TryAcquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("lock file removed: %v", err)
	}
	l2, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("TryAcquire after Release: %v", err)
	}
	_ = l2.Release()
}

func TestAcquire_waitsUntilReleased(t *testing.T) { // FR-014: worker waits
	path := filepath.Join(t.TempDir(), "render.lock")
	held, err := TryAcquire(path)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(200*time.Millisecond, func() { _ = held.Release() })

	start := time.Now()
	l, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Release() }()
	if waited := time.Since(start); waited < 200*time.Millisecond {
		t.Errorf("acquired after %v, before the holder released", waited)
	}
}

func TestAcquire_cancelStopsWaiting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "render.lock")
	held, err := TryAcquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Release() }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if _, err := Acquire(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
}
