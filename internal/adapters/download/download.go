// Package download fetches a file over HTTP with a sha256 check and unpacks
// tar.gz and zip archives. It is the only HTTP client in the code and is reachable
// only from `setup` (decision 77); render stays offline (NFR-003).
package download

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

// extractRoot creates dstDir if needed and resolves it with EvalSymlinks: the
// real directory every entry's path must stay inside (M3/M2). Resolving once,
// up front, means writeEntry only ever has to reason about one real root.
func extractRoot(dstDir string) (string, error) {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dstDir, err)
	}
	root, err := filepath.EvalSymlinks(dstDir)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", dstDir, err)
	}
	return root, nil
}

// insideRoot reports whether path is root itself or lies under it.
func insideRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// resolveParent finds the deepest existing component of name's parent
// directory under root, refuses a dangling symlink there, and resolves what
// exists so far with EvalSymlinks, requiring the result to stay inside root
// (a symlinked parent planted by an earlier entry, e.g. "l -> ..", must not
// let a later entry "l/up/victim" write outside root). It returns the real
// directory the final component of name should be created or written under.
// name is already safeName-cleaned and relative, so it climbs out nowhere on
// its own; only a symlink actually on disk can send it outside root.
func resolveParent(root, name string) (string, error) {
	parent := filepath.Dir(name)
	if parent == "." {
		return root, nil // the entry sits directly under root
	}
	parts := strings.Split(parent, string(filepath.Separator))
	cur := root
	for i, part := range parts {
		next := filepath.Join(cur, part)
		fi, err := os.Lstat(next)
		if errors.Is(err, fs.ErrNotExist) {
			// Nothing here yet, so nothing deeper can exist either (MkdirAll
			// never leaves a symlink behind): join the rest literally.
			return filepath.Join(append([]string{cur}, parts[i:]...)...), nil
		}
		if err != nil {
			return "", fmt.Errorf("stat %s: %w", next, err)
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(next)
			if err != nil {
				return "", fmt.Errorf("resolve symlink %s: %w", next, err)
			}
			next = resolved
		}
		if !insideRoot(root, next) {
			return "", fmt.Errorf("%q resolves outside the destination", part)
		}
		cur = next
	}
	return cur, nil
}

// Extract unpacks the tar.gz tgz into dstDir, overwriting existing files. It
// handles directories, regular files and relative symlinks that stay inside
// dstDir (the Piper tarball has nothing else) and rejects absolute paths, ".."
// escapes, a symlinked parent that resolves outside dstDir, and any other
// entry type.
func (d Downloader) Extract(ctx context.Context, tgz, dstDir string) error {
	root, err := extractRoot(dstDir)
	if err != nil {
		return err
	}
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
		if err := extractEntry(tr, h, root); err != nil {
			return fmt.Errorf("%s: %s: %w", tgz, h.Name, err)
		}
	}
}

// entryKind is what an archive entry becomes on disk. tar and zip map their own
// type flags onto it, so one writer holds every path and mode rule.
type entryKind int

const (
	kindDir entryKind = iota
	kindFile
	kindSymlink
)

// maxLinkname bounds a zip symlink target, which is stored as the entry's content.
const maxLinkname = 4096

// ExtractFile unpacks the one regular entry named entry from the tar.gz tgz to
// the file dst with permission bits mode, replacing what is there (the pinned
// Node.js archive holds hundreds of files setup does not need; only bin/node
// is used). The entry must be a regular file; a missing entry is an error.
func (d Downloader) ExtractFile(ctx context.Context, tgz, entry, dst string, mode fs.FileMode) error {
	root, err := extractRoot(filepath.Dir(dst))
	if err != nil {
		return err
	}
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
			return fmt.Errorf("%s: no entry %q", tgz, entry)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", tgz, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if h.Name != entry {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return fmt.Errorf("%s: entry %q is not a regular file", tgz, entry)
		}
		if err := writeEntry(root, filepath.Base(dst), kindFile, mode.Perm(), "", tr); err != nil {
			return fmt.Errorf("%s: %s: %w", tgz, entry, err)
		}
		return nil
	}
}

func extractEntry(r io.Reader, h *tar.Header, root string) error {
	var kind entryKind
	switch h.Typeflag {
	case tar.TypeDir:
		kind = kindDir
	case tar.TypeReg:
		kind = kindFile
	case tar.TypeSymlink:
		kind = kindSymlink
	default:
		return fmt.Errorf("unsupported entry type %q", h.Typeflag)
	}
	return writeEntry(root, h.Name, kind, os.FileMode(h.Mode).Perm(), h.Linkname, r)
}

// ExtractZip unpacks the zip at zipPath into dstDir, overwriting existing files
// (Chromium, its headless shell and Playwright's ffmpeg). It follows the same
// rules as Extract: directories, regular files and relative symlinks that stay
// inside dstDir; absolute paths, ".." escapes, a symlinked parent that
// resolves outside dstDir, and any other entry type are rejected.
func (d Downloader) ExtractZip(ctx context.Context, zipPath, dstDir string) error {
	root, err := extractRoot(dstDir)
	if err != nil {
		return err
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("%s: %w", zipPath, err)
	}
	defer func() { _ = zr.Close() }()
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := extractZipEntry(f, root); err != nil {
			return fmt.Errorf("%s: %s: %w", zipPath, f.Name, err)
		}
	}
	return nil
}

func extractZipEntry(f *zip.File, root string) error {
	mode := f.Mode()
	var kind entryKind
	switch {
	case mode.IsDir():
		kind = kindDir
	case mode.IsRegular():
		kind = kindFile
	case mode&fs.ModeSymlink != 0:
		kind = kindSymlink
	default:
		return fmt.Errorf("unsupported entry type %v", mode.Type())
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	link := ""
	if kind == kindSymlink {
		// A zip stores a symlink's target as the entry's content.
		b, err := io.ReadAll(io.LimitReader(rc, maxLinkname+1))
		if err != nil {
			return err
		}
		if len(b) > maxLinkname {
			return errors.New("symlink target too long")
		}
		link = string(b)
	}
	return writeEntry(root, f.Name, kind, mode.Perm(), link, rc)
}

// writeEntry is the one place an archive entry reaches the disk, for tar and
// zip alike: the path is cleaned against dstDir, a symlink must be relative and
// stay inside it, and mode keeps only the permission bits.
func writeEntry(root, entryName string, kind entryKind, mode fs.FileMode, link string, r io.Reader) error {
	name, err := safeName(entryName)
	if err != nil {
		return err
	}
	if name == "." {
		return nil
	}
	// resolveParent catches what safeName cannot: an earlier entry's symlink
	// (e.g. "l -> ..") as an ancestor of name, which would otherwise let this
	// write land outside root even though name itself is clean (M2).
	parentDir, err := resolveParent(root, name)
	if err != nil {
		return err
	}
	path := filepath.Join(parentDir, filepath.Base(name))
	switch kind {
	case kindDir:
		return os.MkdirAll(path, 0o755)
	case kindFile:
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		// Remove first: it replaces a symlink instead of writing through it
		// and a binary that is running.
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, r); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	case kindSymlink:
		if filepath.IsAbs(link) {
			return fmt.Errorf("symlink target %q is absolute", link)
		}
		if target := filepath.Clean(filepath.Join(parentDir, link)); !insideRoot(root, target) {
			return fmt.Errorf("symlink target %q leaves the destination", link)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return os.Symlink(link, path)
	default:
		return fmt.Errorf("unsupported entry kind %d", kind)
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
