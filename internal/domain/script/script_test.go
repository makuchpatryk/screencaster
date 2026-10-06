package script

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"screencaster/internal/domain/failure"
)

const samples = "../../../testdata/scripts"

func readSample(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(samples, rel))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// parseErrors parses data and returns the validation errors, failing the test
// if the error is of another kind.
func parseErrors(t *testing.T, data []byte) failure.ValidationErrors {
	t.Helper()
	_, err := Parse(data)
	if err == nil {
		return nil
	}
	var ve failure.ValidationErrors
	if !errors.As(err, &ve) {
		t.Fatalf("Parse() error = %v, want failure.ValidationErrors", err)
	}
	return ve
}

func TestParse_validSamples(t *testing.T) {
	tests := []struct {
		file      string
		wantLangs []string // as written in the script
		wantSteps int
	}{
		{"valid/default-langs.yaml", nil, 2},
		{"valid/en-pl.yaml", []string{"en", "pl"}, 2},
		{"valid/all-actions.yaml", nil, 12},
		{"valid/all-target-fields.yaml", nil, 1},
		{"valid/cards.yaml", nil, 1},
		{"valid/silent-narration.yaml", []string{"en", "pl"}, 2},
		{"valid/cards-text.yaml", nil, 1},
		{"valid/screenshots-areas.yaml", nil, 6},
		{"valid/screenshots-annotate.yaml", nil, 4},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			s, err := Parse(readSample(t, tt.file))
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if !reflect.DeepEqual(s.Languages, tt.wantLangs) {
				t.Errorf("Languages = %v, want %v", s.Languages, tt.wantLangs)
			}
			if len(s.Steps) != tt.wantSteps {
				t.Errorf("len(Steps) = %d, want %d", len(s.Steps), tt.wantSteps)
			}
			if errs := Validate(s, Languages(nil, s.Languages)); len(errs) != 0 {
				t.Errorf("Validate() = %v, want none (FR-002 AC3)", errs)
			}
		})
	}
}

