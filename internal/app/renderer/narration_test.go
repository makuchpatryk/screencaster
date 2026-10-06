package renderer

import (
	"context"
	"errors"
	"hash/fnv"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"screencaster/internal/domain/failure"
	"screencaster/internal/domain/script"
)

// poolTTS is a Synthesizer safe for concurrent calls. The text of a step says
// what happens: "ok", "slow-fail" (fails after a delay), "fail" (fails at once)
// or "block" (waits for ctx). Every call sleeps a few ms that depend on its
// text, so clips finish out of order.
type poolTTS struct {
	mu                    sync.Mutex
	inFlight, maxInFlight int
	// barrier, when set, makes each call wait until that many are in flight.
	barrier int
	reached chan struct{}
	once    sync.Once
	started chan struct{} // receives once per call that began, when set
}

func (p *poolTTS) Synthesize(ctx context.Context, _ /*voice*/, text, out string) (time.Duration, error) {
	p.mu.Lock()
	p.inFlight++
	p.maxInFlight = max(p.maxInFlight, p.inFlight)
	n := p.inFlight
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.inFlight--
		p.mu.Unlock()
	}()
	if p.started != nil {
		p.started <- struct{}{}
	}
	if p.barrier > 0 {
		if n >= p.barrier {
			p.once.Do(func() { close(p.reached) })
		}
		select {
		case <-p.reached:
		case <-time.After(5 * time.Second):
			return 0, errors.New("barrier: pool never ran that many clips at once")
		}
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(text + out))
	time.Sleep(time.Duration(h.Sum32()%5) * time.Millisecond)

	switch {
	case strings.HasPrefix(text, "slow-fail"):
		time.Sleep(40 * time.Millisecond)
		return 0, errors.New(text)
	case strings.HasPrefix(text, "fail"):
		return 0, errors.New(text)
	case strings.HasPrefix(text, "block"):
		<-ctx.Done()
		return 0, ctx.Err()
	}
	return time.Duration(len(text)) * time.Second, nil
}

// narrated returns steps whose English narration is texts[i]; "" is un-narrated.
func narrated(texts ...string) []script.Step {
	steps := make([]script.Step, len(texts))
	for i, t := range texts {
		steps[i] = script.Step{Action: script.Pause{D: time.Second}}
		if t != "" {
			steps[i].Narration = map[string]string{"en": t, "pl": "inne"}
		}
	}
	return steps
}

func poolDeps(tts Synthesizer) Deps { return Deps{TTS: tts, Now: time.Now} }

func TestSynthesizeAll_sameResultAsSequential(t *testing.T) { // decision 71
	steps := narrated("one", "", "three!", "four", "", "six", "seven77", "eight888", "n", "ten")
	seq, err := synthesizeAll(context.Background(), poolDeps(&poolTTS{}), "v", "en", steps, "/tmp/clips", 1)
	if err != nil {
		t.Fatal(err)
	}
	for range 20 { // out-of-order finishes differ run to run; the result must not
		got, err := synthesizeAll(context.Background(), poolDeps(&poolTTS{}), "v", "en", steps, "/tmp/clips", 4)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.durations, seq.durations) || !reflect.DeepEqual(got.paths, seq.paths) {
			t.Fatalf("pool = %v %v\nsequential = %v %v", got.durations, got.paths, seq.durations, seq.paths)
		}
	}
	if want := filepath.Join("/tmp/clips", "3.wav"); seq.paths[2] != want {
		t.Errorf("clip for step 3 = %q, want %q", seq.paths[2], want)
	}
	if _, ok := seq.paths[1]; ok {
		t.Error("un-narrated step 2 got a clip")
	}
	if seq.durations[3] != 4*time.Second {
		t.Errorf("duration of step 4 = %v, want 4s", seq.durations[3])
	}
}

func TestSynthesizeAll_onlyTheRequestedLanguage(t *testing.T) {
	got, err := synthesizeAll(context.Background(), poolDeps(&poolTTS{}), "v", "pl", narrated("one", ""), "/c", 2)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[int]time.Duration{0: 4 * time.Second}; !reflect.DeepEqual(got.durations, want) { // "inne"
		t.Errorf("durations = %v, want %v", got.durations, want)
	}
}

func TestSynthesizeAll_clipsRunInParallelUpToThePoolSize(t *testing.T) {
	const workers = 3
	tts := &poolTTS{barrier: workers, reached: make(chan struct{})} // fails if fewer than 3 run at once
	steps := narrated("a", "b", "c", "d", "e", "f", "g", "h", "i", "j")

	if _, err := synthesizeAll(context.Background(), poolDeps(tts), "v", "en", steps, "/c", workers); err != nil {
		t.Fatal(err)
	}
	if got := tts.max(); got != workers {
		t.Errorf("max clips in flight = %d, want exactly the pool size %d", got, workers)
	}
}

func TestSynthesizeAll_poolSizeOneIsSequential(t *testing.T) {
	tts := &poolTTS{}
	if _, err := synthesizeAll(context.Background(), poolDeps(tts), "v", "en", narrated("a", "b", "c", "d"), "/c", 1); err != nil {
		t.Fatal(err)
	}
	if got := tts.max(); got != 1 {
		t.Errorf("max clips in flight = %d, want 1", got)
	}
}

func TestSynthesizeAll_reportsTheLowestFailingStep(t *testing.T) { // what the sequential loop reports
	// Step 5 fails at once, step 3 only after a delay: the report is still step 3.
	steps := narrated("ok", "ok", "slow-fail 3", "ok", "fail 5", "ok", "ok", "ok")
	for range 20 {
		_, err := synthesizeAll(context.Background(), poolDeps(&poolTTS{}), "v", "en", steps, "/c", 4)
		var f *failure.Failure
		if !errors.As(err, &f) {
			t.Fatalf("err = %v, want *failure.Failure", err)
		}
		if want := "tts failed at step 3 (en): slow-fail 3"; f.Error() != want {
			t.Fatalf("failure = %q, want %q", f.Error(), want)
		}
	}
}

func TestSynthesizeAll_cancelReturnsCtxErr(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tts := &poolTTS{started: make(chan struct{}, 8)}
	go func() {
		<-tts.started // a clip is in flight
		cancel()
	}()

	_, err := synthesizeAll(ctx, poolDeps(tts), "v", "en", narrated("block", "block", "block", "block", "block", "block"), "/c", 2)
	var f *failure.Failure
	if !errors.Is(err, context.Canceled) || errors.As(err, &f) {
		t.Errorf("err = %v, want context.Canceled and no Failure", err)
	}
}

func TestSynthesizeAll_cancelledBeforeStartStartsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tts := &poolTTS{}

	_, err := synthesizeAll(ctx, poolDeps(tts), "v", "en", narrated("a", "b"), "/c", 2)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if got := tts.max(); got != 0 {
		t.Errorf("%d clips started after cancel, want none", got)
	}
}

func TestSynthesizeAll_summedSynthesisTime(t *testing.T) {
	var now atomic.Int64 // each Now() call moves the clock by 1 s, so each clip measures 1 s
	d := Deps{TTS: &poolTTS{}, Now: func() time.Time { return time.Unix(now.Add(1), 0) }}
	got, err := synthesizeAll(context.Background(), d, "v", "en", narrated("a"), "/c", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.work != time.Second {
		t.Errorf("work = %v, want 1s", got.work)
	}
}

func (p *poolTTS) max() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxInFlight
}
