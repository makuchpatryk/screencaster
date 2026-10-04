// Package renderer orchestrates a render (ARCHITECTURE §4): validate, then per
// language synthesize, record and assemble, then publish. Render is the one
// code path for the CLI and the MCP worker; Prepare is also the MCP server's
// synchronous check in render_video.
package renderer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"screencaster/core/assembler"
	"screencaster/core/config"
	"screencaster/core/executor"
	"screencaster/core/failure"
	"screencaster/core/recorder"
	"screencaster/core/script"
	"screencaster/core/voices"
)

// timestampLayout is the FR-010 job-start format, UTC.
const timestampLayout = "20060102T150405Z"

// Request says what to render. ScriptPath is relative to WorkDir unless
// absolute. A non-empty LangOverride wins over the script's languages (BR-002).
// Progress, when set, is told about each step right before it runs.
type Request struct {
	WorkDir      string
	ScriptPath   string
	LangOverride []string
	Progress     Progress
}

// Progress reports step i (1-based) of n for lang; target is the selector or
// URL (empty when the step has none).
type Progress func(lang string, i, n int, action, target string)

// Synthesizer turns text into a WAV at outPath and returns its duration
// (core/tts).
type Synthesizer interface {
	Synthesize(ctx context.Context, voicePath, text, outPath string) (time.Duration, error)
}

// Recorder records one language (core/recorder).
type Recorder interface {
	Record(ctx context.Context, in recorder.Input) (recorder.Output, error)
}

// Assembler mixes the clips into the recording and writes the MP4, returning
// its duration in ms (core/assembler).
type Assembler interface {
	Assemble(ctx context.Context, in assembler.Input) (int64, error)
}

// Deps are the collaborators and the environment of a render, wired in main.
// Now and RunID are injected so tests get fixed names (CODE_QUALITY, SOLID).
type Deps struct {
	TTS    Synthesizer
	Rec    Recorder
	Asm    Assembler
	Voices voices.Installed
	Now    func() time.Time
	RunID  func() string
}

// Output is one published video.
type Output struct {
	Lang       string
	Path       string // absolute
	DurationMs int64
}

// Plan is everything the pipeline needs after validation, so later stages
// never go back to the config, the script file or the voice directories.
type Plan struct {
	Cfg       config.Config
	Script    script.Script
	Languages []string
	Voices    map[string]string // language -> .onnx path
}

// Prepare validates config, script, narration and voices without starting any
// browser, TTS or ffmpeg work (FR-001, FR-002, BR-011). Problems with the
// script and voices come back together as failure.ValidationErrors.
func Prepare(req Request, installed voices.Installed) (Plan, error) {
	cfg, err := config.Load(req.WorkDir)
	if err != nil {
		return Plan{}, err
	}

	path := req.ScriptPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(req.WorkDir, path)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Plan{}, fmt.Errorf("script not found: %s", path)
	}
	if err != nil {
		return Plan{}, fmt.Errorf("read script: %w", err)
	}

	s, err := script.Parse(data)
	if err != nil {
		return Plan{}, err
	}

	langs := script.Languages(req.LangOverride, s.Languages)
	errs := script.Validate(s, langs)
	paths, voiceErrs := voices.Resolve(langs, s.Voices, cfg.Voices, installed)
	errs = append(errs, voiceErrs...)
	if len(errs) > 0 {
		return Plan{}, errs
	}
	return Plan{Cfg: cfg, Script: s, Languages: langs, Voices: paths}, nil
}

// OutputName is the one place that builds <name>.<lang>.<timestamp>.mp4
// (FR-010). ts is the job start time.
func OutputName(name, lang string, ts time.Time) string {
	return fmt.Sprintf("%s.%s.%s.mp4", name, lang, ts.UTC().Format(timestampLayout))
}

// Render validates the request, renders every selected language in order and
// publishes the MP4s only when all of them succeeded (BR-004, FR-010). Work
// happens under <work>/.screencaster/tmp/<runId>, which is removed on every
// path. A step, TTS or assembly problem comes back as *failure.Failure; a
// cancelled ctx as ctx.Err().
func Render(ctx context.Context, d Deps, req Request) ([]Output, error) {
	start := d.Now() // FR-010: the job start time names every output
	plan, err := Prepare(req, d.Voices)
	if err != nil {
		return nil, err
	}

	runDir := filepath.Join(plan.Cfg.WorkDir, ".screencaster", "tmp", d.RunID())
	defer func() { _ = os.RemoveAll(runDir) }()

	outs := make([]Output, 0, len(plan.Languages))
	for _, lang := range plan.Languages {
		out, err := renderLanguage(ctx, d, plan, lang, filepath.Join(runDir, lang), req.Progress)
		if err != nil {
			return nil, err
		}
		outs = append(outs, out)
	}
	return publish(plan.Cfg.OutputDir, plan.Script.Name, start, outs)
}

