package renderer

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
)

// timestampLayout is the FR-010 job-start format, UTC.
const timestampLayout = "20060102T150405Z"

// OutputName is the one place that builds <name>.<lang>.<timestamp>.mp4
// (FR-010). ts is the job start time.
func OutputName(name, lang string, ts time.Time) string {
	return fmt.Sprintf("%s.%s.%s.mp4", name, lang, ts.UTC().Format(timestampLayout))
}

// publish moves the temp MP4s into outputDir under their final names, all
// with the same timestamp (FR-010). No existing file is ever opened for
// writing (BR-006): every target is checked first. If a move fails, the files
// this call already published are removed again, so a failed render leaves
// outputDir as it was (BR-004).
func publish(files Files, outputDir, name string, ts time.Time, outs []Output) ([]Output, error) {
	if err := files.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}
	dsts := make([]string, len(outs))
	for i, o := range outs {
		dsts[i] = filepath.Join(outputDir, OutputName(name, o.Lang, ts))
		if _, err := files.Lstat(dsts[i]); err == nil {
			return nil, fmt.Errorf("output already exists: %s", dsts[i])
		}
	}

	published := make([]Output, 0, len(outs))
	for i, o := range outs {
		if err := move(files, o.Path, dsts[i]); err != nil {
			for _, p := range published {
				_ = files.Remove(p.Path)
			}
			return nil, err
		}
		published = append(published, Output{Lang: o.Lang, Path: dsts[i], DurationMs: o.DurationMs})
	}
	return published, nil
}

// shotFile matches the names domain/shooter.ShotName gives a PNG: two digits,
// or three past 99 shots. Only such files in the screenshots folder belong to
// the tool.
var shotFile = regexp.MustCompile(`^\d{2,3}\.png$`)

// publishShots moves the temp PNGs into dir under their own names, replacing
// the files of the previous run, then removes the NN.png files this run did
// not write. It never removes the folder or any other file. This is the
// deliberate exception to BR-006: the paths are stable so docs can embed the
// images (decision 73). Rename replaces a target, and a cross-filesystem copy
// still goes through <dst>.part created with CreateExcl, so no existing file is
// opened for writing. If a move fails part-way, dir holds a mix of new and old
// shots and the error names the one that failed.
func publishShots(files Files, dir string, src []string) ([]string, error) {
	if err := files.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create screenshots dir: %w", err)
	}
	dsts := make([]string, len(src))
	fresh := make(map[string]bool, len(src))
	for i, s := range src {
		dsts[i] = filepath.Join(dir, filepath.Base(s))
		if err := move(files, s, dsts[i]); err != nil {
			return nil, err
		}
		fresh[filepath.Base(s)] = true
	}

	entries, err := files.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read screenshots dir: %w", err)
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !shotFile.MatchString(e.Name()) || fresh[e.Name()] {
			continue
		}
		if err := files.Remove(filepath.Join(dir, e.Name())); err != nil {
			return nil, fmt.Errorf("remove stale screenshot: %w", err)
		}
	}
	return dsts, nil
}

// move renames src to dst. When they are on different filesystems it copies
// to <dst>.part in the target directory and renames that, so dst never exists
// half-written (ARCHITECTURE §4, rule 5).
func move(files Files, src, dst string) error {
	err := files.Rename(src, dst)
	if errors.Is(err, syscall.EXDEV) {
		return copyThenRename(files, src, dst)
	}
	if err != nil {
		return fmt.Errorf("publish %s: %w", dst, err)
	}
	return nil
}

func copyThenRename(files Files, src, dst string) (err error) {
	part := dst + ".part"
	in, err := files.Open(src)
	if err != nil {
		return fmt.Errorf("publish %s: %w", dst, err)
	}
	defer func() { _ = in.Close() }()

	// Never open an existing file for writing (BR-006).
	out, err := files.CreateExcl(part)
	if err != nil {
		return fmt.Errorf("publish %s: %w", dst, err)
	}
	defer func() {
		if err != nil {
			_ = files.Remove(part)
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("publish %s: %w", dst, err)
	}
	if err = out.Close(); err != nil {
		return fmt.Errorf("publish %s: %w", dst, err)
	}
	if err = files.Rename(part, dst); err != nil {
		return fmt.Errorf("publish %s: %w", dst, err)
	}
	return nil
}
