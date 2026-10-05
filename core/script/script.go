// Package script parses and validates demo scripts (FR-002). The JSON Schema
// is the one source of truth for the format; the rules a schema cannot
// express live in Validate.
package script

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/goccy/go-yaml"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"screencaster/core/failure"
)

//go:embed script.schema.json
var schemaJSON []byte

//go:embed example.yaml
var exampleYAML []byte

const (
	schemaURL       = "script.schema.json"
	defaultLanguage = "en"
)

// Script is a parsed demo script.
type Script struct {
	Name         string            `json:"name"`
	BaseURL      string            `json:"baseUrl"`
	StorageState *StorageState     `json:"storageState"`
	OutputDir    string            `json:"outputDir"`
	Languages    []string          `json:"languages"`
	Voices       map[string]string `json:"voices"`
	Meta         *Meta             `json:"meta"`
	Steps        []Step            `json:"steps"`
}

// StorageState is the session the browser starts with, in the shape of
// Playwright's context.storageState(). The tags are Playwright's, so core/browser
// can hand it over by a JSON round trip. omitempty keeps unset fields out of the
// JSON, which the explore_page input schema is inferred from.
type StorageState struct {
	Cookies []Cookie `json:"cookies,omitempty"`
	Origins []Origin `json:"origins,omitempty"`
}

