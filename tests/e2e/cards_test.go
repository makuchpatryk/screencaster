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

// Decision 63: by default a video is 3 s card + recording + 3 s card, a custom
// image replaces the card, and false removes it. Decision 62: the picture and
// the output folder are relative to the demo file.
func TestRender_cliCards(t *testing.T) {
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

	writeDemo(t, dir, "plain.yaml", fmt.Sprintf(cardsScript, base, "intro: false\noutro: false\n"))
	writeDemo(t, dir, "default.yaml", fmt.Sprintf(cardsScript, base, ""))
	writeDemo(t, dir, "custom.yaml", fmt.Sprintf(cardsScript, base, "outputDir: videos\nintro:\n  image: assets/logo.png\n  durationMs: 2000\noutro: false\n"))

	render := func(demo string) string {
		t.Helper()
		res := runCLI(t, bin, dir, "render", "demos/"+demo)
		if res.code != 0 {
			t.Fatalf("render %s exit %d\n%s", demo, res.code, res.stderr)
		}
		return outputPaths(t, res.stdout, "en")[0]
	}

	// The recording length varies a little between runs, so the card time is
	// measured against a render of the same demo without cards.
	const tolerance = 300 * time.Millisecond
	plain := assertVideo(t, render("plain.yaml"))

	def := render("default.yaml")
	if got := assertVideo(t, def); got < plain+2*defaultCard-tolerance || got > plain+2*defaultCard+tolerance {
		t.Errorf("default cards: %v, want about %v (recording %v + 6 s)", got, plain+2*defaultCard, plain)
	}
	for _, at := range []time.Duration{500 * time.Millisecond, 2500 * time.Millisecond} {
		if img := frameAt(t, def, at); !near(img, 960, 540, cardBackground) || !near(img, 5, 5, cardBackground) {
			t.Errorf("default intro at %v is not the dark card", at)
		}
	}
	if img := frameAt(t, def, plain+2*defaultCard-time.Second); !near(img, 5, 5, cardBackground) {
		t.Error("the last second is not the dark end card")
	}

	custom := render("custom.yaml")
	if got, want := filepath.Dir(custom), filepath.Join(dir, "demos", "videos"); got != want {
		t.Errorf("custom video in %s, want %s (outputDir is relative to the demo)", got, want)
	}
	if got := assertVideo(t, custom); got < plain+2*time.Second-tolerance || got > plain+2*time.Second+tolerance {
		t.Errorf("custom intro, no outro: %v, want about %v", got, plain+2*time.Second)
	}
	img := frameAt(t, custom, 500*time.Millisecond)
	if !near(img, 960, 540, color.RGBA{0xff, 0, 0, 0xff}) || !near(img, 100, 540, cardBackground) {
		t.Error("custom intro is not the red picture centred on the dark background")
	}
}
