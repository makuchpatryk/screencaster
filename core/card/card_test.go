package card

import (
	"strings"
	"testing"

	"screencaster/core/voices"
)

func TestHTML_escapesText(t *testing.T) {
	got, err := HTML(Text{Title: `<script>alert("x")</script>`, Subtitle: "a & b"})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"<script>", `alert("x")`} {
		if strings.Contains(got, bad) {
			t.Errorf("page contains unescaped %q", bad)
		}
	}
	if !strings.Contains(got, "&lt;script&gt;") || !strings.Contains(got, "a &amp; b") {
		t.Errorf("text not escaped as HTML:\n%s", got)
	}
}

func TestHTML_keepsPolishText(t *testing.T) {
	got, err := HTML(Text{Title: "Dziękujemy za uwagę", Subtitle: "żółć"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Dziękujemy za uwagę", "żółć", `charset="utf-8"`} {
		if !strings.Contains(got, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestHTML_emptySubtitleLeavesLineOut(t *testing.T) {
	with, _ := HTML(Text{Title: "T", Subtitle: "S"})
	without, _ := HTML(Text{Title: "T"})
	if !strings.Contains(with, "<p>S</p>") || strings.Contains(without, "<p>") {
		t.Errorf("subtitle line: with = %v, without = %v", strings.Contains(with, "<p>"), strings.Contains(without, "<p>"))
	}
}

func TestHTML_isDeterministic(t *testing.T) { // BR-001
	a, _ := HTML(Text{Title: "T", Subtitle: "S"})
	b, _ := HTML(Text{Title: "T", Subtitle: "S"})
	if a != b {
		t.Error("same text gave different pages")
	}
}

func TestOutro(t *testing.T) {
	tests := []struct{ lang, want string }{
		{"en", "Thank you for watching"},
		{"pl", "Dziękujemy za uwagę"},
		{"de", "Thank you for watching"}, // no line for de: English
		{"", "Thank you for watching"},
	}
	for _, tt := range tests {
		t.Run(tt.lang, func(t *testing.T) {
			if got := Outro(tt.lang); got != tt.want {
				t.Errorf("Outro(%q) = %q, want %q", tt.lang, got, tt.want)
			}
		})
	}
}

func TestOutro_coversBuiltInVoiceLanguages(t *testing.T) { // BR-011
	// With nothing installed, Options lists exactly the built-in languages.
	opts := voices.Options(voices.Installed{})
	if len(opts) == 0 {
		t.Fatal("no built-in languages listed")
	}
	for _, o := range opts {
		if outro[o.Code] == "" {
			t.Errorf("no closing line for built-in language %q", o.Code)
		}
	}
}
