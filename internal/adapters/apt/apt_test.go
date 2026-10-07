package apt

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
	env  []string
	argv []string
}

// fakeRun replaces run for the test and returns the calls it saw.
func fakeRun(t *testing.T, failOn string) *[]call {
	t.Helper()
	old := run
	t.Cleanup(func() { run = old })
	var calls []call
	run = func(_ context.Context, _ io.Writer, env []string, name string, args ...string) error {
		calls = append(calls, call{env, append([]string{name}, args...)})
		if failOn != "" && slices.Contains(args, failOn) {
			return errors.New("exit status 100")
		}
		return nil
	}
	return &calls
}

func TestInstallFFmpeg_updatesThenInstallsNonInteractively(t *testing.T) {
	calls := fakeRun(t, "")
	if err := (Apt{}).InstallFFmpeg(context.Background(), io.Discard); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"apt-get", "update"}, {"apt-get", "install", "-y", "ffmpeg"}}
	if len(*calls) != len(want) {
		t.Fatalf("calls %v, want %v", *calls, want)
	}
	for i, c := range *calls {
		if !slices.Equal(c.argv, want[i]) {
			t.Errorf("call %d = %v, want %v", i, c.argv, want[i])
		}
		if !slices.Contains(c.env, "DEBIAN_FRONTEND=noninteractive") {
			t.Errorf("call %d env %v lacks DEBIAN_FRONTEND=noninteractive", i, c.env)
		}
	}
}

func TestInstallFFmpeg_failureNamesTheCommandAndStops(t *testing.T) {
	tests := []struct{ failOn, want string }{
		{"update", "apt-get update"},
		{"ffmpeg", "apt-get install -y ffmpeg"},
	}
	for _, tt := range tests {
		t.Run(tt.failOn, func(t *testing.T) {
			calls := fakeRun(t, tt.failOn)
			err := (Apt{}).InstallFFmpeg(context.Background(), io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v, want one naming %q", err, tt.want)
			}
			if tt.failOn == "update" && len(*calls) != 1 {
				t.Errorf("install ran after a failed update: %v", *calls)
			}
		})
	}
}

func TestHasApt_followsPATH(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	if (Apt{}).HasApt() {
		t.Error("HasApt true with an empty PATH")
	}
	if err := os.WriteFile(filepath.Join(dir, "apt-get"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !(Apt{}).HasApt() {
		t.Error("HasApt false with apt-get on PATH")
	}
}
