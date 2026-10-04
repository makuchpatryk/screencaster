package recorder

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"screencaster/core/failure"
	"screencaster/core/script"
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
// selector in failSel, and counts Close calls.
type fakeSession struct {
	clk        *clock
	actionTime time.Duration
	failSel    string
	closed     int
	aborted    int
	cancel     context.CancelFunc // called during the first action, if set
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

func (f *fakeSession) Start() error                              { return nil }
func (f *fakeSession) Abort()                                    { f.aborted++ }
func (f *fakeSession) Close() (string, error)                    { f.closed++; return "/tmp/v.webm", nil }
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

func click(sel string) script.Step { return script.Step{Action: "click", Selector: sel} }

func TestRecord_narratedStepWaitsForClip(t *testing.T) { // FR-007 AC1
	clk := &clock{t: time.Unix(1000, 0)}
	sess := &fakeSession{clk: clk, actionTime: 500 * time.Millisecond}
	// Step 0 only moves the clock past the lead-in clamp, so offsets 1 and 2 are exact.
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
	want := []time.Duration{0, 500*time.Millisecond - LeadInCompensation, time.Second - LeadInCompensation}
	if !reflect.DeepEqual(out.Offsets, want) {
		t.Errorf("offsets = %v, want %v", out.Offsets, want)
	}
	if out.WebmPath != "/tmp/v.webm" {
		t.Errorf("WebmPath = %q", out.WebmPath)
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

func TestRecord_offsetsAreShiftedByLeadIn(t *testing.T) { // ADR-46, spike S1
	clk := &clock{t: time.Unix(1000, 0)}
	sess := &fakeSession{clk: clk, actionTime: time.Second}

	out, err := newRecorder(sess).Record(context.Background(), Input{
		Steps: []script.Step{click("#a"), click("#b")}, Lang: "en",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Offsets[0] != 0 {
		t.Errorf("first offset = %v, want 0 (clamped, not negative)", out.Offsets[0])
	}
	if want := time.Second - LeadInCompensation; out.Offsets[1] != want {
		t.Errorf("second offset = %v, want %v", out.Offsets[1], want)
	}
}

func TestRecord_launchGetsContextSettings(t *testing.T) { // FR-004
	clk := &clock{t: time.Unix(1000, 0)}
	sess := &fakeSession{clk: clk}
	var got LaunchOptions
	r := newRecorder(sess)
	r.Launch = func(_ context.Context, o LaunchOptions) (Session, error) { got = o; return sess, nil }

	_, err := r.Record(context.Background(), Input{
		Steps: []script.Step{click("#a")}, Lang: "en",
		Dir: "/tmp/run/en", BaseURL: "http://app.test", StorageState: "/work/auth/s.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := LaunchOptions{VideoDir: "/tmp/run/en", BaseURL: "http://app.test", StorageState: "/work/auth/s.json"}
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
