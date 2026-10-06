package renderer

import (
	"io"
	"io/fs"
)

// Files is the file system the plan and publish steps touch. It is a test
// seam: adapters/osfs is the real one, the unit tests use an in-memory fake.
type Files interface {
	ReadFile(name string) ([]byte, error)
	Stat(name string) (fs.FileInfo, error)
	Lstat(name string) (fs.FileInfo, error)
	Open(name string) (io.ReadCloser, error)
	// CreateExcl creates name for writing and fails if it exists, so no
	// existing file is ever opened for writing (BR-006).
	CreateExcl(name string) (io.WriteCloser, error)
	MkdirAll(name string, perm fs.FileMode) error
	// Rename may fail with syscall.EXDEV across file systems.
	Rename(oldname, newname string) error
	Remove(name string) error
	RemoveAll(name string) error
}
