// Package browser wraps playwright-go: it launches Chromium, optionally
// records video, and implements executor.Page. It is the only package that
// imports playwright-go (CODE_QUALITY, Separation of concerns).
package browser

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	playwright "github.com/mxschmitt/playwright-go"

	"screencaster/core/executor"
)

// Fixed viewport and video size (FR-004, NFR-002).
const (
	Width  = 1920
	Height = 1080
)

//go:embed cursor.js
var cursorJS string

// Launcher starts browser sessions. DriverDir is where the Playwright driver
// is installed; empty means the library default, which honours
// PLAYWRIGHT_DRIVER_PATH. Wired in main.
type Launcher struct{ DriverDir string }

// Options configure one session. Zero values mean off.
type Options struct {
	BaseURL          string
	StorageStatePath string
	VideoDir         string // record a video of the page into this directory
	Visuals          bool   // inject the cursor overlay (FR-006)
}

// Session is one Chromium with a single context and page. It implements
// executor.Page; every action is bounded by executor.ActionTimeout.
type Session struct {
	pw      *playwright.Playwright
	browser playwright.Browser
	context playwright.BrowserContext
	page    playwright.Page // nil until Start
	record  bool
	stop    func() bool // detaches the ctx watcher

	closeOnce sync.Once
	videoPath string
	closeErr  error
}

var _ executor.Page = (*Session)(nil)

// Launch starts the driver, Chromium and a fresh context. No page exists yet,
// so recording has not begun: Start creates the page, and the recorder takes
// its t0 right before that (ARCHITECTURE §5, ADR-46).
//
// Cancelling ctx aborts the session, which fails any action in flight. The
// driver's output is discarded: stdout must stay clean for MCP framing
// (ARCHITECTURE §12).
func (l Launcher) Launch(ctx context.Context, o Options) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pw, err := playwright.Run(&playwright.RunOptions{
		DriverDirectory: l.DriverDir,
		Stdout:          io.Discard,
		Stderr:          io.Discard,
	})
	if err != nil {
		return nil, fmt.Errorf("start playwright: %w", err)
	}
	s := &Session{pw: pw, record: o.VideoDir != ""}
	if err := s.open(o); err != nil {
		s.Abort()
		return nil, err
	}
	s.stop = context.AfterFunc(ctx, s.Abort)
	return s, nil
}

func (s *Session) open(o Options) error {
	var err error
	s.browser, err = s.pw.Chromium.Launch()
	if err != nil {
		return fmt.Errorf("launch chromium: %w", err)
	}

	size := &playwright.Size{Width: Width, Height: Height}
	opts := playwright.BrowserNewContextOptions{Viewport: size}
	if o.BaseURL != "" {
		opts.BaseURL = playwright.String(o.BaseURL)
	}
	if o.StorageStatePath != "" {
		opts.StorageStatePath = playwright.String(o.StorageStatePath)
	}
	if o.VideoDir != "" {
		opts.RecordVideo = &playwright.RecordVideo{Dir: playwright.String(o.VideoDir), Size: size}
	}
	s.context, err = s.browser.NewContext(opts)
	if err != nil {
		return fmt.Errorf("new context: %w", err)
	}

	// One place for the 30 s bound (FR-008); locator actions and navigation both follow it.
	ms := float64(executor.ActionTimeout / time.Millisecond)
	s.context.SetDefaultTimeout(ms)
	s.context.SetDefaultNavigationTimeout(ms)

	if o.Visuals {
		if err := s.context.AddInitScript(playwright.Script{Content: playwright.String(cursorJS)}); err != nil {
			return fmt.Errorf("add cursor script: %w", err)
		}
	}
	return nil
}

// Start opens the page. When VideoDir is set, recording begins here.
func (s *Session) Start() error {
	page, err := s.context.NewPage()
	if err != nil {
		return fmt.Errorf("new page: %w", err)
	}
	s.page = page
	return nil
}

