// Package osfs is the real file system behind renderer.Files: each method is
// the os function of the same name.
package osfs

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// FS is the operating system's file system.
type FS struct{}

func (FS) ReadFile(name string) ([]byte, error)    { return os.ReadFile(name) }
func (FS) Stat(name string) (fs.FileInfo, error)   { return os.Stat(name) }
func (FS) Lstat(name string) (fs.FileInfo, error)  { return os.Lstat(name) }
func (FS) Open(name string) (io.ReadCloser, error) { return os.Open(name) }

func (FS) ReadDir(name string) ([]fs.DirEntry, error) { return os.ReadDir(name) }

// CreateExcl opens name with O_EXCL, so an existing file is never written
// (BR-006).
func (FS) CreateExcl(name string) (io.WriteCloser, error) {
	return os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
}

func (FS) MkdirAll(name string, perm fs.FileMode) error { return os.MkdirAll(name, perm) }
func (FS) Rename(oldname, newname string) error         { return os.Rename(oldname, newname) }
func (FS) Remove(name string) error                     { return os.Remove(name) }
func (FS) RemoveAll(name string) error                  { return os.RemoveAll(name) }

// WriteFile replaces name. It is for `setup`'s marker, never for an output
// (BR-006: renderer.Files has no such method).
func (FS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(name, data, perm)
}

// MakeReadable does `chmod -R a+rX` on each of paths: everything gets read
// bits, and directories and executables get execute bits, so any user can
// render from an install dir that root filled. A path that does not exist is
// skipped; symlinks are left alone, including a path that is one.
func (FS) MakeReadable(paths ...string) error {
	for _, root := range paths {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.Type()&fs.ModeSymlink != 0 {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			mode := info.Mode().Perm() | 0o444
			if d.IsDir() || mode&0o111 != 0 {
				mode |= 0o111
			}
			return os.Chmod(path, mode)
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}
