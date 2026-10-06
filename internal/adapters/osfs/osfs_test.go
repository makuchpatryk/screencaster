package osfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateExcl_refusesExistingFile(t *testing.T) { // BR-006
	p := filepath.Join(t.TempDir(), "out.mp4")
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (FS{}).CreateExcl(p); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("CreateExcl() error = %v, want fs.ErrExist", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "old" {
		t.Errorf("existing file changed to %q", b)
	}
}

func TestCreateExcl_createsNewFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "out.mp4")
	w, err := (FS{}).CreateExcl(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "new" {
		t.Errorf("file = %q, want new", b)
	}
}
