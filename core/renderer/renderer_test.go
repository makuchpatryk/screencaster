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
	enOnly = `name: demo
baseUrl: http://host.docker.internal:3000
steps:
  - action: goto
    url: /projects
    narration:
      en: Hello.
`
	enPl = `name: demo
baseUrl: http://host.docker.internal:3000
languages: [en, pl]
steps:
  - action: goto
    url: /projects
    narration:
      en: Hello.
      pl: Cześć.
`
	enPlMissingPl = `name: demo
baseUrl: http://host.docker.internal:3000
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

// workDir builds a project: a storageState file and demos/demo.yaml (when
// script != "").
func workDir(t *testing.T, script string) string {
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
			dir := workDir(t, tt.script)
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
			if plan.Script.Name != "demo" || plan.Script.BaseURL != "http://host.docker.internal:3000" {
				t.Errorf("plan = %+v", plan)
			}
		})
	}
}

func TestPrepare_usesBuiltInVoicePerLanguage(t *testing.T) {
	dir := workDir(t, enPl)
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
	dir := workDir(t, enOnly)
	other := t.TempDir()
	plan, err := Prepare(Request{WorkDir: other, ScriptPath: filepath.Join(dir, "demos/demo.yaml")}, stock)
	// The other work dir has no demos/, so success proves the script path was not joined onto it.
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if want := filepath.Join(other, "output"); plan.OutputDir != want {
		t.Errorf("OutputDir = %q, want %q (paths resolve against WorkDir)", plan.OutputDir, want)
	}
}

func TestPrepare_failsBeforeAnyWork(t *testing.T) {
	tests := []struct {
		name     string
		script   string
		override []string
		inst     voices.Installed
		want     func(dir string) string
	}{
		{
			name:   "baseUrl required (FR-001 AC1)",
			script: "name: demo\nsteps:\n  - action: goto\n    url: /\n",
			inst:   stock,
			want:   func(string) string { return "missing property 'baseUrl'" },
		},
		{
			name:   "baseUrl must be absolute",
			script: "name: demo\nbaseUrl: /app\nsteps:\n  - action: goto\n    url: /\n",
			inst:   stock,
			want:   func(string) string { return "/baseUrl: baseUrl must be an absolute http or https URL: /app" },
		},
		{
			name: "script file missing",
			want: func(dir string) string { return "script not found: " + filepath.Join(dir, "demos/demo.yaml") },
		},
		{
			name:   "narration missing for a selected language points at the step (FR-002 AC1)",
			script: enPlMissingPl,
			inst:   stock,
			want:   func(string) string { return "/steps/0/narration: missing narration for language pl" },
		},
		{
			name:     "override can select a language the script has no narration for",
			script:   enOnly,
			override: []string{"en", "pl"},
			inst:     stock,
			want:     func(string) string { return "/steps/0/narration: missing narration for language pl" },
		},
		{
			name:   "voice not installed (BR-011)",
			script: enOnly,
			inst:   voices.Installed{},
			want:   func(string) string { return "voice not installed: en_US-ryan-high" },
		},
		{
			name:   "narration and voice problems are reported together",
			script: enPlMissingPl,
			inst:   voices.Installed{"en_US-ryan-high": "/v/en.onnx"},
			want: func(string) string {
				return "/steps/0/narration: missing narration for language pl\nvoice not installed: pl_PL-darkman-medium"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := workDir(t, tt.script)
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

// withTarget returns enOnly with extra top-level script fields.
func withTarget(extra string) string {
	return strings.Replace(enOnly, "steps:", extra+"steps:", 1)
}

func TestPrepare_storageStateOptional(t *testing.T) {
	dir := workDir(t, enOnly)
	plan, err := Prepare(Request{WorkDir: dir, ScriptPath: "demos/demo.yaml"}, stock)
	if err != nil {
		t.Fatalf("Prepare() error = %v, want none: a public site needs no storageState", err)
	}
	if plan.StorageState != "" {
		t.Errorf("StorageState = %q, want empty", plan.StorageState)
	}
}

func TestPrepare_storageStateResolvesAgainstWorkDir(t *testing.T) {
	dir := workDir(t, withTarget("storageState: auth/storageState.json\n"))
	plan, err := Prepare(Request{WorkDir: dir, ScriptPath: "demos/demo.yaml"}, stock)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "auth/storageState.json"); plan.StorageState != want {
		t.Errorf("StorageState = %q, want %q", plan.StorageState, want)
	}
}

func TestPrepare_storageStateMissingIsValidationError(t *testing.T) {
	dir := workDir(t, withTarget("storageState: auth/nope.json\n"))
	// A voice problem too: both must come back in one list (fail early, all at once).
	_, err := Prepare(Request{WorkDir: dir, ScriptPath: "demos/demo.yaml"}, voices.Installed{})
	var ve failure.ValidationErrors
	if !errors.As(err, &ve) {
		t.Fatalf("Prepare() error = %v, want ValidationErrors", err)
	}
	want := failure.ValidationErrors{
		{Message: "voice not installed: en_US-ryan-high"},
		{Pointer: "/storageState", Message: "storageState not found: " + filepath.Join(dir, "auth/nope.json")},
	}
	if !reflect.DeepEqual(ve, want) {
		t.Errorf("errors = %v, want %v", ve, want)
	}
}

func TestPrepare_storageStateMustBeAFile(t *testing.T) {
	dir := workDir(t, withTarget("storageState: auth\n"))
	_, err := Prepare(Request{WorkDir: dir, ScriptPath: "demos/demo.yaml"}, stock)
	if err == nil || !strings.Contains(err.Error(), "/storageState: storageState not found: ") {
		t.Errorf("Prepare() error = %v, want storageState not found for a directory", err)
	}
}

func TestPrepare_outputDirDefaultsToOutput(t *testing.T) {
	dir := workDir(t, enOnly)
	plan, err := Prepare(Request{WorkDir: dir, ScriptPath: "demos/demo.yaml"}, stock)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "output"); plan.OutputDir != want {
		t.Errorf("OutputDir = %q, want %q", plan.OutputDir, want)
	}
}

func TestPrepare_outputDirRelativeToWorkDir(t *testing.T) {
	tests := []struct {
		name, field string
		want        func(dir string) string
	}{
		{"relative", "outputDir: videos/out\n", func(dir string) string { return filepath.Join(dir, "videos/out") }},
		{"dot is the work dir", "outputDir: .\n", func(dir string) string { return dir }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := workDir(t, withTarget(tt.field))
			plan, err := Prepare(Request{WorkDir: dir, ScriptPath: "demos/demo.yaml"}, stock)
			if err != nil {
				t.Fatal(err)
			}
			if want := tt.want(dir); plan.OutputDir != want {
				t.Errorf("OutputDir = %q, want %q", plan.OutputDir, want)
			}
		})
	}
}

func TestPrepare_absolutePathInsideWorkDirIsKept(t *testing.T) {
	dir := workDir(t, "")
	// The temp dir is only known now, so write the script after it.
	body := withTarget("outputDir: " + filepath.Join(dir, "videos") + "\n")
	if err := os.MkdirAll(filepath.Join(dir, "demos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "demos/demo.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := Prepare(Request{WorkDir: dir, ScriptPath: "demos/demo.yaml"}, stock)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "videos"); plan.OutputDir != want {
		t.Errorf("OutputDir = %q, want %q", plan.OutputDir, want)
	}
}

// The script is LLM-written: paths that leave the working directory are
// validation errors, reported with the other problems (FR-001, fail early).
func TestPrepare_pathsOutsideWorkDirAreValidationErrors(t *testing.T) {
	tests := []struct {
		name, field, pointer, value string
	}{
		{"outputDir parent", "outputDir: ../out\n", "/outputDir", "../out"},
		{"outputDir deep parent", "outputDir: videos/../../out\n", "/outputDir", "videos/../../out"},
		{"outputDir absolute", "outputDir: /srv/videos\n", "/outputDir", "/srv/videos"},
		{"storageState parent", "storageState: ../auth/s.json\n", "/storageState", "../auth/s.json"},
		{"storageState absolute", "storageState: /etc/passwd\n", "/storageState", "/etc/passwd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := workDir(t, withTarget(tt.field))
			_, err := Prepare(Request{WorkDir: dir, ScriptPath: "demos/demo.yaml"}, stock)
			var ve failure.ValidationErrors
			if !errors.As(err, &ve) {
				t.Fatalf("Prepare() error = %v, want ValidationErrors", err)
			}
			field := strings.TrimPrefix(tt.pointer, "/")
			want := failure.ValidationErrors{{
				Pointer: tt.pointer,
				Message: field + " must stay inside the working directory: " + tt.value,
			}}
			if !reflect.DeepEqual(ve, want) {
				t.Errorf("errors = %v, want %v", ve, want)
			}
		})
	}
}

// screencaster.yaml is gone (decision 58): a stray one changes nothing.
func TestPrepare_ignoresScreencasterYAML(t *testing.T) {
	dir := workDir(t, enOnly)
	stray := "baseUrl: http://elsewhere:9\nstorageState: auth/other.json\noutputDir: elsewhere\nvoices: {en: nope}\n"
	if err := os.WriteFile(filepath.Join(dir, "screencaster.yaml"), []byte(stray), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := Prepare(Request{WorkDir: dir, ScriptPath: "demos/demo.yaml"}, stock)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Script.BaseURL != "http://host.docker.internal:3000" || plan.StorageState != "" ||
		plan.OutputDir != filepath.Join(dir, "output") || plan.Voices["en"] != "/v/en_US-ryan-high.onnx" {
		t.Errorf("a stray screencaster.yaml changed the plan: %+v", plan)
	}
}

func TestPrepare_schemaErrorsAreValidationErrors(t *testing.T) {
	dir := workDir(t, "name: BAD NAME\nbaseUrl: http://x\nsteps: []\n")
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
