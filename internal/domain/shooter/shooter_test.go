package shooter

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"screencaster/internal/domain/failure"
	"screencaster/internal/domain/script"
)

// fakeSession logs every call in order, so a test reads the run off one slice.
// failAtCapture (1-based, 0 = never) fails that capture; failSel fails the
// click on that selector.
type fakeSession struct {
	log           []string
	captures      int
	failAtCapture int
	failSel       string
	aborted       int
	cancel        context.CancelFunc // called during the first action, if set
}

func (f *fakeSession) act(what string) error {
	f.log = append(f.log, what)
	if f.cancel != nil {
		f.cancel()
	}
	if what == "click "+f.failSel && f.failSel != "" {
		return errors.New("timeout 30000ms exceeded")
	}
	return nil
}

func (f *fakeSession) Start() error { f.log = append(f.log, "start"); return nil }
func (f *fakeSession) Abort()       { f.aborted++; f.log = append(f.log, "abort") }
func (f *fakeSession) Capture(_ script.Screenshot, path string) error {
	f.log = append(f.log, "capture "+path)
	f.captures++
	if f.captures == f.failAtCapture {
		return errors.New("element is not visible")
	}
	return nil
}
func (f *fakeSession) Goto(u string) error                       { return f.act("goto " + u) }
func (f *fakeSession) Click(sel string) error                    { return f.act("click " + sel) }
func (f *fakeSession) Hover(sel string) error                    { return f.act("hover " + sel) }
func (f *fakeSession) Select(sel, _ string) error                { return f.act("select " + sel) }
func (f *fakeSession) Press(sel, _ string) error                 { return f.act("press " + sel) }
func (f *fakeSession) ScrollIntoView(sel string) error           { return f.act("scroll " + sel) }
func (f *fakeSession) ScrollTo(int) error                        { return f.act("scrollTo") }
func (f *fakeSession) WaitVisible(sel string) error              { return f.act("wait " + sel) }
func (f *fakeSession) MoveTo(string, int) error                  { return nil }
func (f *fakeSession) Fill(sel, _ string, _ time.Duration) error { return f.act("fill " + sel) }

func newShooter(sess *fakeSession, got *LaunchOptions) Shooter {
	return Shooter{Launch: func(_ context.Context, o LaunchOptions) (Session, error) {
		if got != nil {
			*got = o
		}
		return sess, nil
	}}
}

func click(sel string) script.Step { return script.Step{Action: script.Click{Selector: sel}} }
func shot(sel string) script.Step {
	return script.Step{Action: script.Screenshot{Selector: sel}}
}

func TestShoot_runsStepsInOrderAndCapturesAtShotSteps(t *testing.T) {
	sess := &fakeSession{}
	steps := []script.Step{
		{Action: script.Goto{URL: "http://x/p"}}, shot(""), click("#a"), shot("#e"),
	}
	paths, err := newShooter(sess, nil).Shoot(context.Background(), Input{Steps: steps, Dir: "/out"})
	if err != nil {
		t.Fatal(err)
	}
	wantLog := []string{"start", "goto http://x/p", "capture /out/01.png", "click #a", "capture /out/02.png", "abort"}
	if !reflect.DeepEqual(sess.log, wantLog) {
		t.Errorf("calls = %v, want %v", sess.log, wantLog)
	}
	if want := []string{"/out/01.png", "/out/02.png"}; !reflect.DeepEqual(paths, want) {
		t.Errorf("paths = %v, want %v", paths, want)
	}
}

func TestShoot_launchesWithoutVideoWithTheScriptsTarget(t *testing.T) {
	state := &script.StorageState{Cookies: []script.Cookie{{Name: "s", Value: "v", URL: "http://x"}}}
	var got LaunchOptions
	_, err := newShooter(&fakeSession{}, &got).Shoot(context.Background(),
		Input{Steps: []script.Step{shot("")}, Dir: "/out", StorageState: state})
	if err != nil {
		t.Fatal(err)
	}
	if got.StorageState != state {
		t.Errorf("launch options = %+v, want the storage state", got)
	}
}

func TestShotName_widthGrowsPastNinetyNine(t *testing.T) {
	tests := []struct {
		k, n int
		want string
	}{
		{1, 1, "01.png"},
		{2, 5, "02.png"},
		{9, 9, "09.png"},
		{10, 99, "10.png"},
		{99, 99, "99.png"},
		{1, 100, "001.png"},
		{100, 100, "100.png"},
		{7, 1000, "0007.png"},
	}
	for _, tt := range tests {
		if got := ShotName(tt.k, tt.n); got != tt.want {
			t.Errorf("ShotName(%d, %d) = %q, want %q", tt.k, tt.n, got, tt.want)
		}
	}
}

