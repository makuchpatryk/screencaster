// Package jobs is the MCP server's render queue as a use case: the job, its
// statuses and the single worker that renders one job at a time (FR-014,
// ARCHITECTURE §6.2, §10). The SQLite table behind it is an adapter
// (adapters/sqlite) reached through Store.
package jobs

import (
	"context"
	"errors"
	"time"

	"screencaster/internal/app/renderer"
	"screencaster/internal/domain/failure"
)

// Status is where a job is in its life (PRD §13). Nothing else compares status
// strings (CODE_QUALITY, DRY).
type Status string

const (
	Queued    Status = "queued"
	Running   Status = "running"
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
)

// ErrEmpty means no job is queued.
var ErrEmpty = errors.New("no queued job")

// NewJob is what a submit stores. Languages are the selection resolved at
// submit time (BR-002) and Script is the YAML as validated then; the worker
// renders exactly these.
type NewJob struct {
	ID         string
	ScriptPath string
	Script     []byte
	DemoName   string
	Languages  []string
}

// Job is a stored job. Position is set only for queued jobs.
type Job struct {
	ID         string
	ScriptPath string
	Script     []byte // nil for a row stored before the snapshot column
	DemoName   string
	Languages  []string
	Status     Status
	Position   int
	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	Outputs    []renderer.Output
	Error      *failure.Failure
}

// Store is what the worker needs from the job table.
type Store interface {
	// Next returns the oldest queued job, or ErrEmpty.
	Next(ctx context.Context) (Job, error)
	// Start marks the job running.
	Start(ctx context.Context, id string) error
	// Finish stores the result: failed when f is not nil, else succeeded.
	Finish(ctx context.Context, id string, outs []renderer.Output, f *failure.Failure) error
}
