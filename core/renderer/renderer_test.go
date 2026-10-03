package renderer

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"screencaster/core/failure"
	"screencaster/core/voices"
)

const (
	configYAML = "baseUrl: http://host.docker.internal:3000\nstorageState: auth/storageState.json\n"

	enOnly = `name: demo
steps:
  - action: goto
    url: /projects
    narration:
      en: Hello.
`
	enPl = `name: demo
languages: [en, pl]
steps:
  - action: goto
    url: /projects
    narration:
      en: Hello.
      pl: Cześć.
`
	enPlMissingPl = `name: demo
languages: [en, pl]
steps:
  - action: goto
    url: /projects
    narration:
      en: Hello.
`
)

var stock = voices.Installed{
	"en_US-ryan-high":      "/v/en_US-ryan-high.onnx",
	"pl_PL-darkman-medium": "/v/pl_PL-darkman-medium.onnx",
}

// workDir builds a project: config (when cfg != ""), storageState and
// demos/demo.yaml (when script != "").
func workDir(t *testing.T, cfg, script string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("auth/storageState.json", "{}")
	if cfg != "" {
		write("screencaster.yaml", cfg)
	}
	if script != "" {
		write("demos/demo.yaml", script)
	}
	return dir
}

func TestPrepare_validPlan(t *testing.T) {
	tests := []struct {
		name      string
		script    string
		override  []string
		wantLangs []string
	}{
		{"default languages are en only (BR-002)", enOnly, nil, []string{"en"}},
		{"script languages", enPl, nil, []string{"en", "pl"}},
		{"override beats script (BR-002)", enPl, []string{"pl"}, []string{"pl"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := workDir(t, configYAML, tt.script)
			plan, err := Prepare(Request{WorkDir: dir, ScriptPath: "demos/demo.yaml", LangOverride: tt.override}, stock)
			if err != nil {
				t.Fatalf("Prepare() error = %v", err)
			}
			if !reflect.DeepEqual(plan.Languages, tt.wantLangs) {
				t.Errorf("Languages = %v, want %v", plan.Languages, tt.wantLangs)
			}
			for _, lang := range tt.wantLangs {
				if plan.Voices[lang] == "" {
					t.Errorf("no voice resolved for %s: %v", lang, plan.Voices)
				}
			}
			if len(plan.Voices) != len(tt.wantLangs) {
				t.Errorf("Voices = %v, want only selected languages", plan.Voices)
			}
			if plan.Script.Name != "demo" || plan.Cfg.BaseURL != "http://host.docker.internal:3000" {
				t.Errorf("plan = %+v", plan)
			}
		})
	}
}

func TestPrepare_usesBuiltInVoicePerLanguage(t *testing.T) {
	dir := workDir(t, configYAML, enPl)
	plan, err := Prepare(Request{WorkDir: dir, ScriptPath: "demos/demo.yaml"}, stock) // FR-001 AC2
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"en": "/v/en_US-ryan-high.onnx", "pl": "/v/pl_PL-darkman-medium.onnx"}
	if !reflect.DeepEqual(plan.Voices, want) {
		t.Errorf("Voices = %v, want %v", plan.Voices, want)
	}
}

func TestPrepare_absoluteScriptPath(t *testing.T) {
	dir := workDir(t, configYAML, enOnly)
	_, err := Prepare(Request{WorkDir: t.TempDir(), ScriptPath: filepath.Join(dir, "demos/demo.yaml")}, stock)
	// The work dir has no config, so this proves the script path was not joined onto it.
	if err == nil || !strings.HasPrefix(err.Error(), "config not found:") {
		t.Errorf("Prepare() error = %v, want config not found", err)
	}
}

func TestPrepare_failsBeforeAnyWork(t *testing.T) {
	tests := []struct {
		name     string
		cfg      string
		script   string
		override []string
		inst     voices.Installed
		want     func(dir string) string
	}{
		{
			name: "config missing (UF-002)",
			want: func(dir string) string { return "config not found: " + filepath.Join(dir, "screencaster.yaml") },
		},
		{
			name:   "baseUrl required (FR-001 AC1)",
			cfg:    "storageState: auth/storageState.json\n",
			script: enOnly,
			want:   func(string) string { return "baseUrl is required" },
		},
		{
			name: "script file missing",
			cfg:  configYAML,
			want: func(dir string) string { return "script not found: " + filepath.Join(dir, "demos/demo.yaml") },
		},
		{
			name:   "narration missing for a selected language points at the step (FR-002 AC1)",
			cfg:    configYAML,
			script: enPlMissingPl,
			inst:   stock,
			want:   func(string) string { return "/steps/0/narration: missing narration for language pl" },
		},
		{
			name:     "override can select a language the script has no narration for",
			cfg:      configYAML,
			script:   enOnly,
			override: []string{"en", "pl"},
			inst:     stock,
			want:     func(string) string { return "/steps/0/narration: missing narration for language pl" },
		},
		{
			name:   "voice not installed (BR-011)",
			cfg:    configYAML,
			script: enOnly,
			inst:   voices.Installed{},
			want:   func(string) string { return "voice not installed: en_US-ryan-high" },
		},
		{
			name:   "narration and voice problems are reported together",
			cfg:    configYAML,
			script: enPlMissingPl,
			inst:   voices.Installed{"en_US-ryan-high": "/v/en.onnx"},
			want: func(string) string {
				return "/steps/0/narration: missing narration for language pl\nvoice not installed: pl_PL-darkman-medium"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := workDir(t, tt.cfg, tt.script)
			_, err := Prepare(Request{WorkDir: dir, ScriptPath: "demos/demo.yaml", LangOverride: tt.override}, tt.inst)
			if err == nil {
				t.Fatal("Prepare() error = nil")
			}
			if got, want := err.Error(), tt.want(dir); got != want {
				t.Errorf("Prepare() error = %q, want %q", got, want)
			}
		})
	}
}

func TestPrepare_schemaErrorsAreValidationErrors(t *testing.T) {
	dir := workDir(t, configYAML, "name: BAD NAME\nsteps: []\n")
	_, err := Prepare(Request{WorkDir: dir, ScriptPath: "demos/demo.yaml"}, stock)
	var ve failure.ValidationErrors
	if !errors.As(err, &ve) || len(ve) != 2 {
		t.Fatalf("Prepare() error = %v, want 2 validation errors", err)
	}
}

func TestOutputName(t *testing.T) {
	tests := []struct {
		name string
		ts   time.Time
		want string
	}{
		{"FR-010 format", time.Date(2026, 10, 3, 10, 15, 0, 0, time.UTC), "create-project.en.20261003T101500Z.mp4"},
		{
			"timestamp is always UTC",
			time.Date(2026, 10, 3, 12, 15, 0, 0, time.FixedZone("CEST", 2*60*60)),
			"create-project.en.20261003T101500Z.mp4",
		},
		{"sub-second part is dropped", time.Date(2026, 1, 2, 3, 4, 5, 999_000_000, time.UTC), "create-project.en.20260102T030405Z.mp4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OutputName("create-project", "en", tt.ts); got != tt.want {
				t.Errorf("OutputName() = %q, want %q", got, tt.want)
			}
		})
	}
}
