//go:build e2e

package e2e

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// shotsScript is a format template: the first %s is the fixture's base URL,
// the second its storageState line. Six shots of /shots.html: the viewport,
// the full page, one element, a clip, a shot with every marker, and the
// viewport again, which must show nothing of the markers.
const shotsScript = `name: e2e-shots
type: screenshots
%[2]s
steps:
  - goto: %[1]s/shots.html
  - screenshot: true
  - screenshot: { fullPage: true }
  - screenshot: { selector: "#panel" }
  - screenshot: { clip: { x: 100, y: 200, width: 400, height: 150 } }
  - screenshot:
      annotate: { selector: "#target", box: true, arrow: true, dim: true, label: Click Create }
  - screenshot: true
`

// shotsShortScript has the name of shotsScript and two shots, so a rerun with
// it must remove the four numbered shots it no longer writes.
const shotsShortScript = `name: e2e-shots
type: screenshots
%[2]s
steps:
  - goto: %[1]s/shots.html
  - screenshot: true
  - screenshot: { selector: "#panel" }
`

// shotsFailScript fails at step 3: .dup matches two elements, and locators are
// strict, so the annotation fails at once instead of after the 30 s timeout.
const shotsFailScript = `name: e2e-shots
type: screenshots
%[2]s
steps:
  - goto: %[1]s/shots.html
  - screenshot: true
  - screenshot: { annotate: { selector: ".dup", box: true } }
`

// shotsWallTime is the bound on one screenshots run through the CLI: browser
// start, six shots and publish, with no TTS, video or ffmpeg.
const shotsWallTime = 15 * time.Second

// overlayColor is the box colour in adapters/browser/overlay.js.
var overlayColor = [3]uint8{0xff, 0x2d, 0x55}

func decodePNG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return img
}

// rgb is the pixel at (x, y) as 8-bit channels.
func rgb(img image.Image, x, y int) [3]uint8 {
	r, g, b, _ := img.At(x, y).RGBA()
	return [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}
}

// channelsNear reports whether every channel of got is within tol of want.
func channelsNear(got, want [3]uint8, tol int) bool {
	for i := range got {
		if d := int(got[i]) - int(want[i]); d < -tol || d > tol {
			return false
		}
	}
	return true
}

func samePixels(a, b image.Image) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	bd := a.Bounds()
	for y := bd.Min.Y; y < bd.Max.Y; y++ {
		for x := bd.Min.X; x < bd.Max.X; x++ {
			if rgb(a, x, y) != rgb(b, x, y) {
				return false
			}
		}
	}
	return true
}

func assertSize(t *testing.T, img image.Image, name string, w, h int) {
	t.Helper()
	if got := img.Bounds().Size(); got.X != w || got.Y != h {
		t.Errorf("%s is %dx%d, want %dx%d", name, got.X, got.Y, w, h)
	}
}

