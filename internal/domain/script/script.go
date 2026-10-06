// Package script parses and validates demo scripts (FR-002). The JSON Schema
// is the one source of truth for the format; the rules a schema cannot
// express live in Validate.
package script

import (
	"bytes"
	"cmp"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"sync"

	"github.com/goccy/go-yaml"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"screencaster/internal/domain/failure"
)

//go:embed script.schema.json
var schemaJSON []byte

//go:embed example.yaml
var exampleYAML []byte

//go:embed example-screenshots.yaml
var exampleScreenshotsYAML []byte

const (
	schemaURL       = "script.schema.json"
	defaultLanguage = "en"
)

// Script is a parsed demo script.
type Script struct {
	Name         string            `json:"name"`
	Type         string            `json:"type"` // "" means video; see Kind
	BaseURL      string            `json:"baseUrl"`
	StorageState *StorageState     `json:"storageState"`
	OutputDir    string            `json:"outputDir"`
	Languages    []string          `json:"languages"`
	Voices       map[string]string `json:"voices"`
	Meta         *Meta             `json:"meta"`
	Intro        *Bookend          `json:"intro"`
	Outro        *Bookend          `json:"outro"`
	Steps        []Step            `json:"steps"`
}

// The script types. A screenshots script writes PNGs at its screenshot steps
// instead of a narrated video.
const (
	TypeVideo       = "video"
	TypeScreenshots = "screenshots"
)

// Kind returns the script type with the default applied.
func (s Script) Kind() string { return cmp.Or(s.Type, TypeVideo) }

// DefaultCardMs is how long a start or end card is shown unless the script
// says otherwise (ARCHITECTURE §5, decision 63).
const DefaultCardMs = 3000

// Bookend is the start or end card. A nil *Bookend in Script means the
// built-in card with default text; Off means no card. Image, when set, is a
// path relative to the demo file's folder and replaces the built-in card, so
// the schema rejects it together with Title or Subtitle.
type Bookend struct {
	Off        bool   `json:"-"`
	Image      string `json:"image"`
	Title      string `json:"title"`
	Subtitle   string `json:"subtitle"`
	DurationMs int    `json:"durationMs"`
}

// UnmarshalJSON maps the schema's `false` to Off. Nothing else is custom: the
// schema already rejected `true` and every other scalar.
func (b *Bookend) UnmarshalJSON(data []byte) error {
	if string(bytes.TrimSpace(data)) == "false" {
		*b = Bookend{Off: true}
		return nil
	}
	type plain Bookend // no methods, so no recursion
	return json.Unmarshal(data, (*plain)(b))
}

// StorageState is the session the browser starts with, in the shape of
// Playwright's context.storageState(). The tags are Playwright's, so adapters/browser
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

// Audiences are the meta.audience values; a test keeps them equal to the
// schema's enum. get_options offers them (FR-018).
var Audiences = []string{"release-notes", "sales", "marketing"}

// SchemaJSON returns the embedded JSON Schema, for the render_video tool
// description.
func SchemaJSON() []byte { return schemaJSON }

// ExampleYAML returns the embedded example script, for the render_video tool
// description. A test keeps it valid.
func ExampleYAML() []byte { return exampleYAML }

// ExampleScreenshotsYAML returns the embedded example screenshots script, for
// the take_screenshots tool description. A test keeps it valid.
func ExampleScreenshotsYAML() []byte { return exampleScreenshotsYAML }

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
			return Script{}, append(oldStepForm(jsonData), schemaErrors(ve)...)
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
	if errs := checkType(s); len(errs) > 0 {
		return Script{}, errs
	}
	return s, nil
}

// checkType holds the rules that tie the script type to the fields and steps
// the schema allows in any script: screenshot steps belong to a screenshots
// script, and a screenshots script has no narration, cards, languages or
// voices. It runs in Parse, not Validate, so ParseSteps (explore_page) rejects
// a screenshot step too: its placeholder script is a video script.
func checkType(s Script) failure.ValidationErrors {
	var errs failure.ValidationErrors
	add := func(pointer, msg string) {
		errs = append(errs, failure.ValidationError{Pointer: pointer, Message: msg})
	}
	isShot := func(st Step) bool { _, ok := st.Action.(Screenshot); return ok }

	if s.Kind() == TypeVideo {
		for i, st := range s.Steps {
			if isShot(st) {
				add(fmt.Sprintf("/steps/%d/screenshot", i), "screenshot steps need type: screenshots")
			}
		}
		return errs
	}

	for _, f := range []struct {
		name string
		set  bool
	}{
		{"languages", s.Languages != nil},
		{"voices", s.Voices != nil},
		{"intro", s.Intro != nil},
		{"outro", s.Outro != nil},
	} {
		if f.set {
			add("/"+f.name, f.name+" is not allowed in a screenshots script")
		}
	}
	for i, st := range s.Steps {
		if st.Narration != nil {
			add(fmt.Sprintf("/steps/%d/narration", i), "narration is not allowed in a screenshots script")
		}
	}
	if !slices.ContainsFunc(s.Steps, isShot) {
		add("/steps", "a screenshots script needs at least one screenshot step")
	}
	return errs
}

