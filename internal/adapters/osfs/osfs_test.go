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

func TestReadDir_listsEntriesWithTheirTypes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "01.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := (FS{}).ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{} // name -> is a regular file
	for _, e := range entries {
		got[e.Name()] = e.Type().IsRegular()
	}
	if len(got) != 2 || !got["01.png"] || got["sub"] {
		t.Errorf("entries = %v, want 01.png as a file and sub as a directory", got)
	}
	if _, err := (FS{}).ReadDir(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadDir(missing) error = %v, want fs.ErrNotExist", err)
	}
}

func TestMakeReadable_addsReadAndExecuteLikeAplusrX(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	modes := map[string][2]fs.FileMode{ // path -> {before, after}
		filepath.Join(sub, "data"): {0o600, 0o644},
		filepath.Join(sub, "bin"):  {0o700, 0o755},
	}
	for p, m := range modes {
		if err := os.WriteFile(p, []byte("x"), m[0]); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("data", filepath.Join(sub, "link")); err != nil {
		t.Fatal(err)
	}
	if err := (FS{}).MakeReadable(dir); err != nil {
		t.Fatal(err)
	}
	for p, m := range modes {
		if st, _ := os.Stat(p); st.Mode().Perm() != m[1] {
			t.Errorf("%s mode %v, want %v", p, st.Mode().Perm(), m[1])
		}
	}
	if st, _ := os.Stat(sub); st.Mode().Perm() != 0o755 {
		t.Errorf("dir mode %v, want 0755", st.Mode().Perm())
	}
}

func TestMakeReadable_onlyTheGivenPathsAndMissingOnesAreSkipped(t *testing.T) {
	dir := t.TempDir()
	owned, other := filepath.Join(dir, "owned"), filepath.Join(dir, "other")
	for _, p := range []string{owned, other} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := (FS{}).MakeReadable(filepath.Join(dir, "missing"), owned); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(owned); st.Mode().Perm() != 0o644 {
		t.Errorf("owned mode %v, want 0644", st.Mode().Perm())
	}
	if st, _ := os.Stat(other); st.Mode().Perm() != 0o600 {
		t.Errorf("other mode %v, want 0600 (not given)", st.Mode().Perm())
	}
}

func TestWriteFile_replacesTheFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "setup.json")
	for _, content := range []string{"one", "two"} {
		if err := (FS{}).WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(p); string(b) != "two" {
		t.Errorf("file = %q, want two", b)
	}
}
