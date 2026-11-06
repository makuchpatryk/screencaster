package download

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
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

// A symlink "l" to "." looks harmless on its own, and a second symlink
// "l/up" to ".." looks safe by the OLD lexical-only check too (Dir("l/up")
// + ".." cleans to "."): it never asks what "l" itself resolves to. But "l"
// resolves to dst itself, so "l/up" really lands at dst/up on disk, whose
// target ".." truly escapes dst. resolveParent must catch it at "l/up"
// itself, before the third entry ever runs (M2).
func TestExtract_refusesWriteThroughSymlinkedParent(t *testing.T) {
	tgz := archive(t,
		entry{name: "l", typ: tar.TypeSymlink, link: "."},
		entry{name: "l/up", typ: tar.TypeSymlink, link: ".."},
		entry{name: "l/up/victim", typ: tar.TypeReg, body: "pwned", mode: 0o644},
	)
	root := t.TempDir()
	dst := filepath.Join(root, "dst")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (Downloader{}).Extract(context.Background(), tgz, dst); err == nil {
		t.Fatal("want an error")
	}
	if _, err := os.Lstat(filepath.Join(root, "up")); err == nil {
		t.Error("the escaping symlink was created outside dst")
	}
	if _, err := os.Lstat(filepath.Join(root, "victim")); err == nil {
		t.Error("victim written outside dst")
	}
}

// "link" -> "real" is a legitimate in-tree symlink (both inside dst); writing
// through it must still work.
func TestExtract_inTreeSymlinkedParentStillExtracts(t *testing.T) {
	tgz := archive(t,
		entry{name: "real/", typ: tar.TypeDir, mode: 0o755},
		entry{name: "link", typ: tar.TypeSymlink, link: "real"},
		entry{name: "link/file", typ: tar.TypeReg, body: "ok", mode: 0o644},
	)
	dst := t.TempDir()
	if err := (Downloader{}).Extract(context.Background(), tgz, dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "real", "file")); err != nil || string(b) != "ok" {
		t.Errorf("real/file = %q, %v; want ok", b, err)
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

// zentry is one zip entry: a directory (name ends in "/"), a file or a symlink.
type zentry struct {
	name, body string
	mode       fs.FileMode // permission bits, plus fs.ModeSymlink or another type
}

func zipArchive(t *testing.T, entries ...zentry) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Store}
		h.SetMode(e.mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "a.zip")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractZip_extractsFilesModesAndSymlinks(t *testing.T) {
	zp := zipArchive(t,
		zentry{name: "chrome-linux64/", mode: fs.ModeDir | 0o755},
		zentry{name: "chrome-linux64/chrome", body: "bin", mode: 0o755},
		zentry{name: "chrome-linux64/data.pak", body: "pak", mode: 0o644},
		zentry{name: "chrome-linux64/libx.so", body: "data.pak", mode: fs.ModeSymlink | 0o777},
	)
	dst := t.TempDir()
	if err := (Downloader{}).ExtractZip(context.Background(), zp, dst); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dst, "chrome-linux64", "chrome"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Errorf("chrome mode %v, want 0755", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Join(dst, "chrome-linux64", "data.pak")); st.Mode().Perm() != 0o644 {
		t.Errorf("data.pak mode %v, want 0644", st.Mode().Perm())
	}
	if b, err := os.ReadFile(filepath.Join(dst, "chrome-linux64", "libx.so")); err != nil || string(b) != "pak" {
		t.Errorf("symlink read = %q, %v; want pak", b, err)
	}
}

func TestExtractZip_overwritesExistingFiles(t *testing.T) {
	zp := zipArchive(t, zentry{name: "ffmpeg-linux", body: "new", mode: 0o755})
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(dst, "ffmpeg-linux"), []byte("old"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := (Downloader{}).ExtractZip(context.Background(), zp, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "ffmpeg-linux")); string(b) != "new" {
		t.Errorf("content %q, want new", b)
	}
}

func TestExtractZip_rejectsUnsafeEntries(t *testing.T) {
	tests := []struct {
		name string
		e    zentry
	}{
		{"dot-dot path", zentry{name: "../x", body: "x", mode: 0o644}},
		{"nested dot-dot", zentry{name: "chrome/../../x", body: "x", mode: 0o644}},
		{"absolute path", zentry{name: "/tmp/x", body: "x", mode: 0o644}},
		{"absolute symlink", zentry{name: "chrome/l", body: "/etc/passwd", mode: fs.ModeSymlink | 0o777}},
		{"escaping symlink", zentry{name: "chrome/l", body: "../../etc/passwd", mode: fs.ModeSymlink | 0o777}},
		{"named pipe", zentry{name: "chrome/p", mode: fs.ModeNamedPipe | 0o644}},
		{"device", zentry{name: "chrome/d", mode: fs.ModeDevice | 0o644}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dst := filepath.Join(root, "dst")
			if err := os.Mkdir(dst, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := (Downloader{}).ExtractZip(context.Background(), zipArchive(t, tt.e), dst); err == nil {
				t.Fatal("want an error")
			}
			if _, err := os.Stat(filepath.Join(root, "x")); err == nil {
				t.Error("file written outside the destination")
			}
			for _, leftover := range []string{"l", "p", "d"} {
				if _, err := os.Lstat(filepath.Join(dst, "chrome", leftover)); err == nil {
					t.Errorf("%s created", leftover)
				}
			}
		})
	}
}

