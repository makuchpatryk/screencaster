//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"screencaster/internal/adapters/browser"
	"screencaster/internal/domain/executor"
	"screencaster/internal/domain/recorder"
	"screencaster/internal/domain/script"
)

// TestMain builds the CLI once for every CLI test, unless SCREENCASTER_BIN names
// a binary already (the e2e-runtime target).
func TestMain(m *testing.M) {
	if os.Getenv("SCREENCASTER_BIN") != "" {
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "screencaster-e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	bin := filepath.Join(dir, "screencaster")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "../../cmd/screencaster"
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "go build cli: %v\n%s", err, out)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	if err := os.Setenv("SCREENCASTER_BIN", bin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// shortActionTimeout lowers executor.ActionTimeout for one test, so an action
// that times out fails in seconds, not 30 s. The variable is process-wide: the
// test must not be parallel.
func shortActionTimeout(t *testing.T) time.Duration {
	t.Helper()
	const d = 3 * time.Second
	prev := executor.ActionTimeout
	executor.ActionTimeout = d
	t.Cleanup(func() { executor.ActionTimeout = prev })
	return d
}

// fixtureApp serves testdata/fixture-app and returns its base URL
// (http://127.0.0.1:<port>, the host the storageState cookie is set for).
func fixtureApp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../../testdata/fixture-app")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(srv.Close)
	return srv.URL
}

// The fixture's login: the cookie that makes #logged-in visible. fixtureState
// and stateYAML are the same session as a Go value and as a script line.
var fixtureState = &script.StorageState{Cookies: []script.Cookie{
	{Name: "sc_session", Value: "fixture", Domain: "127.0.0.1", Path: "/"},
}}

const stateYAML = "storageState: {cookies: [{name: sc_session, value: fixture, domain: 127.0.0.1, path: /}]}"

// newRecorder wires a real recorder like main does. launched, when not nil,
// receives the time the browser was ready, so tests can time the steps without
// the Chromium start-up.
func newRecorder(t *testing.T, launched *time.Time) recorder.Recorder {
	t.Helper()
	launch := func(ctx context.Context, o recorder.LaunchOptions) (recorder.Session, error) {
		s, err := browser.Launcher{}.Launch(ctx, browser.Options{
			StorageState: o.StorageState,
			VideoDir:     o.VideoDir,
			Marker:       true,
			Visuals:      true,
		})
		if err != nil {
			return nil, err
		}
		if launched != nil {
			*launched = time.Now()
		}
		return s, nil
	}
	return recorder.New(launch)
}

// brightnessThreshold separates the fixture's black page from the white flash
// (YAVG is 16 for black and 235 for white in limited-range video).
const brightnessThreshold = 128

// lastFlashOnset returns the presentation time of the last bright frame that
// follows a dark one, looking only at frames from `from` on. The fixture's
// pages before marker.html are white (index.html paints bright right after the
// intro), so the first transition is a page paint, not the drift probe: the
// marker step is the last in the script, which makes its flash the last
// transition. The dark requirement skips any white frames the recording starts
// with; `from` skips an intro card, which is dark too.
func lastFlashOnset(t *testing.T, video string, from time.Duration) time.Duration {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-i", video,
		"-vf", "signalstats,metadata=mode=print:file=-", "-f", "null", "-").Output()
	if err != nil {
		t.Fatalf("ffmpeg signalstats %s: %v", video, err)
	}

	var pts, onset time.Duration
	seenDark, found := false, false
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.Contains(line, "pts_time:"):
			sec, err := strconv.ParseFloat(line[strings.Index(line, "pts_time:")+len("pts_time:"):], 64)
			if err != nil {
				t.Fatalf("parse %q: %v", line, err)
			}
			pts = time.Duration(sec * float64(time.Second))
		case pts < from:
			// still in the intro card
		case strings.HasPrefix(line, "lavfi.signalstats.YAVG="):
			yavg, err := strconv.ParseFloat(strings.TrimPrefix(line, "lavfi.signalstats.YAVG="), 64)
			if err != nil {
				t.Fatalf("parse %q: %v", line, err)
			}
			if yavg < brightnessThreshold {
				seenDark = true
			} else if seenDark {
				onset, found = pts, true
				seenDark = false // the next transition needs a new dark frame
			}
		}
	}
	if !found {
		t.Fatalf("no dark-to-bright transition in %s", video)
	}
	return onset
}

// brightestFrame returns the highest YAVG of any frame in the video.
func brightestFrame(t *testing.T, video string) float64 {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-i", video,
		"-vf", "signalstats,metadata=mode=print:file=-", "-f", "null", "-").Output()
	if err != nil {
		t.Fatalf("ffmpeg signalstats %s: %v", video, err)
	}
	var max float64
	frames := 0
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		v, ok := strings.CutPrefix(sc.Text(), "lavfi.signalstats.YAVG=")
		if !ok {
			continue
		}
		yavg, err := strconv.ParseFloat(v, 64)
		if err != nil {
			t.Fatalf("parse %q: %v", sc.Text(), err)
		}
		max = math.Max(max, yavg)
		frames++
	}
	if frames == 0 {
		t.Fatalf("no frames in %s", video)
	}
	return max
}