func TestParse_decodesFields(t *testing.T) {
	s, err := Parse(readSample(t, "valid/all-actions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Action{
		Goto{URL: "/projects"},
		Click{Selector: `role=button[name="New project"]`},
		Fill{Selector: `input[name="name"]`, Value: "My Project"},
		Fill{Selector: `input[name="notes"]`, Value: ""}, // clears the field
		Select{Selector: `select[name="visibility"]`, Value: "private"},
		Press{Key: "Enter"},
		Press{Key: "Tab", Selector: `input[name="name"]`},
		Hover{Selector: "text=Create"},
		ScrollInto{Selector: "#footer"},
		ScrollTo{Y: 0},
		Pause{D: 30 * time.Second},
		WaitFor{Selector: "#toast"},
	}
	var got []Action
	for _, st := range s.Steps {
		got = append(got, st.Action)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("actions =\n%#v\nwant\n%#v", got, want)
	}

	pl, err := Parse(readSample(t, "valid/en-pl.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if pl.Meta == nil || pl.Meta.Audience != "sales" || pl.Voices["pl"] != "pl_PL-gosia-medium" {
		t.Errorf("meta/voices not decoded: %+v", pl)
	}
	if got := pl.Steps[0].Narration["pl"]; got != "To jest strona projektów." {
		t.Errorf("narration.pl = %q", got)
	}
}

func TestParse_decodesTargetFields(t *testing.T) {
	s, err := Parse(readSample(t, "valid/all-target-fields.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if s.BaseURL != "https://example.com/app" || s.OutputDir != "videos" {
		t.Errorf("target fields = %q %q", s.BaseURL, s.OutputDir)
	}
	want := &StorageState{
		Cookies: []Cookie{
			{Name: "session", Value: "abc123", Domain: "example.com", Path: "/", Expires: -1, HTTPOnly: true, Secure: true, SameSite: "Lax"},
			{Name: "by-url", Value: "x", URL: "https://example.com/app"},
		},
		Origins: []Origin{{Origin: "https://example.com", LocalStorage: []NameValue{{Name: "token", Value: "t0k3n"}}}},
	}
	if !reflect.DeepEqual(s.StorageState, want) {
		t.Errorf("StorageState = %+v, want %+v", s.StorageState, want)
	}

	plain, err := Parse(readSample(t, "valid/default-langs.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if plain.StorageState != nil || plain.OutputDir != "" {
		t.Errorf("storageState and outputDir are optional and stay empty, got %v %q", plain.StorageState, plain.OutputDir)
	}
}

func TestParse_decodesBookends(t *testing.T) { // decision 63
	tests := []struct {
		file      string
		wantIntro *Bookend
		wantOutro *Bookend
	}{
		{"valid/default-langs.yaml", nil, nil},
		{"valid/cards.yaml", &Bookend{Image: "assets/logo.png", DurationMs: 4500}, &Bookend{Off: true}},
		{"valid/cards-text.yaml",
			&Bookend{Title: "Projects tour", Subtitle: "Create and share a project in one minute."},
			&Bookend{Title: "See you next time"}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			s, err := Parse(readSample(t, tt.file))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(s.Intro, tt.wantIntro) || !reflect.DeepEqual(s.Outro, tt.wantOutro) {
				t.Errorf("intro, outro = %+v, %+v; want %+v, %+v", s.Intro, s.Outro, tt.wantIntro, tt.wantOutro)
			}
		})
	}
}

func TestParse_invalidSamples(t *testing.T) {
	tests := []struct {
		file        string
		wantPointer string
		wantMessage string // substring
	}{
		{"invalid/unknown-top-field.yaml", "", "title"},
		{"invalid/unknown-step-field.yaml", "/steps/0", "'url' not allowed"},
		{"invalid/empty-steps.yaml", "/steps", ""},
		{"invalid/scroll-y-and-selector.yaml", "/steps/0/scroll", "'selector' not allowed"},
		{"invalid/wait-zero.yaml", "/steps/1/wait", ""},
		{"invalid/bad-name.yaml", "/name", ""},
		{"invalid/bad-lang-code.yaml", "/languages/0", ""},
		{"invalid/goto-missing-url.yaml", "/steps/0/goto", "want string"},
		{"invalid/unknown-action.yaml", "/steps/0", "'drag' not allowed"},
		{"invalid/two-actions.yaml", "/steps/0", oneAction},
		{"invalid/no-action.yaml", "/steps/0", oneAction},
		{"invalid/press-object-without-key.yaml", "/steps/0/press", "key"},
		{"invalid/fill-missing-value.yaml", "/steps/0/fill", "value"},
		{"invalid/missing-baseurl.yaml", "", "baseUrl"},
		{"invalid/baseurl-relative.yaml", "/baseUrl", "baseUrl must be an absolute http or https URL: /app"},
		{"invalid/cookie-no-domain.yaml", "/storageState/cookies/0", ""},
		{"invalid/storagestate-is-path.yaml", "/storageState", ""},
		{"invalid/intro-true.yaml", "/intro", ""},
		{"invalid/intro-image-and-title.yaml", "/intro", ""},
		{"invalid/outro-image-and-subtitle.yaml", "/outro", ""},
		{"invalid/intro-image-not-png.yaml", "/intro/image", ""},
		{"invalid/intro-duration-short.yaml", "/intro/durationMs", ""},
		{"invalid/intro-unknown-field.yaml", "/intro", "color"},
		{"invalid/screenshot-in-video.yaml", "/steps/1/screenshot", "screenshot steps need type: screenshots"},
		{"invalid/screenshots-narration.yaml", "/steps/1/narration", "narration is not allowed in a screenshots script"},
		{"invalid/screenshots-intro.yaml", "/intro", "intro is not allowed in a screenshots script"},
		{"invalid/screenshots-outro.yaml", "/outro", "outro is not allowed in a screenshots script"},
		{"invalid/screenshots-languages.yaml", "/languages", "languages is not allowed in a screenshots script"},
		{"invalid/screenshots-voices.yaml", "/voices", "voices is not allowed in a screenshots script"},
		{"invalid/screenshots-no-shot.yaml", "/steps", "a screenshots script needs at least one screenshot step"},
		{"invalid/screenshot-selector-and-fullpage.yaml", "/steps/0/screenshot", ""},
		{"invalid/screenshot-clip-and-fullpage.yaml", "/steps/0/screenshot", ""},
		{"invalid/screenshot-false.yaml", "/steps/0/screenshot", ""},
		{"invalid/screenshot-annotate-no-marker.yaml", "/steps/0/screenshot/annotate", "missing property 'box'"},
		{"invalid/screenshot-annotate-no-selector.yaml", "/steps/0/screenshot/annotate", "selector"},
		{"invalid/fullpage-on-click.yaml", "/steps/0/click", ""},
		{"invalid/screenshot-type-unknown.yaml", "/type", ""},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			errs := parseErrors(t, readSample(t, tt.file))
			if len(errs) == 0 {
				t.Fatal("Parse() accepted an invalid script")
			}
			for _, e := range errs {
				if e.Pointer == tt.wantPointer && strings.Contains(e.Message, tt.wantMessage) {
					return
				}
			}
			t.Errorf("no error at %q containing %q; got:\n%v", tt.wantPointer, tt.wantMessage, errs)
		})
	}
}

// A broken step gets one error: an unknown key is not also reported as a
// wrong number of actions, and a bad value only at its action.
func TestParse_oneRootCausePerBrokenStep(t *testing.T) {
	tests := []struct {
		file        string
		wantPointer string
	}{
		{"invalid/scroll-y-and-selector.yaml", "/steps/0/scroll"},
		{"invalid/wait-zero.yaml", "/steps/1/wait"},
		{"invalid/unknown-action.yaml", "/steps/0"},
		{"invalid/unknown-step-field.yaml", "/steps/0"},
		{"invalid/two-actions.yaml", "/steps/0"},
		{"invalid/baseurl-relative.yaml", "/baseUrl"},
		{"invalid/intro-true.yaml", "/intro"},
		{"invalid/intro-image-and-title.yaml", "/intro"},
		{"invalid/intro-image-not-png.yaml", "/intro/image"},
		{"invalid/intro-duration-short.yaml", "/intro/durationMs"},
		{"invalid/screenshot-selector-and-fullpage.yaml", "/steps/0/screenshot"},
		{"invalid/screenshot-clip-and-fullpage.yaml", "/steps/0/screenshot"},
		{"invalid/screenshot-false.yaml", "/steps/0/screenshot"},
		{"invalid/screenshot-annotate-no-selector.yaml", "/steps/0/screenshot/annotate"},
		{"invalid/fullpage-on-click.yaml", "/steps/0/click"},
		{"invalid/screenshots-no-shot.yaml", "/steps"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			errs := parseErrors(t, readSample(t, tt.file))
			if len(errs) != 1 || errs[0].Pointer != tt.wantPointer {
				t.Errorf("errors = %v, want exactly one at %s", errs, tt.wantPointer)
			}
		})
	}
}

