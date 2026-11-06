package browser

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// Installer runs the installed Playwright driver for `screencaster setup`. It
// implements setup.Browser. It downloads nothing: the driver, Chromium and
// ffmpeg are fetched by setup from pinned, sha256-checked files, and the
// library's own installer is never used (decision 82).
type Installer struct{}

// InstallDeps runs the driver's `install-deps chromium` with a fixed argument
// list: Chromium's system libraries through apt (needs root and apt; setup
// calls it only when apt is on PATH). The command line is
// `<driverDir>/node <driverDir>/package/cli.js install-deps chromium`, with
// output to out so nothing reaches the process's own stdout.
//
// It changes the system and is not unit-tested beyond its argument list
// (install_test.go); the native run in the manual pre-release check is its
// test (ARCHITECTURE §13).
func (Installer) InstallDeps(ctx context.Context, driverDir string, out io.Writer) error {
	node := filepath.Join(driverDir, "node")
	cli := filepath.Join(driverDir, "package", "cli.js")
	env := []string{"DEBIAN_FRONTEND=noninteractive"}
	if err := run(ctx, out, env, node, cli, "install-deps", "chromium"); err != nil {
		return fmt.Errorf("playwright install-deps chromium: %w", err)
	}
	return nil
}

// run starts a command with extra environment variables; a package variable
// so the unit test checks the argument list without running the driver.
var run = func(ctx context.Context, out io.Writer, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}
