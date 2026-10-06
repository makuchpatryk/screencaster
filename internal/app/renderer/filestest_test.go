package renderer

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"
	"testing/fstest"
)

// memFS is the Files fake: an fstest.MapFS that the write methods mutate.
// Keys are absolute paths without the leading "/". The fake tools write their
// outputs here too, so a test never touches the disk.
type memFS struct {
	mu sync.Mutex
	m  fstest.MapFS
	// mount, when set, is a second file system: a Rename with exactly one
	// side below it fails with EXDEV, as os.Rename does across devices.
	mount string
}

func newMemFS() *memFS { return &memFS{m: fstest.MapFS{}} }

func key(name string) string { return strings.TrimPrefix(path.Clean(name), "/") }

func pathErr(op, name string, err error) error { return &fs.PathError{Op: op, Path: name, Err: err} }

// write puts a file at name; its parents exist implicitly.
func (f *memFS) write(name, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[key(name)] = &fstest.MapFile{Data: []byte(content), Mode: 0o644}
}

// read returns the content of name, or "" with ok false when it is missing.
func (f *memFS) read(name string) (string, bool) {
	b, err := f.ReadFile(name)
	return string(b), err == nil
}

// exists reports whether name is a file or a directory.
func (f *memFS) exists(name string) bool {
	_, err := f.Stat(name)
	return err == nil
}

// list returns the names directly inside dir, sorted.
func (f *memFS) list(dir string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries, _ := fs.ReadDir(f.m, key(dir))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func (f *memFS) ReadFile(name string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fs.ReadFile(f.m, key(name))
}

func (f *memFS) Stat(name string) (fs.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fs.Stat(f.m, key(name))
}

func (f *memFS) Lstat(name string) (fs.FileInfo, error) { return f.Stat(name) }

func (f *memFS) Open(name string) (io.ReadCloser, error) {
	b, err := f.ReadFile(name)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (f *memFS) CreateExcl(name string) (io.WriteCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := key(name)
	if _, err := fs.Stat(f.m, k); err == nil {
		return nil, pathErr("open", name, fs.ErrExist)
	}
	if fi, err := fs.Stat(f.m, path.Dir(k)); err != nil || !fi.IsDir() {
		return nil, pathErr("open", name, fs.ErrNotExist)
	}
	f.m[k] = &fstest.MapFile{Mode: 0o644}
	return &memFile{fs: f, key: k}, nil
}

// memFile buffers writes and stores them on Close.
type memFile struct {
	fs  *memFS
	key string
	buf bytes.Buffer
}

func (w *memFile) Write(p []byte) (int, error) { return w.buf.Write(p) }

func (w *memFile) Close() error {
	w.fs.mu.Lock()
	defer w.fs.mu.Unlock()
	w.fs.m[w.key] = &fstest.MapFile{Data: w.buf.Bytes(), Mode: 0o644}
	return nil
}

func (f *memFS) MkdirAll(name string, perm fs.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := key(name)
	if fi, err := fs.Stat(f.m, k); err == nil {
		if !fi.IsDir() {
			return pathErr("mkdir", name, syscall.ENOTDIR)
		}
		return nil
	}
	f.m[k] = &fstest.MapFile{Mode: fs.ModeDir | perm}
	return nil
}

func (f *memFS) Rename(oldname, newname string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	from, to := key(oldname), key(newname)
	file, ok := f.m[from]
	if !ok || file.Mode.IsDir() {
		return pathErr("rename", oldname, fs.ErrNotExist)
	}
	if f.mount != "" && f.below(from) != f.below(to) {
		return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: syscall.EXDEV}
	}
	delete(f.m, from)
	f.m[to] = file
	return nil
}

func (f *memFS) below(k string) bool {
	m := key(f.mount)
	return k == m || strings.HasPrefix(k, m+"/")
}

func (f *memFS) Remove(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := key(name)
	if _, ok := f.m[k]; !ok {
		return pathErr("remove", name, fs.ErrNotExist)
	}
	delete(f.m, k)
	return nil
}

func (f *memFS) RemoveAll(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := key(name)
	for p := range f.m {
		if p == k || strings.HasPrefix(p, k+"/") {
			delete(f.m, p)
		}
	}
	return nil
}