func TestParse_reportsAllErrorsAtOnce(t *testing.T) {
	errs := parseErrors(t, []byte("name: BAD\nlanguages: [english]\nsteps: []\n"))
	pointers := map[string]bool{}
	for _, e := range errs {
		pointers[e.Pointer] = true
	}
	for _, p := range []string{"/name", "/languages/0", "/steps"} {
		if !pointers[p] {
			t.Errorf("missing error for %s; got:\n%v", p, errs)
		}
	}
}

// A step without an action gets one plain message, not one per action.
func TestParse_missingActionIsOneError(t *testing.T) {
	errs := parseErrors(t, readSample(t, "invalid/no-action.yaml"))
	want := failure.ValidationErrors{{Pointer: "/steps/0", Message: oneAction}}
	if !reflect.DeepEqual(errs, want) {
		t.Errorf("errors = %v, want %v", errs, want)
	}
}

// A script in the flat `action:` form gets one hint, at its first old step,
// ahead of the schema errors (decision 70).
func TestParse_oldStepFormGetsOneHint(t *testing.T) {
	// Inline, not a testdata sample: no repo YAML keeps the old form.
	old := "name: demo\nbaseUrl: http://x\nsteps:\n" +
		"  - goto: /\n" +
		"  - action: click\n    selector: \"#a\"\n" +
		"  - action: wait\n    ms: 500\n"
	errs := parseErrors(t, []byte(old))
	if len(errs) < 2 {
		t.Fatalf("errors = %v, want the hint and the schema errors", errs)
	}
	if want := (failure.ValidationError{Pointer: "/steps/1", Message: oldFormHint}); errs[0] != want {
		t.Errorf("first error = %v, want %v", errs[0], want)
	}
	for _, e := range errs[1:] {
		if e.Message == oldFormHint {
			t.Errorf("hint repeated: %v", errs)
		}
	}
}

