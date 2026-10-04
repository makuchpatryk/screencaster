//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"screencaster/core/tts"
)

// markerText is the narration of the drift probe step. It starts with a
// plosive so the speech onset is sharp.
const markerText = "Bang. This is the marker."

// renderScript covers every action, two narrated steps on the way and the
// narrated marker step: press Enter on #marker flashes the viewport white.
// press has no cursor glide, so the flash starts right at the step offset.
const renderScript = `name: e2e-demo
meta:
  title: E2E demo
  description: Every action plus the drift marker.
steps:
  - action: goto
    url: /index.html
    narration:
      en: Welcome to the fixture app.
      pl: Witaj w aplikacji testowej.
  - action: wait
    selector: "#logged-in"
  - action: goto
    url: /projects.html
  - action: click
    selector: role=button[name="New project"]
    narration:
      en: Open the form.
      pl: Otwórz formularz.
  - action: fill
    selector: input[name="name"]
    value: Demo
  - action: select
    selector: select[name="visibility"]
    value: private
  - action: press
    selector: input[name="name"]
    key: Tab
  - action: hover
    selector: role=button[name="Create"]
  - action: click
    selector: role=button[name="Create"]
  - action: wait
    selector: "#toast"
  - action: scroll
    selector: "#footer"
  - action: scroll
    y: 0
  - action: goto
    url: /marker.html
  - action: wait
    ms: 1000
  - action: press
    selector: "#marker"
    key: Enter
    narration:
      en: ` + markerText + `
      pl: Bum. To jest znacznik.
  - action: wait
    ms: 1500
`

const failingScript = `name: e2e-fail
steps:
  - action: goto
    url: /index.html
  - action: wait
    selector: "#logged-in"
  - action: goto
    url: /projects.html
  - action: click
    selector: "#does-not-exist"
`

var outputName = regexp.MustCompile(`^e2e-demo\.(en|pl)\.(\d{8}T\d{6}Z)\.mp4$`)

// cliBinary builds the screencaster binary, or returns the one named by
// SCREENCASTER_BIN: `make e2e-runtime` points it at the runtime image's binary.
func cliBinary(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("SCREENCASTER_BIN"); bin != "" {
		return bin
	}
	bin := filepath.Join(t.TempDir(), "screencaster")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "../../cli"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build cli: %v\n%s", err, out)
	}
	return bin
}

// project builds a work dir like a user's repo: config, storageState and the
// scripts under demos/.
func project(t *testing.T, baseURL string) string {
	t.Helper()
	dir := t.TempDir()
	state, err := os.ReadFile(storageStatePath(t))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"screencaster.yaml":      "baseUrl: " + baseURL + "\nstorageState: auth/storageState.json\n",
		"auth/storageState.json": string(state),
		"demos/e2e-demo.yaml":    renderScript,
		"demos/e2e-fail.yaml":    failingScript,
	}
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type cliResult struct {
	code           int
	stdout, stderr string
	elapsed        time.Duration
}

// cliTimeout bounds one CLI run, so a hung tool fails the test instead of the
// whole suite timing out.
const cliTimeout = 4 * time.Minute

func runCLI(t *testing.T, bin, dir string, args ...string) cliResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err := cmd.Run()
	res := cliResult{stdout: stdout.String(), stderr: stderr.String(), elapsed: time.Since(start)}
	var ee *exec.ExitError
	if ctx.Err() != nil {
		t.Fatalf("screencaster %v did not finish within %v\n%s", args, cliTimeout, res.stderr)
	}
	if errors.As(err, &ee) {
		res.code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run cli: %v", err)
	}
	return res
}

