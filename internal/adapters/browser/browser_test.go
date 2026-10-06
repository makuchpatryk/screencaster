package browser

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	playwright "github.com/mxschmitt/playwright-go"

	"screencaster/internal/domain/script"
)

// The script's storage state must reach Playwright field for field.
func TestPlaywrightState_mapsEveryField(t *testing.T) {
	got, err := playwrightState(&script.StorageState{
		Cookies: []script.Cookie{
			{Name: "s", Value: "v", Domain: "app.test", Path: "/", Expires: -1, HTTPOnly: true, Secure: true, SameSite: "Lax"},
			{Name: "u", Value: "w", URL: "http://app.test"},
		},
		Origins: []script.Origin{{Origin: "http://app.test", LocalStorage: []script.NameValue{{Name: "token", Value: "t"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Cookies) != 2 || len(got.Origins) != 1 {
		t.Fatalf("state = %+v, want 2 cookies and 1 origin", got)
	}
	c := got.Cookies[0]
	if c.Name != "s" || c.Value != "v" || *c.Domain != "app.test" || *c.Path != "/" || *c.Expires != -1 ||
		!*c.HttpOnly || !*c.Secure || string(*c.SameSite) != "Lax" {
		t.Errorf("cookie 0 = %+v", c)
	}
	if u := got.Cookies[1]; *u.URL != "http://app.test" || u.Domain != nil || u.Path != nil {
		t.Errorf("cookie 1 = %+v, want only the url set", u)
	}
	if o := got.Origins[0]; o.Origin != "http://app.test" || len(o.LocalStorage) != 1 || o.LocalStorage[0].Name != "token" {
		t.Errorf("origin = %+v", o)
	}
}

// With no picture the start page is the card colour alone; with one it carries
// the picture, typed by its content, so the recording continues the start card.
func TestStartPage(t *testing.T) {
	png := filepath.Join(t.TempDir(), "card.png")
	if err := os.WriteFile(png, []byte("\x89PNG\r\n\x1a\nrest"), 0o600); err != nil {
		t.Fatal(err)
	}

	plain, err := startPage("", false)
	if err != nil || !strings.Contains(plain, startColor) || strings.Contains(plain, "<img") || strings.Contains(plain, markerID) {
		t.Errorf("startPage(\"\") = %q, %v, want the colour and no picture", plain, err)
	}
	pic, err := startPage(png, false)
	if err != nil || !strings.Contains(pic, startColor) || !strings.Contains(pic, `src="data:image/png;base64,`) {
		t.Errorf("startPage(png) = %q, %v, want the colour and an inline PNG", pic, err)
	}
	if _, err := startPage(filepath.Join(t.TempDir(), "missing.png"), false); err == nil {
		t.Error("startPage(missing) succeeded, want an error")
	}
}

// The sync marker goes after the picture, so it covers it (decision 69).
func TestStartPage_markerCoversThePicture(t *testing.T) {
	png := filepath.Join(t.TempDir(), "card.png")
	if err := os.WriteFile(png, []byte("\x89PNG\r\n\x1a\nrest"), 0o600); err != nil {
		t.Fatal(err)
	}
	page, err := startPage(png, true)
	if err != nil {
		t.Fatal(err)
	}
	img, marker := strings.Index(page, "<img"), strings.Index(page, `id="`+markerID+`"`)
	if img < 0 || marker < img || !strings.Contains(page, markerColor) {
		t.Errorf("startPage(png, true) = %q, want the marker after the picture", page)
	}
}

// Each capture area reaches Playwright as the option it needs, and every shot
// freezes animations and the caret so the pixels repeat (BR-001).
func TestScreenshotOptions_mapEachArea(t *testing.T) {
	const path = "/tmp/01.png"
	tests := []struct {
		name     string
		shot     script.Screenshot
		fullPage bool
		clip     *playwright.Rect
	}{
		{"viewport", script.Screenshot{}, false, nil},
		{"full page", script.Screenshot{FullPage: true}, true, nil},
		{"clip", script.Screenshot{Clip: &script.Clip{X: 1, Y: 2, Width: 30, Height: 40}}, false,
			&playwright.Rect{X: 1, Y: 2, Width: 30, Height: 40}},
		{"annotated viewport", script.Screenshot{Annotate: &script.Annotate{Selector: "#a", Box: true}}, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := pageScreenshotOptions(tt.shot, path)
			if *o.Path != path || o.Animations != playwright.ScreenshotAnimationsDisabled || o.Caret != playwright.ScreenshotCaretHide {
				t.Errorf("options = %+v, want the path, animations disabled and caret hidden", o)
			}
			if got := o.FullPage != nil && *o.FullPage; got != tt.fullPage {
				t.Errorf("FullPage = %v, want %v", got, tt.fullPage)
			}
			if !reflect.DeepEqual(o.Clip, tt.clip) {
				t.Errorf("Clip = %+v, want %+v", o.Clip, tt.clip)
			}
		})
	}

	l := locatorScreenshotOptions(path)
	if *l.Path != path || l.Animations != playwright.ScreenshotAnimationsDisabled || l.Caret != playwright.ScreenshotCaretHide {
		t.Errorf("locator options = %+v, want the path, animations disabled and caret hidden", l)
	}
}

// Capture removes the overlay by the id overlay.js draws it with.
func TestOverlayJS_usesAnnotationID(t *testing.T) {
	if !strings.Contains(overlayJS, `"`+annotationID+`"`) {
		t.Errorf("overlay.js does not use the id %q Capture removes it by", annotationID)
	}
}