// Zip equivalent of TestExtract_refusesWriteThroughSymlinkedParent.
func TestExtractZip_refusesWriteThroughSymlinkedParent(t *testing.T) {
	zp := zipArchive(t,
		zentry{name: "l", body: ".", mode: fs.ModeSymlink | 0o777},
		zentry{name: "l/up", body: "..", mode: fs.ModeSymlink | 0o777},
		zentry{name: "l/up/victim", body: "pwned", mode: 0o644},
	)
	root := t.TempDir()
	dst := filepath.Join(root, "dst")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (Downloader{}).ExtractZip(context.Background(), zp, dst); err == nil {
		t.Fatal("want an error")
	}
	if _, err := os.Lstat(filepath.Join(root, "up")); err == nil {
		t.Error("the escaping symlink was created outside dst")
	}
	if _, err := os.Lstat(filepath.Join(root, "victim")); err == nil {
		t.Error("victim written outside dst")
	}
}

// Zip equivalent of TestExtract_inTreeSymlinkedParentStillExtracts.
func TestExtractZip_inTreeSymlinkedParentStillExtracts(t *testing.T) {
	zp := zipArchive(t,
		zentry{name: "real/", mode: fs.ModeDir | 0o755},
		zentry{name: "link", body: "real", mode: fs.ModeSymlink | 0o777},
		zentry{name: "link/file", body: "ok", mode: 0o644},
	)
	dst := t.TempDir()
	if err := (Downloader{}).ExtractZip(context.Background(), zp, dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "real", "file")); err != nil || string(b) != "ok" {
		t.Errorf("real/file = %q, %v; want ok", b, err)
	}
}

func TestExtractZip_notAZipIsAnError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.zip")
	if err := os.WriteFile(p, []byte("plain"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (Downloader{}).ExtractZip(context.Background(), p, t.TempDir()); err == nil {
		t.Error("want an error")
	}
}

func TestExtractZip_cancelledContextStopsBeforeWriting(t *testing.T) {
	zp := zipArchive(t, zentry{name: "f", body: "x", mode: 0o644})
	dst := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (Downloader{}).ExtractZip(ctx, zp, dst); err == nil {
		t.Fatal("want an error")
	}
	if entries, _ := os.ReadDir(dst); len(entries) != 0 {
		t.Errorf("files left behind: %v", entries)
	}
}

func TestExtractFile_extractsOneEntryWithTheGivenMode(t *testing.T) {
	tgz := archive(t,
		entry{name: "node-v1/", typ: tar.TypeDir, mode: 0o755},
		entry{name: "node-v1/bin/node", typ: tar.TypeReg, body: "node", mode: 0o644},
		entry{name: "node-v1/bin/npm", typ: tar.TypeReg, body: "npm", mode: 0o755},
	)
	dst := filepath.Join(t.TempDir(), "driver", "node")
	if err := (Downloader{}).ExtractFile(context.Background(), tgz, "node-v1/bin/node", dst, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Errorf("mode %v, want 0755 (the caller's, not the archive's)", st.Mode().Perm())
	}
	if b, _ := os.ReadFile(dst); string(b) != "node" {
		t.Errorf("content %q, want node", b)
	}
	if entries, _ := os.ReadDir(filepath.Dir(dst)); len(entries) != 1 {
		t.Errorf("driver dir has %d entries, want only node", len(entries))
	}
}

func TestExtractFile_failures(t *testing.T) {
	tgz := archive(t,
		entry{name: "d/node", typ: tar.TypeReg, body: "node", mode: 0o755},
		entry{name: "d/link", typ: tar.TypeSymlink, link: "node"},
	)
	tests := []struct{ name, entry, want string }{
		{"missing entry", "d/nope", "no entry"},
		{"symlink entry", "d/link", "not a regular file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			err := (Downloader{}).ExtractFile(context.Background(), tgz, tt.entry, filepath.Join(dir, "node"), 0o755)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %v, want one containing %q", err, tt.want)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("files left behind: %v", entries)
			}
		})
	}
}