// FR-011, FR-009, FR-010, NFR-001, NFR-002 and the FR-007 drift bound, end to
// end through the CLI binary with real Chromium, Piper and ffmpeg.
func TestRender_cli(t *testing.T) {
	bin := cliBinary(t)
	dir := project(t, fixtureApp(t))

	// Default languages: one EN video (FR-004 AC2).
	en := runCLI(t, bin, dir, "render", "demos/e2e-demo.yaml")
	if en.code != 0 {
		t.Fatalf("default render exit %d\n%s", en.code, en.stderr)
	}
	enPaths := outputPaths(t, en.stdout, "en")
	if !strings.Contains(en.stderr, "[en] step 4/16 click role=button[name=\"New project\"]") {
		t.Errorf("progress line missing from stderr:\n%s", en.stderr)
	}

	// EN + PL (FR-004 AC1), timed for NFR-001.
	both := runCLI(t, bin, dir, "render", "demos/e2e-demo.yaml", "--lang", "en,pl")
	if both.code != 0 {
		t.Fatalf("en,pl render exit %d\n%s", both.code, both.stderr)
	}
	bothPaths := outputPaths(t, both.stdout, "en", "pl")

	// FR-010: one timestamp per job, and a second render never overwrites.
	if ts(bothPaths[0]) != ts(bothPaths[1]) {
		t.Errorf("en and pl timestamps differ: %s, %s", bothPaths[0], bothPaths[1])
	}
	if enPaths[0] == bothPaths[0] {
		t.Errorf("second render reused %s", enPaths[0])
	}
	for _, p := range append(enPaths, bothPaths...) {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("output missing: %v", err)
		}
	}

	var total time.Duration
	for _, p := range bothPaths {
		total += assertVideo(t, p)
	}
	t.Logf("NFR-001: en,pl render took %v for %v of video (ratio %.2f)", both.elapsed.Round(time.Millisecond), total.Round(time.Millisecond), float64(both.elapsed)/float64(total))
	if both.elapsed > 2*total {
		t.Errorf("NFR-001: render took %v, more than 2x the %v of output", both.elapsed, total)
	}

	assertDrift(t, enPaths[0])

	if left, _ := filepath.Glob(filepath.Join(dir, ".screencaster", "tmp", "*")); len(left) > 0 {
		t.Errorf("temp dirs left behind: %v", left)
	}
}

// FR-008 AC through the CLI: a missing selector fails with exit 1 and the
// output dir stays as it was.
func TestRender_cliFailureLeavesOutputUnchanged(t *testing.T) {
	bin := cliBinary(t)
	dir := project(t, fixtureApp(t))

	res := runCLI(t, bin, dir, "render", "demos/e2e-fail.yaml")
	if res.code != 1 {
		t.Fatalf("exit %d, want 1\n%s", res.code, res.stderr)
	}
	if !strings.Contains(res.stderr, "step 4 (en) click #does-not-exist") {
		t.Errorf("stderr does not name step 4 (en):\n%s", res.stderr)
	}
	if res.stdout != "" {
		t.Errorf("stdout = %q, want no paths", res.stdout)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "output")); len(entries) > 0 {
		t.Errorf("output dir has %d entries, want none", len(entries))
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".screencaster", "tmp", "*")); len(left) > 0 {
		t.Errorf("temp dirs left behind: %v", left)
	}
}

// outputPaths checks stdout lists one absolute path per wanted language, in
// order, named <name>.<lang>.<ts>.mp4.
func outputPaths(t *testing.T, stdout string, langs ...string) []string {
	t.Helper()
	paths := strings.Fields(stdout)
	if len(paths) != len(langs) {
		t.Fatalf("stdout = %q, want %d paths", stdout, len(langs))
	}
	for i, p := range paths {
		m := outputName.FindStringSubmatch(filepath.Base(p))
		if !filepath.IsAbs(p) || m == nil || m[1] != langs[i] {
			t.Errorf("path %q, want absolute e2e-demo.%s.<ts>.mp4", p, langs[i])
		}
	}
	return paths
}

func ts(path string) string {
	return outputName.FindStringSubmatch(filepath.Base(path))[2]
}

type probe struct {
	Streams []struct {
		CodecType  string `json:"codec_type"`
		CodecName  string `json:"codec_name"`
		Width      int    `json:"width"`
		Height     int    `json:"height"`
		RFrameRate string `json:"r_frame_rate"`
	} `json:"streams"`
	Format struct {
		Duration string            `json:"duration"`
		Tags     map[string]string `json:"tags"`
	} `json:"format"`
}

