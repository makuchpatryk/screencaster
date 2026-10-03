// Package renderer orchestrates a render. For now it holds the validation
// half of the pipeline (ARCHITECTURE §4, rule 1), shared by the CLI and by the
// MCP server's synchronous check in render_video.
package renderer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"screencaster/core/config"
	"screencaster/core/script"
	"screencaster/core/voices"
)

// timestampLayout is the FR-010 job-start format, UTC.
const timestampLayout = "20060102T150405Z"

// Request says what to render. ScriptPath is relative to WorkDir unless
// absolute. A non-empty LangOverride wins over the script's languages (BR-002).
type Request struct {
	WorkDir      string
	ScriptPath   string
	LangOverride []string
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
