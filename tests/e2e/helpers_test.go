//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"screencaster/core/browser"
	"screencaster/core/recorder"
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

func storageStatePath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../testdata/storageState.json")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// newRecorder wires a real recorder like main does. launched, when not nil,
// receives the time the browser was ready, so tests can time the steps without
// the Chromium start-up.
func newRecorder(t *testing.T, baseURL string, launched *time.Time) recorder.Recorder {
	t.Helper()
	launch := func(ctx context.Context, dir string) (recorder.Session, error) {
		s, err := browser.Launcher{}.Launch(ctx, browser.Options{
			BaseURL:          baseURL,
			StorageStatePath: storageStatePath(t),
			VideoDir:         dir,
			Visuals:          true,
		})
		if err != nil {
			return nil, err
		}
		if launched != nil {
			*launched = time.Now()
		}
		return s, nil
	}
	return recorder.New(launch, baseURL)
}

// brightnessThreshold separates the fixture's black page from the white flash
// (YAVG is 16 for black and 235 for white in limited-range video).
const brightnessThreshold = 128

// flashOnset returns the presentation time of the first bright frame that
// follows a dark one. The dark requirement skips the white about:blank frames
// every recording starts with.
func flashOnset(t *testing.T, video string) time.Duration {
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
