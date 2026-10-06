// Package wav reads the duration of a PCM WAV clip. Every TTS provider must
// deliver WAV, so the reader is shared (ADR-49).
package wav

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"time"
)

// Duration reads the duration from the RIFF header instead of spawning
// ffprobe (ADR-49): data bytes / byte rate. Adapters deliver PCM s16le mono
// with a plain fmt + data layout (spike S3); other chunks are skipped.
func Duration(path string) (time.Duration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read wav: %w", err)
	}
	d, err := parse(data)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}

const pcm = 1

func parse(data []byte) (time.Duration, error) {
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
			if format := binary.LittleEndian.Uint16(body[0:2]); format != pcm {
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
