// Package executor runs one script step against a Page (FR-005, FR-006). It is
// the single implementation of step semantics, shared by render and
// explore_page so a selector that works in one works in the other (ARCHITECTURE
// §7). It knows nothing about timing between steps or narration.
package executor

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"screencaster/core/failure"
	"screencaster/core/script"
)

const (
	// ActionTimeout bounds every page action; there is no retry (BR-004, FR-008).
	ActionTimeout = 30 * time.Second
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
	base *url.URL
	mode Mode
}

// New returns an executor that resolves relative goto URLs against baseURL
// (BR-010). baseURL is an absolute URL validated by script.Parse.
func New(p Page, baseURL string, m Mode) (*Executor, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse baseUrl: %w", err)
	}
	return &Executor{page: p, base: base, mode: m}, nil
}

// Run executes step s, the i-th (1-based) of the script. Any error comes back
// as *failure.Failure without Lang; the recorder fills that in.
func (e *Executor) Run(ctx context.Context, i int, s script.Step) error {
	target := Target(s)
	if err := ctx.Err(); err != nil {
		return failure.Step(i, "", s.Action, target, err)
	}
	if err := e.dispatch(ctx, s); err != nil {
		return failure.Step(i, "", s.Action, target, err)
	}
	return nil
}

// Target is what a step acts on: the URL for goto, else the selector (empty
// for a key press without one, or a timed wait). Failures and CLI progress
// both show it.
func Target(s script.Step) string {
	if s.Action == "goto" {
		return s.URL
	}
	return s.Selector
}

func (e *Executor) dispatch(ctx context.Context, s script.Step) error {
	switch s.Action {
	case "goto":
		return e.page.Goto(e.resolve(s.URL))
	case "click":
		return e.interact(s.Selector, func() error { return e.page.Click(s.Selector) })
	case "hover":
		return e.interact(s.Selector, func() error { return e.page.Hover(s.Selector) })
	case "fill":
		return e.interact(s.Selector, func() error { return e.page.Fill(s.Selector, s.Value, e.typeDelay()) })
	case "select":
		return e.interact(s.Selector, func() error { return e.page.Select(s.Selector, s.Value) })
	case "press":
		return e.page.Press(s.Selector, s.Key)
	case "scroll":
		if s.Selector != "" {
			return e.page.ScrollIntoView(s.Selector)
		}
		return e.page.ScrollTo(*s.Y) // the schema guarantees y when selector is absent
	case "wait":
		if s.Selector != "" {
			return e.page.WaitVisible(s.Selector)
		}
		return sleep(ctx, time.Duration(*s.Ms)*time.Millisecond) // the schema guarantees ms
	}
	return fmt.Errorf("unknown action %q", s.Action)
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

// resolve keeps absolute URLs and joins the rest to baseUrl (BR-010).
func (e *Executor) resolve(raw string) string {
	ref, err := url.Parse(raw)
	if err != nil || ref.IsAbs() {
		return raw
	}
	return e.base.ResolveReference(ref).String()
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
