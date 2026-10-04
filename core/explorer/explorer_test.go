package explorer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"screencaster/core/executor"
	"screencaster/core/failure"
	"screencaster/core/script"
)

// counts maps a selector to its match count; unknown selectors match once.
func countFrom(counts map[string]int) func(string) (int, error) {
	return func(sel string) (int, error) {
		if n, ok := counts[sel]; ok {
			return n, nil
		}
		return 1, nil
	}
}

func TestAnnotate_selectorsForInteractiveRoles(t *testing.T) {
	raw := `- heading "Projects" [level=1]
- button "New project"
- textbox "Name": Demo
- combobox "Visibility":
  - option "Public" [selected]
- button
- link "Say \"hi\""
- contentinfo: Footer`
	want := `- heading "Projects" [level=1]
- button "New project" -> role=button[name="New project"]
- textbox "Name" -> role=textbox[name="Name"]: Demo
- combobox "Visibility" -> role=combobox[name="Visibility"]:
  - option "Public" [selected] -> role=option[name="Public"]
- button
- link "Say \"hi\"" -> role=link[name="Say \"hi\""]
- contentinfo: Footer`
	got, err := annotate(raw, countFrom(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("annotate() =\n%s\nwant\n%s", got, want)
	}
}

func TestAnnotate_collisionsGetNth(t *testing.T) {
	raw := "- button \"Save\"\n- button \"Save\"\n- button \"Cancel\""
	got, err := annotate(raw, countFrom(map[string]int{`role=button[name="Save"]`: 2}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`- button "Save" -> role=button[name="Save"] >> nth=0`,
		`- button "Save" -> role=button[name="Save"] >> nth=1`,
		`- button "Cancel" -> role=button[name="Cancel"]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("annotate() missing %q in\n%s", want, got)
		}
	}
}

func TestAnnotate_countErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	_, err := annotate(`- button "A"`, func(string) (int, error) { return 0, boom })
	if !errors.Is(err, boom) {
		t.Errorf("annotate() error = %v, want %v", err, boom)
	}
}

func TestTruncate_cutsAtLineBoundary(t *testing.T) {
	line := strings.Repeat("x", 99) + "\n"
	long := strings.Repeat(line, MaxSnapshot/100+10)
	got, truncated := truncate(long)
	if !truncated || len(got) > MaxSnapshot || !strings.HasSuffix(got, "x") {
		t.Errorf("truncate() len=%d truncated=%v, want cut under %d at a line end", len(got), truncated, MaxSnapshot)
	}
	if got, truncated := truncate("short"); got != "short" || truncated {
		t.Errorf("truncate(short) = %q, %v", got, truncated)
	}
}

// fakeSession records the page calls and fails the click of failOn.
type fakeSession struct {
	calls  []string
	failOn string
	aborts int
}

func (f *fakeSession) rec(s string) error {
	f.calls = append(f.calls, s)
	if s == f.failOn {
		return errors.New("no such element")
	}
	return nil
}
func (f *fakeSession) Goto(u string) error                     { return f.rec("goto " + u) }
func (f *fakeSession) Click(s string) error                    { return f.rec("click " + s) }
func (f *fakeSession) Fill(s, v string, _ time.Duration) error { return f.rec("fill " + s) }
func (f *fakeSession) Select(s, v string) error                { return f.rec("select " + s) }
func (f *fakeSession) Press(s, k string) error                 { return f.rec("press " + s) }
func (f *fakeSession) Hover(s string) error                    { return f.rec("hover " + s) }
func (f *fakeSession) ScrollIntoView(s string) error           { return f.rec("scroll " + s) }
func (f *fakeSession) ScrollTo(y int) error                    { return f.rec("scrollto") }
func (f *fakeSession) WaitVisible(s string) error              { return f.rec("wait " + s) }
func (f *fakeSession) MoveTo(s string, _ int) error            { return f.rec("move " + s) }
func (f *fakeSession) Start() error                            { return nil }
func (f *fakeSession) Abort()                                  { f.aborts++ }
func (f *fakeSession) AriaSnapshot() (string, error)           { return `- button "New project"`, nil }
func (f *fakeSession) Count(string) (int, error)               { return 1, nil }
func (f *fakeSession) Title() (string, error)                  { return "Projects", nil }
func (f *fakeSession) URL() string                             { return "http://app/projects" }

func explorerWith(f *fakeSession) Explorer {
	return Explorer{Launch: func(context.Context, LaunchOptions) (Session, error) { return f, nil }}
}

func TestExplore_runsGotoThenActionsWithoutVisuals(t *testing.T) {
	f := &fakeSession{}
	out, err := explorerWith(f).Explore(context.Background(), Input{
		BaseURL: "http://app", URL: "/projects",
		Actions: []script.Step{{Action: "click", Selector: "#a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(f.calls, ","), "goto http://app/projects,click #a"; got != want {
		t.Errorf("page calls = %s, want %s (no MoveTo: explore has no visuals)", got, want)
	}
	if out.Title != "Projects" || out.URL != "http://app/projects" || !strings.Contains(out.Snapshot, `role=button[name="New project"]`) {
		t.Errorf("Output = %+v", out)
	}
	if f.aborts != 1 {
		t.Errorf("browser closed %d times, want 1", f.aborts)
	}
}

func TestExplore_failedActionReturnsStepAndSnapshot(t *testing.T) {
	f := &fakeSession{failOn: "click #missing"}
	out, err := explorerWith(f).Explore(context.Background(), Input{
		BaseURL: "http://app", URL: "/projects",
		Actions: []script.Step{{Action: "hover", Selector: "#a"}, {Action: "click", Selector: "#missing"}},
	})
	var fail *failure.Failure
	if !errors.As(err, &fail) || fail.Step == nil || *fail.Step != 2 || fail.Action != "click" || fail.Target != "#missing" {
		t.Fatalf("error = %v, want step 2 click #missing", err)
	}
	if !strings.Contains(out.Snapshot, "New project") {
		t.Errorf("snapshot at failure missing: %+v", out)
	}
}

func TestExplore_launchFailure(t *testing.T) {
	e := Explorer{Launch: func(context.Context, LaunchOptions) (Session, error) { return nil, errors.New("no chromium") }}
	if _, err := e.Explore(context.Background(), Input{BaseURL: "http://app", URL: "/"}); err == nil {
		t.Fatal("want error")
	}
}

var _ executor.Page = (*fakeSession)(nil)