// writeShotsDemo adds a screenshots script under demos/ of the project.
func writeShotsDemo(t *testing.T, dir, name, tmpl, baseURL string) {
	t.Helper()
	p := filepath.Join(dir, "demos", name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(fmt.Sprintf(tmpl, baseURL, stateYAML)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func listNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// Screenshots end to end through the CLI binary with real Chromium: the
// capture areas and their sizes, the annotation markers in the pixels, no
// overlay left for the next shot, the stable output folder (overwrite, stale
// removal, foreign files kept), and a failure that leaves the folder alone
// (decisions 72-75).
func TestShots_cli(t *testing.T) {
	bin := cliBinary(t)
	base := fixtureApp(t)
	dir := t.TempDir()
	writeShotsDemo(t, dir, "e2e-shots.yaml", shotsScript, base)
	writeShotsDemo(t, dir, "e2e-shots-short.yaml", shotsShortScript, base)
	writeShotsDemo(t, dir, "e2e-shots-fail.yaml", shotsFailScript, base)
	out := filepath.Join(dir, "demos", "output", "e2e-shots", "screenshots")

	// A language override means nothing to a screenshots script: refused
	// before any browser starts.
	if res := runCLI(t, bin, dir, "render", "demos/e2e-shots.yaml", "--lang", "pl"); res.code != 1 ||
		!strings.Contains(res.stderr, "/languages: languages are not used by a screenshots script") {
		t.Errorf("--lang: exit %d, stderr %q, want exit 1 and the languages error", res.code, res.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "demos", "output")); err == nil {
		t.Error("a refused render created the output dir")
	}

	// Files that are not NN.png belong to the user and survive every run.
	for _, name := range []string{"notes.txt", "cover.png"} {
		if err := os.MkdirAll(out, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, name), []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	res := runCLI(t, bin, dir, "render", "demos/e2e-shots.yaml")
	if res.code != 0 {
		t.Fatalf("render exit %d\n%s", res.code, res.stderr)
	}
	t.Logf("screenshots run took %v", res.elapsed)
	if res.elapsed > shotsWallTime {
		t.Errorf("screenshots run took %v, want <= %v", res.elapsed, shotsWallTime)
	}
	var want []string
	for k := 1; k <= 6; k++ {
		want = append(want, filepath.Join(out, fmt.Sprintf("%02d.png", k)))
	}
	if got := strings.Fields(res.stdout); !reflect.DeepEqual(got, want) {
		t.Errorf("stdout paths = %v, want %v", got, want)
	}
	// Progress has no [lang] prefix; the log says what happened (decision 64).
	for _, line := range []string{"step 2/7 screenshot\n", "step 6/7 screenshot #target\n",
		"render start: demos/e2e-shots.yaml name=e2e-shots type=screenshots steps=7 ", "screenshots: 6 captured in ", "render done in "} {
		if !strings.Contains(res.stderr, line) {
			t.Errorf("stderr lacks %q:\n%s", line, res.stderr)
		}
	}

	shot := func(k int) image.Image { return decodePNG(t, filepath.Join(out, fmt.Sprintf("%02d.png", k))) }
	viewport := shot(1)
	assertSize(t, viewport, "01.png (viewport)", 1920, 1080)
	if full := shot(2); full.Bounds().Dx() != 1920 || full.Bounds().Dy() <= 1080 {
		t.Errorf("02.png (fullPage) is %v, want 1920 wide and taller than 1080", full.Bounds().Size())
	}
	element := shot(3)
	assertSize(t, element, "03.png (#panel)", 300, 120) // the panel's box in shots.html
	if c := rgb(element, 150, 60); !channelsNear(c, [3]uint8{0x3b, 0x82, 0xf6}, 2) {
		t.Errorf("03.png centre = %v, want the panel's blue", c)
	}
	clip := shot(4)
	assertSize(t, clip, "04.png (clip)", 400, 150)
	if c := rgb(clip, 5, 5); !channelsNear(c, [3]uint8{0x3b, 0x82, 0xf6}, 2) { // the clip starts on the panel's corner
		t.Errorf("04.png (5,5) = %v, want the panel's blue", c)
	}

	// The annotated shot: #target is at (800,400) 240x70. The box is 6 px
	// outside it with a 4 px border, so x=796 sits inside the left border; the
	// dim shadow darkens the page's white corner.
	annotated := shot(5)
	assertSize(t, annotated, "05.png (annotated)", 1920, 1080)
	if c := rgb(annotated, 796, 435); !channelsNear(c, overlayColor, 3) {
		t.Errorf("05.png box outline at (796,435) = %v, want %v", c, overlayColor)
	}
	if c, plain := rgb(annotated, 1800, 1000), rgb(viewport, 1800, 1000); c[0] >= plain[0]-60 {
		t.Errorf("05.png corner = %v, want it darker than the plain shot's %v (dim)", c, plain)
	}
	if samePixels(annotated, viewport) {
		t.Error("05.png equals the plain viewport: no markers were drawn")
	}

	// The overlay is gone again for the next shot.
	if !samePixels(shot(6), viewport) {
		t.Error("06.png differs from 01.png: the overlay leaked into the next shot")
	}

	for _, name := range []string{"notes.txt", "cover.png"} {
		if b, err := os.ReadFile(filepath.Join(out, name)); err != nil || string(b) != "mine" {
			t.Errorf("%s = %q, %v; want it untouched", name, b, err)
		}
	}
	assertNoTemp(t, dir)

	// A rerun with fewer shots overwrites 01 and 02 and removes 03-06.
	res = runCLI(t, bin, dir, "render", "demos/e2e-shots-short.yaml")
	if res.code != 0 {
		t.Fatalf("short render exit %d\n%s", res.code, res.stderr)
	}
	if got, want := listNames(t, out), []string{"01.png", "02.png", "cover.png", "notes.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("folder after the rerun = %v, want %v", got, want)
	}
	assertSize(t, decodePNG(t, filepath.Join(out, "02.png")), "02.png after the rerun (#panel)", 300, 120)

	// A failing step reports itself and leaves the folder as the last good run
	// made it (BR-004).
	res = runCLI(t, bin, dir, "render", "demos/e2e-shots-fail.yaml")
	if res.code != 1 || !strings.Contains(res.stderr, "step 3 screenshot .dup: ") {
		t.Errorf("failing render: exit %d, stderr %q, want exit 1 and the step 3 failure", res.code, res.stderr)
	}
	if got, want := listNames(t, out), []string{"01.png", "02.png", "cover.png", "notes.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("folder after the failed run = %v, want it unchanged %v", got, want)
	}
	assertNoTemp(t, dir)
}

// assertNoTemp checks the run's temp dir was removed (ARCHITECTURE §11).
func assertNoTemp(t *testing.T, dir string) {
	t.Helper()
	if left, _ := filepath.Glob(filepath.Join(dir, ".screencaster", "tmp", "*")); len(left) > 0 {
		t.Errorf("temp dirs left behind: %v", left)
	}
}