// Close ends the session and returns the recorded video's path, or "" when
// nothing was recorded. The video is complete only after the context closes,
// so it is closed before the path is read. Safe to call more than once and from
// Abort; later calls return the first result.
func (s *Session) Close() (string, error) {
	s.closeOnce.Do(func() {
		if s.stop != nil {
			s.stop()
		}
		var errs []error
		if s.context != nil {
			errs = append(errs, s.context.Close())
		}
		if s.record && s.page != nil {
			path, err := s.page.Video().Path()
			s.videoPath = path
			errs = append(errs, err)
		}
		if s.browser != nil {
			errs = append(errs, s.browser.Close())
		}
		errs = append(errs, s.pw.Stop())
		s.closeErr = errors.Join(errs...)
	})
	return s.videoPath, s.closeErr
}

// Abort drops the session without finishing the video. Closing a recording
// context encodes the video first, which took ~14 s after a 35 s recording and
// would push an aborted render past the 31 s of FR-008; an aborted render
// discards the video anyway. Stopping the driver closes Chromium with it.
func (s *Session) Abort() {
	s.closeOnce.Do(func() {
		if s.stop != nil {
			s.stop()
		}
		s.closeErr = s.pw.Stop()
	})
}

func (s *Session) Goto(url string) error {
	_, err := s.page.Goto(url, playwright.PageGotoOptions{WaitUntil: playwright.WaitUntilStateLoad})
	return err
}

// Locators are strict: a selector matching several elements is an error
// naming the count (spike S2, ARCHITECTURE §7).
func (s *Session) Click(selector string) error {
	return s.page.Locator(selector).Click()
}

func (s *Session) Hover(selector string) error {
	return s.page.Locator(selector).Hover()
}

// Fill with no delay sets the value at once. With a delay it clears the field
// and types, so the video shows the text appearing (FR-006).
func (s *Session) Fill(selector, v string, delay time.Duration) error {
	loc := s.page.Locator(selector)
	if delay <= 0 {
		return loc.Fill(v)
	}
	if err := loc.Fill(""); err != nil {
		return err
	}
	return loc.PressSequentially(v, playwright.LocatorPressSequentiallyOptions{
		Delay: playwright.Float(float64(delay / time.Millisecond)),
	})
}

// Select matches the option by value or label (FR-005).
func (s *Session) Select(selector, v string) error {
	_, err := s.page.Locator(selector).SelectOption(playwright.SelectOptionValues{ValuesOrLabels: playwright.StringSlice(v)})
	return err
}

func (s *Session) Press(selector, key string) error {
	if selector == "" {
		return s.page.Keyboard().Press(key)
	}
	return s.page.Locator(selector).Press(key)
}

func (s *Session) ScrollIntoView(selector string) error {
	return s.page.Locator(selector).ScrollIntoViewIfNeeded()
}

func (s *Session) ScrollTo(y int) error {
	_, err := s.page.Evaluate("y => window.scrollTo(0, y)", y)
	return err
}

func (s *Session) WaitVisible(selector string) error {
	return s.page.Locator(selector).WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateVisible})
}

// MoveTo scrolls the element into view and glides the mouse to its center.
func (s *Session) MoveTo(selector string, steps int) error {
	loc := s.page.Locator(selector)
	if err := loc.ScrollIntoViewIfNeeded(); err != nil {
		return err
	}
	box, err := loc.BoundingBox()
	if err != nil {
		return err
	}
	if box == nil {
		return fmt.Errorf("element %q has no bounding box", selector)
	}
	return s.page.Mouse().Move(box.X+box.Width/2, box.Y+box.Height/2, playwright.MouseMoveOptions{Steps: playwright.Int(steps)})
}

// AriaSnapshot returns the accessibility tree of the page body as indented
// text (explore_page, ARCHITECTURE §8).
func (s *Session) AriaSnapshot() (string, error) {
	return s.page.Locator("body").AriaSnapshot()
}

// Count returns how many elements match selector right now.
func (s *Session) Count(selector string) (int, error) {
	return s.page.Locator(selector).Count()
}

// Title returns the document title.
func (s *Session) Title() (string, error) { return s.page.Title() }

// URL returns the page's current address.
func (s *Session) URL() string { return s.page.URL() }
