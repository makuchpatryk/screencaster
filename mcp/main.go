// Command screencaster-mcp is the MCP server over stdio (FR-012…FR-019): it
// wires the real tools, the job queue and the single worker around the core
// library. stdout carries protocol frames only; everything else goes to stderr
// (ARCHITECTURE §12).
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"screencaster/core/assembler"
	"screencaster/core/browser"
	"screencaster/core/explorer"
	"screencaster/core/lock"
	"screencaster/core/recorder"
	"screencaster/core/renderer"
	"screencaster/core/tts"
	"screencaster/core/voices"
	"screencaster/mcp/queue"
	"screencaster/mcp/server"
)

// version is reported to MCP clients.
const version = "0.1.0"

// Tool locations in the image layout (Dockerfile). ffmpeg and ffprobe come
// from PATH.
const (
	piperBin      = "/opt/piper/piper"
	builtinVoices = "/opt/piper/voices"
	projectVoices = "voices" // relative to the work dir: /work/voices in the image
	ffmpegBin     = "ffmpeg"
	ffprobeBin    = "ffprobe"
	stateDir      = ".screencaster"
	dbFile        = "jobs.db"
	lockFile      = "render.lock"
	tmpDir        = "tmp"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := run(); err != nil {
		slog.Error("screencaster-mcp", "err", err)
		os.Exit(1)
	}
}

func run() error {
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	// EOF on stdin ends Server.Run; a signal (docker stop, with --init) cancels
	// the context. Both end the worker, which closes the browser and removes the
	// job's temp dir (ARCHITECTURE §6.4).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Decision 58: the retired config is never read. slog goes to stderr.
	if _, err := os.Stat(filepath.Join(wd, "screencaster.yaml")); err == nil {
		slog.Warn("screencaster.yaml is ignored; move its fields into the demo script")
	}

	state := filepath.Join(wd, stateDir)
	store, err := queue.Open(filepath.Join(state, dbFile), time.Now)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	// BR-009: before any call is accepted, interrupted jobs are reported.
	if err := store.Recover(ctx); err != nil {
		return err
	}
	if err := queue.RemoveStaleTemp(filepath.Join(state, lockFile), filepath.Join(state, tmpDir)); err != nil {
		return err
	}

	discover := func() (voices.Installed, error) {
		return voices.Discover(builtinVoices, filepath.Join(wd, projectVoices))
	}
	worker := queue.NewWorker(store)
	workerCtx, cancelWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		worker.Run(workerCtx, renderJob(wd, discover), acquireLock(filepath.Join(state, lockFile)))
	}()

	srv := server.New(server.Deps{
		WorkDir:  wd,
		Store:    store,
		Worker:   worker,
		Explorer: explorer.Explorer{Launch: launchExplorer},
		Voices:   discover,
		NewID:    uuid.NewString,
	}, version)
	err = srv.Run(ctx, &mcp.StdioTransport{})

	cancelWorker()
	<-workerDone // let an aborted render clean up before the process exits
	return err
}

// renderJob is the worker's render: the shared pipeline with the real tools.
// The job's stored languages are the override, so a render uses the selection
// made at submit time (BR-002), and the job ID names the temp dir
// (ARCHITECTURE §11).
func renderJob(wd string, discover func() (voices.Installed, error)) func(context.Context, queue.Job) ([]queue.Output, error) {
	launch := func(ctx context.Context, o recorder.LaunchOptions) (recorder.Session, error) {
		s, err := browser.Launcher{}.Launch(ctx, browser.Options{
			BaseURL:      o.BaseURL,
			StorageState: o.StorageState,
			VideoDir:     o.VideoDir,
			StartImage:   o.StartImage,
			Visuals:      true,
		})
		if err != nil {
			return nil, err
		}
		return s, nil
	}
	return func(ctx context.Context, job queue.Job) ([]queue.Output, error) {
		installed, err := discover()
		if err != nil {
			return nil, err
		}
		outs, err := renderer.Render(ctx, renderer.Deps{
			TTS:    tts.Piper{Bin: piperBin},
			Rec:    recorder.New(launch),
			Asm:    assembler.FFmpeg{Bin: ffmpegBin, Probe: ffprobeBin},
			Cards:  cards{},
			Voices: installed,
			Now:    time.Now,
			RunID:  func() string { return job.ID },
		}, renderer.Request{
			WorkDir: wd, ScriptPath: job.ScriptPath, LangOverride: job.Languages,
			// slog goes to stderr; stdout stays protocol-only (ARCHITECTURE §12).
			Log: func(msg string) { slog.Info(msg, "job", job.ID) },
		})
		if err != nil {
			return nil, err
		}
		res := make([]queue.Output, len(outs))
		for i, o := range outs {
			res[i] = queue.Output{Lang: o.Lang, Path: o.Path, DurationMs: o.DurationMs}
		}
		return res, nil
	}
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

// acquireLock waits for the render lock the CLI also uses, so a job stays
// queued while a CLI render runs (FR-014, ARCHITECTURE §6.3).
func acquireLock(path string) func(context.Context) (func(), error) {
	return func(ctx context.Context) (func(), error) {
		l, err := lock.Acquire(ctx, path)
		if err != nil {
			return nil, err
		}
		return func() { _ = l.Release() }, nil
	}
}

// launchExplorer opens a plain, unrecorded session (FR-017).
func launchExplorer(ctx context.Context, o explorer.LaunchOptions) (explorer.Session, error) {
	s, err := browser.Launcher{}.Launch(ctx, browser.Options{BaseURL: o.BaseURL, StorageState: o.StorageState})
	if err != nil {
		return nil, err
	}
	return s, nil
}
