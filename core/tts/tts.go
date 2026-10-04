// Package tts wraps the Piper binary: text in, WAV out, plus the clip's
// duration (FR-003). It knows nothing about steps, languages or jobs.
package tts

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Piper runs the Piper C++ binary at Bin. The binary finds its libraries and
// espeak-ng data next to itself (spike S3, ARCHITECTURE §17).
type Piper struct{ Bin string }

// Error is a failed Piper run. Error() is Piper's stderr, so the renderer can
// put it into failure.TTS unchanged.
type Error struct {
	Stderr string
	err    error
}

func (e *Error) Error() string {
	if e.Stderr == "" {
		return e.err.Error()
	}
	return e.Stderr
}

func (e *Error) Unwrap() error { return e.err }

// Synthesize speaks text with the voice model at voicePath into outPath and
// returns the clip's duration. Piper prints the output path on stdout, which
// goes to the null device (cmd.Stdout nil), and stderr is captured, so nothing
// reaches the MCP stdout (ADR-48).
func (p Piper) Synthesize(ctx context.Context, voicePath, text, outPath string) (time.Duration, error) {
	cmd := exec.CommandContext(ctx, p.Bin, "--model", voicePath, "--output_file", outPath)
	cmd.Stdin = strings.NewReader(text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, &Error{Stderr: strings.TrimSpace(stderr.String()), err: err}
	}
	return wavDuration(outPath)
}

// wavDuration reads the duration from the RIFF header instead of spawning
// ffprobe (ADR-49): data bytes / byte rate. Piper writes PCM s16le, 22050 Hz,
// mono with a plain fmt + data layout (spike S3); other chunks are skipped.
func wavDuration(path string) (time.Duration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read wav: %w", err)
	}
	d, err := parseWAV(data)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}

const wavPCM = 1

func parseWAV(data []byte) (time.Duration, error) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return 0, errors.New("not a RIFF/WAVE file")
	}
	var byteRate uint32
	for rest := data[12:]; len(rest) >= 8; {
		id := string(rest[0:4])
		size := binary.LittleEndian.Uint32(rest[4:8])
		body := rest[8:]
		switch id {
		case "fmt ":
			if size < 16 || len(body) < 16 {
				return 0, errors.New("short fmt chunk")
			}
			if format := binary.LittleEndian.Uint16(body[0:2]); format != wavPCM {
				return 0, fmt.Errorf("unsupported wav format %d, want PCM", format)
			}
			byteRate = binary.LittleEndian.Uint32(body[8:12])
		case "data":
			if byteRate == 0 {
				return 0, errors.New("data chunk before fmt chunk")
			}
			// A streamed WAV may leave the size unset; trust the file instead.
			n := min(uint64(size), uint64(len(body)))
			return time.Duration(n * uint64(time.Second) / uint64(byteRate)), nil
		}
		// Chunks are word-aligned.
		skip := uint64(size) + uint64(size&1)
		if skip > uint64(len(body)) {
			break
		}
		rest = body[skip:]
	}
	return 0, errors.New("no data chunk")
}
