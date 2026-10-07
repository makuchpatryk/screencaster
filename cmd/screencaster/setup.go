package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"screencaster/internal/app/setup"
)

// setupConfig is what the setup command needs besides its arguments: the ports,
// the install dir, the release version that tags the browser install and the
// environment the download pins are read from.
type setupConfig struct {
	Deps    setup.Deps
	Dir     string
	Version string
	Getenv  func(string) string
}

// errSetupIncomplete is the exit-1 message of `setup --check`; the per-piece
// lines are already on stdout.
var errSetupIncomplete = errors.New("setup is incomplete: run `sudo ./screencaster setup`")

// setupCmd is `setup [--check]`, the one command that downloads (decision 77).
// The report is data and goes to stdout, one line per piece; the installers'
// progress goes to stderr.
func setupCmd(cfg setupConfig, stdout, stderr io.Writer) *cobra.Command {
	var check bool
	var pinsFiles []string
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Install Piper, voices, the Playwright driver, Chromium and ffmpeg (needs root and network)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Pins first: a bad value or file fails before any work.
			var texts []string
			for _, f := range pinsFiles {
				b, err := os.ReadFile(f)
				if err != nil {
					return fmt.Errorf("--pins-file: %w", err)
				}
				texts = append(texts, string(b))
			}
			pins, err := setup.LoadPins(cfg.Getenv, texts...)
			if err != nil {
				return err
			}
			opts := setup.Options{Dir: cfg.Dir, Pins: pins, Version: cfg.Version, Out: stderr}
			if check {
				return report(stdout, func() (setup.Report, error) {
					return setup.Check(cmd.Context(), cfg.Deps, opts)
				})
			}
			return report(stdout, func() (setup.Report, error) {
				return setup.Run(cmd.Context(), cfg.Deps, opts)
			})
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only report the state of each piece; changes nothing, needs no root")
	cmd.Flags().StringArrayVar(&pinsFiles, "pins-file", nil, "KEY=VALUE file overriding the embedded download pins (same keys as the SCREENCASTER_PIPER_* / SCREENCASTER_VOICES_* variables, which still win)")
	return cmd
}

// report prints what get returns, even next to an error (a failed run still
// has the state it reached), then maps an incomplete report to the exit code.
func report(stdout io.Writer, get func() (setup.Report, error)) error {
	rep, err := get()
	for _, p := range rep {
		if _, werr := fmt.Fprintln(stdout, p); werr != nil {
			return werr
		}
	}
	if err != nil {
		return err
	}
	if !rep.AllOK() {
		return errSetupIncomplete
	}
	return nil
}
