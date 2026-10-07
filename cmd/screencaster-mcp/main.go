// Command screencaster-mcp is the MCP server over stdio (FR-012…FR-019): it
// wires the job store, the single worker and the server around the shared
// composition root (app/wire). stdout carries protocol frames only;
// everything else goes to stderr (ARCHITECTURE §12).
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

	"screencaster/internal/adapters/lock"
	"screencaster/internal/adapters/mcpserver"
	"screencaster/internal/adapters/osfs"
	"screencaster/internal/adapters/sqlite"
	"screencaster/internal/adapters/tts"
	"screencaster/internal/app/jobs"
	"screencaster/internal/app/renderer"
	"screencaster/internal/app/wire"
)

// version is reported to MCP clients. A release build sets it with
// -ldflags "-X main.version=X.Y.Z" (ARCHITECTURE §12), which cannot set a
// const.
var version = "dev"

// Files under <work>/.screencaster (ARCHITECTURE §11). The TTS provider and
// its paths come from the image's environment (app.TTS).
const (
	stateDir = ".screencaster"
	dbFile   = "jobs.db"
	lockFile = "render.lock"
	tmpDir   = "tmp"
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
	// Fail before opening the store or accepting any call when the image names
	// no usable TTS provider.
	eng, err := wire.TTS(os.Getenv, wd)
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
	store, err := sqlite.Open(filepath.Join(state, dbFile), time.Now)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	// BR-009: before any call is accepted, interrupted jobs are reported.
	if err := store.Recover(ctx); err != nil {
		return err
	}
	if err := sqlite.RemoveStaleTemp(filepath.Join(state, lockFile), filepath.Join(state, tmpDir)); err != nil {
		return err
	}

	worker := jobs.NewWorker(store, renderJob(wd, eng), acquireLock(filepath.Join(state, lockFile)))
	workerCtx, cancelWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		worker.Run(workerCtx)
	}()

	srv := mcpserver.New(mcpserver.Deps{
		WorkDir:  wd,
		Store:    store,
		Worker:   worker,
		Explorer: wire.Explorer(),
		Files:    osfs.FS{},
		Voices:   eng.Catalog,
		NewID:    uuid.NewString,
	}, version)
	err = srv.Run(ctx, &mcp.StdioTransport{})

	cancelWorker()
	<-workerDone // let an aborted render clean up before the process exits
	return err
}

// renderJob is the worker's render: the shared pipeline with the real tools.
// The job's stored script and languages are rendered, so a render uses what
// was validated at submit time (BR-002), and the job ID names the temp dir
// (ARCHITECTURE §11).
func renderJob(wd string, eng tts.Engine) jobs.RenderFunc {
	return func(ctx context.Context, job jobs.Job) ([]renderer.Output, error) {
		deps, err := wire.NewDeps(eng, func() string { return job.ID })
		if err != nil {
			return nil, err
		}
		return renderer.Render(ctx, deps, renderer.Request{
			WorkDir: wd, ScriptPath: job.ScriptPath, Script: job.Script, LangOverride: job.Languages,
			// slog goes to stderr; stdout stays protocol-only (ARCHITECTURE §12).
			Log: func(msg string) { slog.Info(msg, "job", job.ID) },
		})
	}
}

// acquireLock waits for the render lock the CLI also uses, so a job stays
// queued while a CLI render runs (FR-014, ARCHITECTURE §6.3).
func acquireLock(path string) jobs.AcquireFunc {
	return func(ctx context.Context) (func(), error) {
		l, err := lock.Acquire(ctx, path)
		if err != nil {
			return nil, err
		}
		return func() { _ = l.Release() }, nil
	}
}