// ParseSteps checks loose steps (explore_page actions, each a raw JSON
// object) against the same schema as a script's steps and decodes them, so
// the executor never meets a step without the fields its action needs.
// Pointers read /steps/<i>/... like a script's. No steps is fine.
func ParseSteps(raw []json.RawMessage) ([]Step, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	// Placeholder name and baseUrl satisfy the script's required fields; only
	// the steps are being checked. JSON is YAML, so Parse reads it as is.
	data, err := json.Marshal(struct {
		Name    string            `json:"name"`
		BaseURL string            `json:"baseUrl"`
		Steps   []json.RawMessage `json:"steps"`
	}{"explore", "http://explore.invalid", raw})
	if err != nil {
		return nil, fmt.Errorf("encode steps: %w", err)
	}
	s, err := Parse(data)
	if err != nil {
		return nil, err
	}
	return s.Steps, nil
}

// oldFormHint is the one message a script in the flat `action:` step form
// gets ahead of its schema errors (decision 70).
const oldFormHint = "old step form: use the action as the key, e.g. `- click: \"#id\"` (see README)"

// oldStepForm returns a hint at the first step written as `action: ...`, the
// form before decision 70, or nil when there is none.
func oldStepForm(jsonData []byte) failure.ValidationErrors {
	var doc struct {
		Steps []map[string]json.RawMessage `json:"steps"`
	}
	if json.Unmarshal(jsonData, &doc) != nil {
		return nil // not the expected shape: the schema reports it
	}
	for i, step := range doc.Steps {
		if _, ok := step["action"]; ok {
			return failure.ValidationErrors{{Pointer: fmt.Sprintf("/steps/%d", i), Message: oldFormHint}}
		}
	}
	return nil
}

// oneAction replaces the schema's property-count message on a step, the only
// object the schema counts properties of (decision 70).
const oneAction = "a step needs exactly one action: goto, click, fill, select, press, hover, scroll, wait or screenshot"

// schemaErrors turns the leaves of the validator's error tree into sorted,
// de-duplicated pointer/message pairs. The inner nodes (allOf, oneOf, if/then)
// only say "something below failed".
func schemaErrors(ve *jsonschema.ValidationError) failure.ValidationErrors {
	type leaf struct {
		failure.ValidationError
		count, unknown bool // a property-count or an unknown-property error
	}
	var leaves []leaf
	var walk func(u jsonschema.OutputUnit)
	walk = func(u jsonschema.OutputUnit) {
		if len(u.Errors) == 0 && u.Error != nil {
			l := leaf{ValidationError: failure.ValidationError{Pointer: u.InstanceLocation, Message: u.Error.String()}}
			switch u.Error.Kind.(type) {
			case *kind.MinProperties, *kind.MaxProperties:
				l.count, l.Message = true, oneAction
			case *kind.AdditionalProperties:
				l.unknown = true
			}
			leaves = append(leaves, l)
			return
		}
		for _, c := range u.Errors {
			walk(c)
		}
	}
	walk(*ve.DetailedOutput())

	// An unknown key in a step also breaks its property count; the unknown key
	// is the root cause, so the count message is dropped.
	var errs failure.ValidationErrors
	for _, l := range leaves {
		if l.count && slices.ContainsFunc(leaves, func(o leaf) bool { return o.unknown && o.Pointer == l.Pointer }) {
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
// an entry for every selected language (FR-002). An empty string is an entry:
// the step stays silent in that language. Text for unselected languages is
// allowed and ignored.
func Validate(s Script, langs []string) failure.ValidationErrors {
	var errs failure.ValidationErrors
	for i, step := range s.Steps {
		if len(step.Narration) == 0 {
			continue // un-narrated step
		}
		for _, lang := range langs {
			if _, ok := step.Narration[lang]; !ok {
				errs = append(errs, failure.ValidationError{
					Pointer: fmt.Sprintf("/steps/%d/narration", i),
					Message: "missing narration for language " + lang,
				})
			}
		}
	}
	return errs
}
