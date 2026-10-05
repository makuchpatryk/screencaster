// Command screencaster renders a demo script into narrated MP4s (FR-011). It
// is a thin shell over core/renderer: it parses arguments, wires the real
// tools, prints progress and maps the result to an exit code.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"screencaster/core/assembler"
	"screencaster/core/browser"
	"screencaster/core/lock"
	"screencaster/core/recorder"
	"screencaster/core/renderer"
	"screencaster/core/tts"
	"screencaster/core/voices"
)

// Tool locations in the image layout (Dockerfile). ffmpeg and ffprobe come
// from PATH.
const (
	piperBin       = "/opt/piper/piper"
	builtinVoices  = "/opt/piper/voices"
	projectVoices  = "voices" // relative to the work dir: /work/voices in the image
	ffmpegBin      = "ffmpeg"
	ffprobeBin     = "ffprobe"
	renderLockFile = ".screencaster/render.lock"
)

func main() {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// SIGINT/SIGTERM cancel the render, which closes the browser and removes
	// the temp dir (ARCHITECTURE §6.4).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], wd, render, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// render is the real renderFunc: it takes the render lock without waiting
// (ARCHITECTURE §6.3), then runs the pipeline with the real tools.
func render(ctx context.Context, req renderer.Request) ([]renderer.Output, error) {
	l, err := lockRender(req.WorkDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = l.Release() }()

	installed, err := voices.Discover(builtinVoices, filepath.Join(req.WorkDir, projectVoices))
	if err != nil {
		return nil, err
	}
	launch := func(ctx context.Context, o recorder.LaunchOptions) (recorder.Session, error) {
		return browser.Launcher{}.Launch(ctx, browser.Options{
			BaseURL:      o.BaseURL,
			StorageState: o.StorageState,
			VideoDir:     o.VideoDir,
			StartImage:   o.StartImage,
			Visuals:      true,
		})
	}
	return renderer.Render(ctx, renderer.Deps{
		TTS:    tts.Piper{Bin: piperBin},
		Rec:    recorder.New(launch),
		Asm:    assembler.FFmpeg{Bin: ffmpegBin, Probe: ffprobeBin},
		Cards:  cards{},
		Voices: installed,
		Now:    time.Now,
		RunID:  runID,
	}, req)
}

// cards takes the start and end card screenshots with the real browser.
type cards struct{}

func (cards) Screenshot(ctx context.Context, shots []renderer.Shot) error {
	bs := make([]browser.Shot, len(shots))
	for i, s := range shots {
		bs[i] = browser.Shot(s)
	}
	return browser.Launcher{}.Screenshot(ctx, bs)
}

// errRenderRunning is the PRD wording for a held render lock (ARCHITECTURE
// §6.3).
var errRenderRunning = errors.New("another render is running")

// lockRender takes the render lock without waiting; the CLI fails fast
// instead of queueing (ADR-45).
func lockRender(workDir string) (*lock.Lock, error) {
	l, err := lock.TryAcquire(filepath.Join(workDir, renderLockFile))
	if errors.Is(err, lock.ErrHeld) {
		return nil, errRenderRunning
	}
	return l, err
}

// runID names the CLI's temp dir. It only has to be unique; it never reaches
// an output (ARCHITECTURE §11).
func runID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // never fails on Linux (crypto/rand docs)
	return hex.EncodeToString(b)
}
