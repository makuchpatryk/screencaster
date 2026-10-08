// Package explorer implements explore_page (FR-017): replay a few actions in a
// fresh, non-recorded browser and describe the page that results, with a
// ready-to-use selector on every interactive element. It uses the same
// executor as a render, so a selector it returns works in a script
// (ARCHITECTURE §7, §8). It does not enqueue, record or take the render lock.
package explorer

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"screencaster/internal/domain/executor"
	"screencaster/internal/domain/script"
)

// MaxSnapshot caps the snapshot text (FR-017).
const MaxSnapshot = 50000

// interactive lists the roles that get a selector (ARCHITECTURE §8).
var interactive = map[string]bool{
	"button": true, "link": true, "textbox": true, "checkbox": true, "radio": true,
	"combobox": true, "menuitem": true, "tab": true, "option": true,
}

// Session is the browser the explorer drives. adapters/browser implements it;
// tests use a fake.
type Session interface {
	executor.Page
	// Start opens the page.
	Start() error
	// Abort drops the session and closes Chromium.
	Abort()
	// AriaSnapshot returns the body's accessibility tree as indented text.
	AriaSnapshot() (string, error)
	// Count returns how many elements match selector.
	Count(selector string) (int, error)
	Title() (string, error)
	URL() string
}

// LaunchOptions configure the fresh context of one call (FR-017).
type LaunchOptions struct {
	StorageState *script.StorageState
}

// Explorer opens a browser per call. Launch is wired in main.
type Explorer struct {
	Launch func(ctx context.Context, o LaunchOptions) (Session, error)
}

// Input is one explore call. URL is an absolute http(s) URL (BR-010).
type Input struct {
	StorageState *script.StorageState
	URL          string
	Actions      []script.Step
}

// Output describes the page after the actions.
type Output struct {
	URL       string
	Title     string
	Snapshot  string
	Truncated bool
}

// mu runs concurrent calls one at a time (FR-017). It does not look at the
// render: explore may overlap one (ADR-44).
var mu sync.Mutex

// Explore navigates to in.URL, runs in.Actions and returns the page. If an
// action fails, the error is the *failure.Failure from the executor and the
// Output still holds the page as it stood, so the caller can show both. The
// initial navigation counts as step 0, the actions as steps 1..n.
func (e Explorer) Explore(ctx context.Context, in Input) (Output, error) {
	mu.Lock()
	defer mu.Unlock()

	sess, err := e.Launch(ctx, LaunchOptions{StorageState: in.StorageState})
	if err != nil {
		return Output{}, fmt.Errorf("launch browser: %w", err)
	}
	defer sess.Abort()
	if err := sess.Start(); err != nil {
		return Output{}, fmt.Errorf("start page: %w", err)
	}

	ex := executor.New(sess, executor.Mode{Visuals: false})
	steps := append([]script.Step{{Action: script.Goto{URL: in.URL}}}, in.Actions...)
	var runErr error
	for i, s := range steps {
		if runErr = ex.Run(ctx, i, s); runErr != nil {
			break
		}
	}
	if ctx.Err() != nil {
		return Output{}, ctx.Err()
	}

	out, err := capture(sess)
	if err != nil && runErr == nil {
		return Output{}, err
	}
	// After a failed action a missing snapshot is not worth masking runErr.
	return out, runErr
}

func capture(sess Session) (Output, error) {
	raw, err := sess.AriaSnapshot()
	if err != nil {
		return Output{}, fmt.Errorf("snapshot: %w", err)
	}
	snap, err := annotate(raw, sess.Count)
	if err != nil {
		return Output{}, err
	}
	title, err := sess.Title()
	if err != nil {
		return Output{}, fmt.Errorf("title: %w", err)
	}
	snap, truncated := truncate(snap)
	return Output{URL: sess.URL(), Title: title, Snapshot: snap, Truncated: truncated}, nil
}

// lineRE matches `- role "name" [attr]...`; the name is a double-quoted string.
// Group 0 ends where the annotation goes, before ": value" or the colon that
// introduces children.
var lineRE = regexp.MustCompile(`^\s*- ([a-z]+)(?: ("(?:[^"\\]|\\.)*"))?(?: \[[^\]]*\])*`)

// annotate appends `-> <selector>` to every named interactive line. A
// selector matching several elements gets `>> nth=<i>` where i counts the
// earlier lines with the same selector, so the result is unique and strict
// (FR-017, ARCHITECTURE §7). The role= selector compares the whole name, so
// "Save" does not match "Save all" (TestExplore_selectorsPickTheirOwnElement).
func annotate(raw string, count func(selector string) (int, error)) (string, error) {
	seen := map[string]int{}
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		m := lineRE.FindStringSubmatch(line)
		if m == nil || !interactive[m[1]] || m[2] == "" {
			continue
		}
		name, err := strconv.Unquote(m[2])
		if err != nil {
			name = strings.Trim(m[2], `"`)
		}
		sel := fmt.Sprintf("role=%s[name=%s]", m[1], strconv.Quote(name))
		n, err := count(sel)
		if err != nil {
			return "", fmt.Errorf("check selector %s: %w", sel, err)
		}
		if n == 0 {
			continue
		}
		if n > 1 {
			idx := seen[sel]
			seen[sel]++
			sel += fmt.Sprintf(" >> nth=%d", idx)
		}
		lines[i] = m[0] + " -> " + sel + line[len(m[0]):]
	}
	return strings.Join(lines, "\n"), nil
}

// truncate cuts s to MaxSnapshot at a line boundary.
func truncate(s string) (string, bool) {
	if len(s) <= MaxSnapshot {
		return s, false
	}
	cut := strings.LastIndex(s[:MaxSnapshot], "\n")
	if cut < 0 {
		cut = 0
	}
	return s[:cut], true
}
