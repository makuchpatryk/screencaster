// Package card builds the built-in start and end cards (decision 63): the HTML
// page Chromium screenshots, and the closing line per language. It is pure; no
// I/O and no process. The template embeds all CSS and uses system fonts, so a
// card renders offline (NFR-003) and the same bytes give the same picture
// (BR-001).
package card

import (
	_ "embed"
	"fmt"
	"html/template"
	"strings"
)

//go:embed card.html
var pageHTML string

var page = template.Must(template.New("card").Parse(pageHTML))

// outro is the closing line per language, one place for the text (CODE_QUALITY
// DRY). A test keeps it covering every language with a built-in voice.
var outro = map[string]string{
	"en": "Thank you for watching",
	"pl": "Dziękujemy za uwagę",
}

const outroFallback = "en"

// Text is what a card says. An empty Subtitle leaves the line out.
type Text struct{ Title, Subtitle string }

// HTML returns the page for t. The text is escaped.
func HTML(t Text) (string, error) {
	var b strings.Builder
	if err := page.Execute(&b, t); err != nil {
		return "", fmt.Errorf("render card: %w", err)
	}
	return b.String(), nil
}

// Outro is the closing line in lang, English when lang has none.
func Outro(lang string) string {
	if s, ok := outro[lang]; ok {
		return s
	}
	return outro[outroFallback]
}
