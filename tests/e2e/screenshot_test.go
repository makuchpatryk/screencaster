//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"screencaster/internal/adapters/browser"
	"screencaster/internal/domain/card"
)

// shoot renders each text as a built-in card and returns the PNG paths.
func shoot(t *testing.T, texts ...card.Text) []string {
	t.Helper()
	dir := t.TempDir()
	var shots []browser.Shot
	var paths []string
	for i, tx := range texts {
		html, err := card.HTML(tx)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, string(rune('a'+i))+".png")
		shots = append(shots, browser.Shot{HTML: html, Out: p})
		paths = append(paths, p)
	}
	if err := (browser.Launcher{}).Screenshot(context.Background(), shots); err != nil {
		t.Fatal(err)
	}
	return paths
}

func readPNG(t *testing.T, path string) image.Image {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// Decision 63: the built-in card is a 1920x1080 picture with text on the
// dark background, one Chromium for both cards.
func TestScreenshot_cardIsFullFrameAndNotBlank(t *testing.T) {
	paths := shoot(t, card.Text{Title: "Projects tour", Subtitle: "A short demo"}, card.Text{Title: card.Outro("en")})

	for _, p := range paths {
		img := readPNG(t, p)
		if b := img.Bounds(); b.Dx() != browser.Width || b.Dy() != browser.Height {
			t.Errorf("%s is %dx%d, want %dx%d", filepath.Base(p), b.Dx(), b.Dy(), browser.Width, browser.Height)
		}
		if r, g, b, _ := img.At(5, 5).RGBA(); r>>8 != 0x0f || g>>8 != 0x17 || b>>8 != 0x2a {
			t.Errorf("%s corner = %02x%02x%02x, want the 0f172a background", filepath.Base(p), r>>8, g>>8, b>>8)
		}
		light := 0
		for y := 0; y < browser.Height; y += 4 {
			for x := 0; x < browser.Width; x += 4 {
				if r, _, _, _ := img.At(x, y).RGBA(); r>>8 > 0x80 {
					light++
				}
			}
		}
		if light < 100 {
			t.Errorf("%s has %d light sample pixels, want visible text", filepath.Base(p), light)
		}
	}
}

// Spike S5: the image's fonts have the Polish letters. A missing glyph draws
// the same box for every letter, so two different strings would look alike.
func TestScreenshot_polishGlyphsAreDistinct(t *testing.T) {
	paths := shoot(t, card.Text{Title: "ĄĘŁŻ"}, card.Text{Title: "ŹŃÓĆ"}, card.Text{Title: "ąęłżźńóć"}, card.Text{Title: "źńóćąęłż"})

	same := func(a, b string) bool {
		x, _ := os.ReadFile(a)
		y, _ := os.ReadFile(b)
		return bytes.Equal(x, y)
	}
	if same(paths[0], paths[1]) || same(paths[2], paths[3]) {
		t.Error("different Polish strings rendered identically: glyphs are missing")
	}
}