func TestParse_eachActionShape(t *testing.T) {
	tests := []struct {
		step    string
		want    Action // nil: invalid
		pointer string // of the error when invalid
	}{
		{`goto: /p`, Goto{URL: "/p"}, ""},
		{`goto: ""`, nil, "/steps/0/goto"},
		{`click: "#a"`, Click{Selector: "#a"}, ""},
		{`click: {selector: "#a"}`, nil, "/steps/0/click"},
		{`hover: "#a"`, Hover{Selector: "#a"}, ""},
		{`hover: ""`, nil, "/steps/0/hover"},
		{`fill: {selector: "#a", value: ""}`, Fill{Selector: "#a"}, ""},
		{`fill: "#a"`, nil, "/steps/0/fill"},
		{`select: {selector: "#s", value: pl}`, Select{Selector: "#s", Value: "pl"}, ""},
		{`select: {selector: "#s"}`, nil, "/steps/0/select"},
		{`press: Enter`, Press{Key: "Enter"}, ""},
		{`press: {key: Enter, selector: "#q"}`, Press{Key: "Enter", Selector: "#q"}, ""},
		{`press: ""`, nil, "/steps/0/press"},
		{`scroll: ".footer"`, ScrollInto{Selector: ".footer"}, ""},
		{`scroll: {y: 400}`, ScrollTo{Y: 400}, ""},
		{`scroll: {y: 1.5}`, nil, "/steps/0/scroll/y"},
		{`wait: 500`, Pause{D: 500 * time.Millisecond}, ""},
		{`wait: ".toast"`, WaitFor{Selector: ".toast"}, ""},
		{`wait: 30001`, nil, "/steps/0/wait"},
	}
	for _, tt := range tests {
		t.Run(tt.step, func(t *testing.T) {
			s, err := Parse([]byte("name: demo\nbaseUrl: http://x\nsteps:\n  - " + tt.step + "\n"))
			if tt.want != nil {
				if err != nil {
					t.Fatalf("Parse() error = %v", err)
				}
				if got := s.Steps[0].Action; got != tt.want {
					t.Errorf("action = %#v, want %#v", got, tt.want)
				}
				return
			}
			var ve failure.ValidationErrors
			if !errors.As(err, &ve) || len(ve) != 1 || ve[0].Pointer != tt.pointer {
				t.Errorf("Parse() error = %v, want one error at %s", err, tt.pointer)
			}
		})
	}
}

