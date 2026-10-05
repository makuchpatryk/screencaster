// Package recorder runs the steps of one language in a fresh recorded browser
// and decides when each step starts (BR-003, FR-007). It does not synthesize
// audio, assemble video or choose output names.
package recorder

import (
	"context"
	"errors"
	"fmt"
	"time"

	"screencaster/core/executor"
	"screencaster/core/failure"
	"screencaster/core/script"
)

// LeadInCompensation is how much later video time 0 is than t0: Playwright's
// recording starts about 90 ms after page creation (spike S1, ARCHITECTURE §17,
// ADR-46). Offsets are shifted by it so narration lines up with the picture.
const LeadInCompensation = 90 * time.Millisecond

// Session is the browser the recorder drives. core/browser implements it; tests
// use a fake. Close returns the finished video's path.
type Session interface {
	executor.Page
	// Start opens the page; recording begins here.
	Start() error
	// Close finishes the video and returns its path.
	Close() (videoPath string, err error)
	// Abort drops the session fast and discards the video.
	Abort()
}

// LaunchOptions configure the fresh browser context of one recording
// (FR-004).
type LaunchOptions struct {
	VideoDir     string
	BaseURL      string
	StorageState *script.StorageState
	StartImage   string // picture the page shows until the first goto paints; "": plain
}

// Recorder records one language at a time. Launch, Now and Sleep are injected
// so timing is testable with a fake clock; New fills in the real ones.
type Recorder struct {
	// Launch opens a session that records video into o.VideoDir.
	Launch func(ctx context.Context, o LaunchOptions) (Session, error)
	Now    func() time.Time
	Sleep  func(ctx context.Context, d time.Duration) error
}

// New returns a Recorder on the real clock.
func New(launch func(ctx context.Context, o LaunchOptions) (Session, error)) Recorder {
	return Recorder{Launch: launch, Now: time.Now, Sleep: sleep}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Input is one language's recording job. Clips maps a step index (0-based, into
// Steps) to the duration of its narration clip. OnStep, when set, is called
// with the 0-based index right before each step runs (CLI progress).
type Input struct {
	Steps        []script.Step
	Clips        map[int]time.Duration
	Lang         string
	Dir          string // video output directory
	BaseURL      string
	StorageState *script.StorageState // nil: logged-out session
	StartImage   string               // the start card's picture, continued until the first page loads
	OnStep       func(i int)
}

// Output is a finished recording. Offsets[i] is when Steps[i] starts in the
// video (time since t0 minus LeadInCompensation, never negative), for placing
// the narration clips (FR-009.1).
type Output struct {
	WebmPath string
	Offsets  []time.Duration
}

// Record runs the steps. The first failing step stops everything: the browser
// is aborted and a *failure.Failure with Lang set is returned (BR-004). If ctx
// ends, ctx.Err() is returned. Temp files live under in.Dir; the caller removes
// them.
func (r Recorder) Record(ctx context.Context, in Input) (Output, error) {
	sess, err := r.Launch(ctx, LaunchOptions{VideoDir: in.Dir, BaseURL: in.BaseURL, StorageState: in.StorageState, StartImage: in.StartImage})
	if err != nil {
		return Output{}, fmt.Errorf("launch browser (%s): %w", in.Lang, err)
	}
	ex, err := executor.New(sess, in.BaseURL, executor.Mode{Visuals: true})
	if err != nil {
		sess.Abort()
		return Output{}, err
	}

	// t0 is taken right before the page exists, because that is when Playwright
	// starts recording (ARCHITECTURE §5, ADR-46).
	t0 := r.Now()
	if err := sess.Start(); err != nil {
		sess.Abort()
		return Output{}, fmt.Errorf("start page (%s): %w", in.Lang, err)
	}

	offsets := make([]time.Duration, len(in.Steps))
	for i, step := range in.Steps {
		start := r.Now()
		offsets[i] = max(start.Sub(t0)-LeadInCompensation, 0)
		if in.OnStep != nil {
			in.OnStep(i)
		}

		err := ex.Run(ctx, i+1, step)
		if err == nil {
			err = r.waitForClip(ctx, start, in.Clips[i])
		}
		if err != nil {
			sess.Abort()
			return Output{}, stepError(ctx, in.Lang, err)
		}
	}

	path, err := sess.Close()
	if err != nil {
		return Output{}, fmt.Errorf("close browser (%s): %w", in.Lang, err)
	}
	if path == "" {
		return Output{}, fmt.Errorf("no video recorded (%s)", in.Lang)
	}
	return Output{WebmPath: path, Offsets: offsets}, nil
}

// waitForClip holds the next step back until start+clip, so a narrated step
// lasts max(action, clip) (FR-007). An un-narrated step has clip 0.
func (r Recorder) waitForClip(ctx context.Context, start time.Time, clip time.Duration) error {
	if rest := clip - r.Now().Sub(start); rest > 0 {
		return r.Sleep(ctx, rest)
	}
	return nil
}

// stepError reports a cancelled ctx as itself and anything else with the
// language filled in.
func stepError(ctx context.Context, lang string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var f *failure.Failure
	if errors.As(err, &f) {
		f.Lang = lang
	}
	return err
}
