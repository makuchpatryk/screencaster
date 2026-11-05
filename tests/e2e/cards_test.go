//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// cardsScript is a short silent demo; the one %s is the fixture's base URL and
// the next the extra top-level fields (intro, outro, outputDir). The tags match
// assertVideo's.
const cardsScript = `name: e2e-demo
meta:
  title: E2E demo
  description: Every action plus the drift marker.
%[2]ssteps:
  - goto: %[1]s/marker.html
  - wait: 500
`

// frameAt decodes the video frame at `at`.
func frameAt(t *testing.T, video string, at time.Duration) image.Image {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-ss", fmt.Sprintf("%.3f", at.Seconds()), "-i", video,
		"-frames:v", "1", "-f", "image2pipe", "-vcodec", "png", "-").Output()
	if err != nil {
		t.Fatalf("ffmpeg frame at %v of %s: %v", at, video, err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode frame at %v: %v", at, err)
	}
	return img
}

// near reports whether the pixel at (x, y) is within 16 of want on every
// channel (the H.264 round trip is lossy).
func near(img image.Image, x, y int, want color.RGBA) bool {
	r, g, b, _ := img.At(x, y).RGBA()
	d := func(got uint32, want uint8) bool { return int(got>>8)-int(want) <= 16 && int(want)-int(got>>8) <= 16 }
	return d(r, want.R) && d(g, want.G) && d(b, want.B)
}

// cardBackground is the built-in card's and the picture padding's colour.
var cardBackground = color.RGBA{0x0f, 0x17, 0x2a, 0xff}

// pageBackground is the fixture's marker.html (body.dark in app.css).
var pageBackground = color.RGBA{0, 0, 0, 0xff}

// minRecording is the shortest recording cardsScript can make: its 500 ms wait.
const minRecording = 500 * time.Millisecond

func writeDemo(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, "demos", name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Decision 63, 81: cards only when set; true is the built-in card, an image
// replaces it, and omitted or false is none. Decision 62: the picture and
// the output folder are relative to the demo file.
func TestRender_cliCards(t *testing.T) {
	t.Parallel()
	bin := cliBinary(t)
	base := fixtureApp(t)
	dir := t.TempDir()

	// An 800x800 red square: on a 16:9 frame it sits centred between dark bars.
	red := image.NewRGBA(image.Rect(0, 0, 800, 800))
	for i := 0; i < len(red.Pix); i += 4 {
		copy(red.Pix[i:], []byte{0xff, 0, 0, 0xff})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, red); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "demos", "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "demos", "assets", "logo.png"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	writeDemo(t, dir, "plain.yaml", fmt.Sprintf(cardsScript, base, ""))
	writeDemo(t, dir, "default.yaml", fmt.Sprintf(cardsScript, base, "intro: true\noutro: true\n"))
	writeDemo(t, dir, "custom.yaml", fmt.Sprintf(cardsScript, base, "outputDir: videos\nintro:\n  image: assets/logo.png\n  durationMs: 2000\noutro: false\n"))

	render := func(demo string) string {
		t.Helper()
		res := runCLI(t, bin, dir, "render", "demos/"+demo)
		if res.code != 0 {
			t.Fatalf("render %s exit %d\n%s", demo, res.code, res.stderr)
		}
		return outputPaths(t, res.stdout, "en")[0]
	}

	// The recording length is not stable (the goto's paint can take a second
	// under load), so a card is measured inside its own video, never against a
	// second render. The fixture page is black and a card is dark blue, so the
	// pixel at (5, 5) tells them apart. Only the end card has a sharp edge: the
	// start page carries the intro picture on until the first goto paints.
	const edge = 250 * time.Millisecond
	assertCards := func(video string, outro bool) time.Duration {
		t.Helper()
		got := assertVideo(t, video)
		if !outro {
			if img := frameAt(t, video, got-edge); !near(img, 5, 5, pageBackground) {
				t.Errorf("%s: the last frame is not the recorded page, want no end card", filepath.Base(video))
			}
			return got
		}
		if got < 2*defaultCard+minRecording {
			t.Errorf("%s: %v, want at least %v (two cards + the recording)", filepath.Base(video), got, 2*defaultCard+minRecording)
			return got
		}
		if img := frameAt(t, video, got-defaultCard-edge); !near(img, 5, 5, pageBackground) {
			t.Errorf("%s: no recorded page %v before the end card's start (%v)", filepath.Base(video), edge, got-defaultCard)
		}
		if img := frameAt(t, video, got-defaultCard+edge); !near(img, 5, 5, cardBackground) {
			t.Errorf("%s: no end card %v after its start (%v), want a %v card", filepath.Base(video), edge, got-defaultCard, defaultCard)
		}
		return got
	}

	assertCards(render("plain.yaml"), false)

	def := render("default.yaml")
	assertCards(def, true)
	for _, at := range []time.Duration{500 * time.Millisecond, 2500 * time.Millisecond} {
		if img := frameAt(t, def, at); !near(img, 960, 540, cardBackground) || !near(img, 5, 5, cardBackground) {
			t.Errorf("default intro at %v is not the dark card", at)
		}
	}

	custom := render("custom.yaml")
	if got, want := filepath.Dir(custom), filepath.Join(dir, "demos", "videos"); got != want {
		t.Errorf("custom video in %s, want %s (outputDir is relative to the demo)", got, want)
	}
	if got := assertCards(custom, false); got < 2*time.Second+minRecording {
		t.Errorf("custom intro, no outro: %v, want at least %v", got, 2*time.Second+minRecording)
	}
	if img := frameAt(t, custom, 1750*time.Millisecond); !near(img, 960, 540, color.RGBA{0xff, 0, 0, 0xff}) {
		t.Error("custom intro is shorter than its 2 s")
	}
	img := frameAt(t, custom, 500*time.Millisecond)
	if !near(img, 960, 540, color.RGBA{0xff, 0, 0, 0xff}) || !near(img, 100, 540, cardBackground) {
		t.Error("custom intro is not the red picture centred on the dark background")
	}
}
