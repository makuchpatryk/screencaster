// Package executor runs one script step against a Page (FR-005, FR-006). It is
// the single implementation of step semantics, shared by render and
// explore_page so a selector that works in one works in the other (ARCHITECTURE
// §7). It knows nothing about timing between steps or narration.
package executor

import (
	"context"
	"fmt"
	"time"

	"screencaster/internal/domain/failure"
	"screencaster/internal/domain/script"
)

// ActionTimeout bounds every page action; there is no retry (BR-004, FR-008).
// A variable only so the e2e tests can shorten it; production never sets it.
var ActionTimeout = 30 * time.Second

const (
	// GlideSteps is the number of interpolated mouse moves before an
	// interaction (FR-006).
	GlideSteps = 25
	// TypeDelay is the pause between typed characters (FR-006).
	TypeDelay = 60 * time.Millisecond
)

// Page is what the executor needs from a browser page. Each call is bounded by
// ActionTimeout. Selectors are strict: one matching several elements is an
// error, never a silent click on the first (ARCHITECTURE §7).
type Page interface {
	// Goto navigates and waits for the load event.
	Goto(url string) error
	Click(selector string) error
	// Fill types v into the element, delay apart per character.
	Fill(selector, v string, delay time.Duration) error
	Select(selector, v string) error
	// Press presses key, focused on selector when it is not empty.
	Press(selector, key string) error
	Hover(selector string) error
	ScrollIntoView(selector string) error
	ScrollTo(y int) error
	WaitVisible(selector string) error
	// MoveTo glides the mouse to the element's center in steps moves.
	MoveTo(selector string, steps int) error
}

// Mode picks between rendering and exploring. Visuals turns on the cursor
// glide and the typing delay; the cursor overlay itself is an init script
// installed by the browser (FR-006).
type Mode struct{ Visuals bool }

// Executor runs steps against one page.
type Executor struct {
	page Page
	mode Mode
}

// New returns an executor for page p. goto URLs are absolute, checked by
// script.Parse (BR-010), and go to the page as they are.
func New(p Page, m Mode) *Executor {
	return &Executor{page: p, mode: m}
}

// Run executes step s, the i-th (1-based) of the script. Any error comes back
// as *failure.Failure without Lang; the recorder fills that in.
func (e *Executor) Run(ctx context.Context, i int, s script.Step) error {
	if err := ctx.Err(); err != nil {
		return failure.Step(i, "", s.Action.Name(), s.Action.Target(), err)
	}
	if err := e.dispatch(ctx, s.Action); err != nil {
		return failure.Step(i, "", s.Action.Name(), s.Action.Target(), err)
	}
	return nil
}

// dispatch is the one place that maps an action to page calls (FR-005).
func (e *Executor) dispatch(ctx context.Context, a script.Action) error {
	switch a := a.(type) {
	case script.Goto:
		return e.page.Goto(a.URL)
	case script.Click:
		return e.interact(a.Selector, func() error { return e.page.Click(a.Selector) })
	case script.Hover:
		return e.interact(a.Selector, func() error { return e.page.Hover(a.Selector) })
	case script.Fill:
		return e.interact(a.Selector, func() error { return e.page.Fill(a.Selector, a.Value, e.typeDelay()) })
	case script.Select:
		return e.interact(a.Selector, func() error { return e.page.Select(a.Selector, a.Value) })
	case script.Press:
		return e.page.Press(a.Selector, a.Key)
	case script.ScrollInto:
		return e.page.ScrollIntoView(a.Selector)
	case script.ScrollTo:
		return e.page.ScrollTo(a.Y)
	case script.WaitFor:
		return e.page.WaitVisible(a.Selector)
	case script.Pause:
		return sleep(ctx, a.D)
	}
	return fmt.Errorf("unknown action %T", a)
}

// interact glides the mouse to the element first when Visuals is on, so the
// video shows the cursor arriving before the action (FR-006).
func (e *Executor) interact(selector string, do func() error) error {
	if e.mode.Visuals {
		if err := e.page.MoveTo(selector, GlideSteps); err != nil {
			return err
		}
	}
	return do()
}

func (e *Executor) typeDelay() time.Duration {
	if e.mode.Visuals {
		return TypeDelay
	}
	return 0
}

// sleep waits d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
