// Package sqlite is the job store of the MCP server: the jobs table, its
// stored JSON and the startup clean-up (FR-014, FR-015, ARCHITECTURE §10). It
// implements jobs.Store and knows nothing about MCP.
package sqlite

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

	"screencaster/internal/adapters/lock"
	"screencaster/internal/app/jobs"
	"screencaster/internal/app/renderer"
	"screencaster/internal/domain/failure"
)

var _ jobs.Store = (*Store)(nil)

// ErrNotFound means no job has the requested ID.
var ErrNotFound = errors.New("job not found")

// timeLayout is RFC 3339 in UTC, fixed-width to the nanosecond. RFC3339Nano
// drops trailing zeros, which would break the lexical ordering that FIFO
// relies on.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

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
  error_json   TEXT,
  script       TEXT
);
CREATE INDEX IF NOT EXISTS jobs_status_created ON jobs(status, created_at);`

const columns = `id, script_path, demo_name, languages, status, created_at, started_at, finished_at, outputs_json, error_json, script`

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
	if err := addScriptColumn(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, now: now}, nil
}

// addScriptColumn brings a jobs.db from before the script snapshot up to
// date. Old rows keep a NULL there; Recover has already failed every one that
// was still queued or running, so none of them is ever rendered.
func addScriptColumn(db *sql.DB) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info('jobs')`)
	if err != nil {
		return fmt.Errorf("read jobs columns: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("read jobs columns: %w", err)
		}
		if name == "script" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read jobs columns: %w", err)
	}
	if _, err := db.Exec(`ALTER TABLE jobs ADD COLUMN script TEXT`); err != nil {
		return fmt.Errorf("add script column: %w", err)
	}
	return nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) stamp() string { return s.now().UTC().Format(timeLayout) }

// Recover implements BR-009: every queued or running job becomes failed with
// the message "interrupted". It runs before the server accepts calls.
func (s *Store) Recover(ctx context.Context) error {
	errJSON, err := json.Marshal(toFailureRecord(failure.Interrupted()))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE jobs SET status = ?, error_json = ?, finished_at = ? WHERE status IN (?, ?)`,
		jobs.Failed, string(errJSON), s.stamp(), jobs.Queued, jobs.Running)
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
func (s *Store) Insert(ctx context.Context, j jobs.NewJob) (int, error) {
	langs, err := json.Marshal(j.Languages)
	if err != nil {
		return 0, err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO jobs (id, script_path, demo_name, languages, status, created_at, script) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.ScriptPath, j.DemoName, string(langs), jobs.Queued, s.stamp(), string(j.Script))
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
FROM jobs me WHERE me.id = ?`, jobs.Queued, jobs.Queued, id).Scan(&pos)
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
func (s *Store) Get(ctx context.Context, id string) (jobs.Job, error) {
	j, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return jobs.Job{}, ErrNotFound
	}
	if err != nil {
		return jobs.Job{}, fmt.Errorf("get job %s: %w", id, err)
	}
	if j.Position, err = s.Position(ctx, id); err != nil {
		return jobs.Job{}, err
	}
	return j, nil
}

// Next returns the oldest queued job, or jobs.ErrEmpty.
func (s *Store) Next(ctx context.Context) (jobs.Job, error) {
	j, err := scan(s.db.QueryRowContext(ctx,
		`SELECT `+columns+` FROM jobs WHERE status = ? ORDER BY created_at, rowid LIMIT 1`, jobs.Queued))
	if errors.Is(err, sql.ErrNoRows) {
		return jobs.Job{}, jobs.ErrEmpty
	}
	if err != nil {
		return jobs.Job{}, fmt.Errorf("next job: %w", err)
	}
	return j, nil
}

// Start marks the job running.
func (s *Store) Start(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status = ?, started_at = ? WHERE id = ?`, jobs.Running, s.stamp(), id)
	if err != nil {
		return fmt.Errorf("start job %s: %w", id, err)
	}
	return nil
}

// Finish stores the result: failed when f is not nil, else succeeded.
func (s *Store) Finish(ctx context.Context, id string, outs []renderer.Output, f *failure.Failure) error {
	status := jobs.Succeeded
	var outJSON, errJSON any
	if f != nil {
		status = jobs.Failed
		b, err := json.Marshal(toFailureRecord(f))
		if err != nil {
			return err
		}
		errJSON = string(b)
	} else {
		b, err := json.Marshal(toOutputRecords(outs))
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

func scan(r rowScanner) (jobs.Job, error) {
	var (
		j                           jobs.Job
		langs, created              string
		started, finished, out, err sql.NullString
		script                      sql.NullString
	)
	if e := r.Scan(&j.ID, &j.ScriptPath, &j.DemoName, &langs, &j.Status, &created, &started, &finished, &out, &err, &script); e != nil {
		return jobs.Job{}, e
	}
	if script.Valid {
		j.Script = []byte(script.String)
	}
	if e := json.Unmarshal([]byte(langs), &j.Languages); e != nil {
		return jobs.Job{}, fmt.Errorf("decode languages: %w", e)
	}
	var e error
	if j.CreatedAt, e = time.Parse(timeLayout, created); e != nil {
		return jobs.Job{}, e
	}
	if j.StartedAt, e = parseTime(started); e != nil {
		return jobs.Job{}, e
	}
	if j.FinishedAt, e = parseTime(finished); e != nil {
		return jobs.Job{}, e
	}
	if out.Valid {
		var recs []outputRecord
		if e := json.Unmarshal([]byte(out.String), &recs); e != nil {
			return jobs.Job{}, fmt.Errorf("decode outputs: %w", e)
		}
		j.Outputs = fromOutputRecords(recs)
	}
	if err.Valid {
		var rec failureRecord
		if e := json.Unmarshal([]byte(err.String), &rec); e != nil {
			return jobs.Job{}, fmt.Errorf("decode error: %w", e)
		}
		j.Error = rec.failure()
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
