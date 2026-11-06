// Package apparmor installs an AppArmor profile that lets Chromium create
// user namespaces when the kernel restricts unprivileged ones (`screencaster
// setup`, decision 83, Ubuntu 24.04). It is one of the packages that start
// subprocesses (decision 77).
package apparmor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// ProfileName is the fixed AppArmor profile name screencaster installs and
// looks for.
const ProfileName = "screencaster-chromium"

// profilePath is the fixed file Install writes; apparmor_parser loads
// profiles from /etc/apparmor.d by path.
var profilePath = "/etc/apparmor.d/" + ProfileName

// restrictSysctl is the sysctl that gates unprivileged user namespaces for a
// process without an AppArmor profile allowing `userns` (Ubuntu 24.04+). A
// package variable so the unit test points it at a fixture file.
var restrictSysctl = "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"

// loadedProfiles is where the kernel lists loaded AppArmor profiles, one per
// line as "<name> (<mode>)". A package variable for the same reason.
var loadedProfiles = "/sys/kernel/security/apparmor/profiles"

// AppArmor installs and inspects the Chromium userns profile.
type AppArmor struct{}

// Restricted reports whether this kernel restricts unprivileged user
// namespaces. A missing sysctl file (older kernel, non-Ubuntu, no AppArmor)
// means no restriction: false, nil.
func (AppArmor) Restricted() (bool, error) {
	b, err := os.ReadFile(restrictSysctl)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", restrictSysctl, err)
	}
	return strings.TrimSpace(string(b)) == "1", nil
}

// Loaded reports whether a profile named name is loaded in the kernel. A
// missing securityfs listing (AppArmor disabled, non-Linux) means false, not
// an error: Restricted already says whether the sandbox needs the profile.
func (AppArmor) Loaded(name string) bool {
	f, err := os.Open(loadedProfiles)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		profile, _, _ := strings.Cut(sc.Text(), " (")
		if profile == name {
			return true
		}
	}
	return false
}

// profile is the fixed content Install writes, parameterized only by the
// Chromium binary's absolute path.
func profile(chromePath string) string {
	return fmt.Sprintf(
		"abi <abi/4.0>,\n\nprofile %s %s flags=(unconfined) {\n  userns,\n}\n",
		ProfileName, chromePath,
	)
}

// Install writes the profile for chromePath to /etc/apparmor.d and loads it
// with `apparmor_parser -r`. Needs root. Output goes to out, never to the
// process's own stdout.
func (AppArmor) Install(ctx context.Context, chromePath string, out io.Writer) error {
	if err := os.WriteFile(profilePath, []byte(profile(chromePath)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", profilePath, err)
	}
	if err := run(ctx, out, "apparmor_parser", "-r", profilePath); err != nil {
		return fmt.Errorf("apparmor_parser -r %s: %w", profilePath, err)
	}
	return nil
}

// run starts a command with a fixed argv; a package variable so the unit
// test checks the argument list without running apparmor_parser (pattern of
// adapters/apt).
var run = func(ctx context.Context, out io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}