// MarshalJSON writes the keyed form back, so a step survives a round trip.
func TestStep_jsonRoundTrip(t *testing.T) {
	s, err := Parse(readSample(t, "valid/all-actions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s.Steps[0].Narration = map[string]string{"en": "Hi."}
	data, err := json.Marshal(s.Steps)
	if err != nil {
		t.Fatal(err)
	}
	var back []Step
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, s.Steps) {
		t.Errorf("round trip =\n%+v\nwant\n%+v\n(json %s)", back, s.Steps, data)
	}
}

func TestAction_nameAndTarget(t *testing.T) { // failures and CLI progress
	tests := []struct {
		a            Action
		name, target string
	}{
		{Goto{URL: "/p"}, "goto", "/p"},
		{Press{Key: "Enter"}, "press", ""},
		{Press{Key: "Enter", Selector: "#q"}, "press", "#q"},
		{ScrollTo{Y: 3}, "scroll", ""},
		{ScrollInto{Selector: "#f"}, "scroll", "#f"},
		{Pause{D: time.Second}, "wait", ""},
		{WaitFor{Selector: "#t"}, "wait", "#t"},
		{Screenshot{}, "screenshot", ""},
		{Screenshot{Selector: "#e"}, "screenshot", "#e"},
		{Screenshot{FullPage: true}, "screenshot", ""},
		{Screenshot{Annotate: &Annotate{Selector: "#b", Box: true}}, "screenshot", "#b"},
		{Screenshot{Selector: "#e", Annotate: &Annotate{Selector: "#b", Box: true}}, "screenshot", "#e"},
	}
	for _, tt := range tests {
		if tt.a.Name() != tt.name || tt.a.Target() != tt.target {
			t.Errorf("%#v: Name, Target = %q, %q; want %q, %q", tt.a, tt.a.Name(), tt.a.Target(), tt.name, tt.target)
		}
	}
}

func TestAbsoluteHTTP(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"http://host.docker.internal:3000", true},
		{"https://example.com/app", true},
		{"ftp://x", false},
		{"/path", false},
		{"http://", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := AbsoluteHTTP(tt.in); got != tt.want {
				t.Errorf("AbsoluteHTTP(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParse_notYAMLOrNotAMapping(t *testing.T) {
	tests := []struct{ name, in string }{
		{"syntax error", "name: [unclosed\n"},
		{"empty document", ""},
		{"scalar", "just text\n"},
		{"list", "- a\n- b\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if errs := parseErrors(t, []byte(tt.in)); len(errs) == 0 {
				t.Error("Parse() = nil error, want validation errors")
			}
		})
	}
}

func TestParse_errorsAreDeterministic(t *testing.T) {
	in := []byte("name: BAD\nlanguages: [english]\nsteps: []\nextra: 1\n")
	first := parseErrors(t, in).Error()
	for range 5 {
		if got := parseErrors(t, in).Error(); got != first {
			t.Fatalf("error order changed:\n%s\n---\n%s", first, got)
		}
	}
}

func TestValidate_narrationPerSelectedLanguage(t *testing.T) {
	parse := func(t *testing.T, name string) Script {
		t.Helper()
		s, err := Parse(readSample(t, name))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	tests := []struct {
		name  string
		file  string
		langs []string
		want  []failure.ValidationError
	}{
		{
			name:  "missing pl points at the step's narration (FR-002 AC1)",
			file:  "invalid/missing-narration-pl.yaml",
			langs: []string{"en", "pl"},
			want:  []failure.ValidationError{{Pointer: "/steps/0/narration", Message: "missing narration for language pl"}},
		},
		{
			name:  "only en narration is valid with default languages (FR-002 AC2)",
			file:  "invalid/missing-narration-pl.yaml",
			langs: []string{"en"},
		},
		{
			name:  "text for unselected languages is ignored",
			file:  "valid/en-pl.yaml",
			langs: []string{"en"},
		},
		{
			name:  "empty narration is an entry: the step stays silent",
			file:  "valid/silent-narration.yaml",
			langs: []string{"en", "pl"},
		},
		{
			name:  "un-narrated steps need nothing",
			file:  "valid/all-actions.yaml",
			langs: []string{"en", "pl"},
		},
		{
			name:  "render-time override can select a language the script never narrated",
			file:  "valid/default-langs.yaml",
			langs: []string{"en", "pl"},
			want:  []failure.ValidationError{{Pointer: "/steps/0/narration", Message: "missing narration for language pl"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Validate(parse(t, tt.file), tt.langs)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual([]failure.ValidationError(got), tt.want) {
				t.Errorf("Validate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLanguages_overrideBeatsScriptBeatsDefault(t *testing.T) {
	tests := []struct {
		name             string
		override, script []string
		want             []string
	}{
		{"override wins (BR-002)", []string{"pl"}, []string{"en", "de"}, []string{"pl"}},
		{"script when no override", nil, []string{"en", "pl"}, []string{"en", "pl"}},
		{"default is en", nil, nil, []string{"en"}},
		{"empty slices count as absent", []string{}, []string{}, []string{"en"}},
		{"order is kept", []string{"pl", "en"}, nil, []string{"pl", "en"}},
		{"duplicates are dropped", []string{"en", "pl", "en"}, nil, []string{"en", "pl"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Languages(tt.override, tt.script); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Languages() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLanguages_doesNotAliasInput(t *testing.T) {
	in := []string{"en", "pl"}
	out := Languages(in, nil)
	out[0] = "xx"
	if in[0] != "en" {
		t.Error("Languages returned the caller's slice")
	}
}

// Guards drift: the example shown in the render_video tool description must
// pass the schema it is shown with.
func TestExampleYAML_isValid(t *testing.T) {
	s, err := Parse(ExampleYAML())
	if err != nil {
		t.Fatalf("example script does not validate: %v", err)
	}
	if errs := Validate(s, Languages(nil, s.Languages)); len(errs) != 0 {
		t.Errorf("example script fails cross-field rules: %v", errs)
	}
}

func TestSchemaJSON_isJSONAndCompiles(t *testing.T) {
	if !json.Valid(SchemaJSON()) {
		t.Fatal("SchemaJSON() is not valid JSON")
	}
	if _, err := compiledSchema(); err != nil {
		t.Fatalf("schema does not compile: %v", err)
	}
}

func TestParseSteps_appliesTheScriptRules(t *testing.T) {
	tests := []struct {
		name    string
		steps   []string
		wantPtr string // empty: valid
	}{
		{"no steps", nil, ""},
		{"click with selector", []string{`{"click": "#a"}`}, ""},
		{"scroll to zero", []string{`{"scroll": {"y": 0}}`}, ""},
		{"click without selector", []string{`{"goto": "/"}`, `{"click": ""}`}, "/steps/1/click"},
		{"unknown action", []string{`{"drag": "#a"}`}, "/steps/0"},
		{"old form", []string{`{"action": "click", "selector": "#a"}`}, "/steps/0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw []json.RawMessage
			for _, s := range tt.steps {
				raw = append(raw, json.RawMessage(s))
			}
			steps, err := ParseSteps(raw)
			if tt.wantPtr == "" {
				if err != nil || len(steps) != len(raw) {
					t.Fatalf("ParseSteps() = %v, %v, want %d steps", steps, err, len(raw))
				}
				return
			}
			var ve failure.ValidationErrors
			if !errors.As(err, &ve) {
				t.Fatalf("ParseSteps() = %v, want ValidationErrors", err)
			}
			if ve[0].Pointer != tt.wantPtr {
				t.Errorf("first pointer = %q, want %q", ve[0].Pointer, tt.wantPtr)
			}
		})
	}
}

// An empty fill value is a real value: it clears the field. It reaches the
// executor unchanged, with no step re-encoding in between.
func TestParseSteps_keepsEmptyFillValue(t *testing.T) {
	steps, err := ParseSteps([]json.RawMessage{json.RawMessage(`{"fill": {"selector": "#n", "value": ""}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Fill{Selector: "#n"}); steps[0].Action != want {
		t.Errorf("action = %#v, want %#v", steps[0].Action, want)
	}
}

func TestAudiences_matchSchemaEnum(t *testing.T) {
	var schema struct {
		Properties struct {
			Meta struct {
				Properties struct {
					Audience struct {
						Enum []string `json:"enum"`
					} `json:"audience"`
				} `json:"properties"`
			} `json:"meta"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(SchemaJSON(), &schema); err != nil {
		t.Fatal(err)
	}
	if got := schema.Properties.Meta.Properties.Audience.Enum; !reflect.DeepEqual(got, Audiences) {
		t.Errorf("schema audience enum = %v, Audiences = %v", got, Audiences)
	}
}

func TestParse_decodesScreenshotShapes(t *testing.T) {
	s, err := Parse(readSample(t, "valid/screenshots-areas.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Action{
		Goto{URL: "/projects"},
		Screenshot{},
		Screenshot{},
		Screenshot{FullPage: true},
		Screenshot{Selector: "#list"},
		Screenshot{Clip: &Clip{X: 10, Y: 20, Width: 300, Height: 200}},
	}
	var got []Action
	for _, st := range s.Steps {
		got = append(got, st.Action)
	}
	if !reflect.DeepEqual(got, want) { // DeepEqual: Clip is a pointer
		t.Errorf("actions =\n%#v\nwant\n%#v", got, want)
	}

	a, err := Parse(readSample(t, "valid/screenshots-annotate.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	wantA := []Action{
		Goto{URL: "/projects"},
		Screenshot{Annotate: &Annotate{Selector: "#create", Box: true}},
		Screenshot{Selector: "#list", Annotate: &Annotate{Selector: "#create", Box: true, Arrow: true, Dim: true, Label: "Click Create"}},
		Screenshot{FullPage: true, Annotate: &Annotate{Selector: "#create", Label: "Here"}},
	}
	got = nil
	for _, st := range a.Steps {
		got = append(got, st.Action)
	}
	if !reflect.DeepEqual(got, wantA) {
		t.Errorf("annotate actions =\n%#v\nwant\n%#v", got, wantA)
	}
}

func TestScript_kindDefaultsToVideo(t *testing.T) {
	tests := []struct {
		file, want string
	}{
		{"valid/default-langs.yaml", TypeVideo},
		{"valid/screenshots-areas.yaml", TypeScreenshots},
	}
	for _, tt := range tests {
		s, err := Parse(readSample(t, tt.file))
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Kind(); got != tt.want {
			t.Errorf("%s: Kind() = %q, want %q", tt.file, got, tt.want)
		}
	}
	if got := (Script{Type: TypeVideo}).Kind(); got != TypeVideo {
		t.Errorf("explicit video: Kind() = %q", got)
	}
}

// The type rules the schema cannot express (decision 72). Each case is one
// script, so a rule that stops firing shows by its pointer.
func TestCheckType_rules(t *testing.T) {
	shot := Step{Action: Screenshot{}}
	click := Step{Action: Click{Selector: "#a"}}
	narrated := Step{Action: Screenshot{}, Narration: map[string]string{"en": "x"}}
	silent := Step{Action: Screenshot{}, Narration: map[string]string{}}
	tests := []struct {
		name string
		s    Script
		want failure.ValidationErrors
	}{
		{"video without shots", Script{Steps: []Step{click}}, nil},
		{"video with a shot", Script{Steps: []Step{click, shot}},
			failure.ValidationErrors{{Pointer: "/steps/1/screenshot", Message: "screenshot steps need type: screenshots"}}},
		{"screenshots, plain", Script{Type: TypeScreenshots, Steps: []Step{click, shot}}, nil},
		{"screenshots, no shot", Script{Type: TypeScreenshots, Steps: []Step{click}},
			failure.ValidationErrors{{Pointer: "/steps", Message: "a screenshots script needs at least one screenshot step"}}},
		{"screenshots, narrated step", Script{Type: TypeScreenshots, Steps: []Step{narrated}},
			failure.ValidationErrors{{Pointer: "/steps/0/narration", Message: "narration is not allowed in a screenshots script"}}},
		{"screenshots, empty narration object", Script{Type: TypeScreenshots, Steps: []Step{silent}},
			failure.ValidationErrors{{Pointer: "/steps/0/narration", Message: "narration is not allowed in a screenshots script"}}},
		{"screenshots, every video field", Script{
			Type: TypeScreenshots, Steps: []Step{shot},
			Languages: []string{"en"}, Voices: map[string]string{"en": "v"}, Intro: &Bookend{Off: true}, Outro: &Bookend{},
		}, failure.ValidationErrors{
			{Pointer: "/languages", Message: "languages is not allowed in a screenshots script"},
			{Pointer: "/voices", Message: "voices is not allowed in a screenshots script"},
			{Pointer: "/intro", Message: "intro is not allowed in a screenshots script"},
			{Pointer: "/outro", Message: "outro is not allowed in a screenshots script"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := checkType(tt.s); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("checkType() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStep_screenshotJSONRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		step     Step
		wantJSON string
	}{
		{"viewport", Step{Action: Screenshot{}}, `{"screenshot":true}`},
		{"full page", Step{Action: Screenshot{FullPage: true}}, `{"screenshot":{"fullPage":true}}`},
		{"element", Step{Action: Screenshot{Selector: "#e"}}, `{"screenshot":{"selector":"#e"}}`},
		{"clip", Step{Action: Screenshot{Clip: &Clip{X: 1, Y: 2, Width: 3, Height: 4}}},
			`{"screenshot":{"clip":{"x":1,"y":2,"width":3,"height":4}}}`},
		{"annotated", Step{Action: Screenshot{Annotate: &Annotate{Selector: "#b", Box: true, Label: "L"}}},
			`{"screenshot":{"annotate":{"selector":"#b","box":true,"label":"L"}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.step)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tt.wantJSON {
				t.Errorf("json = %s, want %s", data, tt.wantJSON)
			}
			var back Step
			if err := json.Unmarshal(data, &back); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(back, tt.step) {
				t.Errorf("round trip = %+v, want %+v", back, tt.step)
			}
		})
	}
}

// explore_page has no files to write, so a screenshot step in its actions is
// refused by Parse, before the executor's "unknown action".
func TestParseSteps_rejectsScreenshot(t *testing.T) {
	_, err := ParseSteps([]json.RawMessage{json.RawMessage(`{"goto": "/"}`), json.RawMessage(`{"screenshot": true}`)})
	var ve failure.ValidationErrors
	if !errors.As(err, &ve) || len(ve) != 1 || ve[0].Pointer != "/steps/1/screenshot" {
		t.Errorf("ParseSteps() error = %v, want one error at /steps/1/screenshot", err)
	}
}

func TestExampleScreenshotsYAML_isValid(t *testing.T) {
	s, err := Parse(ExampleScreenshotsYAML())
	if err != nil {
		t.Fatalf("example screenshots script does not validate: %v", err)
	}
	if s.Kind() != TypeScreenshots {
		t.Errorf("Kind() = %q, want %q", s.Kind(), TypeScreenshots)
	}
}
