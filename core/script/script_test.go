package script

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"screencaster/core/failure"
)

const samples = "../../testdata/scripts"

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
	scrollTop := s.Steps[9]
	if scrollTop.Action != "scroll" || scrollTop.Y == nil || *scrollTop.Y != 0 {
		t.Errorf("scroll y:0 must decode to a non-nil 0, got %+v", scrollTop)
	}
	if wait := s.Steps[10]; wait.Ms == nil || *wait.Ms != 30000 {
		t.Errorf("wait ms = %v, want 30000", wait.Ms)
	}
	if fill := s.Steps[3]; fill.Action != "fill" || fill.Value != "" {
		t.Errorf("fill with empty value is valid, got %+v", fill)
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
	if s.BaseURL != "https://example.com/app" || s.StorageState != "auth/storageState.json" || s.OutputDir != "videos" {
		t.Errorf("target fields = %q %q %q", s.BaseURL, s.StorageState, s.OutputDir)
	}

	plain, err := Parse(readSample(t, "valid/default-langs.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if plain.StorageState != "" || plain.OutputDir != "" {
		t.Errorf("storageState and outputDir are optional and stay empty, got %q %q", plain.StorageState, plain.OutputDir)
	}
}

func TestParse_invalidSamples(t *testing.T) {
	tests := []struct {
		file        string
		wantPointer string
		wantMessage string // substring
	}{
		{"invalid/unknown-top-field.yaml", "", "title"},
		{"invalid/unknown-step-field.yaml", "/steps/0/url", "not allowed"},
		{"invalid/empty-steps.yaml", "/steps", ""},
		{"invalid/scroll-selector-and-y.yaml", "/steps/0", ""},
		{"invalid/wait-ms-zero.yaml", "/steps/1/ms", ""},
		{"invalid/bad-name.yaml", "/name", ""},
		{"invalid/bad-lang-code.yaml", "/languages/0", ""},
		{"invalid/goto-missing-url.yaml", "/steps/0", "url"},
		{"invalid/unknown-action.yaml", "/steps/0/action", ""},
		{"invalid/missing-baseurl.yaml", "", "baseUrl"},
		{"invalid/baseurl-relative.yaml", "/baseUrl", "baseUrl must be an absolute http or https URL: /app"},
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

// A failing branch must not make unevaluatedProperties blame valid fields of
// the same step.
func TestParse_oneRootCausePerBrokenStep(t *testing.T) {
	tests := []struct {
		file        string
		wantPointer string
	}{
		{"invalid/scroll-selector-and-y.yaml", "/steps/0"},
		{"invalid/wait-ms-zero.yaml", "/steps/1/ms"},
		{"invalid/unknown-action.yaml", "/steps/0/action"},
		{"invalid/baseurl-relative.yaml", "/baseUrl"},
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

// Each action's rules sit behind an if on `action`; without one, no branch
// may fire and flood the output with every action's required fields.
func TestParse_missingActionDoesNotTriggerEveryBranch(t *testing.T) {
	errs := parseErrors(t, []byte("name: demo\nbaseUrl: http://x\nsteps:\n  - narration: {en: hi}\n"))
	want := failure.ValidationErrors{{Pointer: "/steps/0", Message: "missing property 'action'"}}
	if !reflect.DeepEqual(errs, want) {
		t.Errorf("errors = %v, want %v", errs, want)
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

func TestValidateSteps_appliesTheScriptRules(t *testing.T) {
	zero := 0
	tests := []struct {
		name    string
		steps   []Step
		wantPtr string // empty: valid
	}{
		{"no steps", nil, ""},
		{"click with selector", []Step{{Action: "click", Selector: "#a"}}, ""},
		{"scroll to zero", []Step{{Action: "scroll", Y: &zero}}, ""},
		{"click without selector", []Step{{Action: "goto", URL: "/"}, {Action: "click"}}, "/steps/1"},
		{"unknown action", []Step{{Action: "drag"}}, "/steps/0/action"},
		{"scroll without target", []Step{{Action: "scroll"}}, "/steps/0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSteps(tt.steps)
			if tt.wantPtr == "" {
				if err != nil {
					t.Fatalf("ValidateSteps() = %v, want nil", err)
				}
				return
			}
			var ve failure.ValidationErrors
			if !errors.As(err, &ve) {
				t.Fatalf("ValidateSteps() = %v, want ValidationErrors", err)
			}
			if !strings.HasPrefix(ve[0].Pointer, tt.wantPtr) {
				t.Errorf("first pointer = %q, want prefix %q", ve[0].Pointer, tt.wantPtr)
			}
		})
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
