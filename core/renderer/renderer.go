// Package renderer orchestrates a render (ARCHITECTURE §4): validate, then per
// language synthesize, record and assemble, then publish. Render is the one
// code path for the CLI and the MCP worker; Prepare is also the MCP server's
// synchronous check in render_video.
package renderer

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"screencaster/core/assembler"
	"screencaster/core/card"
	"screencaster/core/executor"
	"screencaster/core/failure"
	"screencaster/core/recorder"
	"screencaster/core/script"
	"screencaster/core/voices"
)

const (
	// timestampLayout is the FR-010 job-start format, UTC.
	timestampLayout  = "20060102T150405Z"
	defaultOutputDir = "output"
)

// Request says what to render. ScriptPath is relative to WorkDir unless
// absolute. A non-empty LangOverride wins over the script's languages (BR-002).
// Progress, when set, is told about each step right before it runs. Log, when
// set, gets one line per render event (start, each phase, end); the wording is
// built here, callers only choose where the lines go (decision 64).
type Request struct {
	WorkDir      string
	ScriptPath   string
	LangOverride []string
	Progress     Progress
	Log          func(msg string)
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

// Shot is one card picture to take: an HTML page and the PNG path it goes to.
// It mirrors browser.Shot, so renderer does not import the browser wrapper.
type Shot struct{ HTML, Out string }

// Cards takes the screenshots of the built-in start and end cards, all in one
// browser launch (core/browser).
type Cards interface {
	Screenshot(ctx context.Context, shots []Shot) error
}

// Deps are the collaborators and the environment of a render, wired in main.
// Now and RunID are injected so tests get fixed names (CODE_QUALITY, SOLID).
type Deps struct {
	TTS    Synthesizer
	Rec    Recorder
	Asm    Assembler
	Cards  Cards
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
// never go back to the script file or the voice directories.
type Plan struct {
	Script    script.Script
	Languages []string
	Voices    map[string]string // language -> .onnx path
	DemoDir   string            // absolute; every path in the demo resolves against it (decision 62)
	OutputDir string            // absolute
	Intro     Card
	Outro     Card
}

// Card is a resolved start or end card. Off means none. Image, when not empty,
// is the absolute path of the picture to show; otherwise the built-in card is
// drawn from Title and Subtitle. An empty Title on the outro stands for the
// closing line of each language (card.Outro), filled in per language.
type Card struct {
	Off      bool
	Image    string
	Title    string
	Subtitle string
	Duration time.Duration
}

// Prepare validates script, narration, voices, output directory and card
// images without starting any browser, TTS or ffmpeg work (FR-001, FR-002,
// BR-011). Problems with the script, voices and paths come back together as
// failure.ValidationErrors.
func Prepare(req Request, installed voices.Installed) (Plan, error) {
	path := resolve(req.WorkDir, req.ScriptPath)
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
	paths, voiceErrs := voices.Resolve(langs, s.Voices, installed)
	errs = append(errs, voiceErrs...)

	// Paths in a demo resolve against the demo's folder, so a demo and its
	// output and images move together (decision 62). The script is written by
	// an LLM, so every path it names must still stay inside the working
	// directory (the mounted project).
	demoDir := filepath.Dir(path)
	outputDir, ok := within(req.WorkDir, resolve(demoDir, cmp.Or(s.OutputDir, defaultOutputDir)))
	if !ok {
		// With no outputDir the folder is the default one next to the demo, so
		// the message names it instead of an empty value.
		errs = append(errs, failure.ValidationError{Pointer: "/outputDir", Message: outsideWorkDir("outputDir", cmp.Or(s.OutputDir, outputDir))})
	}

	title, description := s.Name, ""
	if m := s.Meta; m != nil {
		title, description = cmp.Or(m.Title, s.Name), m.Description
	}
	intro, introErrs := resolveCard("intro", s.Intro, Card{Title: title, Subtitle: description}, demoDir, req.WorkDir)
	outro, outroErrs := resolveCard("outro", s.Outro, Card{Subtitle: title}, demoDir, req.WorkDir)
	errs = append(errs, introErrs...)
	errs = append(errs, outroErrs...)
	if len(errs) > 0 {
		return Plan{}, errs
	}

	return Plan{
		Script:    s,
		Languages: langs,
		Voices:    paths,
		DemoDir:   demoDir,
		OutputDir: outputDir,
		Intro:     intro,
		Outro:     outro,
	}, nil
}

// resolveCard turns the script's intro or outro (nil: not mentioned) into a
// Card. def is the built-in card's text, each field of which an explicit text
// replaces. A custom image is checked here, so a missing file fails before any
// browser or TTS work (FR-002).
func resolveCard(name string, b *script.Bookend, def Card, demoDir, workDir string) (Card, failure.ValidationErrors) {
	def.Duration = script.DefaultCardMs * time.Millisecond
	if b == nil {
		return def, nil
	}
	if b.Off {
		return Card{Off: true}, nil
	}
	c := Card{
		Title:    cmp.Or(b.Title, def.Title),
		Subtitle: cmp.Or(b.Subtitle, def.Subtitle),
		Duration: def.Duration,
	}
	if b.DurationMs > 0 {
		c.Duration = time.Duration(b.DurationMs) * time.Millisecond
	}
	if b.Image == "" {
		return c, nil
	}

	verr := func(msg string) failure.ValidationErrors {
		return failure.ValidationErrors{{Pointer: "/" + name + "/image", Message: msg}}
	}
	abs, ok := within(workDir, resolve(demoDir, b.Image))
	if !ok {
		return Card{}, verr(outsideWorkDir(name+".image", b.Image))
	}
	fi, err := os.Stat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Card{}, verr("image not found: " + abs)
	case err != nil:
		return Card{}, verr("read image: " + err.Error())
	case !fi.Mode().IsRegular():
		return Card{}, verr("image is not a regular file: " + abs)
	}
	// The extension is only a hint (schema); a wrong file would otherwise fail
	// in ffmpeg after TTS and the recording.
	switch isImage, err := sniffImage(abs); {
	case err != nil:
		return Card{}, verr("read image: " + err.Error())
	case !isImage:
		return Card{}, verr("image is not a PNG or JPEG: " + abs)
	}
	// The picture is the whole card, so the text is not used.
	return Card{Image: abs, Duration: c.Duration}, nil
}

// Leading bytes of the two picture formats a card image may have.
var (
	pngMagic  = []byte("\x89PNG\r\n\x1a\n")
	jpegMagic = []byte{0xff, 0xd8, 0xff}
)

// sniffImage reports whether the file at path starts like a PNG or a JPEG.
func sniffImage(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, len(pngMagic))
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return false, err
	}
	head = head[:n]
	return bytes.HasPrefix(head, pngMagic) || bytes.HasPrefix(head, jpegMagic), nil
}