func TestShoot_namesUseTheRunsShotCount(t *testing.T) {
	steps := make([]script.Step, 100)
	for i := range steps {
		steps[i] = shot("")
	}
	paths, err := newShooter(&fakeSession{}, nil).Shoot(context.Background(), Input{Steps: steps, Dir: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 100 || paths[0] != "d/001.png" || paths[99] != "d/100.png" {
		t.Errorf("paths = %d, first %q, last %q; want 100 names from 001.png to 100.png", len(paths), paths[0], paths[len(paths)-1])
	}
}

// k counts shots, not steps: the action steps in between do not use a number.
func TestShoot_numbersCountShotsNotSteps(t *testing.T) {
	steps := []script.Step{click("#a"), click("#b"), shot(""), click("#c"), shot("")}
	paths, err := newShooter(&fakeSession{}, nil).Shoot(context.Background(), Input{Steps: steps, Dir: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"d/01.png", "d/02.png"}; !reflect.DeepEqual(paths, want) {
		t.Errorf("paths = %v, want %v", paths, want)
	}
}

func TestShoot_captureErrorIsAStepFailure(t *testing.T) {
	tests := []struct {
		name       string
		step       script.Step
		wantTarget string
	}{
		{"element shot", shot("#e"), "#e"},
		{"annotated shot", script.Step{Action: script.Screenshot{Annotate: &script.Annotate{Selector: "#b", Box: true}}}, "#b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := &fakeSession{failAtCapture: 1}
			steps := []script.Step{click("#a"), tt.step, shot("#never")}
			paths, err := newShooter(sess, nil).Shoot(context.Background(), Input{Steps: steps, Dir: "d"})
			var f *failure.Failure
			if !errors.As(err, &f) {
				t.Fatalf("Shoot() error = %v, want *failure.Failure", err)
			}
			if f.Step == nil || *f.Step != 2 || f.Action != "screenshot" || f.Target != tt.wantTarget || f.Lang != "" {
				t.Errorf("failure = %+v, want step 2, action screenshot, target %q, no lang", f, tt.wantTarget)
			}
			if paths != nil {
				t.Errorf("paths = %v, want none on failure", paths)
			}
			if sess.captures != 1 {
				t.Errorf("captures = %d, want the run to stop at the failing one: %v", sess.captures, sess.log)
			}
		})
	}
}

func TestShoot_actionFailureStopsTheRun(t *testing.T) {
	sess := &fakeSession{failSel: "#bad"}
	_, err := newShooter(sess, nil).Shoot(context.Background(),
		Input{Steps: []script.Step{shot(""), click("#bad"), shot("")}, Dir: "d"})
	var f *failure.Failure
	if !errors.As(err, &f) || f.Step == nil || *f.Step != 2 || f.Action != "click" {
		t.Fatalf("Shoot() error = %v, want a click failure at step 2", err)
	}
	if sess.captures != 1 {
		t.Errorf("captures = %d, want 1: the second capture ran after the failed click: %v", sess.captures, sess.log)
	}
}

// The session is dropped on every path: success, a step failure, a failed
// launch step and a cancelled ctx.
func TestShoot_abortsTheSessionOnEveryPath(t *testing.T) {
	tests := []struct {
		name  string
		sess  *fakeSession
		steps []script.Step
	}{
		{"success", &fakeSession{}, []script.Step{shot("")}},
		{"action fails", &fakeSession{failSel: "#a"}, []script.Step{click("#a")}},
		{"capture fails", &fakeSession{failAtCapture: 1}, []script.Step{shot("")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _ = newShooter(tt.sess, nil).Shoot(context.Background(), Input{Steps: tt.steps, Dir: "d"})
			if tt.sess.aborted != 1 {
				t.Errorf("Abort called %d times, want 1", tt.sess.aborted)
			}
		})
	}
}

func TestShoot_cancelledCtxReturnsCtxErr(t *testing.T) {
	t.Run("before the run", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		sess := &fakeSession{}
		_, err := newShooter(sess, nil).Shoot(ctx, Input{Steps: []script.Step{shot("")}, Dir: "d"})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Shoot() error = %v, want context.Canceled", err)
		}
		if sess.aborted != 1 {
			t.Errorf("Abort called %d times, want 1", sess.aborted)
		}
	})
	t.Run("during an action", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		sess := &fakeSession{cancel: cancel, failSel: "#a"}
		_, err := newShooter(sess, nil).Shoot(ctx, Input{Steps: []script.Step{click("#a"), shot("")}, Dir: "d"})
		var f *failure.Failure
		if !errors.Is(err, context.Canceled) || errors.As(err, &f) {
			t.Errorf("Shoot() error = %v, want the bare ctx error, not a step failure", err)
		}
	})
}

func TestShoot_launchFailureIsReturned(t *testing.T) {
	boom := errors.New("no chromium")
	s := Shooter{Launch: func(context.Context, LaunchOptions) (Session, error) { return nil, boom }}
	_, err := s.Shoot(context.Background(), Input{Steps: []script.Step{shot("")}})
	if !errors.Is(err, boom) {
		t.Errorf("Shoot() error = %v, want it to wrap %v", err, boom)
	}
}

func TestShoot_onStepGetsEveryZeroBasedIndexBeforeItsStep(t *testing.T) {
	sess := &fakeSession{}
	type seen struct{ i, calls int } // calls: what the session had done when OnStep ran
	var got []seen
	in := Input{
		Steps:  []script.Step{click("#a"), shot(""), shot("")},
		Dir:    "d",
		OnStep: func(i int) { got = append(got, seen{i, len(sess.log)}) },
	}
	if _, err := newShooter(sess, nil).Shoot(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	// The log holds "start" before step 0; the click and the first capture add one each.
	if want := []seen{{0, 1}, {1, 2}, {2, 3}}; !reflect.DeepEqual(got, want) {
		t.Errorf("OnStep calls = %v, want %v", got, want)
	}
}
