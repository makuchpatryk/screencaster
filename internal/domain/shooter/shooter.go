// Package shooter runs one screenshots script in a fresh browser: it runs the
// ordinary steps through the executor and takes a PNG at each screenshot step
// (decision 75). It does not publish files or choose where they end up. It
// sits next to recorder: one run's steps behind a Session port.
package shooter

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"

	"screencaster/internal/domain/executor"
	"screencaster/internal/domain/failure"
	"screencaster/internal/domain/script"
)

// Session is the browser the shooter drives. adapters/browser implements it;
// tests use a fake.
type Session interface {
	executor.Page
	// Start opens the page.
	Start() error
	// Capture writes one PNG of the page to path.
	Capture(shot script.Screenshot, path string) error
	// Abort drops the session; there is no video to keep.
	Abort()
}

// LaunchOptions configure the fresh browser context of one run: no video, no
// cursor overlay.
type LaunchOptions struct {
	StorageState *script.StorageState
}

// Input is one screenshots run. OnStep, when set, is called with the 0-based
// index right before each step runs (CLI progress).
type Input struct {
	Steps        []script.Step
	Dir          string               // PNGs are written here, named by ShotName
	StorageState *script.StorageState // nil: logged-out session
	OnStep       func(i int)
}

// Shooter takes the screenshots of one script. Launch is injected so tests
// need no browser.
type Shooter struct {
	Launch func(ctx context.Context, o LaunchOptions) (Session, error)
}

// Shoot runs the steps and returns the PNG paths in step order. The first
// failing step stops everything (BR-004): the browser is aborted and a
// *failure.Failure is returned. If ctx ends, ctx.Err() is returned. The
// caller removes in.Dir.
func (s Shooter) Shoot(ctx context.Context, in Input) ([]string, error) {
	sess, err := s.Launch(ctx, LaunchOptions{StorageState: in.StorageState})
	if err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}
	defer sess.Abort()

	ex := executor.New(sess, executor.Mode{})
	if err := sess.Start(); err != nil {
		return nil, fmt.Errorf("start page: %w", err)
	}

	total := countShots(in.Steps)
	var paths []string
	for i, step := range in.Steps {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if in.OnStep != nil {
			in.OnStep(i)
		}

		shot, ok := step.Action.(script.Screenshot)
		if !ok {
			if err := ex.Run(ctx, i+1, step); err != nil {
				return nil, orCtx(ctx, err)
			}
			continue
		}
		path := filepath.Join(in.Dir, ShotName(len(paths)+1, total))
		if err := sess.Capture(shot, path); err != nil {
			return nil, orCtx(ctx, failure.Step(i+1, "", shot.Name(), shot.Target(), err))
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// countShots is the number of screenshot steps, which decides the width of
// the PNG names.
func countShots(steps []script.Step) int {
	n := 0
	for _, st := range steps {
		if _, ok := st.Action.(script.Screenshot); ok {
			n++
		}
	}
	return n
}

// ShotName is the one place that names a PNG: the k-th shot (1-based) of n,
// zero-padded to max(2, digits of n): 01.png, or 001.png when n > 99. The same
// width for every shot of a run keeps the names in step order when sorted.
func ShotName(k, n int) string {
	width := max(2, len(strconv.Itoa(n)))
	return fmt.Sprintf("%0*d.png", width, k)
}

// orCtx reports a cancelled ctx as itself and anything else unchanged.
func orCtx(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
