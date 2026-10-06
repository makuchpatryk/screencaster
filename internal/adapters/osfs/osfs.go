// Package osfs is the real file system behind renderer.Files: each method is
// the os function of the same name.
package osfs

import (
	"io"
	"io/fs"
	"os"
)

// FS is the operating system's file system.
type FS struct{}

func (FS) ReadFile(name string) ([]byte, error)    { return os.ReadFile(name) }
func (FS) Stat(name string) (fs.FileInfo, error)   { return os.Stat(name) }
func (FS) Lstat(name string) (fs.FileInfo, error)  { return os.Lstat(name) }
func (FS) Open(name string) (io.ReadCloser, error) { return os.Open(name) }

// CreateExcl opens name with O_EXCL, so an existing file is never written
// (BR-006).
func (FS) CreateExcl(name string) (io.WriteCloser, error) {
	return os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
}

func (FS) MkdirAll(name string, perm fs.FileMode) error { return os.MkdirAll(name, perm) }
func (FS) Rename(oldname, newname string) error         { return os.Rename(oldname, newname) }
func (FS) Remove(name string) error                     { return os.Remove(name) }
func (FS) RemoveAll(name string) error                  { return os.RemoveAll(name) }
