package executor

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"screencaster/internal/domain/failure"
	"screencaster/internal/domain/script"
)

// fakePage records calls as strings and fails the call named in failOn.
type fakePage struct {
	calls  []string
	failOn string
	err    error
}

func (f *fakePage) rec(name string, args ...any) error {
	f.calls = append(f.calls, fmt.Sprintf(name+"(%s)", fmt.Sprint(args...)))
	if f.failOn == name {
		return f.err
	}
	return nil
}

func (f *fakePage) Goto(u string) error           { return f.rec("Goto", u) }
func (f *fakePage) Click(sel string) error        { return f.rec("Click", sel) }
func (f *fakePage) Hover(sel string) error        { return f.rec("Hover", sel) }
func (f *fakePage) Select(sel, v string) error    { return f.rec("Select", sel, " ", v) }
func (f *fakePage) Press(sel, key string) error   { return f.rec("Press", sel, " ", key) }
func (f *fakePage) ScrollIntoView(s string) error { return f.rec("ScrollIntoView", s) }
func (f *fakePage) ScrollTo(y int) error          { return f.rec("ScrollTo", y) }
func (f *fakePage) WaitVisible(s string) error    { return f.rec("WaitVisible", s) }
func (f *fakePage) MoveTo(sel string, n int) error {
	return f.rec("MoveTo", sel, " ", n)
}
func (f *fakePage) Fill(sel, v string, d time.Duration) error {
	return f.rec("Fill", sel, " ", v, " ", d)
}

func TestRun_actions(t *testing.T) {
	tests := []struct {
		name string
		step script.Step
		mode Mode
		want []string
	}{
		{
			name: "goto passes the URL through unchanged (FR-005 AC)",
			step: script.Step{Action: script.Goto{URL: "https://example.com/a?b=1"}},
			want: []string{"Goto(https://example.com/a?b=1)"},
		},
		{
			name: "click without visuals does not glide",
			step: script.Step{Action: script.Click{Selector: "#a"}},
			want: []string{"Click(#a)"},
		},
		{
			name: "click with visuals glides first (FR-006)",
			step: script.Step{Action: script.Click{Selector: "#a"}},
			mode: Mode{Visuals: true},
			want: []string{"MoveTo(#a 25)", "Click(#a)"},
		},
		{
			name: "hover with visuals glides first",
			step: script.Step{Action: script.Hover{Selector: "#a"}},
			mode: Mode{Visuals: true},
			want: []string{"MoveTo(#a 25)", "Hover(#a)"},
		},
		{
			name: "fill without visuals types instantly",
			step: script.Step{Action: script.Fill{Selector: "#n", Value: "x"}},
			want: []string{"Fill(#n x 0s)"},
		},
		{
			name: "fill with visuals glides and types 60 ms per char (FR-006)",
			step: script.Step{Action: script.Fill{Selector: "#n", Value: "x"}},
			mode: Mode{Visuals: true},
			want: []string{"MoveTo(#n 25)", "Fill(#n x 60ms)"},
		},
		{
			name: "select with visuals glides first",
			step: script.Step{Action: script.Select{Selector: "#s", Value: "private"}},
			mode: Mode{Visuals: true},
			want: []string{"MoveTo(#s 25)", "Select(#s private)"},
		},
		{
			name: "press with a selector",
			step: script.Step{Action: script.Press{Key: "Tab", Selector: "#n"}},
			want: []string{"Press(#n Tab)"},
		},
		{
			name: "press without a selector",
			step: script.Step{Action: script.Press{Key: "Enter"}},
			want: []string{"Press( Enter)"},
		},
		{
			name: "press never glides",
			step: script.Step{Action: script.Press{Key: "Enter"}},
			mode: Mode{Visuals: true},
			want: []string{"Press( Enter)"},
		},
		{
			name: "scroll to a selector",
			step: script.Step{Action: script.ScrollInto{Selector: "#footer"}},
			want: []string{"ScrollIntoView(#footer)"},
		},
		{
			name: "scroll to y 0 is valid",
			step: script.Step{Action: script.ScrollTo{Y: 0}},
			want: []string{"ScrollTo(0)"},
		},
		{
			name: "wait for a selector",
			step: script.Step{Action: script.WaitFor{Selector: "#toast"}},
			want: []string{"WaitVisible(#toast)"},
		},
		{
			name: "wait ms sleeps and touches no page",
			step: script.Step{Action: script.Pause{D: time.Millisecond}},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &fakePage{}
			e := New(p, tt.mode)
			if err := e.Run(context.Background(), 1, tt.step); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !reflect.DeepEqual(p.calls, tt.want) {
				t.Errorf("calls = %q, want %q", p.calls, tt.want)
			}
		})
	}
}

func TestRun_failureCarriesStepActionTarget(t *testing.T) {
	boom := errors.New("strict mode violation: matched 2 elements")
	tests := []struct {
		name       string
		step       script.Step
		failOn     string
		wantAction string
		wantTarget string
	}{
		{"click reports its selector", script.Step{Action: script.Click{Selector: "#a"}}, "Click", "click", "#a"},
		{"goto reports its url, not the resolved one", script.Step{Action: script.Goto{URL: "/x"}}, "Goto", "goto", "/x"},
		{"glide failure is reported against the step", script.Step{Action: script.Hover{Selector: "#h"}}, "MoveTo", "hover", "#h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &fakePage{failOn: tt.failOn, err: boom}
			e := New(p, Mode{Visuals: true})
			err := e.Run(context.Background(), 4, tt.step)

			var f *failure.Failure
			if !errors.As(err, &f) {
				t.Fatalf("err = %v, want *failure.Failure", err)
			}
			if f.Step == nil || *f.Step != 4 || f.Action != tt.wantAction || f.Target != tt.wantTarget {
				t.Errorf("failure = %+v, want step 4 %s %s", f, tt.wantAction, tt.wantTarget)
			}
			if !errors.Is(err, boom) {
				t.Error("failure does not wrap the page error")
			}
		})
	}
}

func TestRun_canceledContextStopsBeforeTouchingThePage(t *testing.T) {
	p := &fakePage{}
	e := New(p, Mode{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := e.Run(ctx, 1, script.Step{Action: script.Click{Selector: "#a"}})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if len(p.calls) != 0 {
		t.Errorf("page was called: %q", p.calls)
	}
}

func TestRun_waitMsStopsOnCancel(t *testing.T) {
	e := New(&fakePage{}, Mode{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := e.Run(ctx, 1, script.Step{Action: script.Pause{D: 30 * time.Second}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("wait did not stop on cancel")
	}
}