// resolve anchors a script path at the working directory, so no stage needs it.
func resolve(workDir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(workDir, p)
}

// within resolves p against workDir and reports whether the result is workDir
// or below it. Symlinks are not followed: the check is on the path text.
func within(workDir, p string) (string, bool) {
	abs := resolve(workDir, p)
	rel, err := filepath.Rel(workDir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return abs, false
	}
	return abs, true
}

func outsideWorkDir(field, value string) string {
	return field + " must stay inside the working directory: " + value
}

// ScriptPath checks that a script path from tool input stays inside workDir,
// so a render_video call cannot make the parser read, and quote in its errors,
// files elsewhere in the container. The CLI does not use it: a developer may
// name any script (Request.ScriptPath).
func ScriptPath(workDir, p string) (string, error) {
	abs, ok := within(workDir, p)
	if !ok {
		return "", errors.New(outsideWorkDir("script", p))
	}
	return abs, nil
}

// OutputName is the one place that builds <name>.<lang>.<timestamp>.mp4
// (FR-010). ts is the job start time.
func OutputName(name, lang string, ts time.Time) string {
	return fmt.Sprintf("%s.%s.%s.mp4", name, lang, ts.UTC().Format(timestampLayout))
}

// reporter is how a render talks to its caller: step progress and log lines,
// with the clock that times the phases.
type reporter struct {
	progress Progress
	log      func(msg string)
	now      func() time.Time
}

// logf sends one line to Request.Log, if the caller set one.
func (r reporter) logf(format string, a ...any) {
	if r.log != nil {
		r.log(fmt.Sprintf(format, a...))
	}
}

// since is the time elapsed since t, to a tenth of a second.
func (r reporter) since(t time.Time) string { return round(r.now().Sub(t)) }

func round(d time.Duration) string { return d.Round(100 * time.Millisecond).String() }

// Render validates the request, renders every selected language in order and
// publishes the MP4s only when all of them succeeded (BR-004, FR-010). Work
// happens under <work>/.screencaster/tmp/<runId>, which is removed on every
// path. A step, TTS or assembly problem comes back as *failure.Failure; a
// cancelled ctx as ctx.Err(). Once the script is accepted, Request.Log gets a
// start line, one line per phase and language, and an end line (decision 64).
func Render(ctx context.Context, d Deps, req Request) (_ []Output, err error) {
	start := d.Now() // FR-010: the job start time names every output
	plan, err := Prepare(req, d.Voices)
	if err != nil {
		return nil, err
	}

	rep := reporter{progress: req.Progress, log: req.Log, now: d.Now}
	rep.logf("render start: %s name=%s languages=%s voices=%s steps=%d output=%s",
		req.ScriptPath, plan.Script.Name, strings.Join(plan.Languages, ","), voiceNames(plan), len(plan.Script.Steps), plan.OutputDir)
	defer func() {
		if err != nil {
			rep.logf("render failed after %s", rep.since(start))
		}
	}()

	runDir := filepath.Join(req.WorkDir, ".screencaster", "tmp", d.RunID())
	defer func() { _ = os.RemoveAll(runDir) }()

	outs := make([]Output, 0, len(plan.Languages))
	for _, lang := range plan.Languages {
		out, err := renderLanguage(ctx, d, plan, lang, filepath.Join(runDir, lang), rep)
		if err != nil {
			return nil, err
		}
		outs = append(outs, out)
	}
	outs, err = publish(plan.OutputDir, plan.Script.Name, start, outs)
	if err != nil {
		return nil, err
	}
	rep.logf("render done in %s", rep.since(start))
	for _, o := range outs {
		rep.logf("[%s] %s (%s)", o.Lang, o.Path, round(time.Duration(o.DurationMs)*time.Millisecond))
	}
	return outs, nil
}

