package host

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHasOnPath_findsOnlyExecutablesOnPATH(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	if (Host{}).HasOnPath("tool") {
		t.Error("found a tool in an empty PATH")
	}
	p := filepath.Join(dir, "tool")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if (Host{}).HasOnPath("tool") {
		t.Error("found a file without the execute bit")
	}
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if !(Host{}).HasOnPath("tool") {
		t.Error("missed an executable on PATH")
	}
}

func TestArch_isTheRuntimeArch(t *testing.T) {
	if got := (Host{}).Arch(); got != runtime.GOARCH {
		t.Errorf("Arch = %s, want %s", got, runtime.GOARCH)
	}
}
