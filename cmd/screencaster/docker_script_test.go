package main

import (
	"os"
	"strings"
	"testing"
)

// scriptTemplate is the template the release workflow fills in (decision 77).
const scriptTemplate = "../../scripts/screencaster-docker.sh"

func filledScript(t *testing.T, version string) string {
	t.Helper()
	b, err := os.ReadFile(scriptTemplate)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "@VERSION@", version)
}

func TestDockerScript_runsTheTaggedImageWithTheRenderFlags(t *testing.T) {
	script := filledScript(t, "9.9.9")
	tests := []struct{ name, want string }{
		{"posix shell", "#!/bin/sh\n"},
		{"fails on error", "set -eu"},
		{"removes the container", "docker run --rm"},
		{"forwards signals", "--init"},
		{"runs as the caller", `--user "$(id -u):$(id -g)"`},
		{"mounts the project", `-v "$PWD:/work"`},
		{"works in the project", "-w /work"},
		{"the image has no ENTRYPOINT", "--entrypoint screencaster"},
		{"image tag is the version", "ghcr.io/makuchpatryk/screencaster:9.9.9"},
		{"passes the arguments through", `"$@"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(script, tt.want) {
				t.Errorf("script lacks %q", tt.want)
			}
		})
	}
}

// Decision 79: Chromium needs no --shm-size and a host app is reached by IP, so
// neither flag is in the wrapper.
func TestDockerScript_hasNoShmSizeAndNoAddHost(t *testing.T) {
	script := filledScript(t, "9.9.9")
	for _, flag := range []string{"--shm-size", "--add-host"} {
		if strings.Contains(script, flag) {
			t.Errorf("script contains %q", flag)
		}
	}
}

func TestDockerScript_templateHasOnePlaceholderAndFillingLeavesNone(t *testing.T) {
	b, err := os.ReadFile(scriptTemplate)
	if err != nil {
		t.Fatal(err)
	}
	// The workflow fills it with a one-per-line sed, so exactly one.
	if n := strings.Count(string(b), "@VERSION@"); n != 1 {
		t.Errorf("template has %d @VERSION@, want 1 (the image tag)", n)
	}
	if strings.Contains(filledScript(t, "9.9.9"), "@VERSION@") {
		t.Error("placeholder left after filling")
	}
}

func TestDockerScript_isExecutable(t *testing.T) {
	st, err := os.Stat(scriptTemplate)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o111 == 0 {
		t.Errorf("mode %v, want an execute bit", st.Mode().Perm())
	}
}
