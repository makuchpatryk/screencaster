package jobs

import (
	"context"
	"errors"
	"log/slog"

	"screencaster/internal/app/renderer"
	"screencaster/internal/domain/failure"
)

// errNoSnapshot fails a job that has no stored script to render.
var errNoSnapshot = errors.New("job has no script snapshot")

// RenderFunc does the work for one job. It renders job.Script, the YAML as
// validated at submit time, never the file as it is now.
type RenderFunc func(ctx context.Context, job Job) ([]renderer.Output, error)

// AcquireFunc takes the cross-process render lock and returns its release
// (ARCHITECTURE §6.3).
type AcquireFunc func(ctx context.Context) (release func(), err error)

// Worker renders queued jobs one at a time, oldest first (BR-008, FR-014).
// It is the only long-lived goroutine of the MCP server (ARCHITECTURE §6.2).
type Worker struct {
	store   Store
	render  RenderFunc
	acquire AcquireFunc
	wake    chan struct{}
}

// NewWorker returns a worker that renders the jobs of store with render,
// each under the lock acquire takes. Call Run in one goroutine.
func NewWorker(store Store, render RenderFunc, acquire AcquireFunc) *Worker {
	return &Worker{store: store, render: render, acquire: acquire, wake: make(chan struct{}, 1)}
}

// Notify tells the worker a job was inserted. It never blocks: a pending wake
// already covers every job inserted before the worker looks.
func (w *Worker) Notify() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run drains the queue, then sleeps until Notify, until ctx ends. A job waits
// for the render lock while still queued. When ctx ends mid-render the job
// stays running, so the next start reports it as interrupted (ARCHITECTURE
// §6.4).
func (w *Worker) Run(ctx context.Context) {
	for {
		w.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		}
	}
}

func (w *Worker) drain(ctx context.Context) {
	for ctx.Err() == nil {
		job, err := w.store.Next(ctx)
		if errors.Is(err, ErrEmpty) {
			return
		}
		if err != nil {
			slog.Error("dequeue", "err", err)
			return
		}
		if !w.runOne(ctx, job) {
			return
		}
	}
}

// runOne renders job and stores the result. It returns false when the worker
// should stop looping (ctx ended, or the store is failing).
func (w *Worker) runOne(ctx context.Context, job Job) bool {
	release, err := w.acquire(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("acquire render lock", "job", job.ID, "err", err)
		}
		return false
	}
	defer release()

	if err := w.store.Start(ctx, job.ID); err != nil {
		slog.Error("start job", "job", job.ID, "err", err)
		return false
	}
	var outs []renderer.Output
	if job.Script == nil {
		// Only a row from before the snapshot column has none (ARCHITECTURE §10).
		err = errNoSnapshot
	} else {
		outs, err = w.render(ctx, job)
	}
	if ctx.Err() != nil {
		return false // shutdown: leave the job running for recovery
	}
	if err := w.store.Finish(ctx, job.ID, outs, asFailure(err)); err != nil {
		slog.Error("finish job", "job", job.ID, "err", err)
		return false
	}
	return true
}

// asFailure maps a render error to the one stored shape (ARCHITECTURE §9);
// nil stays nil.
func asFailure(err error) *failure.Failure {
	if err == nil {
		return nil
	}
	var f *failure.Failure
	if errors.As(err, &f) {
		return f
	}
	return &failure.Failure{Message: err.Error()}
}
