package config

import (
	"os"
	"path/filepath"
	"testing"
)

// project writes screencaster.yaml (when yaml != "") and an empty
// auth/storageState.json into a fresh work dir.
func project(t *testing.T, yaml string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "auth"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth", "storageState.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if yaml != "" {
		if err := os.WriteFile(filepath.Join(dir, "screencaster.yaml"), []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoad_errors(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		want    func(dir string) string
		noFile  bool
		wantAny bool // only assert that an error occurs
	}{
		{
			name:   "missing file names the absolute path (UF-002)",
			noFile: true,
			want:   func(dir string) string { return "config not found: " + filepath.Join(dir, "screencaster.yaml") },
		},
		{
			name: "baseUrl required (FR-001 AC1)",
			yaml: "storageState: auth/storageState.json\n",
			want: func(string) string { return "baseUrl is required" },
		},
		{
			name: "baseUrl required even when storageState is also missing",
			yaml: "outputDir: out\n",
			want: func(string) string { return "baseUrl is required" },
		},
		{
			name: "baseUrl must be absolute http(s)",
			yaml: "baseUrl: /projects\nstorageState: auth/storageState.json\n",
			want: func(string) string { return "baseUrl must be an absolute http or https URL: /projects" },
		},
		{
			name: "baseUrl scheme must be http(s)",
			yaml: "baseUrl: ftp://host\nstorageState: auth/storageState.json\n",
			want: func(string) string { return "baseUrl must be an absolute http or https URL: ftp://host" },
		},
		{
			name: "storageState required",
			yaml: "baseUrl: http://localhost:3000\n",
			want: func(string) string { return "storageState is required" },
		},
		{
			name: "storageState must exist",
			yaml: "baseUrl: http://localhost:3000\nstorageState: auth/nope.json\n",
			want: func(dir string) string { return "storageState not found: " + filepath.Join(dir, "auth", "nope.json") },
		},
		{
			name:    "unknown keys rejected",
			yaml:    "baseUrl: http://localhost:3000\nstorageState: auth/storageState.json\nbaseURL: x\n",
			wantAny: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := project(t, tt.yaml)
			if tt.noFile {
				dir = t.TempDir()
			}
			_, err := Load(dir)
			if err == nil {
				t.Fatal("Load() error = nil")
			}
			if tt.wantAny {
				return
			}
			if got, want := err.Error(), tt.want(dir); got != want {
				t.Errorf("Load() error = %q, want %q", got, want)
			}
		})
	}
}

func TestLoad_defaultsAndResolvedPaths(t *testing.T) {
	dir := project(t, "baseUrl: http://host.docker.internal:3000\nstorageState: auth/storageState.json\n")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "output"); cfg.OutputDir != want {
		t.Errorf("OutputDir = %q, want default %q", cfg.OutputDir, want)
	}
	if want := filepath.Join(dir, "auth", "storageState.json"); cfg.StorageState != want {
		t.Errorf("StorageState = %q, want %q", cfg.StorageState, want)
	}
	if cfg.BaseURL != "http://host.docker.internal:3000" || cfg.WorkDir != dir {
		t.Errorf("BaseURL/WorkDir = %q/%q", cfg.BaseURL, cfg.WorkDir)
	}
	if len(cfg.Voices) != 0 {
		t.Errorf("Voices = %v, want none (built-ins live in core/voices)", cfg.Voices)
	}
}

func TestLoad_keepsVoicesAndAbsoluteOutputDir(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "videos")
	dir := project(t, "baseUrl: https://app.example.com\nstorageState: auth/storageState.json\noutputDir: "+abs+"\nvoices:\n  pl: pl_PL-gosia-medium\n")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OutputDir != abs {
		t.Errorf("OutputDir = %q, want %q", cfg.OutputDir, abs)
	}
	if cfg.Voices["pl"] != "pl_PL-gosia-medium" {
		t.Errorf("Voices = %v", cfg.Voices)
	}
}
