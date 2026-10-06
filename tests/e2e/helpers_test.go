//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"screencaster/internal/adapters/browser"
	"screencaster/internal/domain/recorder"
	"screencaster/internal/domain/script"
)

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
			BaseURL:      o.BaseURL,
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

// flashOnset returns the presentation time of the first bright frame that
// follows a dark one, looking only at frames from `from` on. The dark
// requirement skips any white frames the recording starts with; `from` skips
// an intro card, which is dark too.
func flashOnset(t *testing.T, video string, from time.Duration) time.Duration {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-i", video,
		"-vf", "signalstats,metadata=mode=print:file=-", "-f", "null", "-").Output()
	if err != nil {
		t.Fatalf("ffmpeg signalstats %s: %v", video, err)
	}

	var pts time.Duration
	seenDark := false
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
				return pts
			}
		}
	}
	t.Fatalf("no dark-to-bright transition in %s", video)
	return 0
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
