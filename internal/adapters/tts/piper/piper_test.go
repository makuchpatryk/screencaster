package piper

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"screencaster/internal/domain/voices"
)

func touch(t *testing.T, dir string, names ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// pcmWAV is a mono s16le clip with n data bytes at rate.
func pcmWAV(rate uint32, n int) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+n))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), rate, rate * 2, uint16(2), uint16(16)} {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(n))
	b.Write(make([]byte, n))
	return b.Bytes()
}

// fakeBin writes a shell script standing in for Piper. Piper is called as:
// piper --model <path> --output_file <out>.
func fakeBin(t *testing.T, body string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "piper")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// withVoice returns a Piper on bin with one installed voice, en_US-ryan-high.
func withVoice(t *testing.T, bin string) *Piper {
	t.Helper()
	image := t.TempDir()
	touch(t, image, "en_US-ryan-high.onnx")
	p, err := New(bin, image, filepath.Join(t.TempDir(), "voices"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNew_failsAtStartup(t *testing.T) {
	voicesDir := t.TempDir()
	tests := []struct {
		name         string
		bin, imageVs string
		want         string
	}{
		{"binary not set", "", voicesDir, "SCREENCASTER_PIPER_BIN"},
		{"voice folder not set", fakeBin(t, "true"), "", "SCREENCASTER_PIPER_VOICES"},
		{"binary missing", "/nope/piper", voicesDir, "piper binary not found: /nope/piper"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.bin, tt.imageVs, "")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("New() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCatalog_listsModelsFromAllDirsAndSkipsMissingDir(t *testing.T) {
	image, work := t.TempDir(), filepath.Join(t.TempDir(), "voices")
	touch(t, image, "en_US-ryan-high.onnx", "en_US-ryan-high.onnx.json", "notes.txt", "README.onnx")
	touch(t, work, "pl_PL-gosia-medium.onnx", "en_US-ryan-high.onnx")
	p, err := New(fakeBin(t, "true"), image, work)
	if err != nil {
		t.Fatal(err)
	}

	got, err := p.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	want := voices.Catalog{
		Voices: []voices.Voice{
			{Name: "README", Lang: ""}, // no "_": installed, but in no language
			{Name: "en_US-ryan-high", Lang: "en"},
			{Name: "pl_PL-gosia-medium", Lang: "pl"},
		},
		Defaults: Defaults,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Catalog() = %+v, want %+v", got, want)
	}
}

func TestCatalog_laterDirWins(t *testing.T) {
	image, work := t.TempDir(), t.TempDir()
	touch(t, image, "en_US-ryan-high.onnx")
	touch(t, work, "en_US-ryan-high.onnx")
	p, err := New(fakeBin(t, "true"), image, work)
	if err != nil {
		t.Fatal(err)
	}
	models, err := p.scan()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(work, "en_US-ryan-high.onnx"); models["en_US-ryan-high"] != want {
		t.Errorf("model = %q, want project voice %q", models["en_US-ryan-high"], want)
	}
}

func TestCatalog_languageIsNameUpToFirstUnderscore(t *testing.T) {
	tests := []struct{ voice, want string }{
		{"pl_PL-darkman-medium", "pl"},
		{"en_US-ryan-high", "en"},
		{"de_DE-thorsten-medium", "de"},
		{"nounderscore", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.voice, func(t *testing.T) {
			if got := lang(tt.voice); got != tt.want {
				t.Errorf("lang(%q) = %q, want %q", tt.voice, got, tt.want)
			}
		})
	}
}

func TestCatalog_defaultsAreACopy(t *testing.T) {
	p := withVoice(t, fakeBin(t, "true"))
	c, err := p.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	c.Defaults["en"] = "changed"
	if Defaults["en"] != "en_US-ryan-high" {
		t.Errorf("Defaults[en] = %q, a caller changed the built-in", Defaults["en"])
	}
}

func TestSynthesize_returnsClipDuration(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.wav")
	if err := os.WriteFile(src, pcmWAV(22050, 88200), 0o644); err != nil {
		t.Fatal(err)
	}
	// Reads the text, echoes the path like Piper does, copies the clip.
	p := withVoice(t, fakeBin(t, `cat >/dev/null; echo "$4"; cp `+src+` "$4"`))

	got, err := p.Synthesize(context.Background(), "en_US-ryan-high", "Hello.", filepath.Join(dir, "out.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if got != 2*time.Second {
		t.Errorf("duration = %v, want 2s", got)
	}
}

func TestSynthesize_passesModelPathAndText(t *testing.T) {
	dir := t.TempDir()
	src, args, stdin := filepath.Join(dir, "src.wav"), filepath.Join(dir, "args"), filepath.Join(dir, "stdin")
	if err := os.WriteFile(src, pcmWAV(22050, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	p := withVoice(t, fakeBin(t, `echo "$@" >`+args+`; cat >`+stdin+`; cp `+src+` "$4"`))

	out := filepath.Join(dir, "out.wav")
	if _, err := p.Synthesize(context.Background(), "en_US-ryan-high", "Hello there.", out); err != nil {
		t.Fatal(err)
	}
	gotArgs, _ := os.ReadFile(args)
	if want := "--model " + p.dirs[0] + "/en_US-ryan-high.onnx --output_file " + out; strings.TrimSpace(string(gotArgs)) != want {
		t.Errorf("args = %q, want %q", gotArgs, want)
	}
	if gotText, _ := os.ReadFile(stdin); string(gotText) != "Hello there." {
		t.Errorf("stdin = %q, want the narration text", gotText)
	}
}

func TestSynthesize_unknownVoice(t *testing.T) {
	p := withVoice(t, fakeBin(t, `echo should not run >&2; exit 1`))

	_, err := p.Synthesize(context.Background(), "de_DE-nobody", "Hello.", filepath.Join(t.TempDir(), "out.wav"))
	if err == nil || err.Error() != "voice not installed: de_DE-nobody" {
		t.Errorf("err = %v, want voice not installed", err)
	}
}

func TestSynthesize_nonZeroExitCarriesStderr(t *testing.T) { // FR-003 edge case
	p := withVoice(t, fakeBin(t, `echo "  what():  Model file doesn't exist" >&2; exit 134`))

	_, err := p.Synthesize(context.Background(), "en_US-ryan-high", "Hello.", filepath.Join(t.TempDir(), "out.wav"))
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want *piper.Error", err)
	}
	if e.Error() != "what():  Model file doesn't exist" {
		t.Errorf("Error() = %q, want the trimmed stderr", e.Error())
	}
}

func TestSynthesize_cancelReturnsCtxErr(t *testing.T) {
	p := withVoice(t, fakeBin(t, `exec sleep 5`))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := p.Synthesize(ctx, "en_US-ryan-high", "Hello.", filepath.Join(t.TempDir(), "out.wav"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
}
