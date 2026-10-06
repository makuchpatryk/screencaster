// Package renderer orchestrates a render (ARCHITECTURE §4): validate, then per
// language synthesize, record and assemble, then publish. Render is the one
// code path for the CLI and the MCP worker; Prepare is also the MCP server's
// synchronous check in render_video.
package renderer

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"screencaster/internal/domain/failure"
	"screencaster/internal/domain/script"
	"screencaster/internal/domain/voices"
)

// defaultOutputDir is where MP4s go, next to the demo, when the script names
// no outputDir (decision 62).
const defaultOutputDir = "output"

// Request says what to render. ScriptPath is relative to WorkDir unless
// absolute. A non-empty LangOverride wins over the script's languages (BR-002).
// Progress, when set, is told about each step right before it runs. Log, when
// set, gets one line per render event (start, each phase, end); the wording is
// built here, callers only choose where the lines go (decision 64).
type Request struct {
	WorkDir    string
	ScriptPath string
	// Script, when set, is the script's content, parsed instead of reading
	// ScriptPath: an MCP job renders the snapshot taken at submit time.
	// ScriptPath still anchors the demo folder and names the script.
	Script       []byte
	LangOverride []string
	Progress     Progress
	Log          func(msg string)
}

// Progress reports step i (1-based) of n for lang; target is the selector or
// URL (empty when the step has none).
type Progress func(lang string, i, n int, action, target string)

// Plan is everything the pipeline needs after validation, so later stages
// never go back to the script file or the voice directories.
type Plan struct {
	Script    script.Script
	Type      string            // script.TypeVideo or script.TypeScreenshots
	Languages []string          // nil for a screenshots script
	Voices    map[string]string // language -> voice name; nil for a screenshots script
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
// failure.ValidationErrors. files reads the script and the card images. A
// screenshots script has no languages, voices or cards, so those checks are
// skipped and a language override is rejected (decision 72).
func Prepare(req Request, catalog voices.Catalog, files Files) (Plan, error) {
	path := resolve(req.WorkDir, req.ScriptPath)
	data := req.Script
	if data == nil {
		var err error
		if data, err = ReadScript(files, path); err != nil {
			return Plan{}, err
		}
	}

	s, err := script.Parse(data)
	if err != nil {
		return Plan{}, err
	}

	shots := s.Kind() == script.TypeScreenshots
	var (
		langs  []string
		chosen map[string]string
		errs   failure.ValidationErrors
	)
	if shots {
		if len(req.LangOverride) > 0 {
			errs = append(errs, failure.ValidationError{Pointer: "/languages", Message: "languages are not used by a screenshots script"})
		}
	} else {
		langs = script.Languages(req.LangOverride, s.Languages)
		errs = script.Validate(s, langs)
		var voiceErrs failure.ValidationErrors
		chosen, voiceErrs = voices.Resolve(langs, s.Voices, catalog)
		errs = append(errs, voiceErrs...)
	}

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

	var intro, outro Card
	if !shots {
		title, description := s.Name, ""
		if m := s.Meta; m != nil {
			title, description = cmp.Or(m.Title, s.Name), m.Description
		}
		var introErrs, outroErrs failure.ValidationErrors
		intro, introErrs = resolveCard(files, "intro", s.Intro, Card{Title: title, Subtitle: description}, demoDir, req.WorkDir)
		outro, outroErrs = resolveCard(files, "outro", s.Outro, Card{Subtitle: title}, demoDir, req.WorkDir)
		errs = append(errs, introErrs...)
		errs = append(errs, outroErrs...)
	}
	if len(errs) > 0 {
		return Plan{}, errs
	}

	return Plan{
		Script:    s,
		Type:      s.Kind(),
		Languages: langs,
		Voices:    chosen,
		DemoDir:   demoDir,
		OutputDir: outputDir,
		Intro:     intro,
		Outro:     outro,
	}, nil
}

// ReadScript reads the script at path, with the message a missing file gets
// wherever it is read (CLI, render_video).
func ReadScript(files Files, path string) ([]byte, error) {
	data, err := files.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("script not found: %s", path)
	}
	if err != nil {
		return nil, fmt.Errorf("read script: %w", err)
	}
	return data, nil
}

// resolveCard turns the script's intro or outro (nil: not mentioned) into a
// Card. def is the built-in card's text, each field of which an explicit text
// replaces. A custom image is checked here, so a missing file fails before any
// browser or TTS work (FR-002).
func resolveCard(files Files, name string, b *script.Bookend, def Card, demoDir, workDir string) (Card, failure.ValidationErrors) {
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
	fi, err := files.Stat(abs)
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
	switch isImage, err := sniffImage(files, abs); {
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
func sniffImage(files Files, path string) (bool, error) {
	f, err := files.Open(path)
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
