package wire

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"screencaster/internal/adapters/tts/piper"
	"screencaster/internal/domain/card"
	"screencaster/internal/domain/voices"
)

// env is a getenv over a map, so no test touches the process environment.
func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestTTS(t *testing.T) { // PRD NFR-003 offline: Piper is the only adapter
	bin := filepath.Join(t.TempDir(), "piper")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	voicesDir := t.TempDir()

	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"piper", map[string]string{
			"SCREENCASTER_TTS": "piper", "SCREENCASTER_PIPER_BIN": bin, "SCREENCASTER_PIPER_VOICES": voicesDir,
		}, ""},
		{"unknown", map[string]string{"SCREENCASTER_TTS": "foo"}, `unknown TTS provider "foo" (available: piper)`},
		{"piper binary missing", map[string]string{
			"SCREENCASTER_TTS": "piper", "SCREENCASTER_PIPER_BIN": "/nope/piper", "SCREENCASTER_PIPER_VOICES": voicesDir,
		}, "piper binary not found: /nope/piper"},
		{"piper binary missing in the install dir", map[string]string{"SCREENCASTER_HOME": "/nope"},
			"piper binary not found: /nope/piper/piper (run: sudo screencaster setup)"},
		{"piper named, nothing set, nothing installed", map[string]string{"SCREENCASTER_TTS": "piper", "SCREENCASTER_HOME": "/nope"},
			"piper binary not found: /nope/piper/piper"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng, err := TTS(env(tt.env), t.TempDir())
			if tt.wantErr == "" {
				if err != nil || eng == nil {
					t.Fatalf("TTS() = %v, %v, want an engine", eng, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("TTS() error = %v, want %q", err, tt.wantErr)
			}
			if eng != nil {
				t.Errorf("TTS() engine = %v on error, want nil", eng)
			}
		})
	}
}

// installDir fakes what `screencaster setup` leaves: the Piper binary and a
// voice under <dir>/piper.
func installDir(t *testing.T, voice string) string {
	t.Helper()
	dir := t.TempDir()
	bin, voices := PiperPaths(dir)
	if err := os.MkdirAll(voices, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(voices, voice+".onnx"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func voiceNames(t *testing.T, e interface {
	Catalog() (voices.Catalog, error)
}) []string {
	t.Helper()
	c, err := e.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, v := range c.Voices {
		names = append(names, v.Name)
	}
	return names
}

// An unset SCREENCASTER_TTS is piper in the install dir: a native user sets no
// variable after `setup` (decision 77).
func TestTTS_unsetDefaultsToPiperAtTheInstallDir(t *testing.T) {
	dir := installDir(t, "en_US-ryan-high")
	eng, err := TTS(env(map[string]string{"SCREENCASTER_HOME": dir}), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := voiceNames(t, eng); len(got) != 1 || got[0] != "en_US-ryan-high" {
		t.Errorf("voices = %v, want the one under the install dir", got)
	}
}

func TestTTS_envOverrideBeatsTheInstallDir(t *testing.T) {
	home := installDir(t, "en_US-ryan-high")
	other := installDir(t, "pl_PL-darkman-medium")
	bin, voicesDir := PiperPaths(other)
	eng, err := TTS(env(map[string]string{
		"SCREENCASTER_HOME": home, "SCREENCASTER_PIPER_BIN": bin, "SCREENCASTER_PIPER_VOICES": voicesDir,
	}), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := voiceNames(t, eng); len(got) != 1 || got[0] != "pl_PL-darkman-medium" {
		t.Errorf("voices = %v, want the override's", got)
	}
}

func TestTTS_projectVoicesComeFromWorkDir(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "piper")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "voices"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "voices", "de_DE-thorsten-medium.onnx"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	eng, err := TTS(env(map[string]string{
		"SCREENCASTER_TTS": "piper", "SCREENCASTER_PIPER_BIN": bin, "SCREENCASTER_PIPER_VOICES": t.TempDir(),
	}), work)
	if err != nil {
		t.Fatal(err)
	}
	c, err := eng.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Voices) != 1 || c.Voices[0].Name != "de_DE-thorsten-medium" {
		t.Errorf("voices = %+v, want the one in <work>/voices (FR-016)", c.Voices)
	}
}

// Every language with a built-in voice has its own closing line on the end
// card; a missing one would fall back to English (BR-011, decision 63). The
// test lives here because only the composition root sees both packages.
func TestBuiltInVoiceLanguagesHaveAClosingLine(t *testing.T) {
	english := card.Outro("en")
	for lang := range piper.Defaults {
		if lang != "en" && card.Outro(lang) == english {
			t.Errorf("no closing line for built-in language %q", lang)
		}
	}
}
