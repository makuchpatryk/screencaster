package subtitle

import (
	"strings"
	"testing"
	"time"
)

func TestASS_zeroStyleMatchesFFmpegBareSRTDefault(t *testing.T) { // FR-021, spike step 1
	got := string(ASS([]Cue{{Text: "hi"}}, Style{}))
	for _, want := range []string{
		"Style: Default,Arial,16,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,",
		"Dialogue: 0,0:00:00.00,0:00:00.00,Default,,0,0,0,,hi\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ASS() = %q, want it to contain %q", got, want)
		}
	}
}

func TestASS_styleFieldsOverrideDefaults(t *testing.T) {
	got := string(ASS(nil, Style{Font: "Courier New", Size: 60, Color: "#FF0000", Background: "#00FF007F"}))
	want := "Style: Default,Courier New,60,&H000000FF,&H000000FF,&H00000000,&H8000FF00,"
	if !strings.Contains(got, want) {
		t.Errorf("ASS() = %q, want it to contain %q", got, want)
	}
}

func TestAssColor(t *testing.T) { // pinned against ffmpeg's tx3g output (spike step 1)
	tests := []struct {
		name, hex, want string
	}{
		{"opaque RGB, no alpha", "#FF0000", "&H000000FF"},
		{"RGBA fully opaque", "#00FF00FF", "&H0000FF00"},
		{"RGBA half transparent", "#00FF007F", "&H8000FF00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := assColor(tt.hex); got != tt.want {
				t.Errorf("assColor(%q) = %q, want %q", tt.hex, got, tt.want)
			}
		})
	}
}

func TestAssTime(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0:00:00.00"},
		{1500 * time.Millisecond, "0:00:01.50"},
		{61*time.Second + 10*time.Millisecond, "0:01:01.01"},
		{time.Hour + 2*time.Second, "1:00:02.00"},
		{1234 * time.Millisecond, "0:00:01.23"}, // rounds to nearest centisecond
	}
	for _, tt := range tests {
		if got := assTime(tt.d); got != tt.want {
			t.Errorf("assTime(%s) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestASS_cueTimesAndOrder(t *testing.T) {
	cues := []Cue{
		{Start: 0, End: 2 * time.Second, Text: "one"},
		{Start: 2 * time.Second, End: 5500 * time.Millisecond, Text: "two"},
	}
	got := string(ASS(cues, Style{}))
	for _, want := range []string{
		"Dialogue: 0,0:00:00.00,0:00:02.00,Default,,0,0,0,,one\n",
		"Dialogue: 0,0:00:02.00,0:00:05.50,Default,,0,0,0,,two\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ASS() = %q, want it to contain %q", got, want)
		}
	}
	// one must come before two (ffmpeg input order).
	if strings.Index(got, "one") > strings.Index(got, "two") {
		t.Error("cues out of order")
	}
}

func TestEscape(t *testing.T) { // pinned against ffmpeg's ASS->mov_text parsing (spike step 1)
	tests := []struct {
		name, in, want string
	}{
		{"recognized override tag neutralized", `bold {\b1}BOLD{\b0} normal`, "bold (b1)BOLD(b0) normal"},
		{"unrecognized brace content also neutralized", "before {this is a brace block} after", "before (this is a brace block) after"},
		{"lone backslash dropped", `solo backslash \ here`, "solo backslash  here"},
		{"backslash-n sequence cannot form a soft break", `a\nb`, "anb"},
		{"CR dropped, LF becomes hard break", "a\r\nb", `a\Nb`},
		{"plain text untouched", "Click the New project button", "Click the New project button"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escape(tt.in); got != tt.want {
				t.Errorf("escape(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestLang(t *testing.T) {
	tests := []struct{ iso1, want string }{
		{"en", "eng"},
		{"pl", "pol"},
		{"de", "deu"},
		{"", "und"},
		{"###", "und"},
	}
	for _, tt := range tests {
		if got := Lang(tt.iso1); got != tt.want {
			t.Errorf("Lang(%q) = %q, want %q", tt.iso1, got, tt.want)
		}
	}
}
