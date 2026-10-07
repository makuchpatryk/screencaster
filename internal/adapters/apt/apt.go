// Package apt installs ffmpeg through apt-get (`screencaster setup`). It
// implements setup.Packages and is one of the packages that start
// subprocesses (decision 77).
package apt

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// Apt is the Debian/Ubuntu package manager.
type Apt struct{}

// HasApt reports whether apt-get is on PATH. It is the only distro detection
// setup does.
func (Apt) HasApt() bool {
	_, err := exec.LookPath("apt-get")
	return err == nil
}

// InstallFFmpeg refreshes the package lists and installs ffmpeg, which brings
// ffprobe. Output goes to out, never to the process's own stdout. Needs root.
func (Apt) InstallFFmpeg(ctx context.Context, out io.Writer) error {
	noninteractive := []string{"DEBIAN_FRONTEND=noninteractive"}
	if err := run(ctx, out, noninteractive, "apt-get", "update"); err != nil {
		return fmt.Errorf("apt-get update: %w", err)
	}
	if err := run(ctx, out, noninteractive, "apt-get", "install", "-y", "ffmpeg"); err != nil {
		return fmt.Errorf("apt-get install -y ffmpeg: %w", err)
	}
	return nil
}

// run starts a command with extra environment variables; a package variable
// so the unit test checks the argument list without running apt.
var run = func(ctx context.Context, out io.Writer, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}
