// Package download fetches a file over HTTP with a sha256 check and unpacks
// tar.gz archives. It is the only HTTP client in the code and is reachable
// only from `setup` (decision 77); render stays offline (NFR-003).
package download

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// fetchTimeout bounds one download, voices of ~100 MB included. The caller's
// ctx still cancels earlier.
const fetchTimeout = 20 * time.Minute

// ErrChecksum means the downloaded bytes are not the pinned ones.
var ErrChecksum = errors.New("sha256 mismatch")

// Downloader implements setup.Fetcher. The zero value uses a client with
// fetchTimeout; tests set Client.
type Downloader struct{ Client *http.Client }

func (d Downloader) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return &http.Client{Timeout: fetchTimeout}
}

// Fetch downloads url to dst, replacing an existing file. The bytes go to
// dst+".part" first and are renamed only when their sha256 equals the pinned
// hex value, so a bad or partial download never leaves a file at dst.
func (d Downloader) Fetch(ctx context.Context, url, dst, sha string) (err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	part := dst + ".part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(part)
		}
	}()
	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(f, h), resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		return fmt.Errorf("GET %s: %w", url, copyErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sha {
		return fmt.Errorf("%w: want %s, got %s", ErrChecksum, sha, got)
	}
	return os.Rename(part, dst)
}

// Extract unpacks the tar.gz tgz into dstDir, overwriting existing files. It
// handles directories, regular files and relative symlinks that stay inside
// dstDir (the Piper tarball has nothing else) and rejects absolute paths, ".."
// escapes and any other entry type.
func (d Downloader) Extract(ctx context.Context, tgz, dstDir string) error {
	f, err := os.Open(tgz)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s: %w", tgz, err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", tgz, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := extractEntry(tr, h, dstDir); err != nil {
			return fmt.Errorf("%s: %s: %w", tgz, h.Name, err)
		}
	}
}

func extractEntry(r io.Reader, h *tar.Header, dstDir string) error {
	name, err := safeName(h.Name)
	if err != nil {
		return err
	}
	if name == "." {
		return nil
	}
	path := filepath.Join(dstDir, name)
	switch h.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(path, 0o755)
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		// Remove first: it replaces a symlink instead of writing through it
		// and a binary that is running.
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(h.Mode).Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, r); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	case tar.TypeSymlink:
		if filepath.IsAbs(h.Linkname) {
			return fmt.Errorf("symlink target %q is absolute", h.Linkname)
		}
		if _, err := safeName(filepath.Join(filepath.Dir(name), h.Linkname)); err != nil {
			return fmt.Errorf("symlink target %q leaves the destination", h.Linkname)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return os.Symlink(h.Linkname, path)
	default:
		return fmt.Errorf("unsupported entry type %q", h.Typeflag)
	}
}

// safeName cleans an archive path and rejects one that is absolute or climbs
// out of the destination.
func safeName(name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q leaves the destination", name)
	}
	return clean, nil
}
