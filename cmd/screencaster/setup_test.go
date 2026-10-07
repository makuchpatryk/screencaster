package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"screencaster/internal/adapters/osfs"
	"screencaster/internal/app/setup"
)

// fakeTools records every call that would download or install, so a test can
// assert none happened.
type fakeTools struct {
	calls []string
	root  bool
	arch  string
	apt   bool
	path  map[string]bool
}

func (f *fakeTools) Fetch(_ context.Context, url, _, _ string) error {
	f.calls = append(f.calls, "fetch "+url)
	return nil
}

func (f *fakeTools) Extract(context.Context, string, string) error {
	f.calls = append(f.calls, "extract")
	return nil
}

func (f *fakeTools) Install(context.Context, string, string, bool, io.Writer) error {
	f.calls = append(f.calls, "browser")
	return nil
}

func (f *fakeTools) HasApt() bool { return f.apt }

func (f *fakeTools) InstallFFmpeg(context.Context, io.Writer) error {
	f.calls = append(f.calls, "apt")
	f.path["ffmpeg"], f.path["ffprobe"] = true, true
	return nil
}

func (f *fakeTools) IsRoot() bool            { return f.root }
func (f *fakeTools) Arch() string            { return f.arch }
func (f *fakeTools) HasOnPath(n string) bool { return f.path[n] }

// setupFor is a setup command over the real file system in a temp dir and
// fake tools.
func setupFor(t *testing.T, f *fakeTools) (setupConfig, string) {
	t.Helper()
	dir := t.TempDir() + "/install"
	return setupConfig{
		Deps: setup.Deps{Fetch: f, Browser: f, Packages: f, Files: osfs.FS{}, Host: f},
		Dir:  dir, Version: "1.0.0", Getenv: func(string) string { return "" },
	}, dir
}

func newTools(root bool) *fakeTools {
	return &fakeTools{root: root, arch: "amd64", apt: true, path: map[string]bool{}}
}

func runSetup(cfg setupConfig, args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = run(context.Background(), append([]string{"setup"}, args...), "/work", nil, cfg, &out, &errb)
	return code, out.String(), errb.String()
}

func TestSetupCheck_emptyDirReportsFivePiecesAndExits1(t *testing.T) { // runs without root
	f := newTools(false)
	cfg, dir := setupFor(t, f)
	code, stdout, stderr := runSetup(cfg, "--check")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 5 {
		t.Fatalf("stdout has %d lines, want 5:\n%s", len(lines), stdout)
	}
	for i, piece := range []string{"piper", "voices", "driver", "chromium", "ffmpeg"} {
		if !strings.HasPrefix(lines[i], piece+": missing") {
			t.Errorf("line %d = %q, want %q: missing", i, lines[i], piece)
		}
	}
	if !strings.Contains(stderr, "setup is incomplete") {
		t.Errorf("stderr = %q, want the incomplete hint", stderr)
	}
	if len(f.calls) != 0 {
		t.Errorf("--check did work: %v", f.calls)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("--check created the install dir")
	}
}

func TestSetup_withoutRootExits1BeforeAnyWork(t *testing.T) {
	f := newTools(false)
	cfg, dir := setupFor(t, f)
	code, stdout, stderr := runSetup(cfg)
	if code != 1 || !strings.Contains(stderr, "must run as root") {
		t.Fatalf("code %d, stderr %q; want 1 and the root message", code, stderr)
	}
	if stdout != "" || len(f.calls) != 0 {
		t.Errorf("stdout %q, calls %v; want no work", stdout, f.calls)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("the install dir was created")
	}
}

func TestSetup_unsupportedArchExits1BeforeAnyWork(t *testing.T) {
	f := newTools(true)
	f.arch = "arm64"
	cfg, _ := setupFor(t, f)
	for _, args := range [][]string{nil, {"--check"}} {
		code, _, stderr := runSetup(cfg, args...)
		if code != 1 || !strings.Contains(stderr, "unsupported architecture arm64") {
			t.Errorf("args %v: code %d, stderr %q", args, code, stderr)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("work done: %v", f.calls)
	}
}

func TestSetup_asRootInstallsEverythingAndExits0(t *testing.T) {
	f := newTools(true)
	cfg, dir := setupFor(t, f)
	code, stdout, stderr := runSetup(cfg)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr %q", code, stderr)
	}
	for _, piece := range []string{"piper: ok", "voices: ok", "driver: ok", "chromium: ok", "ffmpeg: ok"} {
		if !strings.Contains(stdout, piece) {
			t.Errorf("stdout lacks %q:\n%s", piece, stdout)
		}
	}
	if _, err := os.Stat(dir + "/setup.json"); err != nil {
		t.Errorf("no marker written: %v", err)
	}
	if !strings.Contains(stderr, "downloading") {
		t.Errorf("progress should reach stderr, got %q", stderr)
	}
}

