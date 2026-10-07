package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"screencaster/internal/app/renderer"
)

func noEnv(string) string { return "" }

// emptyHome points the install dir at an empty folder, so the test does not
// depend on a real /opt/screencaster.
func emptyHome(t *testing.T) func(string) string {
	home := t.TempDir()
	return func(k string) string {
		if k == "SCREENCASTER_HOME" {
			return home
		}
		return ""
	}
}

func TestRenderWith_nothingInstalledFailsOnlyWhenRendering(t *testing.T) { // ARCHITECTURE §12
	render := renderWith(emptyHome(t))

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--help"}, t.TempDir(), render, setupConfig{}, &stdout, &stderr); code != 0 {
		t.Fatalf("--help exit code = %d, stderr %q", code, stderr.String())
	}

	_, err := render(context.Background(), renderer.Request{WorkDir: t.TempDir(), ScriptPath: "demos/a.yaml"})
	if err == nil || !strings.Contains(err.Error(), "sudo screencaster setup") {
		t.Fatalf("render error = %v, want it to point at `screencaster setup`", err)
	}
}
