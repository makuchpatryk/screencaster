// Package voices resolves the voice for each selected language and lists the
// languages on offer (BR-011, FR-018). It works on a provider's Catalog and
// does no I/O; finding the voices is the provider's job (core/provider).
package voices

import (
	"slices"
	"strings"

	"screencaster/core/failure"
	"screencaster/core/script"
)

// Voice is one installed voice as its provider names it.
type Voice struct {
	Name string // opaque ID passed back to the provider
	Lang string // language code, decided by the provider; empty when unknown
}

// Catalog is what a provider offers: its installed voices and its built-in
// default voice per language (BR-011).
type Catalog struct {
	Voices   []Voice
	Defaults map[string]string // language -> voice name
}

func (c Catalog) installed(name string) bool {
	return slices.ContainsFunc(c.Voices, func(v Voice) bool { return v.Name == name })
}

// Resolve picks the voice for each of langs in priority order: script, then
// the provider's default (BR-011). It returns the voice name per language, or
// every language that has no voice or an uninstalled one.
func Resolve(langs []string, scriptVoices map[string]string, c Catalog) (map[string]string, failure.ValidationErrors) {
	names := make(map[string]string, len(langs))
	var errs failure.ValidationErrors
	for _, lang := range langs {
		name := firstNonEmpty(scriptVoices[lang], c.Defaults[lang])
		if name == "" {
			errs = append(errs, failure.ValidationError{Message: "no voice for language: " + lang})
			continue
		}
		if !c.installed(name) {
			errs = append(errs, failure.ValidationError{Message: "voice not installed: " + name})
			continue
		}
		names[lang] = name
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return names, nil
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

// Options lists every language with a default voice or an installed voice,
// sorted by code. The default voice is the provider's (BR-011) and counts
// only when it is installed, because Resolve would reject it otherwise.
func Options(c Catalog) []Option {
	byLang := map[string][]string{}
	for lang := range c.Defaults {
		byLang[lang] = nil
	}
	for _, v := range c.Voices {
		if v.Lang != "" {
			byLang[v.Lang] = append(byLang[v.Lang], v.Name)
		}
	}

	defaults := script.Languages(nil, nil)
	opts := make([]Option, 0, len(byLang))
	for lang, names := range byLang {
		slices.Sort(names)
		def := c.Defaults[lang]
		if !c.installed(def) {
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
