package wav

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// build makes a PCM WAV: mono s16le at rate, n data bytes, with extra chunks
// placed between fmt and data.
func build(rate uint32, n int, extra ...[]byte) []byte {
	var body bytes.Buffer
	body.WriteString("WAVE")
	body.WriteString("fmt ")
	for _, v := range []any{uint32(16), uint16(pcm), uint16(1), rate, rate * 2, uint16(2), uint16(16)} {
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

func TestParse_durationFromHeader(t *testing.T) { // ADR-49
	tests := []struct {
		name string
		data []byte
		want time.Duration
	}{
		// The spike S3 clip: 80104 bytes at 44100 B/s, ffprobe says 1.816417 s.
		{"spike S3 clip", build(22050, 80104), 1816417233 * time.Nanosecond},
		{"one second", build(22050, 44100), time.Second},
		{"empty", build(22050, 0), 0},
		{"odd-sized chunk before data is skipped", build(22050, 44100, chunk("LIST", 3)), time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parse(tt.data)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("duration = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParse_rejectsBadInput(t *testing.T) {
	nonPCM := build(22050, 10)
	binary.LittleEndian.PutUint16(nonPCM[20:22], 3) // IEEE float

	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"not riff", []byte("hello world, not a wav"), "not a RIFF/WAVE file"},
		{"non-PCM", nonPCM, "unsupported wav format 3"},
		{"no data chunk", build(22050, 0)[:36], "no data chunk"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse(tt.data)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestDuration_readsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.wav")
	if err := os.WriteFile(path, build(22050, 88200), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Duration(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2*time.Second {
		t.Errorf("Duration() = %v, want 2s", got)
	}
	if _, err := Duration(filepath.Join(t.TempDir(), "absent.wav")); err == nil {
		t.Error("Duration() of a missing file: error = nil")
	}
}
