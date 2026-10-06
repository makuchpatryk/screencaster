// Package piper is the Piper TTS adapter: the only code that knows Piper's
// binary, its .onnx voice files and its built-in voices. It satisfies
// provider.Engine structurally (BR-011, FR-003, FR-018).
package piper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"screencaster/internal/adapters/tts/wav"
	"screencaster/internal/domain/voices"
)

const modelExt = ".onnx"

// Defaults holds the only languages with a built-in voice (BR-011). Names must
// match the files baked into the image; `make image-check` reads this var.
var Defaults = map[string]string{
	"en": "en_US-ryan-high",
	"pl": "pl_PL-darkman-medium",
}

// Piper runs the Piper C++ binary. The binary finds its libraries and
// espeak-ng data next to itself (spike S3, ARCHITECTURE §17).
type Piper struct {
	bin  string
	dirs []string // voice folders; a later one wins on a name clash
}

// New checks that the binary exists, so a missing install fails at startup and
// not in the middle of a render. imageVoices holds the built-in voices;
// projectVoices (/work/voices) is optional and overrides it.
func New(bin, imageVoices, projectVoices string) (*Piper, error) {
	if bin == "" {
		return nil, errors.New("piper binary not set: set SCREENCASTER_PIPER_BIN")
	}
	if imageVoices == "" {
		return nil, errors.New("piper voice folder not set: set SCREENCASTER_PIPER_VOICES")
	}
	if _, err := os.Stat(bin); err != nil {
		return nil, fmt.Errorf("piper binary not found: %s", bin)
	}
	return &Piper{bin: bin, dirs: []string{imageVoices, projectVoices}}, nil
}

// Catalog lists the voices on disk. It scans on every call, so a voice added
// to /work/voices shows up without a restart. The language is the voice name
// up to the first "_" (pl_PL-darkman-medium -> pl, FR-018).
func (p *Piper) Catalog() (voices.Catalog, error) {
	models, err := p.scan()
	if err != nil {
		return voices.Catalog{}, err
	}
	c := voices.Catalog{Defaults: maps.Clone(Defaults)}
	for name := range models {
		c.Voices = append(c.Voices, voices.Voice{Name: name, Lang: lang(name)})
	}
	slices.SortFunc(c.Voices, func(a, b voices.Voice) int { return strings.Compare(a.Name, b.Name) })
	return c, nil
}

// lang is empty when the name has no "_".
func lang(voice string) string {
	code, _, found := strings.Cut(voice, "_")
	if !found {
		return ""
	}
	return code
}

// scan maps each voice name to the path of its .onnx model. A folder that does
// not exist is skipped (/work/voices is optional).
func (p *Piper) scan() (map[string]string, error) {
	models := map[string]string{}
	for _, dir := range p.dirs {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("list voices in %s: %w", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != modelExt {
				continue
			}
			models[strings.TrimSuffix(e.Name(), modelExt)] = filepath.Join(dir, e.Name())
		}
	}
	return models, nil
}

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

// Synthesize speaks text with the named voice into outPath and returns the
// clip's duration. The model path is looked up per call, so the answer cannot
// go stale between Catalog and here. Piper prints the output path on stdout,
// which goes to the null device (cmd.Stdout nil), and stderr is captured, so
// nothing reaches the MCP stdout (ADR-48).
func (p *Piper) Synthesize(ctx context.Context, voice, text, outPath string) (time.Duration, error) {
	models, err := p.scan()
	if err != nil {
		return 0, err
	}
	model, ok := models[voice]
	if !ok {
		return 0, fmt.Errorf("voice not installed: %s", voice)
	}
	cmd := exec.CommandContext(ctx, p.bin, "--model", model, "--output_file", outPath)
	cmd.Stdin = strings.NewReader(text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, &Error{Stderr: strings.TrimSpace(stderr.String()), err: err}
	}
	return wav.Duration(outPath)
}
