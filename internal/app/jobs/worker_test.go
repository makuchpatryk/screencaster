package jobs

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"screencaster/internal/app/renderer"
	"screencaster/internal/domain/failure"
)

// fakeStore is the job table in memory, FIFO by insertion like the real one.
type fakeStore struct {
	mu   sync.Mutex
	jobs []*Job
}

func openStore(*testing.T) *fakeStore { return &fakeStore{} }

func insert(_ *testing.T, s *fakeStore, ids ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		s.jobs = append(s.jobs, &Job{ID: id, Script: []byte("name: " + id + "\n"), Status: Queued})
	}
}

// job returns the stored job; tests change it only while no worker runs it.
func (s *fakeStore) job(id string) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

func (s *fakeStore) Next(ctx context.Context) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.Status == Queued {
			return *j, nil
		}
	}
	return Job{}, ErrEmpty
}

func (s *fakeStore) Start(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.ID == id {
			j.Status = Running
		}
	}
	return nil
}

func (s *fakeStore) Finish(_ context.Context, id string, outs []renderer.Output, f *failure.Failure) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.ID == id {
			j.Status, j.Outputs, j.Error = Succeeded, outs, f
			if f != nil {
				j.Status = Failed
			}
		}
	}
	return nil
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

func statusOf(t *testing.T, s *fakeStore, id string) Status {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.ID == id {
			return j.Status
		}
	}
	t.Fatalf("no job %s", id)
	return ""
}

func runWorker(t *testing.T, s *fakeStore, render RenderFunc, acquire AcquireFunc) *Worker {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w := NewWorker(s, render, acquire)
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
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
	render := func(context.Context, Job) ([]renderer.Output, error) {
		defer rendered.Done()
		return []renderer.Output{{Lang: "en"}}, nil
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
	render := func(_ context.Context, j Job) ([]renderer.Output, error) {
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
		return []renderer.Output{{Lang: "en", Path: j.ID}}, nil
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
		func(context.Context, Job) ([]renderer.Output, error) { return nil, errors.New("script not found: x") },
		func(context.Context) (func(), error) { return func() {}, nil })
	w.Notify()

	waitFor(t, "job failed", func() bool { return statusOf(t, s, "a") == Failed })
	if j := s.job("a"); j.Error == nil || j.Error.Message != "script not found: x" {
		t.Errorf("error = %+v", j.Error)
	}
}

// ARCHITECTURE §6.4: shutdown leaves the job running; recovery reports it.
func TestWorker_shutdownLeavesJobRunning(t *testing.T) {
	s := openStore(t)
	insert(t, s, "a")
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	render := func(ctx context.Context, _ Job) ([]renderer.Output, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	w := NewWorker(s, render, func(context.Context) (func(), error) { return func() {}, nil })
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
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
		func(context.Context, Job) ([]renderer.Output, error) { return nil, nil },
		func(context.Context) (func(), error) { return func() {}, nil })

	waitFor(t, "job succeeded", func() bool { return statusOf(t, s, "a") == Succeeded })
}

func TestWorker_jobWithoutSnapshotFails(t *testing.T) {
	s := openStore(t)
	insert(t, s, "a")
	s.job("a").Script = nil // a row from before the snapshot column
	var rendered atomic.Bool
	w := runWorker(t, s,
		func(context.Context, Job) ([]renderer.Output, error) { rendered.Store(true); return nil, nil },
		func(context.Context) (func(), error) { return func() {}, nil })
	w.Notify()

	waitFor(t, "job failed", func() bool { return statusOf(t, s, "a") == Failed })
	if j := s.job("a"); j.Error == nil || j.Error.Message != "job has no script snapshot" {
		t.Errorf("error = %+v", j.Error)
	}
	if rendered.Load() {
		t.Error("a job without a snapshot was rendered")
	}
}
