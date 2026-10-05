//go:build e2e

package e2e

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"screencaster/core/browser"
	"screencaster/core/failure"
	"screencaster/core/recorder"
	"screencaster/core/script"
)

func intp(n int) *int { return &n }

// A silent recording of the fixture app (FR-004, FR-005, FR-006): the
// storageState cookie shows #logged-in, a form is filled and submitted.
func TestRecord_fixtureApp(t *testing.T) {
	base := fixtureApp(t)
	steps := []script.Step{
		{Action: "goto", URL: "/index.html"},
		{Action: "wait", Selector: "#logged-in"}, // visible only with the storageState cookie
		{Action: "goto", URL: "/projects.html"},
		{Action: "click", Selector: "role=button[name=\"New project\"]"},
		{Action: "fill", Selector: "input[name=\"name\"]", Value: "Demo"},
		{Action: "select", Selector: "select[name=\"visibility\"]", Value: "private"},
		{Action: "press", Selector: "input[name=\"name\"]", Key: "Tab"},
		{Action: "click", Selector: "role=button[name=\"Create\"]"},
		{Action: "wait", Selector: "#toast"},
		{Action: "scroll", Selector: "#footer"},
		{Action: "scroll", Y: intp(0)},
		{Action: "wait", Ms: intp(200)},
	}

	out, err := newRecorder(t, nil).Record(context.Background(), recordInput(t, base, "en", steps))
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(out.WebmPath); err != nil || fi.Size() == 0 {
		t.Fatalf("webm %q: %v", out.WebmPath, err)
	}
	if len(out.Offsets) != len(steps) {
		t.Errorf("%d offsets for %d steps", len(out.Offsets), len(steps))
	}
}

// FR-004 AC1: each language is its own recording from a fresh context.
func TestRecord_twoRecordingsAreIndependent(t *testing.T) {
	base := fixtureApp(t)
	rec := newRecorder(t, nil)
	steps := []script.Step{{Action: "goto", URL: "/index.html"}, {Action: "wait", Selector: "#logged-in"}}

	en, err := rec.Record(context.Background(), recordInput(t, base, "en", steps))
	if err != nil {
		t.Fatal(err)
	}
	pl, err := rec.Record(context.Background(), recordInput(t, base, "pl", steps))
	if err != nil {
		t.Fatal(err)
	}
	if en.WebmPath == pl.WebmPath {
		t.Errorf("both languages recorded to %s", en.WebmPath)
	}
}

// FR-008 AC: a missing selector at step 4 aborts with step 4 and lang en
// within 31 s. The clock starts once Chromium is up: the 30 s bound is on the
// action, not on the browser launch.
func TestRecord_missingSelectorAbortsWithinTimeout(t *testing.T) {
	base := fixtureApp(t)
	var launched time.Time
	steps := []script.Step{
		{Action: "goto", URL: "/index.html"},
		{Action: "wait", Selector: "#logged-in"},
		{Action: "goto", URL: "/projects.html"},
		{Action: "click", Selector: "#does-not-exist"},
		{Action: "goto", URL: "/index.html"},
	}

	_, err := newRecorder(t, &launched).Record(context.Background(), recordInput(t, base, "en", steps))
	elapsed := time.Since(launched)

	var f *failure.Failure
	if !errors.As(err, &f) {
		t.Fatalf("err = %v, want *failure.Failure", err)
	}
	if f.Step == nil || *f.Step != 4 || f.Lang != "en" || f.Action != "click" || f.Target != "#does-not-exist" {
		t.Errorf("failure = %+v, want step 4, lang en, click #does-not-exist", f)
	}
	if elapsed > 31*time.Second {
		t.Errorf("aborted after %v, want <= 31s", elapsed)
	}
}

// Spike S2 (ARCHITECTURE §17): a selector that matches several elements must
// fail at once, naming the count, never click the first match.
func TestBrowser_ambiguousSelectorFailsFast(t *testing.T) {
	base := fixtureApp(t)
	sess, err := browser.Launcher{}.Launch(context.Background(), browser.Options{BaseURL: base})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = sess.Close() }()
	if err := sess.Start(); err != nil {
		t.Fatal(err)
	}
	if err := sess.Goto(base + "/index.html"); err != nil { // two <a> in the nav
		t.Fatal(err)
	}

	calls := map[string]func() error{
		"click":  func() error { return sess.Click("a") },
		"hover":  func() error { return sess.Hover("a") },
		"fill":   func() error { return sess.Fill("a", "x", 0) },
		"moveTo": func() error { return sess.MoveTo("a", 5) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			err := call()
			if err == nil {
				t.Fatal("ambiguous selector was accepted")
			}
			t.Logf("S2 %s: %v (after %v)", name, err, time.Since(start).Round(time.Millisecond))
			if !strings.Contains(err.Error(), "strict mode violation") || !strings.Contains(err.Error(), "2 elements") {
				t.Errorf("error does not name the count: %v", err)
			}
			if time.Since(start) > 5*time.Second {
				t.Error("took the full timeout instead of failing fast")
			}
		})
	}
}

// Spike S1 (ARCHITECTURE §17, ADR-46): video time 0 starts ~90 ms after t0.
// Click the marker at a known offset from t0 and find the white flash in the raw
// WebM; with recorder.LeadInCompensation applied the two must agree within 100 ms.
// Measured spread without it: 52-129 ms early, one frame step is 40 ms.
func TestRecord_leadInIsWithinTolerance(t *testing.T) {
	base := fixtureApp(t)
	sess, err := browser.Launcher{}.Launch(context.Background(), browser.Options{
		BaseURL:  base,
		VideoDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = sess.Close() }()

	t0 := time.Now()
	if err := sess.Start(); err != nil {
		t.Fatal(err)
	}
	if err := sess.Goto(base + "/marker.html"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	clickAt := time.Since(t0)
	if err := sess.Click("#marker"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond) // the flash lasts 1 s
	video, err := sess.Close()
	if err != nil {
		t.Fatal(err)
	}

	// The recorder places audio at t0-offset minus LeadInCompensation; the flash
	// frame must land there.
	flashAt := flashOnset(t, video, 0)
	drift := flashAt - (clickAt - recorder.LeadInCompensation)
	t.Logf("S1: click at t0+%v, flash frame at %v, difference %v", clickAt.Round(time.Millisecond), flashAt.Round(time.Millisecond), drift.Round(time.Millisecond))
	if drift < -100*time.Millisecond || drift > 100*time.Millisecond {
		t.Errorf("flash frame is %v from the click offset, want within 100ms", drift)
	}
}

func recordInput(t *testing.T, base, lang string, steps []script.Step) recorder.Input {
	t.Helper()
	return recorder.Input{Steps: steps, Lang: lang, Dir: t.TempDir(), BaseURL: base, StorageState: fixtureState}
}
