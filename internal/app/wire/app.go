// Package wire is the composition root shared by both entry points: it picks
// the TTS provider and wires the real tools behind the renderer's and the
// explorer's ports. It is the only app package that imports adapters
// (ARCHITECTURE §3); both mains call it instead of wiring the pipeline by
// hand.
package wire

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"screencaster/internal/adapters/assembler"
	"screencaster/internal/adapters/browser"
	"screencaster/internal/adapters/osfs"
	"screencaster/internal/adapters/tts"
	"screencaster/internal/adapters/tts/piper"
	"screencaster/internal/app/explorer"
	"screencaster/internal/app/renderer"
	"screencaster/internal/domain/recorder"
	"screencaster/internal/domain/shooter"
)

// Tool names; ffmpeg and ffprobe come from PATH.
const (
	ffmpegBin  = "ffmpeg"
	ffprobeBin = "ffprobe"
)

// TTS builds the provider named by SCREENCASTER_TTS; unset means piper, whose
// binary and voices default to the install dir `screencaster setup` fills
// (decision 77). An unknown name is an error, so the entry points fail before
// any work. SCREENCASTER_PIPER_BIN and SCREENCASTER_PIPER_VOICES override the
// defaults (the Docker images set them). workDir is the project folder whose
// voices/ extends the catalog.
func TTS(getenv func(string) string, workDir string) (tts.Engine, error) {
	switch name := getenv("SCREENCASTER_TTS"); name {
	case "", "piper":
		bin, voices := PiperPaths(InstallDir(getenv))
		hint := " (run: sudo screencaster setup)"
		if v := getenv("SCREENCASTER_PIPER_BIN"); v != "" {
			bin, hint = v, ""
		}
		if v := getenv("SCREENCASTER_PIPER_VOICES"); v != "" {
			voices = v
		}
		p, err := piper.New(bin, voices, filepath.Join(workDir, "voices"))
		if err != nil {
			return nil, fmt.Errorf("%w%s", err, hint)
		}
		return p, nil // not `return piper.New(...)`: a nil *Piper would be a non-nil Engine
	default:
		return nil, fmt.Errorf("unknown TTS provider %q (available: piper)", name)
	}
}

// NewDeps wires the real browser, ffmpeg and card tools around eng. The
// catalog is read here, per call, so a voice added to /work/voices is picked
// up by the next render or job. runID names the render's temp dir.
func NewDeps(eng tts.Engine, runID func() string) (renderer.Deps, error) {
	catalog, err := eng.Catalog()
	if err != nil {
		return renderer.Deps{}, err
	}
	return renderer.Deps{
		TTS:    eng,
		Rec:    recorderPort{rec: recorder.New(launchRecording)},
		Asm:    assemblerPort{ffmpeg: assembler.FFmpeg{Bin: ffmpegBin, Probe: ffprobeBin}},
		Cards:  cardsPort{},
		Shots:  shooterPort{sh: shooter.Shooter{Launch: launchShots}},
		Files:  osfs.FS{},
		Voices: catalog,
		Now:    time.Now,
		RunID:  runID,
	}, nil
}

// Explorer returns the explore_page logic over a plain, unrecorded browser
// (FR-017).
func Explorer() explorer.Explorer {
	return explorer.Explorer{Launch: launchExploration}
}

// launchRecording opens a recorded session with the cursor overlay and the
// sync marker (FR-004, FR-006, decision 69).
func launchRecording(ctx context.Context, o recorder.LaunchOptions) (recorder.Session, error) {
	s, err := launcher().Launch(ctx, browser.Options{
		BaseURL:      o.BaseURL,
		StorageState: o.StorageState,
		VideoDir:     o.VideoDir,
		StartImage:   o.StartImage,
		Marker:       true, // the recorder aligns on it (decision 69)
		Visuals:      true,
	})
	if err != nil {
		return nil, err // not `return s, err`: a nil *Session would be a non-nil Session
	}
	return s, nil
}

// launchShots opens an unrecorded session with no cursor overlay: a screenshots
// run has no video to show one in (decision 75).
func launchShots(ctx context.Context, o shooter.LaunchOptions) (shooter.Session, error) {
	s, err := launcher().Launch(ctx, browser.Options{BaseURL: o.BaseURL, StorageState: o.StorageState})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// launchExploration opens a plain, unrecorded session (FR-017).
func launchExploration(ctx context.Context, o explorer.LaunchOptions) (explorer.Session, error) {
	s, err := launcher().Launch(ctx, browser.Options{BaseURL: o.BaseURL, StorageState: o.StorageState})
	if err != nil {
		return nil, err
	}
	return s, nil
}
