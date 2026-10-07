package wire

import (
	"os"
	"testing"
)

func TestInstallDir(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"default", map[string]string{}, "/opt/screencaster"},
		{"override", map[string]string{"SCREENCASTER_HOME": "/srv/sc"}, "/srv/sc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := InstallDir(env(tt.env)); got != tt.want {
				t.Errorf("InstallDir = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestPiperPaths_areUnderTheInstallDir(t *testing.T) {
	bin, voices := PiperPaths("/opt/screencaster")
	if bin != "/opt/screencaster/piper/piper" || voices != "/opt/screencaster/piper/voices" {
		t.Errorf("PiperPaths = %s, %s", bin, voices)
	}
}

func TestSetup_wiresEveryPort(t *testing.T) {
	d := Setup()
	ports := map[string]bool{
		"Fetch": d.Fetch != nil, "Browser": d.Browser != nil, "Packages": d.Packages != nil,
		"Files": d.Files != nil, "Host": d.Host != nil,
	}
	for name, set := range ports {
		if !set {
			t.Errorf("setup.Deps.%s is not wired", name)
		}
	}
}

// A native install sets no Playwright variable: the driver and the browsers
// come from the install dir, and the browsers path is set for the driver
// process (decision 77).
func TestLauncher_nativeUsesTheInstallDir(t *testing.T) {
	t.Setenv("SCREENCASTER_HOME", "")
	t.Setenv("PLAYWRIGHT_DRIVER_PATH", "")
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", "")
	l := launcher()
	if l.DriverDir != "/opt/screencaster/playwright-driver" {
		t.Errorf("DriverDir = %q", l.DriverDir)
	}
	if got := os.Getenv("PLAYWRIGHT_BROWSERS_PATH"); got != "/opt/screencaster/ms-playwright" {
		t.Errorf("PLAYWRIGHT_BROWSERS_PATH = %q", got)
	}
}

func TestLauncher_installDirOverride(t *testing.T) {
	t.Setenv("SCREENCASTER_HOME", "/srv/sc")
	t.Setenv("PLAYWRIGHT_DRIVER_PATH", "")
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", "")
	if l := launcher(); l.DriverDir != "/srv/sc/playwright-driver" {
		t.Errorf("DriverDir = %q", l.DriverDir)
	}
	if got := os.Getenv("PLAYWRIGHT_BROWSERS_PATH"); got != "/srv/sc/ms-playwright" {
		t.Errorf("PLAYWRIGHT_BROWSERS_PATH = %q", got)
	}
}

// The Docker images set both variables; the install dir must not override them.
func TestLauncher_environmentWins(t *testing.T) {
	t.Setenv("PLAYWRIGHT_DRIVER_PATH", "/opt/playwright-driver")
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", "/opt/ms-playwright")
	if l := launcher(); l.DriverDir != "" {
		t.Errorf("DriverDir = %q, want empty so the library reads PLAYWRIGHT_DRIVER_PATH", l.DriverDir)
	}
	if got := os.Getenv("PLAYWRIGHT_BROWSERS_PATH"); got != "/opt/ms-playwright" {
		t.Errorf("PLAYWRIGHT_BROWSERS_PATH = %q, want it unchanged", got)
	}
}
