package apparmor

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type call struct {
	argv []string
}

// fakeRun replaces run for the test and returns the calls it saw.
func fakeRun(t *testing.T, fail bool) *[]call {
	t.Helper()
	old := run
	t.Cleanup(func() { run = old })
	var calls []call
	run = func(_ context.Context, _ io.Writer, name string, args ...string) error {
		calls = append(calls, call{append([]string{name}, args...)})
		if fail {
			return errors.New("exit status 1")
		}
		return nil
	}
	return &calls
}

// withPaths points restrictSysctl, loadedProfiles and profilePath at files
// under a temp dir for the test, restoring the real paths after.
func withPaths(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	oldSysctl, oldProfiles, oldPath := restrictSysctl, loadedProfiles, profilePath
	restrictSysctl = filepath.Join(dir, "apparmor_restrict_unprivileged_userns")
	loadedProfiles = filepath.Join(dir, "profiles")
	profilePath = filepath.Join(dir, ProfileName)
	t.Cleanup(func() {
		restrictSysctl, loadedProfiles, profilePath = oldSysctl, oldProfiles, oldPath
	})
	return dir
}

func TestRestricted_missingSysctlMeansUnrestricted(t *testing.T) {
	withPaths(t)
	restricted, err := (AppArmor{}).Restricted()
	if err != nil {
		t.Fatal(err)
	}
	if restricted {
		t.Error("restricted true with no sysctl file")
	}
}

func TestRestricted_readsTheSysctlValue(t *testing.T) {
	tests := []struct {
		content string
		want    bool
	}{
		{"1\n", true},
		{"0\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.content, func(t *testing.T) {
			withPaths(t)
			if err := os.WriteFile(restrictSysctl, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := (AppArmor{}).Restricted()
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("restricted %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRestricted_unreadableFileIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read anything")
	}
	withPaths(t)
	if err := os.WriteFile(restrictSysctl, []byte("1\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := (AppArmor{}).Restricted(); err == nil {
		t.Error("no error reading an unreadable sysctl file")
	}
}

func TestLoaded_missingSecurityfsMeansNotLoaded(t *testing.T) {
	withPaths(t)
	if (AppArmor{}).Loaded(ProfileName) {
		t.Error("loaded true with no profiles file")
	}
}

func TestLoaded_findsTheNamedProfile(t *testing.T) {
	withPaths(t)
	content := "some-other-profile (enforce)\n" + ProfileName + " (enforce)\n"
	if err := os.WriteFile(loadedProfiles, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if !(AppArmor{}).Loaded(ProfileName) {
		t.Error("missed a loaded profile")
	}
	if (AppArmor{}).Loaded("not-loaded") {
		t.Error("found a profile that is not loaded")
	}
}

func TestInstall_writesTheProfileAndLoadsIt(t *testing.T) {
	withPaths(t)
	calls := fakeRun(t, false)
	if err := (AppArmor{}).Install(context.Background(), "/opt/chromium/chrome", io.Discard); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	want := "abi <abi/4.0>,\n\nprofile screencaster-chromium /opt/chromium/chrome flags=(unconfined) {\n  userns,\n}\n"
	if string(b) != want {
		t.Errorf("profile =\n%s\nwant\n%s", b, want)
	}
	wantArgv := []string{"apparmor_parser", "-r", profilePath}
	if len(*calls) != 1 || !slices.Equal((*calls)[0].argv, wantArgv) {
		t.Errorf("calls = %v, want one call with argv %v", *calls, wantArgv)
	}
}

func TestInstall_parserFailureNamesTheCommand(t *testing.T) {
	withPaths(t)
	fakeRun(t, true)
	err := (AppArmor{}).Install(context.Background(), "/opt/chromium/chrome", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "apparmor_parser -r") {
		t.Fatalf("error %v, want one naming apparmor_parser -r", err)
	}
}
