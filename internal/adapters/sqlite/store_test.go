package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"screencaster/internal/adapters/lock"
	"screencaster/internal/app/jobs"
	"screencaster/internal/app/renderer"
	"screencaster/internal/domain/failure"
)

// instant is the one timestamp every job gets, so only rowid can order them.
var instant = time.Date(2026, 10, 3, 10, 15, 0, 0, time.UTC)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), ".screencaster", "jobs.db"), func() time.Time { return instant })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func insert(t *testing.T, s *Store, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := s.Insert(context.Background(), jobs.NewJob{ID: id, ScriptPath: "demos/" + id + ".yaml", Script: []byte("name: " + id + "\n"), DemoName: id, Languages: []string{"en"}}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestQueue_fifoOrder(t *testing.T) {
	s := openStore(t)
	insert(t, s, "c", "a", "b") // same timestamp: insertion order, not id order
	ctx := context.Background()

	var got []string
	for {
		j, err := s.Next(ctx)
		if errors.Is(err, jobs.ErrEmpty) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, j.ID)
		if err := s.Start(ctx, j.ID); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"c", "a", "b"}; !slices.Equal(got, want) {
		t.Errorf("dequeue order = %v, want %v", got, want)
	}
}

func TestQueue_positionCountsEarlierQueued(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	insert(t, s, "a", "b", "c")

	pos := func(id string) int {
		t.Helper()
		p, err := s.Position(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if pos("a") != 1 || pos("b") != 2 || pos("c") != 3 {
		t.Fatalf("positions = %d %d %d, want 1 2 3", pos("a"), pos("b"), pos("c"))
	}

	// FR-012 AC2: a job already running does not count; the next one is 1.
	if err := s.Start(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if pos("a") != 0 || pos("b") != 1 || pos("c") != 2 {
		t.Errorf("after start: %d %d %d, want 0 1 2", pos("a"), pos("b"), pos("c"))
	}
	if _, err := s.Position(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Position(unknown) = %v, want ErrNotFound", err)
	}
}

func TestQueue_getRoundTrip(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	insert(t, s, "a", "b")

	if _, err := s.Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(unknown) = %v, want ErrNotFound", err)
	}
	j, err := s.Get(ctx, "b")
	if err != nil || j.Status != jobs.Queued || j.Position != 2 || !slices.Equal(j.Languages, []string{"en"}) || !j.CreatedAt.Equal(instant) {
		t.Fatalf("Get(b) = %+v, %v", j, err)
	}

	step := 3
	outs := []renderer.Output{{Lang: "en", Path: "/work/output/a.en.mp4", DurationMs: 1200}}
	if err := s.Start(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, "a", outs, nil); err != nil {
		t.Fatal(err)
	}
	if j, _ = s.Get(ctx, "a"); j.Status != jobs.Succeeded || !slices.Equal(j.Outputs, outs) || j.StartedAt == nil || j.FinishedAt == nil || j.Position != 0 {
		t.Errorf("succeeded job = %+v", j)
	}

	f := &failure.Failure{Step: &step, Lang: "en", Action: "click", Target: "#x", Message: "timeout"}
	if err := s.Finish(ctx, "b", nil, f); err != nil {
		t.Fatal(err)
	}
	if j, _ = s.Get(ctx, "b"); j.Status != jobs.Failed || j.Error == nil || *j.Error.Step != 3 || j.Error.Message != "timeout" {
		t.Errorf("failed job = %+v", j)
	}
}

// BR-009 / FR-015 AC: pre-seeded database, as left by a killed server.
func TestRecover_marksInterrupted(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	insert(t, s, "done", "running", "queued")
	if err := s.Start(ctx, "done"); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, "done", []renderer.Output{{Lang: "en", Path: "p"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctx, "running"); err != nil {
		t.Fatal(err)
	}

	if err := s.Recover(ctx); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"running", "queued"} {
		j, err := s.Get(ctx, id)
		if err != nil || j.Status != jobs.Failed || j.Error == nil || j.Error.Message != "interrupted" || j.FinishedAt == nil {
			t.Errorf("%s after recovery = %+v, %v", id, j, err)
		}
	}
	if j, _ := s.Get(ctx, "done"); j.Status != jobs.Succeeded || j.Error != nil {
		t.Errorf("succeeded job touched by recovery: %+v", j)
	}
}

// staleTemp is a state dir with a lock path and a tmp dir holding one run.
func staleTemp(t *testing.T) (lockPath, tmp string) {
	t.Helper()
	dir := t.TempDir()
	tmp = filepath.Join(dir, "tmp")
	if err := os.MkdirAll(filepath.Join(tmp, "stale-run", "en"), 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "render.lock"), tmp
}

// BR-009 / ARCHITECTURE §11: leftovers of interrupted renders are removed.
func TestRemoveStaleTemp_emptiesTmpWhenLockFree(t *testing.T) {
	lockPath, tmp := staleTemp(t)
	if err := RemoveStaleTemp(lockPath, tmp); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Errorf("tmp not emptied: %v", entries)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Errorf("tmp dir itself must stay: %v", err)
	}
	if err := RemoveStaleTemp(lockPath, filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Errorf("RemoveStaleTemp with no tmp dir = %v, want nil", err)
	}
}

// ARCHITECTURE §6.1, §6.3: a CLI render holding the lock owns its temp dir.
func TestRemoveStaleTemp_keepsTmpWhileRenderRuns(t *testing.T) {
	lockPath, tmp := staleTemp(t)
	l, err := lock.TryAcquire(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Release() }()

	if err := RemoveStaleTemp(lockPath, tmp); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "stale-run", "en")); err != nil {
		t.Errorf("running render's temp dir removed: %v", err)
	}
}

func TestQueue_scriptSnapshotRoundTrips(t *testing.T) { // ARCHITECTURE §10
	s := openStore(t)
	insert(t, s, "a")
	j, err := s.Get(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if string(j.Script) != "name: a\n" {
		t.Errorf("Script = %q, want the submitted bytes", j.Script)
	}
}

// A jobs.db from before the snapshot gets the column on open; its rows keep a
// NULL script.
func TestOpen_addsScriptColumnToAnOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE jobs (
  id TEXT PRIMARY KEY, script_path TEXT NOT NULL, demo_name TEXT NOT NULL, languages TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('queued','running','succeeded','failed')),
  created_at TEXT NOT NULL, started_at TEXT, finished_at TEXT, outputs_json TEXT, error_json TEXT);
INSERT INTO jobs (id, script_path, demo_name, languages, status, created_at)
  VALUES ('old', 'demos/a.yaml', 'a', '["en"]', 'succeeded', '2026-10-03T10:15:00.000000000Z');`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	for i := range 2 { // the second open finds the column and leaves it
		s, err := Open(path, func() time.Time { return instant })
		if err != nil {
			t.Fatal(err)
		}
		j, err := s.Get(context.Background(), "old")
		if err != nil || j.Script != nil || j.Status != jobs.Succeeded {
			t.Errorf("old row = %+v, %v; want it unchanged with no script", j, err)
		}
		if _, err := s.Insert(context.Background(), jobs.NewJob{ID: "new" + strconv.Itoa(i), ScriptPath: "demos/b.yaml", Script: []byte("x"), DemoName: "b", Languages: []string{"en"}}); err != nil {
			t.Errorf("insert after migration: %v", err)
		}
		_ = s.Close()
	}
}