// renderLanguage runs TTS, then the recording, then assembly for one language
// (ARCHITECTURE §4, rule 3: clip durations decide step timing). The returned
// Path is the temp MP4.
func renderLanguage(ctx context.Context, d Deps, plan Plan, lang, dir string, progress Progress) (Output, error) {
	clipDir, videoDir := filepath.Join(dir, "clips"), filepath.Join(dir, "video")
	for _, p := range []string{clipDir, videoDir} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			return Output{}, fmt.Errorf("create temp dir: %w", err)
		}
	}

	steps := plan.Script.Steps
	durations := map[int]time.Duration{}
	clipPaths := map[int]string{}
	for i, step := range steps {
		text := step.Narration[lang]
		if text == "" {
			continue
		}
		path := filepath.Join(clipDir, strconv.Itoa(i+1)+".wav")
		dur, err := d.TTS.Synthesize(ctx, plan.Voices[lang], text, path)
		if err != nil {
			if ctx.Err() != nil {
				return Output{}, ctx.Err()
			}
			return Output{}, failure.TTS(i+1, lang, err.Error())
		}
		durations[i], clipPaths[i] = dur, path
	}

	in := recorder.Input{
		Steps:        steps,
		Clips:        durations,
		Lang:         lang,
		Dir:          videoDir,
		BaseURL:      plan.Cfg.BaseURL,
		StorageState: plan.Cfg.StorageState,
	}
	if progress != nil {
		in.OnStep = func(i int) { progress(lang, i+1, len(steps), steps[i].Action, executor.Target(steps[i])) }
	}
	rec, err := d.Rec.Record(ctx, in)
	if err != nil {
		return Output{}, err
	}

	asm := assembler.Input{Webm: rec.WebmPath, Out: filepath.Join(dir, "out.mp4")}
	for i := range steps { // step order keeps the ffmpeg inputs stable
		if p, ok := clipPaths[i]; ok {
			asm.Clips = append(asm.Clips, assembler.Clip{Path: p, Offset: rec.Offsets[i]})
		}
	}
	if m := plan.Script.Meta; m != nil {
		asm.Title, asm.Comment = m.Title, m.Description
	}
	ms, err := d.Asm.Assemble(ctx, asm)
	if err != nil {
		if ctx.Err() != nil {
			return Output{}, ctx.Err()
		}
		return Output{}, failure.Assembly(lang, err.Error())
	}
	return Output{Lang: lang, Path: asm.Out, DurationMs: ms}, nil
}

// publish moves the temp MP4s into outputDir under their final names, all
// with the same timestamp (FR-010). No existing file is ever opened for
// writing (BR-006): every target is checked first. If a move fails, the files
// this call already published are removed again, so a failed render leaves
// outputDir as it was (BR-004).
func publish(outputDir, name string, ts time.Time, outs []Output) ([]Output, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}
	dsts := make([]string, len(outs))
	for i, o := range outs {
		dsts[i] = filepath.Join(outputDir, OutputName(name, o.Lang, ts))
		if _, err := os.Lstat(dsts[i]); err == nil {
			return nil, fmt.Errorf("output already exists: %s", dsts[i])
		}
	}

	published := make([]Output, 0, len(outs))
	for i, o := range outs {
		if err := move(o.Path, dsts[i]); err != nil {
			for _, p := range published {
				_ = os.Remove(p.Path)
			}
			return nil, err
		}
		published = append(published, Output{Lang: o.Lang, Path: dsts[i], DurationMs: o.DurationMs})
	}
	return published, nil
}

// move renames src to dst. When they are on different filesystems it copies
// to <dst>.part in the target directory and renames that, so dst never exists
// half-written (ARCHITECTURE §4, rule 5).
func move(src, dst string) error {
	err := os.Rename(src, dst)
	if errors.Is(err, syscall.EXDEV) {
		return copyThenRename(src, dst)
	}
	if err != nil {
		return fmt.Errorf("publish %s: %w", dst, err)
	}
	return nil
}

func copyThenRename(src, dst string) (err error) {
	part := dst + ".part"
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("publish %s: %w", dst, err)
	}
	defer func() { _ = in.Close() }()

	// O_EXCL: never open an existing file for writing (BR-006).
	out, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("publish %s: %w", dst, err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(part)
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("publish %s: %w", dst, err)
	}
	if err = out.Close(); err != nil {
		return fmt.Errorf("publish %s: %w", dst, err)
	}
	if err = os.Rename(part, dst); err != nil {
		return fmt.Errorf("publish %s: %w", dst, err)
	}
	return nil
}
