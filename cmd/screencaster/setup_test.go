package main

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"screencaster/internal/adapters/osfs"
	"screencaster/internal/app/setup"
)

// fakeTools records every call that would download or install, so a test can
// assert none happened.
type fakeTools struct {
	calls      []string
	root       bool
	arch       string
	apt        bool
	path       map[string]bool
	restricted bool // Sandbox.Restricted; false (unrestricted) by default
	loaded     bool // Sandbox.Loaded, flips true after Install
}

func (f *fakeTools) Fetch(_ context.Context, url, _, _ string) error {
	f.calls = append(f.calls, "fetch "+url)
	return nil
}

func (f *fakeTools) Extract(_ context.Context, tgz, dstDir string) error {
	f.calls = append(f.calls, "extract")
	if filepath.Base(tgz) == "playwright-core.tgz" {
		// setup patches the driver bundle, so the real file system needs one.
		lib := filepath.Join(dstDir, "package", "lib")
		if err := os.MkdirAll(lib, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(lib, "coreBundle.js"), []byte("pageError.location.url pageError.location.lineNumber pageError.location.columnNumber"), 0o644)
	}
	return nil
}

func (f *fakeTools) ExtractZip(_ context.Context, _, dstDir string) error {
	f.calls = append(f.calls, "extract zip")
	return os.MkdirAll(dstDir, 0o755)
}

func (f *fakeTools) ExtractFile(context.Context, string, string, string, fs.FileMode) error {
	f.calls = append(f.calls, "extract file")
	return nil
}

func (f *fakeTools) InstallDeps(context.Context, string, io.Writer) error {
	f.calls = append(f.calls, "browser deps")
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

func (f *fakeTools) Restricted() (bool, error) { return f.restricted, nil }
func (f *fakeTools) Loaded(string) bool        { return f.loaded }

func (f *fakeTools) Install(context.Context, string, io.Writer) error {
	f.calls = append(f.calls, "sandbox")
	f.loaded = true
	return nil
}

// setupFor is a setup command over the real file system in a temp dir and
// fake tools.
func setupFor(t *testing.T, f *fakeTools) (setupConfig, string) {
	t.Helper()
	dir := t.TempDir() + "/install"
	return setupConfig{
		Deps: setup.Deps{Fetch: f, Browser: f, Packages: f, Files: osfs.FS{}, Host: f, Sandbox: f},
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

func TestSetupCheck_emptyDirReportsSixPiecesAndExits1(t *testing.T) { // runs without root
	f := newTools(false)
	cfg, dir := setupFor(t, f)
	code, stdout, stderr := runSetup(cfg, "--check")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 6 {
		t.Fatalf("stdout has %d lines, want 6:\n%s", len(lines), stdout)
	}
	// Sandbox is a property of the kernel, not the install dir: the fake's
	// kernel is unrestricted by default, so it is ok here, unlike every other
	// piece on an empty dir.
	want := []struct{ piece, status string }{
		{"piper", "missing"}, {"voices", "missing"}, {"driver", "missing"},
		{"chromium", "missing"}, {"sandbox", "ok"}, {"ffmpeg", "missing"},
	}
	for i, w := range want {
		if !strings.HasPrefix(lines[i], w.piece+": "+w.status) {
			t.Errorf("line %d = %q, want %q: %s", i, lines[i], w.piece, w.status)
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
	for _, piece := range []string{"piper: ok", "voices: ok", "driver: ok", "chromium: ok", "sandbox: ok", "ffmpeg: ok"} {
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
