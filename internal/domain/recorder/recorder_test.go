package recorder

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"screencaster/internal/domain/failure"
	"screencaster/internal/domain/script"
)

// clock is a fake time source: only Sleep and fake actions advance it.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }
func (c *clock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.t = c.t.Add(d)
	return nil
}

// fakeSession advances the clock by actionTime for every action, fails the
// selector in failSel, and counts Close calls. It records when Start,
// HideMarker and Close ran on the fake clock.
type fakeSession struct {
	clk        *clock
	actionTime time.Duration
	failSel    string
	hideErr    error
	closed     int
	aborted    int
	cancel     context.CancelFunc // called during the first action, if set

	startedAt, hiddenAt, closedAt time.Time
}

func (f *fakeSession) act(sel string) error {
	f.clk.t = f.clk.t.Add(f.actionTime)
	if f.cancel != nil {
		f.cancel()
	}
	if sel == f.failSel {
		return errors.New("timeout 30000ms exceeded")
	}
	return nil
}

func (f *fakeSession) Start() error      { f.startedAt = f.clk.t; return nil }
func (f *fakeSession) HideMarker() error { f.hiddenAt = f.clk.t; return f.hideErr }
func (f *fakeSession) Abort()            { f.aborted++ }
func (f *fakeSession) Close() (string, error) {
	f.closed++
	f.closedAt = f.clk.t
	return "/tmp/v.webm", nil
}
func (f *fakeSession) Goto(string) error                         { return f.act("") }
func (f *fakeSession) Click(sel string) error                    { return f.act(sel) }
func (f *fakeSession) Hover(sel string) error                    { return f.act(sel) }
func (f *fakeSession) Select(sel, _ string) error                { return f.act(sel) }
func (f *fakeSession) Press(sel, _ string) error                 { return f.act(sel) }
func (f *fakeSession) ScrollIntoView(sel string) error           { return f.act(sel) }
func (f *fakeSession) ScrollTo(int) error                        { return f.act("") }
func (f *fakeSession) WaitVisible(sel string) error              { return f.act(sel) }
func (f *fakeSession) MoveTo(string, int) error                  { return nil }
func (f *fakeSession) Fill(sel, _ string, _ time.Duration) error { return f.act(sel) }

func newRecorder(sess *fakeSession) Recorder {
	return Recorder{
		Launch: func(context.Context, LaunchOptions) (Session, error) { return sess, nil },
		Now:    sess.clk.now,
		Sleep:  sess.clk.sleep,
	}
}

func click(sel string) script.Step { return script.Step{Action: script.Click{Selector: sel}} }