// voiceNames lists the voice per language as lang=name, in language order.
func voiceNames(plan Plan) string {
	names := make([]string, len(plan.Languages))
	for i, lang := range plan.Languages {
		names[i] = lang + "=" + strings.TrimSuffix(filepath.Base(plan.Voices[lang]), ".onnx")
	}
	return strings.Join(names, ",")
}

// renderLanguage runs TTS, builds the cards, records and assembles one
// language (ARCHITECTURE §4, rule 3: clip durations decide step timing). The
// cards come before the slow recording so a card problem fails early. The
// returned Path is the temp MP4.
func renderLanguage(ctx context.Context, d Deps, plan Plan, lang, dir string, rep reporter) (Output, error) {
	clipDir, videoDir, cardDir := filepath.Join(dir, "clips"), filepath.Join(dir, "video"), filepath.Join(dir, "cards")
	for _, p := range []string{clipDir, videoDir, cardDir} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			return Output{}, fmt.Errorf("create temp dir: %w", err)
		}
	}

	steps := plan.Script.Steps
	durations := map[int]time.Duration{}
	clipPaths := map[int]string{}
	t := d.Now()
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
	rep.logf("[%s] narration: %d clips in %s", lang, len(clipPaths), rep.since(t))

	intro, outro, err := buildCards(ctx, d, plan, lang, cardDir, rep)
	if err != nil {
		return Output{}, err
	}

	in := recorder.Input{
		Steps:        steps,
		Clips:        durations,
		Lang:         lang,
		Dir:          videoDir,
		BaseURL:      plan.Script.BaseURL,
		StorageState: plan.Script.StorageState,
	}
	if intro != nil {
		in.StartImage = intro.Path // the picture carries on until the first page paints
	}
	if rep.progress != nil {
		in.OnStep = func(i int) { rep.progress(lang, i+1, len(steps), steps[i].Action, executor.Target(steps[i])) }
	}
	rep.logf("[%s] recording", lang)
	t = d.Now()
	rec, err := d.Rec.Record(ctx, in)
	if err != nil {
		return Output{}, err
	}
	rep.logf("[%s] recorded in %s", lang, rep.since(t))

	// Offsets stay relative to the recording; the assembler adds the intro.
	asm := assembler.Input{Webm: rec.WebmPath, Intro: intro, Outro: outro, Out: filepath.Join(dir, "out.mp4")}
	for i := range steps { // step order keeps the ffmpeg inputs stable
		if p, ok := clipPaths[i]; ok {
			asm.Clips = append(asm.Clips, assembler.Clip{Path: p, Offset: rec.Offsets[i]})
		}
	}
	if m := plan.Script.Meta; m != nil {
		asm.Title, asm.Comment = m.Title, m.Description
	}
	t = d.Now()
	ms, err := d.Asm.Assemble(ctx, asm)
	if err != nil {
		if ctx.Err() != nil {
			return Output{}, ctx.Err()
		}
		return Output{}, failure.Assembly(lang, err.Error())
	}
	rep.logf("[%s] assembled %s video in %s", lang, round(time.Duration(ms)*time.Millisecond), rep.since(t))
	return Output{Lang: lang, Path: asm.Out, DurationMs: ms}, nil
}

// buildCards returns the intro and outro stills for lang (nil for a card that
// is off). A card with a custom image needs no work; the built-in cards of
// this language are drawn in one browser launch into dir. The outro's closing
// line follows the language (decision 63).
func buildCards(ctx context.Context, d Deps, plan Plan, lang, dir string, rep reporter) (intro, outro *assembler.Still, err error) {
	outroCard := plan.Outro
	outroCard.Title = cmp.Or(outroCard.Title, card.Outro(lang))

	var shots []Shot
	still := func(name string, c Card) (*assembler.Still, error) {
		if c.Off {
			return nil, nil
		}
		if c.Image != "" {
			return &assembler.Still{Path: c.Image, Duration: c.Duration}, nil
		}
		html, err := card.HTML(card.Text{Title: c.Title, Subtitle: c.Subtitle})
		if err != nil {
			return nil, failure.Cards(lang, err)
		}
		out := filepath.Join(dir, name+".png")
		shots = append(shots, Shot{HTML: html, Out: out})
		return &assembler.Still{Path: out, Duration: c.Duration}, nil
	}
	if intro, err = still("intro", plan.Intro); err != nil {
		return nil, nil, err
	}
	if outro, err = still("outro", outroCard); err != nil {
		return nil, nil, err
	}
	if len(shots) == 0 {
		return intro, outro, nil
	}

	t := d.Now()
	if err := d.Cards.Screenshot(ctx, shots); err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, failure.Cards(lang, err)
	}
	rep.logf("[%s] cards: %d built in %s", lang, len(shots), rep.since(t))
	return intro, outro, nil
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
