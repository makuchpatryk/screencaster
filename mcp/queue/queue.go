// Package queue is the SQLite job store and the single worker that renders one
// job at a time (FR-014, FR-015, ARCHITECTURE §10). It knows nothing about MCP.
package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"

	"screencaster/core/failure"
	"screencaster/core/lock"
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

// timeLayout is RFC 3339 in UTC, fixed-width to the nanosecond. RFC3339Nano
// drops trailing zeros, which would break the lexical ordering that FIFO
// relies on.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

// ErrNotFound means no job has the requested ID.
var ErrNotFound = errors.New("job not found")

// ErrEmpty means no job is queued.
var ErrEmpty = errors.New("no queued job")

// Output is one rendered video of a succeeded job.
type Output struct {
	Lang       string `json:"lang"`
	Path       string `json:"path"`
	DurationMs int64  `json:"durationMs"`
}

// NewJob is what a submit stores. Languages are the selection resolved at
// submit time (BR-002); the worker renders exactly these.
type NewJob struct {
	ID         string
	ScriptPath string
	DemoName   string
	Languages  []string
}

// Job is a stored job. Position is set only for queued jobs.
type Job struct {
	ID         string
	ScriptPath string
	DemoName   string
	Languages  []string
	Status     Status
	Position   int
	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	Outputs    []Output
	Error      *failure.Failure
}

const schema = `
CREATE TABLE IF NOT EXISTS jobs (
  id           TEXT PRIMARY KEY,
  script_path  TEXT NOT NULL,
  demo_name    TEXT NOT NULL,
  languages    TEXT NOT NULL,
  status       TEXT NOT NULL CHECK (status IN ('queued','running','succeeded','failed')),
  created_at   TEXT NOT NULL,
  started_at   TEXT,
  finished_at  TEXT,
  outputs_json TEXT,
  error_json   TEXT
);
CREATE INDEX IF NOT EXISTS jobs_status_created ON jobs(status, created_at);`

const columns = `id, script_path, demo_name, languages, status, created_at, started_at, finished_at, outputs_json, error_json`

// Store is the job table. now is injected so tests control the timestamps.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens (and creates) the database at path with WAL and a 5 s busy
// timeout. One connection serves all callers: the load is a few queries a
// minute, and it keeps the per-connection pragmas trivially right.
func Open(path string, now func() time.Time) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db, now: now}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) stamp() string { return s.now().UTC().Format(timeLayout) }

// Recover implements BR-009: every queued or running job becomes failed with
// the message "interrupted". It runs before the server accepts calls.
func (s *Store) Recover(ctx context.Context) error {
	errJSON, err := json.Marshal(failure.Interrupted())
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE jobs SET status = ?, error_json = ?, finished_at = ? WHERE status IN (?, ?)`,
		Failed, string(errJSON), s.stamp(), Queued, Running)
	if err != nil {
		return fmt.Errorf("mark interrupted jobs: %w", err)
	}
	return nil
}

// RemoveStaleTemp empties tmpDir of what interrupted renders left, but only
// while it holds the render lock at lockPath: a CLI render running right now
// owns its temp dir there too (ARCHITECTURE §6.3, §11). When the lock is held
// nothing is removed; the next start cleans up.
func RemoveStaleTemp(lockPath, tmpDir string) error {
	l, err := lock.TryAcquire(lockPath)
	if errors.Is(err, lock.ErrHeld) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = l.Release() }()

	entries, err := os.ReadDir(tmpDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list %s: %w", tmpDir, err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(tmpDir, e.Name())); err != nil {
			return fmt.Errorf("remove stale temp dir: %w", err)
		}
	}
	return nil
}

// Insert stores a queued job and returns its position in the queue.
func (s *Store) Insert(ctx context.Context, j NewJob) (int, error) {
	langs, err := json.Marshal(j.Languages)
	if err != nil {
		return 0, err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO jobs (id, script_path, demo_name, languages, status, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		j.ID, j.ScriptPath, j.DemoName, string(langs), Queued, s.stamp())
	if err != nil {
		return 0, fmt.Errorf("insert job: %w", err)
	}
	return s.Position(ctx, j.ID)
}

// Position is 1 plus the queued jobs submitted before id; rowid breaks ties
// between jobs stamped with the same instant. It is 0 for a job that is not
// queued and ErrNotFound for an unknown one.
func (s *Store) Position(ctx context.Context, id string) (int, error) {
	var pos sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
SELECT CASE WHEN me.status = ? THEN
  (SELECT COUNT(*) + 1 FROM jobs j
   WHERE j.status = ? AND (j.created_at, j.rowid) < (me.created_at, me.rowid))
END
FROM jobs me WHERE me.id = ?`, Queued, Queued, id).Scan(&pos)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("position of %s: %w", id, err)
	}
	return int(pos.Int64), nil
}

