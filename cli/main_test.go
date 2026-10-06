package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"screencaster/core/renderer"
)

func noEnv(string) string { return "" }

func TestRenderWith_noProviderFailsOnlyWhenRendering(t *testing.T) { // ARCHITECTURE §12
	render := renderWith(noEnv)

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--help"}, t.TempDir(), render, &stdout, &stderr); code != 0 {
		t.Fatalf("--help exit code = %d, stderr %q", code, stderr.String())
	}

	_, err := render(context.Background(), renderer.Request{WorkDir: t.TempDir(), ScriptPath: "demos/a.yaml"})
	if err == nil || !strings.Contains(err.Error(), "SCREENCASTER_TTS") {
		t.Fatalf("render error = %v, want it to name SCREENCASTER_TTS", err)
	}
}