// assertVideo checks NFR-002 and the FR-009.5 tags and returns the duration.
func assertVideo(t *testing.T, path string) time.Duration {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_streams", "-show_format", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var p probe
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatal(err)
	}
	var video, audio bool
	for _, s := range p.Streams {
		switch s.CodecType {
		case "video":
			video = true
			if s.CodecName != "h264" || s.Width != 1920 || s.Height != 1080 || s.RFrameRate != "30/1" {
				t.Errorf("%s video = %s %dx%d %s, want h264 1920x1080 30/1", filepath.Base(path), s.CodecName, s.Width, s.Height, s.RFrameRate)
			}
		case "audio":
			audio = true
			if s.CodecName != "aac" {
				t.Errorf("%s audio = %s, want aac", filepath.Base(path), s.CodecName)
			}
		}
	}
	if !video || !audio {
		t.Errorf("%s: video stream %v, audio stream %v, want both", filepath.Base(path), video, audio)
	}
	if p.Format.Tags["title"] != "E2E demo" || p.Format.Tags["comment"] != "Every action plus the drift marker." {
		t.Errorf("%s tags = %v", filepath.Base(path), p.Format.Tags)
	}
	sec, err := strconv.ParseFloat(p.Format.Duration, 64)
	if err != nil {
		t.Fatal(err)
	}
	return time.Duration(sec * float64(time.Second))
}

// assertDrift compares the marker flash with the start of its narration clip
// in the final MP4 (FR-007 AC, ±100 ms). The clip's own leading silence is
// measured on a fresh synthesis of the same text and subtracted, so only the
// placement error remains.
func assertDrift(t *testing.T, mp4 string) {
	t.Helper()
	clip := filepath.Join(t.TempDir(), "marker.wav")
	if _, err := (tts.Piper{Bin: "/opt/piper/piper"}).Synthesize(context.Background(),
		"/opt/piper/voices/en_US-ryan-high.onnx", markerText, clip); err != nil {
		t.Fatal(err)
	}
	lead := leadSilence(t, clip)

	flash := flashOnset(t, mp4)
	// The clip before the marker ends seconds earlier, so the first sound
	// after (flash - 1 s) is the marker narration.
	onset := firstSoundAfter(t, mp4, flash-time.Second)
	drift := flash - (onset - lead)
	t.Logf("drift: flash at %v, narration onset %v, lead silence %v, drift %v",
		flash.Round(time.Millisecond), onset.Round(time.Millisecond), lead.Round(time.Millisecond), drift.Round(time.Millisecond))
	if drift < -100*time.Millisecond || drift > 100*time.Millisecond {
		t.Errorf("drift %v, want within ±100ms", drift)
	}
}

var (
	silenceStart = regexp.MustCompile(`silence_start: (-?[0-9.]+)`)
	silenceEnd   = regexp.MustCompile(`silence_end: ([0-9.]+)`)
)

// silences runs ffmpeg silencedetect (below -50 dB for 20 ms or more) and
// returns the start and end times it reports.
func silences(t *testing.T, media string) (starts, ends []time.Duration) {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-hide_banner", "-nostdin", "-i", media,
		"-af", "silencedetect=noise=-50dB:d=0.02", "-vn", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("silencedetect %s: %v\n%s", media, err, out)
	}
	parse := func(re *regexp.Regexp) []time.Duration {
		var ds []time.Duration
		for _, m := range re.FindAllStringSubmatch(string(out), -1) {
			sec, err := strconv.ParseFloat(m[1], 64)
			if err != nil {
				t.Fatal(err)
			}
			ds = append(ds, time.Duration(sec*float64(time.Second)))
		}
		return ds
	}
	return parse(silenceStart), parse(silenceEnd)
}

// leadSilence is how long a clip stays silent before the speech starts: the
// end of a silence that begins at 0, else 0.
func leadSilence(t *testing.T, clip string) time.Duration {
	t.Helper()
	starts, ends := silences(t, clip)
	if len(starts) == 0 || starts[0] > time.Millisecond || len(ends) == 0 {
		return 0
	}
	return ends[0]
}

// firstSoundAfter returns the first end of silence at or after from.
func firstSoundAfter(t *testing.T, media string, from time.Duration) time.Duration {
	t.Helper()
	_, ends := silences(t, media)
	for _, at := range ends {
		if at >= from {
			return at
		}
	}
	t.Fatalf("no sound after %v in %s", from, media)
	return 0
}
