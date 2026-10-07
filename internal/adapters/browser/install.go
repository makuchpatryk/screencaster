package browser

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	playwright "github.com/mxschmitt/playwright-go"
)

// browsersPathEnv is where the driver looks for browsers; launch reads it from
// the process environment, so wire sets it too (decision 77).
const browsersPathEnv = "PLAYWRIGHT_BROWSERS_PATH"

// Installer installs the Playwright driver and Chromium (`screencaster setup`).
// It implements setup.Browser.
type Installer struct{}

// Install downloads the driver into driverDir and Chromium into browsersDir in
// one playwright.Install call. RunOptions used: DriverDirectory, Browsers
// ["chromium"], WithDeps (maps to `--with-deps`, apt only), Stdout, Stderr and
// Logger, all pointed at out, so nothing reaches the process's own stdout.
//
// It reads the environment the library documents: PLAYWRIGHT_NODEJS_PATH (use
// an installed Node.js instead of downloading one), NODE_MIRROR and
// PLAYWRIGHT_GO_NPM_REGISTRY (mirrors). browsersDir is passed to the driver as
// PLAYWRIGHT_BROWSERS_PATH for the duration of the call.
//
// playwright.Install takes no context, so ctx is checked only before it
// starts; an interrupt during the download ends the process instead.
//
// It needs the network and is not unit-tested; the native run in the setup
// plan (CP6) is its check.
func (Installer) Install(ctx context.Context, driverDir, browsersDir string, withDeps bool, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	prev, had := os.LookupEnv(browsersPathEnv)
	if err := os.Setenv(browsersPathEnv, browsersDir); err != nil {
		return err
	}
	defer func() {
		if had {
			_ = os.Setenv(browsersPathEnv, prev)
		} else {
			_ = os.Unsetenv(browsersPathEnv)
		}
	}()
	err := playwright.Install(&playwright.RunOptions{
		DriverDirectory: driverDir,
		Browsers:        []string{"chromium"},
		WithDeps:        withDeps,
		Verbose:         true,
		Stdout:          out,
		Stderr:          out,
		Logger:          slog.New(slog.NewTextHandler(out, nil)),
	})
	if err != nil {
		return fmt.Errorf("playwright install: %w", err)
	}
	return nil
}
