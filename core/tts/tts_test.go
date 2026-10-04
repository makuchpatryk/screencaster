package tts

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wav builds a PCM WAV: mono s16le at rate, n data bytes, with extra chunks
// placed between fmt and data.
func wav(rate uint32, n int, extra ...[]byte) []byte {
	var body bytes.Buffer
	body.WriteString("WAVE")
	body.WriteString("fmt ")
	for _, v := range []any{uint32(16), uint16(wavPCM), uint16(1), rate, rate * 2, uint16(2), uint16(16)} {
		_ = binary.Write(&body, binary.LittleEndian, v)
	}
	for _, c := range extra {
		body.Write(c)
	}
	body.WriteString("data")
	_ = binary.Write(&body, binary.LittleEndian, uint32(n))
	body.Write(make([]byte, n))

	var out bytes.Buffer
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

func chunk(id string, size int) []byte {
	var b bytes.Buffer
	b.WriteString(id)
	_ = binary.Write(&b, binary.LittleEndian, uint32(size))
	b.Write(make([]byte, size+size%2))
	return b.Bytes()
}

func TestParseWAV_durationFromHeader(t *testing.T) { // ADR-49
	tests := []struct {
		name string
		data []byte
		want time.Duration
	}{
		// The spike S3 clip: 80104 bytes at 44100 B/s, ffprobe says 1.816417 s.
		{"piper clip", wav(22050, 80104), 1816417233 * time.Nanosecond},
		{"one second", wav(22050, 44100), time.Second},
		{"empty", wav(22050, 0), 0},
		{"odd-sized chunk before data is skipped", wav(22050, 44100, chunk("LIST", 3)), time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseWAV(tt.data)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("duration = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseWAV_rejectsBadInput(t *testing.T) {
	nonPCM := wav(22050, 10)
	binary.LittleEndian.PutUint16(nonPCM[20:22], 3) // IEEE float

	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"not riff", []byte("hello world, not a wav"), "not a RIFF/WAVE file"},
		{"non-PCM", nonPCM, "unsupported wav format 3"},
		{"no data chunk", wav(22050, 0)[:36], "no data chunk"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseWAV(tt.data)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

// fakePiper writes a shell script standing in for the binary. Piper is called
// as: piper --model <voice> --output_file <out>.
func fakePiper(t *testing.T, body string) Piper {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "piper")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return Piper{Bin: bin}
}

func TestSynthesize_returnsClipDuration(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.wav")
	if err := os.WriteFile(src, wav(22050, 88200), 0o644); err != nil {
		t.Fatal(err)
	}
	// Reads the text, echoes the path like Piper does, copies the clip.
	p := fakePiper(t, `cat >/dev/null; echo "$4"; cp `+src+` "$4"`)

	got, err := p.Synthesize(context.Background(), "/v/en.onnx", "Hello.", filepath.Join(dir, "out.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if got != 2*time.Second {
		t.Errorf("duration = %v, want 2s", got)
	}
}

func TestSynthesize_nonZeroExitCarriesStderr(t *testing.T) { // FR-003 edge case
	p := fakePiper(t, `echo "  what():  Model file doesn't exist" >&2; exit 134`)

	_, err := p.Synthesize(context.Background(), "/nope.onnx", "Hello.", filepath.Join(t.TempDir(), "out.wav"))
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want *tts.Error", err)
	}
	if e.Error() != "what():  Model file doesn't exist" {
		t.Errorf("Error() = %q, want the trimmed stderr", e.Error())
	}
}

func TestSynthesize_cancelReturnsCtxErr(t *testing.T) {
	p := fakePiper(t, `exec sleep 5`)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := p.Synthesize(ctx, "/v/en.onnx", "Hello.", filepath.Join(t.TempDir(), "out.wav"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
}
