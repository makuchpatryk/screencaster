package queue

import (
	"context"
	"errors"
	"log/slog"

	"screencaster/core/failure"
)

// Worker renders queued jobs one at a time, oldest first (BR-008, FR-014).
// It is the only long-lived goroutine of the MCP server (ARCHITECTURE §6.2).
type Worker struct {
	store *Store
	wake  chan struct{}
}

// NewWorker returns a worker over store. Call Run in one goroutine.
func NewWorker(store *Store) *Worker {
	return &Worker{store: store, wake: make(chan struct{}, 1)}
}

// Notify tells the worker a job was inserted. It never blocks: a pending wake
// already covers every job inserted before the worker looks.
func (w *Worker) Notify() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run drains the queue, then sleeps until Notify, until ctx ends. render does
// the work for one job; acquire takes the cross-process render lock and
// returns its release (ARCHITECTURE §6.3). A job waits for the lock while still
// queued. When ctx ends mid-render the job stays running, so the next start
// reports it as interrupted (ARCHITECTURE §6.4).
func (w *Worker) Run(ctx context.Context, render func(context.Context, Job) ([]Output, error), acquire func(context.Context) (func(), error)) {
	for {
		w.drain(ctx, render, acquire)
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		}
	}
}

func (w *Worker) drain(ctx context.Context, render func(context.Context, Job) ([]Output, error), acquire func(context.Context) (func(), error)) {
	for ctx.Err() == nil {
		job, err := w.store.next(ctx)
		if errors.Is(err, ErrEmpty) {
			return
		}
		if err != nil {
			slog.Error("dequeue", "err", err)
			return
		}
		if !w.runOne(ctx, job, render, acquire) {
			return
		}
	}
}

// runOne renders job and stores the result. It returns false when the worker
// should stop looping (ctx ended, or the store is failing).
func (w *Worker) runOne(ctx context.Context, job Job, render func(context.Context, Job) ([]Output, error), acquire func(context.Context) (func(), error)) bool {
	release, err := acquire(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("acquire render lock", "job", job.ID, "err", err)
		}
		return false
	}
	defer release()

	if err := w.store.start(ctx, job.ID); err != nil {
		slog.Error("start job", "job", job.ID, "err", err)
		return false
	}
	outs, err := render(ctx, job)
	if ctx.Err() != nil {
		return false // shutdown: leave the job running for recovery
	}
	if err := w.store.finish(ctx, job.ID, outs, asFailure(err)); err != nil {
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
