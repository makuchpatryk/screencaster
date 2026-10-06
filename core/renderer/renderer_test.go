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

// defaults stand in for a provider's built-in voices; the renderer only sees names.
var defaults = map[string]string{"en": "en_US-ryan-high", "pl": "pl_PL-darkman-medium"}

var stock = voices.Catalog{
	Voices: []voices.Voice{
		{Name: "en_US-ryan-high", Lang: "en"},
		{Name: "pl_PL-darkman-medium", Lang: "pl"},
	},
	Defaults: defaults,
}

// workDir builds a project holding demos/demo.yaml (when script != "").
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
	want := map[string]string{"en": "en_US-ryan-high", "pl": "pl_PL-darkman-medium"}
	if !reflect.DeepEqual(plan.Voices, want) {
		t.Errorf("Voices = %v, want %v", plan.Voices, want)
	}
}

func TestPrepare_absoluteScriptPath(t *testing.T) {
	dir := workDir(t, enOnly)
	plan, err := Prepare(Request{WorkDir: dir, ScriptPath: filepath.Join(dir, "demos/demo.yaml")}, stock)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if want := filepath.Join(dir, "demos", "output"); plan.OutputDir != want {
		t.Errorf("OutputDir = %q, want %q (paths resolve against the demo's folder)", plan.OutputDir, want)
	}
}

// A script outside the working directory is allowed for the CLI, but its
// default output folder would be outside too (decision 62).
func TestPrepare_scriptOutsideWorkDirHasNoOutputInsideIt(t *testing.T) {
	dir := workDir(t, enOnly)
	other := t.TempDir()
	_, err := Prepare(Request{WorkDir: other, ScriptPath: filepath.Join(dir, "demos/demo.yaml")}, stock)
	var ve failure.ValidationErrors
	if !errors.As(err, &ve) || len(ve) != 1 || ve[0].Pointer != "/outputDir" ||
		ve[0].Message != "outputDir must stay inside the working directory: "+filepath.Join(dir, "demos", "output") {
		t.Fatalf("Prepare() error = %v, want the outputDir error naming the default folder", err)
	}
}

