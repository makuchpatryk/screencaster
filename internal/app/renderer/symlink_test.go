package renderer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"screencaster/internal/domain/failure"
)

// realWorkDir creates a real temp dir with demos/demo.yaml holding script, for
// the symlink tests below: fstest.MapFS (memFS) has no real symlinks, so these
// use osFiles over the actual file system instead.
func realWorkDir(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "demos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "demos", "demo.yaml"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPrepare_outputDirSymlinkOutsideRefused(t *testing.T) {
	dir := realWorkDir(t, withTarget("outputDir: out\n"))
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "demos", "out")); err != nil {
		t.Fatal(err)
	}

	_, err := Prepare(request(dir), stock, osFiles{})
	var ve failure.ValidationErrors
	if !errors.As(err, &ve) || len(ve) != 1 || ve[0].Pointer != "/outputDir" ||
		ve[0].Message != "outputDir must stay inside the working directory: out" {
		t.Fatalf("Prepare() error = %v, want the outputDir symlink error", err)
	}
}

func TestPrepare_cardImageSymlinkOutsideRefused(t *testing.T) {
	dir := realWorkDir(t, withTarget("intro:\n  image: assets/logo.png\n"))
	if err := os.MkdirAll(filepath.Join(dir, "demos", "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	target := filepath.Join(outside, "logo.png")
	if err := os.WriteFile(target, []byte("not even checked: within refuses first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "demos", "assets", "logo.png")); err != nil {
		t.Fatal(err)
	}

	_, err := Prepare(request(dir), stock, osFiles{})
	var ve failure.ValidationErrors
	if !errors.As(err, &ve) || len(ve) != 1 || ve[0].Pointer != "/intro/image" ||
		ve[0].Message != "intro.image must stay inside the working directory: assets/logo.png" {
		t.Fatalf("Prepare() error = %v, want the card image symlink error", err)
	}
}

func TestScriptPath_symlinkOutsideRefused(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	outsideScript := filepath.Join(outside, "evil.yaml")
	if err := os.WriteFile(outsideScript, []byte("name: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideScript, filepath.Join(dir, "demo.yaml")); err != nil {
		t.Fatal(err)
	}

	_, err := ScriptPath(osFiles{}, dir, "demo.yaml")
	if err == nil || err.Error() != "script must stay inside the working directory: demo.yaml" {
		t.Fatalf("ScriptPath() error = %v, want the symlink-outside error", err)
	}
}

// A dangling symlink must be refused even when it would stay inside the
// working directory if it resolved: EvalSymlinks on it fails regardless of
// where the (missing) target would be.
func TestPrepare_danglingSymlinkRefused(t *testing.T) {
	dir := realWorkDir(t, withTarget("outputDir: out\n"))
	if err := os.Symlink(filepath.Join(dir, "does-not-exist"), filepath.Join(dir, "demos", "out")); err != nil {
		t.Fatal(err)
	}

	_, err := Prepare(request(dir), stock, osFiles{})
	var ve failure.ValidationErrors
	if !errors.As(err, &ve) || len(ve) != 1 || ve[0].Pointer != "/outputDir" ||
		ve[0].Message != "outputDir must stay inside the working directory: out" {
		t.Fatalf("Prepare() error = %v, want the dangling-symlink error", err)
	}
}

func TestPrepare_inTreeSymlinkAllowed(t *testing.T) {
	dir := realWorkDir(t, withTarget("outputDir: out\n"))
	if err := os.MkdirAll(filepath.Join(dir, "real-out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "real-out"), filepath.Join(dir, "demos", "out")); err != nil {
		t.Fatal(err)
	}

	plan, err := Prepare(request(dir), stock, osFiles{})
	if err != nil {
		t.Fatalf("Prepare() error = %v, want none: an in-tree symlink still works", err)
	}
	if want := filepath.Join(dir, "demos", "out"); plan.OutputDir != want {
		t.Errorf("OutputDir = %q, want %q (the symlink path itself, not resolved)", plan.OutputDir, want)
	}
}

// osShooter is a minimal real-disk Shooter: it writes one PNG into the
// request's dir, for TestRender_screenshotsFolderSymlinkOutsideRefused.
type osShooter struct{}

func (osShooter) Shoot(_ context.Context, r ShootRequest) ([]string, error) {
	p := filepath.Join(r.Dir, "01.png")
	if err := os.WriteFile(p, []byte("png"), 0o644); err != nil {
		return nil, err
	}
	return []string{p}, nil
}

// A symlinked screenshots folder must be refused even though outputDir itself
// (checked by Prepare) stays inside the working directory: the script's own
// Name joins onto it fresh, after Prepare, right before publishShots (M3).
func TestRender_screenshotsFolderSymlinkOutsideRefused(t *testing.T) {
	dir := realWorkDir(t, shotsScript) // name: shots -> <outputDir>/shots/screenshots
	if err := os.MkdirAll(filepath.Join(dir, "demos", "output"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "demos", "output", "shots")); err != nil {
		t.Fatal(err)
	}

	deps := Deps{
		Files: osFiles{},
		Shots: osShooter{},
		Now:   func() time.Time { return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) },
		RunID: func() string { return "run1" },
	}
	_, err := Render(context.Background(), deps, request(dir))
	if err == nil || !strings.Contains(err.Error(), "screenshots folder must stay inside the working directory") {
		t.Fatalf("Render() error = %v, want the screenshots-folder symlink error", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "01.png")); statErr == nil {
		t.Error("a screenshot was published outside the working directory")
	}
}