func TestRecord_narratedStepWaitsForClip(t *testing.T) { // FR-007 AC1
	clk := &clock{t: time.Unix(1000, 0)}
	sess := &fakeSession{clk: clk, actionTime: 500 * time.Millisecond}
	steps := []script.Step{click("#lead"), click("#a"), click("#b")}

	out, err := newRecorder(sess).Record(context.Background(), Input{
		Steps: steps, Clips: map[int]time.Duration{1: 4000 * time.Millisecond}, Lang: "en",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Offsets[2] - out.Offsets[1]; got != 4000*time.Millisecond {
		t.Errorf("step 2 started %v after step 1, want 4s", got)
	}
}

func TestRecord_longActionBeatsShortClip(t *testing.T) { // max(action end, start+clip)
	clk := &clock{t: time.Unix(1000, 0)}
	sess := &fakeSession{clk: clk, actionTime: 3 * time.Second}

	out, err := newRecorder(sess).Record(context.Background(), Input{
		Steps: []script.Step{click("#lead"), click("#a"), click("#b")},
		Clips: map[int]time.Duration{1: time.Second}, Lang: "en",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Offsets[2] - out.Offsets[1]; got != 3*time.Second {
		t.Errorf("step 2 started %v after step 1, want 3s", got)
	}
}

func TestRecord_unnarratedStepContinuesImmediately(t *testing.T) { // FR-007 AC2
	clk := &clock{t: time.Unix(1000, 0)}
	sess := &fakeSession{clk: clk, actionTime: 500 * time.Millisecond}

	out, err := newRecorder(sess).Record(context.Background(), Input{
		Steps: []script.Step{click("#a"), click("#b"), click("#c")}, Lang: "en",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{narrationLag, narrationLag + 500*time.Millisecond, narrationLag + time.Second}
	if !reflect.DeepEqual(out.Offsets, want) {
		t.Errorf("offsets = %v, want %v", out.Offsets, want)
	}
	if out.WebmPath != "/tmp/v.webm" {
		t.Errorf("WebmPath = %q", out.WebmPath)
	}
}

func TestRecord_recordingCoversLastNarration(t *testing.T) { // the video always covers the audio
	clk := &clock{t: time.Unix(1000, 0)}
	sess := &fakeSession{clk: clk, actionTime: 500 * time.Millisecond}

	_, err := newRecorder(sess).Record(context.Background(), Input{
		Steps: []script.Step{click("#a")}, Clips: map[int]time.Duration{0: 4 * time.Second}, Lang: "en",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, min := sess.closedAt.Sub(sess.hiddenAt), narrationLag+4*time.Second; got < min {
		t.Errorf("recording lasted %v, want at least %v (the placed clip's end)", got, min)
	}
}

func TestRecord_noTailWaitWhenStepsOutlastNarration(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	sess := &fakeSession{clk: clk, actionTime: 2 * time.Second}

	_, err := newRecorder(sess).Record(context.Background(), Input{
		Steps: []script.Step{click("#a"), click("#b")}, Clips: map[int]time.Duration{0: time.Second}, Lang: "en",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sess.closedAt.Sub(sess.hiddenAt), 4*time.Second; got != want {
		t.Errorf("recording lasted %v, want %v (two actions, no extra wait)", got, want)
	}
}

func TestRecord_stepFailureAbortsBrowserAndNamesStepAndLang(t *testing.T) { // BR-004, FR-008
	clk := &clock{t: time.Unix(1000, 0)}
	sess := &fakeSession{clk: clk, failSel: "#missing"}

	_, err := newRecorder(sess).Record(context.Background(), Input{
		Steps: []script.Step{click("#a"), click("#b"), click("#c"), click("#missing"), click("#e")},
		Lang:  "en",
	})

	var f *failure.Failure
	if !errors.As(err, &f) {
		t.Fatalf("err = %v, want *failure.Failure", err)
	}
	if f.Step == nil || *f.Step != 4 || f.Lang != "en" || f.Action != "click" || f.Target != "#missing" {
		t.Errorf("failure = %+v, want step 4, lang en, click #missing", f)
	}
	if sess.aborted != 1 || sess.closed != 0 {
		t.Errorf("Abort called %d times, Close %d times, want 1 and 0", sess.aborted, sess.closed)
	}
}

func TestRecord_cancelAbortsBrowser(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	ctx, cancel := context.WithCancel(context.Background())
	sess := &fakeSession{clk: clk, cancel: cancel}

	_, err := newRecorder(sess).Record(ctx, Input{
		Steps: []script.Step{click("#a"), click("#b")}, Lang: "en",
		Clips: map[int]time.Duration{0: time.Second},
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if sess.aborted != 1 || sess.closed != 0 {
		t.Errorf("Abort called %d times, Close %d times, want 1 and 0", sess.aborted, sess.closed)
	}
}

func TestRecord_launchFailureIsReported(t *testing.T) {
	boom := errors.New("no chromium")
	r := Recorder{Launch: func(context.Context, LaunchOptions) (Session, error) { return nil, boom }}

	_, err := r.Record(context.Background(), Input{Lang: "pl"})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap %v", err, boom)
	}
}

func TestRecord_offsetsStartWhenTheMarkerIsHidden(t *testing.T) { // decision 69
	clk := &clock{t: time.Unix(1000, 0)}
	sess := &fakeSession{clk: clk, actionTime: time.Second}

	out, err := newRecorder(sess).Record(context.Background(), Input{
		Steps: []script.Step{click("#a"), click("#b")}, Lang: "en",
	})
	if err != nil {
		t.Fatal(err)
	}
	if held := sess.hiddenAt.Sub(sess.startedAt); held != MarkerHold {
		t.Errorf("marker shown for %v, want %v", held, MarkerHold)
	}
	if want := []time.Duration{narrationLag, narrationLag + time.Second}; !reflect.DeepEqual(out.Offsets, want) {
		t.Errorf("offsets = %v, want %v (from the marker's end, plus narrationLag)", out.Offsets, want)
	}
}

func TestRecord_hideMarkerFailureAbortsBrowser(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	boom := errors.New("page closed")
	sess := &fakeSession{clk: clk, hideErr: boom}

	_, err := newRecorder(sess).Record(context.Background(), Input{Steps: []script.Step{click("#a")}, Lang: "en"})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap %v", err, boom)
	}
	if sess.aborted != 1 || sess.closed != 0 {
		t.Errorf("Abort called %d times, Close %d times, want 1 and 0", sess.aborted, sess.closed)
	}
}

func TestRecord_cancelDuringMarkerHoldAbortsBrowser(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sess := &fakeSession{clk: clk}

	_, err := newRecorder(sess).Record(ctx, Input{Steps: []script.Step{click("#a")}, Lang: "en"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if sess.aborted != 1 || !sess.hiddenAt.IsZero() {
		t.Errorf("Abort called %d times, marker hidden: %v; want 1 abort and no hide", sess.aborted, !sess.hiddenAt.IsZero())
	}
}

func TestRecord_launchGetsContextSettings(t *testing.T) { // FR-004
	clk := &clock{t: time.Unix(1000, 0)}
	sess := &fakeSession{clk: clk}
	var got LaunchOptions
	r := newRecorder(sess)
	r.Launch = func(_ context.Context, o LaunchOptions) (Session, error) { got = o; return sess, nil }

	state := &script.StorageState{Cookies: []script.Cookie{{Name: "s", Value: "v", Domain: "app.test", Path: "/"}}}
	_, err := r.Record(context.Background(), Input{
		Steps: []script.Step{click("#a")}, Lang: "en",
		Dir: "/tmp/run/en", StorageState: state, StartImage: "/tmp/run/intro.png",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := LaunchOptions{VideoDir: "/tmp/run/en", StorageState: state, StartImage: "/tmp/run/intro.png"}
	if got != want {
		t.Errorf("launch options = %+v, want %+v", got, want)
	}
}

func TestRecord_onStepReportsEachStepBeforeItRuns(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	sess := &fakeSession{clk: clk, failSel: "#c"}
	var seen []int

	_, _ = newRecorder(sess).Record(context.Background(), Input{
		Steps: []script.Step{click("#a"), click("#b"), click("#c"), click("#d")}, Lang: "en",
		OnStep: func(i int) { seen = append(seen, i) },
	})
	if want := []int{0, 1, 2}; !reflect.DeepEqual(seen, want) {
		t.Errorf("OnStep calls = %v, want %v", seen, want)
	}
}