func TestPrepare_failsBeforeAnyWork(t *testing.T) {
	tests := []struct {
		name     string
		script   string
		override []string
		cat      voices.Catalog
		want     func(dir string) string
	}{
		{
			name:   "baseUrl required (FR-001 AC1)",
			script: "name: demo\nsteps:\n  - action: goto\n    url: /\n",
			cat:    stock,
			want:   func(string) string { return "missing property 'baseUrl'" },
		},
		{
			name:   "baseUrl must be absolute",
			script: "name: demo\nbaseUrl: /app\nsteps:\n  - action: goto\n    url: /\n",
			cat:    stock,
			want:   func(string) string { return "/baseUrl: baseUrl must be an absolute http or https URL: /app" },
		},
		{
			name: "script file missing",
			want: func(dir string) string { return "script not found: " + filepath.Join(dir, "demos/demo.yaml") },
		},
		{
			name:   "narration missing for a selected language points at the step (FR-002 AC1)",
			script: enPlMissingPl,
			cat:    stock,
			want:   func(string) string { return "/steps/0/narration: missing narration for language pl" },
		},
		{
			name:     "override can select a language the script has no narration for",
			script:   enOnly,
			override: []string{"en", "pl"},
			cat:      stock,
			want:     func(string) string { return "/steps/0/narration: missing narration for language pl" },
		},
		{
			name:   "voice not installed (BR-011)",
			script: enOnly,
			cat:    voices.Catalog{Defaults: defaults},
			want:   func(string) string { return "voice not installed: en_US-ryan-high" },
		},
		{
			name:   "narration and voice problems are reported together",
			script: enPlMissingPl,
			cat:    voices.Catalog{Voices: []voices.Voice{{Name: "en_US-ryan-high", Lang: "en"}}, Defaults: defaults},
			want: func(string) string {
				return "/steps/0/narration: missing narration for language pl\nvoice not installed: pl_PL-darkman-medium"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := workDir(t, tt.script)
			_, err := Prepare(Request{WorkDir: dir, ScriptPath: "demos/demo.yaml", LangOverride: tt.override}, tt.cat)
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
	if plan.Script.StorageState != nil {
		t.Errorf("StorageState = %+v, want nil", plan.Script.StorageState)
	}
}

func TestPrepare_storageStateIsInline(t *testing.T) {
	dir := workDir(t, withTarget("storageState:\n  cookies:\n    - {name: session, value: abc, url: http://host.docker.internal:3000}\n"))
	plan, err := Prepare(Request{WorkDir: dir, ScriptPath: "demos/demo.yaml"}, stock)
	if err != nil {
		t.Fatal(err)
	}
	st := plan.Script.StorageState
	if st == nil || len(st.Cookies) != 1 || st.Cookies[0].Name != "session" {
		t.Errorf("StorageState = %+v, want the inline session cookie", st)
	}
}

// A path string, the old form, is rejected next to the other problems.
func TestPrepare_storageStatePathIsValidationError(t *testing.T) {
	dir := workDir(t, withTarget("storageState: auth/storageState.json\n"))
	_, err := Prepare(Request{WorkDir: dir, ScriptPath: "demos/demo.yaml"}, stock)
	var ve failure.ValidationErrors
	if !errors.As(err, &ve) || len(ve) == 0 || ve[0].Pointer != "/storageState" {
		t.Errorf("Prepare() error = %v, want a validation error at /storageState", err)
	}
}

func TestPrepare_outputDirDefaultsToDemoOutput(t *testing.T) { // decision 62
	dir := workDir(t, enOnly)
	plan, err := Prepare(Request{WorkDir: dir, ScriptPath: "demos/demo.yaml"}, stock)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "demos", "output"); plan.OutputDir != want {
		t.Errorf("OutputDir = %q, want %q", plan.OutputDir, want)
	}
	if want := filepath.Join(dir, "demos"); plan.DemoDir != want {
		t.Errorf("DemoDir = %q, want %q", plan.DemoDir, want)
	}
}

func TestPrepare_outputDirRelativeToDemoDir(t *testing.T) { // decision 62
	tests := []struct {
		name, field string
		want        func(dir string) string
	}{
		{"relative", "outputDir: videos/out\n", func(dir string) string { return filepath.Join(dir, "demos/videos/out") }},
		{"dot is the demo folder", "outputDir: .\n", func(dir string) string { return filepath.Join(dir, "demos") }},
		{"parent may reach the work dir", "outputDir: ../out\n", func(dir string) string { return filepath.Join(dir, "out") }},
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
		{"outputDir parent of the work dir", "outputDir: ../../out\n", "/outputDir", "../../out"},
		{"outputDir deep parent", "outputDir: videos/../../../out\n", "/outputDir", "videos/../../../out"},
		{"outputDir absolute", "outputDir: /srv/videos\n", "/outputDir", "/srv/videos"},
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
	stray := "baseUrl: http://elsewhere:9\nstorageState: {cookies: [{name: x, value: y, url: http://elsewhere:9}]}\noutputDir: elsewhere\nvoices: {en: nope}\n"
	if err := os.WriteFile(filepath.Join(dir, "screencaster.yaml"), []byte(stray), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := Prepare(Request{WorkDir: dir, ScriptPath: "demos/demo.yaml"}, stock)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Script.BaseURL != "http://host.docker.internal:3000" || plan.Script.StorageState != nil ||
		plan.OutputDir != filepath.Join(dir, "demos", "output") || plan.Voices["en"] != "en_US-ryan-high" {
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

// writeFile puts a file next to the demo, e.g. an image under demos/assets.
func writeFile(t *testing.T, dir, rel string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, append([]byte("\x89PNG\r\n\x1a\n"), "data"...), 0o644); err != nil {
		t.Fatal(err)
	}
}

const metaBlock = "meta:\n  title: Tour\n  description: A short tour.\n"

func TestPrepare_cardsDefaultToBuiltIn(t *testing.T) { // decision 63
	tests := []struct {
		name      string
		extra     string
		wantIntro Card
		wantOutro Card
	}{
		{
			"title and description from meta; outro title left for the language",
			metaBlock,
			Card{Title: "Tour", Subtitle: "A short tour.", Duration: 3 * time.Second},
			Card{Subtitle: "Tour", Duration: 3 * time.Second},
		},
		{
			"no meta: the name is the title",
			"",
			Card{Title: "demo", Duration: 3 * time.Second},
			Card{Subtitle: "demo", Duration: 3 * time.Second},
		},
		{
			"explicit text replaces each default field on its own, durationMs sets the time",
			metaBlock + "intro:\n  title: Hello\n  durationMs: 4500\noutro:\n  subtitle: Bye\n",
			Card{Title: "Hello", Subtitle: "A short tour.", Duration: 4500 * time.Millisecond},
			Card{Subtitle: "Bye", Duration: 3 * time.Second},
		},
		{
			"false turns a card off",
			"intro: false\noutro: false\n",
			Card{Off: true},
			Card{Off: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := workDir(t, withTarget(tt.extra))
			plan, err := Prepare(Request{WorkDir: dir, ScriptPath: "demos/demo.yaml"}, stock)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Intro != tt.wantIntro || plan.Outro != tt.wantOutro {
				t.Errorf("cards = %+v, %+v; want %+v, %+v", plan.Intro, plan.Outro, tt.wantIntro, tt.wantOutro)
			}
		})
	}
}

func TestPrepare_cardImageResolvesAgainstDemoDir(t *testing.T) { // decision 62
	dir := workDir(t, withTarget("intro:\n  image: assets/logo.png\n  durationMs: 2000\n"))
	writeFile(t, dir, "demos/assets/logo.png")

	plan, err := Prepare(Request{WorkDir: dir, ScriptPath: "demos/demo.yaml"}, stock)
	if err != nil {
		t.Fatal(err)
	}
	want := Card{Image: filepath.Join(dir, "demos", "assets", "logo.png"), Duration: 2 * time.Second}
	if plan.Intro != want {
		t.Errorf("Intro = %+v, want %+v (the picture is the whole card, no text)", plan.Intro, want)
	}
	if plan.Outro.Image != "" || plan.Outro.Off {
		t.Errorf("Outro = %+v, want the built-in card", plan.Outro)
	}
}

func TestPrepare_cardImageProblemsAreValidationErrors(t *testing.T) { // FR-002, decision 63
	tests := []struct {
		name    string
		field   string
		setup   func(t *testing.T, dir string)
		pointer string
		message func(dir string) string
	}{
		{
			name:    "missing file",
			field:   "intro:\n  image: assets/logo.png\n",
			pointer: "/intro/image",
			message: func(dir string) string { return "image not found: " + filepath.Join(dir, "demos/assets/logo.png") },
		},
		{
			name:    "outside the work dir",
			field:   "outro:\n  image: ../../logo.png\n",
			pointer: "/outro/image",
			message: func(string) string { return "outro.image must stay inside the working directory: ../../logo.png" },
		},
		{
			name:    "absolute path outside the work dir",
			field:   "intro:\n  image: /etc/logo.png\n",
			pointer: "/intro/image",
			message: func(string) string { return "intro.image must stay inside the working directory: /etc/logo.png" },
		},
		{
			name:  "a folder",
			field: "intro:\n  image: assets/logo.png\n",
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(filepath.Join(dir, "demos/assets/logo.png"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			pointer: "/intro/image",
			message: func(dir string) string {
				return "image is not a regular file: " + filepath.Join(dir, "demos/assets/logo.png")
			},
		},
		{
			name:  "not a picture",
			field: "intro:\n  image: assets/logo.png\n",
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(filepath.Join(dir, "demos/assets"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "demos/assets/logo.png"), []byte("<html>404</html>"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			pointer: "/intro/image",
			message: func(dir string) string {
				return "image is not a PNG or JPEG: " + filepath.Join(dir, "demos/assets/logo.png")
			},
		},
		{
			name:  "empty file",
			field: "outro:\n  image: assets/logo.png\n",
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(filepath.Join(dir, "demos/assets"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "demos/assets/logo.png"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			pointer: "/outro/image",
			message: func(dir string) string {
				return "image is not a PNG or JPEG: " + filepath.Join(dir, "demos/assets/logo.png")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := workDir(t, withTarget(tt.field))
			if tt.setup != nil {
				tt.setup(t, dir)
			}
			_, err := Prepare(Request{WorkDir: dir, ScriptPath: "demos/demo.yaml"}, stock)
			want := failure.ValidationErrors{{Pointer: tt.pointer, Message: tt.message(dir)}}
			var ve failure.ValidationErrors
			if !errors.As(err, &ve) || !reflect.DeepEqual(ve, want) {
				t.Errorf("Prepare() error = %v, want %v", err, want)
			}
		})
	}
}
