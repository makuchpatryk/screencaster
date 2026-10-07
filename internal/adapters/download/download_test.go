package download

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func server(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestFetch_goodSha256RenamesIntoPlace(t *testing.T) {
	url := server(t, "payload")
	dst := filepath.Join(t.TempDir(), "sub", "voice.onnx")
	if err := (Downloader{}).Fetch(context.Background(), url, dst, sum("payload")); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(dst); err != nil || string(b) != "payload" {
		t.Errorf("dst = %q, %v; want payload", b, err)
	}
	if _, err := os.Stat(dst + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".part left behind: %v", err)
	}
}

func TestFetch_replacesAnExistingFile(t *testing.T) {
	url := server(t, "new")
	dst := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (Downloader{}).Fetch(context.Background(), url, dst, sum("new")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "new" {
		t.Errorf("dst = %q, want new", b)
	}
}

func TestFetch_failuresLeaveNoFile(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		sha     string
		want    error
		wantMsg string
	}{
		{"bad sha256", "/f", sum("other"), ErrChecksum, "want " + sum("other") + ", got " + sum("payload")},
		{"http 404", "/missing", sum("payload"), nil, "404"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := server(t, "payload")
			dir := t.TempDir()
			dst := filepath.Join(dir, "f")
			err := (Downloader{}).Fetch(context.Background(), url+tt.path, dst, tt.sha)
			if err == nil {
				t.Fatal("want an error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("error %v, want %v", err, tt.want)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q lacks %q", err, tt.wantMsg)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Errorf("files left behind: %v", entries)
			}
		})
	}
}

func TestFetch_cancelledContextLeavesNoFile(t *testing.T) {
	url := server(t, "payload")
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (Downloader{}).Fetch(ctx, url, filepath.Join(dir, "f"), sum("payload")); err == nil {
		t.Fatal("want an error")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("files left behind: %v", entries)
	}
}

type entry struct {
	name, body, link string
	typ              byte
	mode             int64
}

func archive(t *testing.T, entries ...entry) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, Linkname: e.link, Size: int64(len(e.body))}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "a.tgz")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtract_keepsTopFolderModesAndSymlinks(t *testing.T) {
	tgz := archive(t,
		entry{name: "piper/", typ: tar.TypeDir, mode: 0o755},
		entry{name: "piper/piper", typ: tar.TypeReg, body: "bin", mode: 0o755},
		entry{name: "piper/libx.so.1", typ: tar.TypeReg, body: "lib", mode: 0o644},
		entry{name: "piper/libx.so", typ: tar.TypeSymlink, link: "libx.so.1"},
	)
	dst := t.TempDir()
	if err := (Downloader{}).Extract(context.Background(), tgz, dst); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dst, "piper", "piper"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Errorf("piper mode %v, want 0755", st.Mode().Perm())
	}
	if b, err := os.ReadFile(filepath.Join(dst, "piper", "libx.so")); err != nil || string(b) != "lib" {
		t.Errorf("symlink read = %q, %v; want lib", b, err)
	}
}

func TestExtract_overwritesExistingFiles(t *testing.T) {
	tgz := archive(t, entry{name: "piper/piper", typ: tar.TypeReg, body: "new", mode: 0o755})
	dst := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dst, "piper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "piper", "piper"), []byte("old"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := (Downloader{}).Extract(context.Background(), tgz, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "piper", "piper")); string(b) != "new" {
		t.Errorf("content %q, want new", b)
	}
}

func TestExtract_rejectsUnsafeEntries(t *testing.T) {
	tests := []struct {
		name string
		e    entry
	}{
		{"dot-dot path", entry{name: "../x", typ: tar.TypeReg, body: "x", mode: 0o644}},
		{"nested dot-dot", entry{name: "piper/../../x", typ: tar.TypeReg, body: "x", mode: 0o644}},
		{"absolute path", entry{name: "/tmp/x", typ: tar.TypeReg, body: "x", mode: 0o644}},
		{"absolute symlink", entry{name: "piper/l", typ: tar.TypeSymlink, link: "/etc/passwd"}},
		{"escaping symlink", entry{name: "piper/l", typ: tar.TypeSymlink, link: "../../etc/passwd"}},
		{"hard link", entry{name: "piper/h", typ: tar.TypeLink, link: "piper/piper"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dst := filepath.Join(root, "dst")
			if err := os.Mkdir(dst, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := (Downloader{}).Extract(context.Background(), archive(t, tt.e), dst); err == nil {
				t.Fatal("want an error")
			}
			// Nothing may appear outside dst, nor anything at all in it.
			if _, err := os.Stat(filepath.Join(root, "x")); err == nil {
				t.Error("file written outside the destination")
			}
			if _, err := os.Lstat(filepath.Join(dst, "piper", "l")); err == nil {
				t.Error("symlink created")
			}
		})
	}
}

func TestExtract_notGzipIsAnError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.tgz")
	if err := os.WriteFile(p, []byte("plain"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (Downloader{}).Extract(context.Background(), p, t.TempDir()); err == nil {
		t.Error("want an error")
	}
}
