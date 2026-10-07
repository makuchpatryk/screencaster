// Package host answers `screencaster setup`'s questions about the machine. It
// implements setup.Host; LookPath is why it is one of the packages that import
// os/exec (decision 77).
package host

import (
	"os"
	"os/exec"
	"runtime"
)

// Host is the machine the process runs on.
type Host struct{}

// IsRoot reports whether the process runs as root.
func (Host) IsRoot() bool { return os.Geteuid() == 0 }

// Arch is the Go architecture name, e.g. amd64.
func (Host) Arch() string { return runtime.GOARCH }

// HasOnPath reports whether name is an executable on PATH.
func (Host) HasOnPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