// Get returns the job with id, or ErrNotFound. A queued job carries its
// position.
func (s *Store) Get(ctx context.Context, id string) (Job, error) {
	j, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("get job %s: %w", id, err)
	}
	if j.Position, err = s.Position(ctx, id); err != nil {
		return Job{}, err
	}
	return j, nil
}

// next returns the oldest queued job, or ErrEmpty.
func (s *Store) next(ctx context.Context) (Job, error) {
	j, err := scan(s.db.QueryRowContext(ctx,
		`SELECT `+columns+` FROM jobs WHERE status = ? ORDER BY created_at, rowid LIMIT 1`, Queued))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrEmpty
	}
	if err != nil {
		return Job{}, fmt.Errorf("next job: %w", err)
	}
	return j, nil
}

func (s *Store) start(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status = ?, started_at = ? WHERE id = ?`, Running, s.stamp(), id)
	if err != nil {
		return fmt.Errorf("start job %s: %w", id, err)
	}
	return nil
}

// finish stores the result: failed when f is not nil, else succeeded.
func (s *Store) finish(ctx context.Context, id string, outs []Output, f *failure.Failure) error {
	status := Succeeded
	var outJSON, errJSON any
	if f != nil {
		status = Failed
		b, err := json.Marshal(f)
		if err != nil {
			return err
		}
		errJSON = string(b)
	} else {
		b, err := json.Marshal(outs)
		if err != nil {
			return err
		}
		outJSON = string(b)
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET status = ?, finished_at = ?, outputs_json = ?, error_json = ? WHERE id = ?`,
		status, s.stamp(), outJSON, errJSON, id)
	if err != nil {
		return fmt.Errorf("finish job %s: %w", id, err)
	}
	return nil
}

type rowScanner interface{ Scan(dest ...any) error }

func scan(r rowScanner) (Job, error) {
	var (
		j                           Job
		langs, created              string
		started, finished, out, err sql.NullString
	)
	if e := r.Scan(&j.ID, &j.ScriptPath, &j.DemoName, &langs, &j.Status, &created, &started, &finished, &out, &err); e != nil {
		return Job{}, e
	}
	if e := json.Unmarshal([]byte(langs), &j.Languages); e != nil {
		return Job{}, fmt.Errorf("decode languages: %w", e)
	}
	var e error
	if j.CreatedAt, e = time.Parse(timeLayout, created); e != nil {
		return Job{}, e
	}
	if j.StartedAt, e = parseTime(started); e != nil {
		return Job{}, e
	}
	if j.FinishedAt, e = parseTime(finished); e != nil {
		return Job{}, e
	}
	if out.Valid {
		if e := json.Unmarshal([]byte(out.String), &j.Outputs); e != nil {
			return Job{}, fmt.Errorf("decode outputs: %w", e)
		}
	}
	if err.Valid {
		j.Error = &failure.Failure{}
		if e := json.Unmarshal([]byte(err.String), j.Error); e != nil {
			return Job{}, fmt.Errorf("decode error: %w", e)
		}
	}
	return j, nil
}

func parseTime(s sql.NullString) (*time.Time, error) {
	if !s.Valid {
		return nil, nil
	}
	t, err := time.Parse(timeLayout, s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
