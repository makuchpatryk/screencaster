package queue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"screencaster/core/failure"
	"screencaster/core/lock"
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
		if _, err := s.Insert(context.Background(), NewJob{ID: id, ScriptPath: "demos/" + id + ".yaml", DemoName: id, Languages: []string{"en"}}); err != nil {
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
		j, err := s.next(ctx)
		if errors.Is(err, ErrEmpty) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, j.ID)
		if err := s.start(ctx, j.ID); err != nil {
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
	if err := s.start(ctx, "a"); err != nil {
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
	if err != nil || j.Status != Queued || j.Position != 2 || !slices.Equal(j.Languages, []string{"en"}) || !j.CreatedAt.Equal(instant) {
		t.Fatalf("Get(b) = %+v, %v", j, err)
	}

	step := 3
	outs := []Output{{Lang: "en", Path: "/work/output/a.en.mp4", DurationMs: 1200}}
	if err := s.start(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.finish(ctx, "a", outs, nil); err != nil {
		t.Fatal(err)
	}
	if j, _ = s.Get(ctx, "a"); j.Status != Succeeded || !slices.Equal(j.Outputs, outs) || j.StartedAt == nil || j.FinishedAt == nil || j.Position != 0 {
		t.Errorf("succeeded job = %+v", j)
	}

	f := &failure.Failure{Step: &step, Lang: "en", Action: "click", Target: "#x", Message: "timeout"}
	if err := s.finish(ctx, "b", nil, f); err != nil {
		t.Fatal(err)
	}
	if j, _ = s.Get(ctx, "b"); j.Status != Failed || j.Error == nil || *j.Error.Step != 3 || j.Error.Message != "timeout" {
		t.Errorf("failed job = %+v", j)
	}
}

// BR-009 / FR-015 AC: pre-seeded database, as left by a killed server.
func TestRecover_marksInterrupted(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	insert(t, s, "done", "running", "queued")
	if err := s.start(ctx, "done"); err != nil {
		t.Fatal(err)
	}
	if err := s.finish(ctx, "done", []Output{{Lang: "en", Path: "p"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.start(ctx, "running"); err != nil {
		t.Fatal(err)
	}

	if err := s.Recover(ctx); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"running", "queued"} {
		j, err := s.Get(ctx, id)
		if err != nil || j.Status != Failed || j.Error == nil || j.Error.Message != "interrupted" || j.FinishedAt == nil {
			t.Errorf("%s after recovery = %+v, %v", id, j, err)
		}
	}
	if j, _ := s.Get(ctx, "done"); j.Status != Succeeded || j.Error != nil {
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

// waitFor polls cond for up to two seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func statusOf(t *testing.T, s *Store, id string) Status {
	t.Helper()
	j, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return j.Status
}

func runWorker(t *testing.T, s *Store, render func(context.Context, Job) ([]Output, error), acquire func(context.Context) (func(), error)) *Worker {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w := NewWorker(s)
	done := make(chan struct{})
	go func() { w.Run(ctx, render, acquire); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return w
}

// FR-014 AC2: a held render lock keeps the job queued.
func TestWorker_jobStaysQueuedWhileLockHeld(t *testing.T) {
	s := openStore(t)
	insert(t, s, "a")

	lockFree := make(chan struct{})
	acquiring := make(chan struct{}, 1)
	var rendered sync.WaitGroup
	rendered.Add(1)
	acquire := func(ctx context.Context) (func(), error) {
		acquiring <- struct{}{}
		select {
		case <-lockFree:
			return func() {}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	render := func(context.Context, Job) ([]Output, error) {
		defer rendered.Done()
		return []Output{{Lang: "en"}}, nil
	}
	w := runWorker(t, s, render, acquire)
	w.Notify()

	<-acquiring // the worker is waiting on the lock
	time.Sleep(50 * time.Millisecond)
	if got := statusOf(t, s, "a"); got != Queued {
		t.Fatalf("status while lock held = %s, want queued", got)
	}

	close(lockFree)
	rendered.Wait()
	waitFor(t, "job succeeded", func() bool { return statusOf(t, s, "a") == Succeeded })
}

// BR-008 / FR-014 AC1: strictly one at a time, in submission order.
func TestWorker_oneAtATime(t *testing.T) {
	s := openStore(t)
	insert(t, s, "a", "b", "c")

	var (
		mu      sync.Mutex
		active  int
		maxSeen int
		order   []string
	)
	render := func(_ context.Context, j Job) ([]Output, error) {
		mu.Lock()
		active++
		maxSeen = max(maxSeen, active)
		order = append(order, j.ID)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		if j.ID == "b" {
			return nil, &failure.Failure{Message: "boom"}
		}
		return []Output{{Lang: "en", Path: j.ID}}, nil
	}
	w := runWorker(t, s, render, func(context.Context) (func(), error) { return func() {}, nil })
	w.Notify()

	waitFor(t, "all jobs finished", func() bool {
		return statusOf(t, s, "a") == Succeeded && statusOf(t, s, "b") == Failed && statusOf(t, s, "c") == Succeeded
	})
	mu.Lock()
	defer mu.Unlock()
	if maxSeen != 1 {
		t.Errorf("concurrent renders = %d, want 1", maxSeen)
	}
	if want := []string{"a", "b", "c"}; !slices.Equal(order, want) {
		t.Errorf("render order = %v, want %v", order, want)
	}
}

func TestWorker_plainErrorBecomesFailureMessage(t *testing.T) {
	s := openStore(t)
	insert(t, s, "a")
	w := runWorker(t, s,
		func(context.Context, Job) ([]Output, error) { return nil, errors.New("script not found: x") },
		func(context.Context) (func(), error) { return func() {}, nil })
	w.Notify()

	waitFor(t, "job failed", func() bool { return statusOf(t, s, "a") == Failed })
	if j, _ := s.Get(context.Background(), "a"); j.Error == nil || j.Error.Message != "script not found: x" {
		t.Errorf("error = %+v", j.Error)
	}
}

// ARCHITECTURE §6.4: shutdown leaves the job running; recovery reports it.
func TestWorker_shutdownLeavesJobRunning(t *testing.T) {
	s := openStore(t)
	insert(t, s, "a")
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	render := func(ctx context.Context, _ Job) ([]Output, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	w := NewWorker(s)
	done := make(chan struct{})
	go func() {
		w.Run(ctx, render, func(context.Context) (func(), error) { return func() {}, nil })
		close(done)
	}()
	w.Notify()
	<-started
	cancel()
	<-done

	if got := statusOf(t, s, "a"); got != Running {
		t.Errorf("status after shutdown = %s, want running", got)
	}
}

func TestWorker_drainsJobsQueuedBeforeStart(t *testing.T) {
	s := openStore(t)
	insert(t, s, "a") // inserted before any Notify
	runWorker(t, s,
		func(context.Context, Job) ([]Output, error) { return nil, nil },
		func(context.Context) (func(), error) { return func() {}, nil })

	waitFor(t, "job succeeded", func() bool { return statusOf(t, s, "a") == Succeeded })
}
