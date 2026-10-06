// Command screencaster renders a demo script into narrated MP4s (FR-011). It
// is a thin shell over app/renderer: it parses arguments, gets the real tools
// from app/wire, prints progress and maps the result to an exit code.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"screencaster/internal/adapters/lock"
	"screencaster/internal/app/renderer"
	"screencaster/internal/app/wire"
)

// renderLockFile is the lock shared with the MCP worker (ARCHITECTURE §6.3).
const renderLockFile = ".screencaster/render.lock"

func main() {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// SIGINT/SIGTERM cancel the render, which closes the browser and removes
	// the temp dir (ARCHITECTURE §6.4).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], wd, renderWith(os.Getenv), os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// renderWith is the real renderFunc. It picks the TTS provider from the
// environment first, so a host build without one still serves --help and
// version, then takes the render lock without waiting (ARCHITECTURE §6.3) and
// runs the pipeline with the real tools.
func renderWith(getenv func(string) string) renderFunc {
	return func(ctx context.Context, req renderer.Request) ([]renderer.Output, error) {
		eng, err := wire.TTS(getenv, req.WorkDir)
		if err != nil {
			return nil, err
		}
		l, err := lockRender(req.WorkDir)
		if err != nil {
			return nil, err
		}
		defer func() { _ = l.Release() }()

		deps, err := wire.NewDeps(eng, runID)
		if err != nil {
			return nil, err
		}
		return renderer.Render(ctx, deps, req)
	}
}

// errRenderRunning is the PRD wording for a held render lock (ARCHITECTURE
// §6.3).
var errRenderRunning = errors.New("another render is running")

// lockRender takes the render lock without waiting; the CLI fails fast
// instead of queueing (ADR-45).
func lockRender(workDir string) (*lock.Lock, error) {
	l, err := lock.TryAcquire(filepath.Join(workDir, renderLockFile))
	if errors.Is(err, lock.ErrHeld) {
		return nil, errRenderRunning
	}
	return l, err
}

// runID names the CLI's temp dir. It only has to be unique; it never reaches
// an output (ARCHITECTURE §11).
func runID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // never fails on Linux (crypto/rand docs)
	return hex.EncodeToString(b)
}