func TestSetup_failedPieceStillPrintsTheReport(t *testing.T) {
	f := newTools(true)
	f.apt = false // ffmpeg cannot be installed; the rest can
	cfg, _ := setupFor(t, f)
	code, stdout, stderr := runSetup(cfg)
	if code != 1 || !strings.Contains(stderr, "apt-get install -y ffmpeg") {
		t.Fatalf("code %d, stderr %q; want 1 and the ffmpeg command", code, stderr)
	}
	if !strings.Contains(stdout, "ffmpeg: missing") || !strings.Contains(stdout, "piper: ok") {
		t.Errorf("stdout should show the state reached:\n%s", stdout)
	}
}

func embeddedVersion(t *testing.T) string {
	t.Helper()
	p, err := setup.LoadPins(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	return p.PiperVersion
}

func TestSetup_pinsFileOverridesTheDownloadSource(t *testing.T) {
	f := newTools(true)
	cfg, _ := setupFor(t, f)
	file := t.TempDir() + "/pins.env"
	if err := os.WriteFile(file, []byte("SCREENCASTER_PIPER_URL_BASE=https://file.example/p/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runSetup(cfg, "--pins-file", file); code != 0 {
		t.Fatalf("exit code = %d, stderr %q", code, stderr)
	}
	if want := "fetch https://file.example/p/" + embeddedVersion(t) + "/piper_linux_x86_64.tar.gz"; len(f.calls) == 0 || f.calls[0] != want {
		t.Errorf("calls %v, want the first to be %q", f.calls, want)
	}
}

func TestSetup_missingPinsFileExits1BeforeAnyWork(t *testing.T) {
	f := newTools(true)
	cfg, _ := setupFor(t, f)
	code, _, stderr := runSetup(cfg, "--pins-file", "/nope/pins.env")
	if code != 1 || !strings.Contains(stderr, "--pins-file") || len(f.calls) != 0 {
		t.Errorf("code %d, stderr %q, calls %v", code, stderr, f.calls)
	}
}

func TestSetup_envOverridesTheDownloadSource(t *testing.T) {
	f := newTools(true)
	cfg, _ := setupFor(t, f)
	cfg.Getenv = func(k string) string {
		if k == setup.EnvPiperURLBase {
			return "https://mirror.example/piper/"
		}
		return ""
	}
	if code, _, stderr := runSetup(cfg); code != 0 {
		t.Fatalf("exit code = %d, stderr %q", code, stderr)
	}
	if len(f.calls) == 0 || f.calls[0] != "fetch https://mirror.example/piper/"+embeddedVersion(t)+"/piper_linux_x86_64.tar.gz" {
		t.Errorf("calls %v, want the first fetch from the mirror", f.calls)
	}
}

func TestSetup_badPinEnvExits1BeforeAnyWork(t *testing.T) {
	f := newTools(true)
	cfg, dir := setupFor(t, f)
	cfg.Getenv = func(k string) string {
		if k == setup.EnvPiperSHA256 {
			return "not-hex"
		}
		return ""
	}
	for _, args := range [][]string{nil, {"--check"}} {
		code, _, stderr := runSetup(cfg, args...)
		if code != 1 || !strings.Contains(stderr, setup.EnvPiperSHA256) {
			t.Errorf("args %v: code %d, stderr %q; want 1 naming the variable", args, code, stderr)
		}
	}
	if _, err := os.Stat(dir); err == nil || len(f.calls) != 0 {
		t.Errorf("work done: calls %v", f.calls)
	}
}

func TestSetup_help(t *testing.T) {
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"setup", "--help"}, "/work", nil, setupConfig{}, &out, &errb)
	if code != 0 || !strings.Contains(errb.String(), "--check") {
		t.Errorf("code %d, help %q; want 0 and the --check flag", code, errb.String())
	}
}
