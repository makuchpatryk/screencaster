package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"screencaster/core/renderer"
)

// ignoredConfigFile is the retired per-project config (decision 58). Nothing
// reads it; its presence only gets a warning.
const ignoredConfigFile = "screencaster.yaml"

// renderFunc is the pipeline; tests pass a fake.
type renderFunc func(ctx context.Context, req renderer.Request) ([]renderer.Output, error)

// run executes the command line args and returns the exit code: 0 on
// success, 1 on any failure (FR-011). Progress and errors go to stderr, the
// output paths to stdout.
func run(ctx context.Context, args []string, workDir string, render renderFunc, stdout, stderr io.Writer) int {
	root := &cobra.Command{
		Use:           "screencaster",
		Short:         "Render demo scripts into narrated MP4 videos",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	if _, err := os.Stat(filepath.Join(workDir, ignoredConfigFile)); err == nil {
		_, _ = fmt.Fprintln(stderr, ignoredConfigFile+" is ignored; move its fields into the demo script")
	}
	root.AddCommand(renderCmd(workDir, render, stdout, stderr))
	root.SetArgs(args)
	root.SetOut(stderr)
	root.SetErr(stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		// ValidationErrors print one "pointer: message" per line; a Failure
		// prints its BR-004 one-liner.
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func renderCmd(workDir string, render renderFunc, stdout, stderr io.Writer) *cobra.Command {
	var langs []string
	cmd := &cobra.Command{
		Use:   "render <script>",
		Short: "Render a script synchronously, without the job queue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			outs, err := render(cmd.Context(), renderer.Request{
				WorkDir:      workDir,
				ScriptPath:   args[0],
				LangOverride: langs,
				Progress: func(lang string, i, n int, action, target string) {
					if target != "" {
						target = " " + target
					}
					_, _ = fmt.Fprintf(stderr, "[%s] step %d/%d %s%s\n", lang, i, n, action, target)
				},
				Log: func(msg string) { _, _ = fmt.Fprintln(stderr, msg) },
			})
			if err != nil {
				return err
			}
			for _, o := range outs {
				if _, err := fmt.Fprintln(stdout, o.Path); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&langs, "lang", nil, "languages to render, overriding the script (e.g. en,pl)")
	return cmd
}
