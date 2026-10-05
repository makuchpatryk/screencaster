package voices

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func touch(t *testing.T, dir string, names ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLang(t *testing.T) {
	tests := []struct{ voice, want string }{
		{"pl_PL-darkman-medium", "pl"},
		{"en_US-ryan-high", "en"},
		{"de_DE-thorsten-medium", "de"},
		{"nounderscore", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.voice, func(t *testing.T) {
			if got := Lang(tt.voice); got != tt.want {
				t.Errorf("Lang(%q) = %q, want %q", tt.voice, got, tt.want)
			}
		})
	}
}

func TestDiscover_listsModelsFromAllDirsAndSkipsMissingDir(t *testing.T) {
	image, work := t.TempDir(), filepath.Join(t.TempDir(), "voices")
	touch(t, image, "en_US-ryan-high.onnx", "en_US-ryan-high.onnx.json", "notes.txt")
	touch(t, work, "pl_PL-gosia-medium.onnx", "en_US-ryan-high.onnx")

	got, err := Discover(image, work, filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatal(err)
	}
	want := Installed{
		"en_US-ryan-high":    filepath.Join(work, "en_US-ryan-high.onnx"), // later dir wins
		"pl_PL-gosia-medium": filepath.Join(work, "pl_PL-gosia-medium.onnx"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Discover() = %v, want %v", got, want)
	}
}

func TestResolve(t *testing.T) {
	inst := Installed{
		"en_US-ryan-high":      "/v/en_US-ryan-high.onnx",
		"pl_PL-darkman-medium": "/v/pl_PL-darkman-medium.onnx",
		"pl_PL-gosia-medium":   "/v/pl_PL-gosia-medium.onnx",
		"de_DE-thorsten":       "/v/de_DE-thorsten.onnx",
	}
	tests := []struct {
		name    string
		langs   []string
		script  map[string]string
		inst    Installed
		want    map[string]string
		wantErr string
	}{
		{
			name:  "built-in defaults (FR-001 AC2)",
			langs: []string{"en", "pl"},
			inst:  inst,
			want:  map[string]string{"en": "/v/en_US-ryan-high.onnx", "pl": "/v/pl_PL-darkman-medium.onnx"},
		},
		{
			name:   "script beats built-in",
			langs:  []string{"pl"},
			script: map[string]string{"pl": "pl_PL-gosia-medium"},
			inst:   inst,
			want:   map[string]string{"pl": "/v/pl_PL-gosia-medium.onnx"},
		},
		{
			name:   "language without built-in works when the script names a voice",
			langs:  []string{"de"},
			script: map[string]string{"de": "de_DE-thorsten"},
			inst:   inst,
			want:   map[string]string{"de": "/v/de_DE-thorsten.onnx"},
		},
		{
			name:    "language without any voice fails",
			langs:   []string{"de"},
			inst:    inst,
			wantErr: "no voice for language: de",
		},
		{
			name:    "uninstalled voice fails (BR-011)",
			langs:   []string{"en"},
			inst:    Installed{},
			wantErr: "voice not installed: en_US-ryan-high",
		},
		{
			name:   "unselected languages are not checked",
			langs:  []string{"en"},
			script: map[string]string{"pl": "pl_PL-missing"},
			inst:   Installed{"en_US-ryan-high": "/v/en.onnx"},
			want:   map[string]string{"en": "/v/en.onnx"},
		},
		{
			name:    "reports every failing language",
			langs:   []string{"en", "pl"},
			inst:    Installed{},
			wantErr: "voice not installed: en_US-ryan-high\nvoice not installed: pl_PL-darkman-medium",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, errs := Resolve(tt.langs, tt.script, tt.inst)
			if tt.wantErr != "" {
				if len(errs) == 0 || errs.Error() != tt.wantErr {
					t.Fatalf("Resolve() errors = %q, want %q", errs.Error(), tt.wantErr)
				}
				return
			}
			if len(errs) != 0 {
				t.Fatalf("Resolve() errors = %v", errs)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Resolve() = %v, want %v", got, tt.want)
			}
		})
	}
}

// FR-018 acceptance criteria.
func TestOptions(t *testing.T) {
	stock := Installed{
		"en_US-ryan-high":      "/v/en",
		"pl_PL-darkman-medium": "/v/pl",
	}
	withExtras := Installed{
		"en_US-ryan-high":       "/v/en",
		"pl_PL-darkman-medium":  "/v/pl",
		"pl_PL-gosia-medium":    "/v/gosia",
		"de_DE-thorsten-medium": "/v/de",
		"README":                "/v/ignored",
	}
	tests := []struct {
		name string
		inst Installed
		want []Option
	}{
		{"stock image", stock, []Option{
			{"en", true, "en_US-ryan-high", []string{"en_US-ryan-high"}},
			{"pl", false, "pl_PL-darkman-medium", []string{"pl_PL-darkman-medium"}},
		}},
		{"extra pl voice and a language without default", withExtras, []Option{
			{"de", false, "", []string{"de_DE-thorsten-medium"}},
			{"en", true, "en_US-ryan-high", []string{"en_US-ryan-high"}},
			{"pl", false, "pl_PL-darkman-medium", []string{"pl_PL-darkman-medium", "pl_PL-gosia-medium"}},
		}},
		{"en and pl always listed, default needs an installed voice", Installed{}, []Option{
			{"en", true, "", []string{}},
			{"pl", false, "", []string{}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Options(tt.inst); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Options() = %+v\nwant %+v", got, tt.want)
			}
		})
	}
}
