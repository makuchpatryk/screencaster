// Package voices discovers installed Piper voices and resolves the voice for
// each selected language (BR-011, FR-018).
package voices

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"screencaster/core/failure"
	"screencaster/core/script"
)

const modelExt = ".onnx"

// builtin holds the only languages with a default voice (BR-011). Names must
// match the files baked into the image.
var builtin = map[string]string{
	"en": "en_US-ryan-high",
	"pl": "pl_PL-darkman-medium",
}

// Installed maps a voice name to the path of its .onnx model.
type Installed map[string]string

// Discover lists the *.onnx models in dirs. A directory that does not exist is
// skipped (/work/voices is optional). When a name appears in several
// directories the later one wins, so project voices override the image's.
func Discover(dirs ...string) (Installed, error) {
	inst := Installed{}
	for _, dir := range dirs {
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
			inst[strings.TrimSuffix(e.Name(), modelExt)] = filepath.Join(dir, e.Name())
		}
	}
	return inst, nil
}

// Lang returns the language code of a voice: the part of its name before the
// first "_" (pl_PL-darkman-medium -> pl). It is empty when the name has none.
func Lang(voice string) string {
	code, _, found := strings.Cut(voice, "_")
	if !found {
		return ""
	}
	return code
}

// Resolve picks the voice for each of langs in priority order: script, config,
// built-in (BR-011). It returns the model path per language, or every
// language that has no voice or an uninstalled one.
func Resolve(langs []string, scriptVoices, cfgVoices map[string]string, inst Installed) (map[string]string, failure.ValidationErrors) {
	paths := make(map[string]string, len(langs))
	var errs failure.ValidationErrors
	for _, lang := range langs {
		name := firstNonEmpty(scriptVoices[lang], cfgVoices[lang], builtin[lang])
		if name == "" {
			errs = append(errs, failure.ValidationError{Message: "no voice for language: " + lang})
			continue
		}
		path, ok := inst[name]
		if !ok {
			errs = append(errs, failure.ValidationError{Message: "voice not installed: " + name})
			continue
		}
		paths[lang] = path
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return paths, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Option describes one language for get_options (FR-018). DefaultVoice is empty
// when no usable default exists.
type Option struct {
	Code              string
	SelectedByDefault bool
	DefaultVoice      string
	Voices            []string // installed voice names, sorted
}

// Options lists en, pl and every other language with an installed voice,
// sorted by code. The default voice follows BR-011 (config, then built-in)
// and counts only when it is installed, because Resolve would reject it
// otherwise.
func Options(inst Installed, cfgVoices map[string]string) []Option {
	byLang := map[string][]string{}
	for lang := range builtin {
		byLang[lang] = nil
	}
	for name := range inst {
		if lang := Lang(name); lang != "" {
			byLang[lang] = append(byLang[lang], name)
		}
	}

	defaults := script.Languages(nil, nil)
	opts := make([]Option, 0, len(byLang))
	for lang, names := range byLang {
		slices.Sort(names)
		def := firstNonEmpty(cfgVoices[lang], builtin[lang])
		if _, ok := inst[def]; !ok {
			def = ""
		}
		opts = append(opts, Option{
			Code:              lang,
			SelectedByDefault: slices.Contains(defaults, lang),
			DefaultVoice:      def,
			Voices:            append([]string{}, names...),
		})
	}
	slices.SortFunc(opts, func(a, b Option) int { return strings.Compare(a.Code, b.Code) })
	return opts
}