// Cookie needs a URL, or both Domain and Path (the schema checks this).
type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	URL      string  `json:"url,omitempty"`
	Domain   string  `json:"domain,omitempty"`
	Path     string  `json:"path,omitempty"`
	Expires  float64 `json:"expires,omitempty"` // Unix seconds, -1 for a session cookie
	HTTPOnly bool    `json:"httpOnly,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
	SameSite string  `json:"sameSite,omitempty"` // Strict, Lax or None
}

// Origin holds the localStorage of one origin.
type Origin struct {
	Origin       string      `json:"origin"`
	LocalStorage []NameValue `json:"localStorage,omitempty"`
}

// NameValue is one localStorage entry.
type NameValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Meta is written to the MP4 title and comment tags (FR-009.5).
type Meta struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Audience    string `json:"audience"`
}

// Step is one action plus optional narration keyed by language code. Y and Ms
// are pointers because 0 is a valid scroll position. The omitempty tags keep
// an unset field out of the JSON, which ValidateSteps feeds to the schema and
// the explore_page input schema is inferred from.
type Step struct {
	Action    string            `json:"action"`
	URL       string            `json:"url,omitempty"`
	Selector  string            `json:"selector,omitempty"`
	Value     string            `json:"value,omitempty"`
	Key       string            `json:"key,omitempty"`
	Y         *int              `json:"y,omitempty"`
	Ms        *int              `json:"ms,omitempty"`
	Narration map[string]string `json:"narration,omitempty"`
}

// Audiences are the meta.audience values; a test keeps them equal to the
// schema's enum. get_options offers them (FR-018).
var Audiences = []string{"release-notes", "sales", "marketing"}

// SchemaJSON returns the embedded JSON Schema, for the render_video tool
// description.
func SchemaJSON() []byte { return schemaJSON }

// ExampleYAML returns the embedded example script, for the render_video tool
// description. A test keeps it valid.
func ExampleYAML() []byte { return exampleYAML }

var compiledSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaURL, doc); err != nil {
		return nil, err
	}
	return c.Compile(schemaURL)
})

// AbsoluteHTTP reports whether raw is an absolute http or https URL with a
// host. It is the one URL rule for baseUrl and explore_page's url (BR-010).
func AbsoluteHTTP(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// Parse decodes YAML, validates it against the schema and returns the typed
// script. Problems with the input are returned as failure.ValidationErrors,
// all of them at once.
func Parse(data []byte) (Script, error) {
	schema, err := compiledSchema()
	if err != nil {
		return Script{}, fmt.Errorf("compile script schema: %w", err)
	}

	jsonData, err := yaml.YAMLToJSON(data)
	if err != nil {
		return Script{}, failure.ValidationErrors{{Message: "invalid YAML: " + err.Error()}}
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(jsonData))
	if err != nil {
		return Script{}, fmt.Errorf("decode script: %w", err)
	}

	if err := schema.Validate(instance); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			return Script{}, schemaErrors(ve)
		}
		return Script{}, fmt.Errorf("validate script: %w", err)
	}

	var s Script
	if err := json.Unmarshal(jsonData, &s); err != nil {
		return Script{}, fmt.Errorf("decode script: %w", err)
	}
	// The schema only says "non-empty string"; the URL rule lives in Go.
	if !AbsoluteHTTP(s.BaseURL) {
		return Script{}, failure.ValidationErrors{{
			Pointer: "/baseUrl",
			Message: "baseUrl must be an absolute http or https URL: " + s.BaseURL,
		}}
	}
	return s, nil
}

// ValidateSteps checks loose steps (explore_page actions) against the same
// schema as a script, so the executor never meets a step without the fields
// its action needs. Pointers read /steps/<i>/... like a script's. Empty steps
// are fine.
func ValidateSteps(steps []Step) error {
	if len(steps) == 0 {
		return nil
	}
	// Placeholder name and baseUrl satisfy the script's required fields; only
	// the steps are being checked.
	data, err := json.Marshal(struct {
		Name    string `json:"name"`
		BaseURL string `json:"baseUrl"`
		Steps   []Step `json:"steps"`
	}{"explore", "http://explore.invalid", steps})
	if err != nil {
		return fmt.Errorf("encode steps: %w", err)
	}
	_, err = Parse(data)
	return err
}

// schemaErrors turns the leaves of the validator's error tree into sorted,
// de-duplicated pointer/message pairs. The inner nodes (allOf, oneOf, if/then)
// only say "something below failed".
func schemaErrors(ve *jsonschema.ValidationError) failure.ValidationErrors {
	type leaf struct {
		failure.ValidationError
		notAllowed bool // reported by unevaluatedProperties
	}
	var leaves []leaf
	var walk func(u jsonschema.OutputUnit)
	walk = func(u jsonschema.OutputUnit) {
		if len(u.Errors) == 0 && u.Error != nil {
			l := leaf{ValidationError: failure.ValidationError{Pointer: u.InstanceLocation, Message: u.Error.String()}}
			// unevaluatedProperties reports a rejected property as "false schema".
			if _, ok := u.Error.Kind.(*kind.FalseSchema); ok {
				l.notAllowed, l.Message = true, "property not allowed here"
			}
			leaves = append(leaves, l)
			return
		}
		for _, c := range u.Errors {
			walk(c)
		}
	}
	walk(*ve.DetailedOutput())

	// A failed then-branch (say a bad `ms`) discards its property annotations,
	// so unevaluatedProperties then re-flags the step's valid fields. When the
	// object has any other error, those reports are artifacts: drop them.
	var errs failure.ValidationErrors
	for _, l := range leaves {
		if l.notAllowed && slices.ContainsFunc(leaves, func(o leaf) bool {
			return !o.notAllowed && within(o.Pointer, parentPointer(l.Pointer))
		}) {
			continue
		}
		errs = append(errs, l.ValidationError)
	}

	sort.SliceStable(errs, func(i, j int) bool {
		if errs[i].Pointer != errs[j].Pointer {
			return errs[i].Pointer < errs[j].Pointer
		}
		return errs[i].Message < errs[j].Message
	})
	return slices.Compact(errs)
}

// parentPointer is the pointer of the object holding the property at p.
func parentPointer(p string) string { return p[:strings.LastIndex(p, "/")] }

// within reports whether pointer p is root or lies below it.
func within(p, root string) bool { return p == root || strings.HasPrefix(p, root+"/") }

// Languages is the single implementation of BR-002: the render-time override,
// then the script's languages, then ["en"]. Duplicates are dropped so one
// language is never rendered twice into the same file name.
func Languages(override, scriptLangs []string) []string {
	switch {
	case len(override) > 0:
		return dedupe(override)
	case len(scriptLangs) > 0:
		return dedupe(scriptLangs)
	}
	return []string{defaultLanguage}
}

func dedupe(in []string) []string {
	out := make([]string, 0, len(in))
	for _, l := range in {
		if !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out
}

// Validate checks the rules the schema cannot express: a narrated step needs
// text for every selected language (FR-002). Text for unselected languages is
// allowed and ignored.
func Validate(s Script, langs []string) failure.ValidationErrors {
	var errs failure.ValidationErrors
	for i, step := range s.Steps {
		if len(step.Narration) == 0 {
			continue // un-narrated step
		}
		for _, lang := range langs {
			if step.Narration[lang] == "" {
				errs = append(errs, failure.ValidationError{
					Pointer: fmt.Sprintf("/steps/%d/narration", i),
					Message: "missing narration for language " + lang,
				})
			}
		}
	}
	return errs
}
