package wire

import (
	"os"

	"screencaster/internal/adapters/apt"
	"screencaster/internal/adapters/browser"
	"screencaster/internal/adapters/download"
	"screencaster/internal/adapters/host"
	"screencaster/internal/adapters/osfs"
	"screencaster/internal/app/setup"
)

// defaultInstallDir is where `screencaster setup` installs and render looks
// (decision 77). SCREENCASTER_HOME overrides it.
const defaultInstallDir = "/opt/screencaster"

// InstallDir is the install dir: SCREENCASTER_HOME, else /opt/screencaster.
func InstallDir(getenv func(string) string) string {
	if dir := getenv("SCREENCASTER_HOME"); dir != "" {
		return dir
	}
	return defaultInstallDir
}

// PiperPaths are the Piper binary and the voices folder under the install dir.
func PiperPaths(dir string) (bin, voices string) {
	return setup.PiperBin(dir), setup.VoicesDir(dir)
}

// Setup maps the setup use case's ports onto the real tools.
func Setup() setup.Deps {
	return setup.Deps{
		Fetch:    download.Downloader{},
		Browser:  browser.Installer{},
		Packages: apt.Apt{},
		Files:    osfs.FS{},
		Host:     host.Host{},
	}
}

// Playwright's own variables: the image sets both, a native install sets
// neither and gets the install dir's folders.
const (
	driverPathEnv   = "PLAYWRIGHT_DRIVER_PATH"
	browsersPathEnv = "PLAYWRIGHT_BROWSERS_PATH"
)

// launcher is the browser launcher for the install dir. Unless the
// environment already names them, the driver is <dir>/playwright-driver and
// Chromium is looked up in <dir>/ms-playwright. The driver reads the browsers
// path from this process's environment, so it is set here, before the first
// playwright call, for every entry point that launches a browser.
func launcher() browser.Launcher {
	dir := InstallDir(os.Getenv)
	if os.Getenv(browsersPathEnv) == "" {
		_ = os.Setenv(browsersPathEnv, setup.BrowsersDir(dir))
	}
	if os.Getenv(driverPathEnv) != "" {
		return browser.Launcher{} // the library reads it
	}
	return browser.Launcher{DriverDir: setup.DriverDir(dir)}
}
