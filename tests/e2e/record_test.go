//go:build e2e

package e2e

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"screencaster/internal/adapters/browser"
	"screencaster/internal/domain/failure"
	"screencaster/internal/domain/recorder"
	"screencaster/internal/domain/script"
)

// A silent recording of the fixture app (FR-004, FR-005, FR-006): the
// storageState cookie shows #logged-in, a form is filled and submitted.
func TestRecord_fixtureApp(t *testing.T) {
	t.Parallel()
	base := fixtureApp(t)
	steps := []script.Step{
		{Action: script.Goto{URL: base + "/index.html"}},
		{Action: script.WaitFor{Selector: "#logged-in"}}, // visible only with the storageState cookie
		{Action: script.Goto{URL: base + "/projects.html"}},
		{Action: script.Click{Selector: "role=button[name=\"New project\"]"}},
		{Action: script.Fill{Selector: "input[name=\"name\"]", Value: "Demo"}},
		{Action: script.Select{Selector: "select[name=\"visibility\"]", Value: "private"}},
		{Action: script.Press{Key: "Tab", Selector: "input[name=\"name\"]"}},
		{Action: script.Click{Selector: "role=button[name=\"Create\"]"}},
		{Action: script.WaitFor{Selector: "#toast"}},
		{Action: script.ScrollInto{Selector: "#footer"}},
		{Action: script.ScrollTo{Y: 0}},
		{Action: script.Pause{D: 200 * time.Millisecond}},
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

// The recording opens dark: a new page is white until the first goto paints,
// which showed as a white flash right after the start card. No step navigates
// here, so every frame is the start page: the sync marker (magenta, YAVG about
// 80-105), then the card colour. The assembler cuts the marker off. Not
// parallel: under CPU load the first paint is late and the white page shows.
func TestRecord_startsDark(t *testing.T) {
	base := fixtureApp(t)
	steps := []script.Step{{Action: script.Pause{D: time.Second}}}

	out, err := newRecorder(t, nil).Record(context.Background(), recordInput(t, base, "en", steps))
	if err != nil {
		t.Fatal(err)
	}
	if y := brightestFrame(t, out.WebmPath); y >= brightnessThreshold {
		t.Errorf("brightest frame YAVG = %.0f, want a dark recording (< %d)", y, brightnessThreshold)
	}
}

// FR-004 AC1: each language is its own recording from a fresh context.
func TestRecord_twoRecordingsAreIndependent(t *testing.T) {
	t.Parallel()
	base := fixtureApp(t)
	rec := newRecorder(t, nil)
	steps := []script.Step{{Action: script.Goto{URL: base + "/index.html"}}, {Action: script.WaitFor{Selector: "#logged-in"}}}

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
// within the action timeout (30 s by default, pinned in executor_test; shortened
// here). The clock starts once Chromium is up and leaves out the sync marker
// hold: the bound is on the action, not on the browser launch or the marker
// (decision 69). Not parallel: it shortens executor.ActionTimeout.
func TestRecord_missingSelectorAbortsWithinTimeout(t *testing.T) {
	limit := shortActionTimeout(t)
	base := fixtureApp(t)
	var launched time.Time
	steps := []script.Step{
		{Action: script.Goto{URL: base + "/index.html"}},
		{Action: script.WaitFor{Selector: "#logged-in"}},
		{Action: script.Goto{URL: base + "/projects.html"}},
		{Action: script.Click{Selector: "#does-not-exist"}},
		{Action: script.Goto{URL: base + "/index.html"}},
	}

	_, err := newRecorder(t, &launched).Record(context.Background(), recordInput(t, base, "en", steps))
	elapsed := time.Since(launched) - recorder.MarkerHold

	var f *failure.Failure
	if !errors.As(err, &f) {
		t.Fatalf("err = %v, want *failure.Failure", err)
	}
	if f.Step == nil || *f.Step != 4 || f.Lang != "en" || f.Action != "click" || f.Target != "#does-not-exist" {
		t.Errorf("failure = %+v, want step 4, lang en, click #does-not-exist", f)
	}
	if elapsed > limit+time.Second {
		t.Errorf("aborted after %v, want <= %v", elapsed, limit+time.Second)
	}
}

// Spike S2 (ARCHITECTURE §17): a selector that matches several elements must
// fail at once, naming the count, never click the first match.
func TestBrowser_ambiguousSelectorFailsFast(t *testing.T) {
	t.Parallel()
	base := fixtureApp(t)
	sess, err := browser.Launcher{}.Launch(context.Background(), browser.Options{})
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

func recordInput(t *testing.T, base, lang string, steps []script.Step) recorder.Input {
	t.Helper()
	return recorder.Input{Steps: steps, Lang: lang, Dir: t.TempDir(), StorageState: fixtureState}
}
