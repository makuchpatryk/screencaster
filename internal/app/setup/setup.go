// Package setup is the use case behind `screencaster setup`: it checks the
// native install (Piper, voices, the Playwright driver, Chromium, ffmpeg) and
// downloads or installs what is missing. It is the one explicit download
// command; render stays offline (NFR-003, decision 77). It reaches every tool
// through the ports below; app/wire maps them onto the adapters.
package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
)

// Fetcher downloads and unpacks. Fetch verifies sha256 (hex) before dst
// exists; Extract unpacks a tar.gz into dstDir, overwriting what is there.
type Fetcher interface {
	Fetch(ctx context.Context, url, dst, sha256 string) error
	Extract(ctx context.Context, tgz, dstDir string) error
}

// Browser installs the Playwright driver and Chromium. One call does both, as
// playwright.Install does; withDeps adds Chromium's system libraries (apt).
type Browser interface {
	Install(ctx context.Context, driverDir, browsersDir string, withDeps bool, out io.Writer) error
}

// Packages is the system package manager (apt) for ffmpeg.
type Packages interface {
	HasApt() bool
	InstallFFmpeg(ctx context.Context, out io.Writer) error
}

// Files is the file system the checks and the marker touch.
type Files interface {
	Open(name string) (io.ReadCloser, error)
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm fs.FileMode) error
	Stat(name string) (fs.FileInfo, error)
	ReadDir(name string) ([]fs.DirEntry, error)
	MkdirAll(name string, perm fs.FileMode) error
	Remove(name string) error
	// MakeReadable does `chmod -R a+rX` on each path that exists, so any user
	// can render. Paths that are missing are skipped.
	MakeReadable(paths ...string) error
}

// Host answers questions about the machine.
type Host interface {
	IsRoot() bool
	Arch() string // runtime.GOARCH
	HasOnPath(name string) bool
}

// Deps are the ports Check and Run use.
type Deps struct {
	Fetch    Fetcher
	Browser  Browser
	Packages Packages
	Files    Files
	Host     Host
}

// Options select the install. Dir is the install dir. Pins say what to
// download (see PinsFromEnv). Version tags the driver and Chromium in the
// marker, so a new release reinstalls them. Out receives progress and the
// installers' output; nil discards it.
type Options struct {
	Dir     string
	Pins    Pins
	Version string
	Out     io.Writer
}

// Status is the state of one piece.
type Status string

const (
	StatusOK      Status = "ok"
	StatusMissing Status = "missing"
	StatusBad     Status = "bad"
)

// Pieces, in check and install order.
const (
	PieceChromium = "chromium"
	PieceDriver   = "driver"
	PieceFFmpeg   = "ffmpeg"
	PiecePiper    = "piper"
	PieceVoices   = "voices"
)

// PieceStatus is one line of the report.
type PieceStatus struct {
	Piece  string
	Status Status
	Detail string
}

func (p PieceStatus) String() string {
	s := p.Piece + ": " + string(p.Status)
	if p.Detail != "" {
		s += " (" + p.Detail + ")"
	}
	return s
}

// Report lists the pieces in order: piper, voices, driver, chromium, ffmpeg.
type Report []PieceStatus

// AllOK reports whether every piece is ok.
func (r Report) AllOK() bool {
	for _, p := range r {
		if p.Status != StatusOK {
			return false
		}
	}
	return true
}

// ErrNotRoot is returned by Run when it is not started as root.
var ErrNotRoot = errors.New("setup must run as root (try: sudo ./screencaster setup)")

// The install dir layout, the one place that knows it: wire reads the same
// paths when it builds the render pipeline.

// PiperBin is the Piper binary; the tarball's own top folder is piper/.
func PiperBin(dir string) string { return filepath.Join(dir, "piper", "piper") }

// VoicesDir holds the built-in voices.
func VoicesDir(dir string) string { return filepath.Join(dir, "piper", "voices") }

// DriverDir is the Playwright driver (node and package).
func DriverDir(dir string) string { return filepath.Join(dir, "playwright-driver") }

// BrowsersDir is PLAYWRIGHT_BROWSERS_PATH.
func BrowsersDir(dir string) string { return filepath.Join(dir, "ms-playwright") }

func markerPath(dir string) string { return filepath.Join(dir, "setup.json") }

const (
	supportedArch = "amd64" // Piper is pinned to linux x86_64
	ffprobe       = "ffprobe"
	ffmpeg        = "ffmpeg"
	chromiumDir   = "chromium-"
)

// marker records what setup installed: piece -> tag. It is not trusted alone;
// Check also looks for the files.
type marker map[string]string

// is reports whether the marker records tag for piece.
func (m marker) is(piece, tag string) bool {
	got, ok := m[piece]
	return ok && got == tag
}

func readMarker(files Files, dir string) (marker, error) {
	b, err := files.ReadFile(markerPath(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return marker{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", markerPath(dir), err)
	}
	var m marker
	if json.Unmarshal(b, &m) != nil || m == nil {
		return marker{}, nil // unreadable: treat as empty, the pieces get re-verified
	}
	return m, nil
}

func writeMarker(files Files, dir string, m marker) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := files.WriteFile(markerPath(dir), append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", markerPath(dir), err)
	}
	return nil
}

// state is the result of one read-only check: the report plus what Run needs
// to repair.
type state struct {
	report    Report
	marker    marker
	badVoices []VoiceFile
}

func preflight(d Deps) error {
	if arch := d.Host.Arch(); arch != supportedArch {
		return fmt.Errorf("unsupported architecture %s: setup supports %s only (Piper is pinned to x86_64)", arch, supportedArch)
	}
	return nil
}

// Check inspects every piece and changes nothing. It needs no root.
func Check(ctx context.Context, d Deps, o Options) (Report, error) {
	if err := preflight(d); err != nil {
		return nil, err
	}
	st, err := check(ctx, d, o)
	if err != nil {
		return nil, err
	}
	return st.report, nil
}

func check(ctx context.Context, d Deps, o Options) (state, error) {
	m, err := readMarker(d.Files, o.Dir)
	if err != nil {
		return state{}, err
	}
	st := state{marker: m}
	piper, err := checkPiper(d.Files, o.Dir, m, o.Pins)
	if err != nil {
		return state{}, err
	}
	voices, bad, err := checkVoices(ctx, d.Files, o.Dir, o.Pins.Voices)
	if err != nil {
		return state{}, err
	}
	driver, err := checkDriver(d.Files, o.Dir, m, o.Version)
	if err != nil {
		return state{}, err
	}
	chromium, err := checkChromium(d.Files, o.Dir, m, o.Version)
	if err != nil {
		return state{}, err
	}
	if chromium.Status == StatusOK && !d.Packages.HasApt() {
		chromium.Detail = "system libraries not checked on this distro"
	}
	st.report = Report{piper, voices, driver, chromium, checkFFmpeg(d.Host)}
	st.badVoices = bad
	return st, nil
}

// exists reports whether name is there; any error but "not found" is returned.
func exists(files Files, name string) (bool, error) {
	_, err := files.Stat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", name, err)
	}
	return true, nil
}

func checkPiper(files Files, dir string, m marker, pins Pins) (PieceStatus, error) {
	ok, err := exists(files, PiperBin(dir))
	if err != nil {
		return PieceStatus{}, err
	}
	switch {
	case !ok:
		return PieceStatus{PiecePiper, StatusMissing, PiperBin(dir)}, nil
	case !m.is(PiecePiper, pins.piperTag()):
		// The tarball is not kept, so its sha256 cannot be checked again; the
		// marker records that the pinned one was verified at install.
		return PieceStatus{PiecePiper, StatusBad, "not installed from the pinned release " + pins.PiperVersion}, nil
	}
	return PieceStatus{Piece: PiecePiper, Status: StatusOK}, nil
}

func checkVoices(ctx context.Context, files Files, dir string, voices []VoiceFile) (PieceStatus, []VoiceFile, error) {
	var missing, bad, repair []string
	var badFiles []VoiceFile
	for _, v := range voices {
		name := filepath.Join(VoicesDir(dir), v.Name)
		ok, err := exists(files, name)
		if err != nil {
			return PieceStatus{}, nil, err
		}
		if !ok {
			missing = append(missing, v.Name)
			repair = append(repair, v.Name)
			badFiles = append(badFiles, v)
			continue
		}
		sum, err := hashFile(ctx, files, name)
		if err != nil {
			return PieceStatus{}, nil, err
		}
		if sum != v.SHA256 {
			bad = append(bad, v.Name)
			repair = append(repair, v.Name)
			badFiles = append(badFiles, v)
		}
	}
	switch {
	case len(bad) > 0:
		return PieceStatus{PieceVoices, StatusBad, strings.Join(repair, ", ")}, badFiles, nil
	case len(missing) > 0:
		return PieceStatus{PieceVoices, StatusMissing, strings.Join(missing, ", ")}, badFiles, nil
	}
	return PieceStatus{Piece: PieceVoices, Status: StatusOK}, nil, nil
}

func hashFile(ctx context.Context, files Files, name string) (string, error) {
	f, err := files.Open(name)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", name, err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, &ctxReader{ctx: ctx, r: f}); err != nil {
		return "", fmt.Errorf("read %s: %w", name, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ctxReader stops a long hash when ctx is cancelled.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func checkDriver(files Files, dir string, m marker, version string) (PieceStatus, error) {
	for _, name := range []string{"node", "package"} {
		ok, err := exists(files, filepath.Join(DriverDir(dir), name))
		if err != nil {
			return PieceStatus{}, err
		}
		if !ok {
			return PieceStatus{PieceDriver, StatusMissing, filepath.Join(DriverDir(dir), name)}, nil
		}
	}
	if !m.is(PieceDriver, version) {
		return PieceStatus{PieceDriver, StatusBad, "not installed by this version"}, nil
	}
	return PieceStatus{Piece: PieceDriver, Status: StatusOK}, nil
}

func checkChromium(files Files, dir string, m marker, version string) (PieceStatus, error) {
	entries, err := files.ReadDir(BrowsersDir(dir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return PieceStatus{}, fmt.Errorf("read %s: %w", BrowsersDir(dir), err)
	}
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), chromiumDir) {
			found = true
		}
	}
	switch {
	case !found:
		return PieceStatus{PieceChromium, StatusMissing, filepath.Join(BrowsersDir(dir), chromiumDir+"*")}, nil
	case !m.is(PieceChromium, version):
		return PieceStatus{PieceChromium, StatusBad, "not installed by this version"}, nil
	}
	return PieceStatus{Piece: PieceChromium, Status: StatusOK}, nil
}

func checkFFmpeg(h Host) PieceStatus {
	var missing []string
	for _, bin := range []string{ffmpeg, ffprobe} {
		if !h.HasOnPath(bin) {
			missing = append(missing, bin)
		}
	}
	if len(missing) > 0 {
		return PieceStatus{PieceFFmpeg, StatusMissing, strings.Join(missing, ", ")}
	}
	return PieceStatus{Piece: PieceFFmpeg, Status: StatusOK}
}

// Run checks, then installs each piece that is not ok, in order: piper,
// voices, the browser (driver and Chromium in one call), ffmpeg. The marker is
// written after each success, so a rerun resumes at the piece that failed. It
// refuses before any work when it is not root or the machine is not amd64.
// The returned report is the state after the run.
func Run(ctx context.Context, d Deps, o Options) (Report, error) {
	if err := preflight(d); err != nil {
		return nil, err
	}
	if !d.Host.IsRoot() {
		return nil, ErrNotRoot
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	st, err := check(ctx, d, o)
	if err != nil {
		return nil, err
	}
	existed, err := exists(d.Files, o.Dir)
	if err != nil {
		return nil, err
	}
	if err := d.Files.MkdirAll(o.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", o.Dir, err)
	}
	runErr := install(ctx, d, o, &st)
	// Readable even after a failure, so the pieces that did install work for
	// every user.
	readErr := d.Files.MakeReadable(readablePaths(o.Dir, !existed)...)
	if runErr != nil {
		return st.report, runErr
	}
	if readErr != nil {
		return st.report, fmt.Errorf("make %s readable: %w", o.Dir, readErr)
	}
	return st.report, nil
}

// readablePaths is what Run makes world-readable. A dir setup just created
// holds only its own files, so all of it. A dir that was already there may be
// someone's home or data folder (SCREENCASTER_HOME is free text): only the
// pieces setup owns are touched, never the rest of it or the dir itself.
func readablePaths(dir string, created bool) []string {
	if created {
		return []string{dir}
	}
	return []string{
		filepath.Join(dir, "piper"),
		DriverDir(dir),
		BrowsersDir(dir),
		markerPath(dir),
	}
}

func install(ctx context.Context, d Deps, o Options, st *state) error {
	steps := []struct {
		piece string
		do    func() error
	}{
		{PiecePiper, func() error { return installPiper(ctx, d, o, st) }},
		{PieceVoices, func() error { return installVoices(ctx, d, o, st) }},
		{PieceDriver, func() error { return installBrowser(ctx, d, o, st) }},
		{PieceChromium, func() error { return installBrowser(ctx, d, o, st) }},
		{PieceFFmpeg, func() error { return installFFmpeg(ctx, d, o, st) }},
	}
	for i, s := range steps {
		if st.report[i].Status == StatusOK {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.do(); err != nil {
			return err
		}
	}
	return nil
}

func (st *state) set(piece string, s Status) {
	for i := range st.report {
		if st.report[i].Piece == piece {
			st.report[i].Status = s
			st.report[i].Detail = ""
		}
	}
}

func installPiper(ctx context.Context, d Deps, o Options, st *state) error {
	url := o.Pins.PiperURL()
	_, _ = fmt.Fprintf(o.Out, "piper: downloading %s\n", url)
	tgz := filepath.Join(o.Dir, "piper.tgz")
	if err := d.Fetch.Fetch(ctx, url, tgz, o.Pins.PiperSHA256); err != nil {
		return fmt.Errorf("download piper (%s): %w", url, err)
	}
	defer func() { _ = d.Files.Remove(tgz) }()
	if err := d.Fetch.Extract(ctx, tgz, o.Dir); err != nil {
		return fmt.Errorf("unpack piper: %w", err)
	}
	st.marker[PiecePiper] = o.Pins.piperTag()
	if err := writeMarker(d.Files, o.Dir, st.marker); err != nil {
		return err
	}
	st.set(PiecePiper, StatusOK)
	return nil
}

func installVoices(ctx context.Context, d Deps, o Options, st *state) error {
	if err := d.Files.MkdirAll(VoicesDir(o.Dir), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", VoicesDir(o.Dir), err)
	}
	for _, v := range st.badVoices {
		url := o.Pins.VoiceURL(v)
		_, _ = fmt.Fprintf(o.Out, "voices: downloading %s\n", url)
		if err := d.Fetch.Fetch(ctx, url, filepath.Join(VoicesDir(o.Dir), v.Name), v.SHA256); err != nil {
			return fmt.Errorf("download voice %s (%s): %w", v.Name, url, err)
		}
	}
	st.badVoices = nil
	st.set(PieceVoices, StatusOK)
	return nil
}

// installBrowser serves both the driver and the chromium rows: one Install
// covers both, so the second row finds itself already ok.
func installBrowser(ctx context.Context, d Deps, o Options, st *state) error {
	if st.status(PieceDriver) == StatusOK && st.status(PieceChromium) == StatusOK {
		return nil
	}
	// System libraries need apt; elsewhere Chromium is installed without them
	// and the README says what to add (decision 77).
	withDeps := d.Packages.HasApt()
	_, _ = fmt.Fprintln(o.Out, "driver, chromium: installing the Playwright driver and Chromium")
	if err := d.Browser.Install(ctx, DriverDir(o.Dir), BrowsersDir(o.Dir), withDeps, o.Out); err != nil {
		return fmt.Errorf("install playwright driver and chromium: %w", err)
	}
	st.marker[PieceDriver] = o.Version
	st.marker[PieceChromium] = o.Version
	if err := writeMarker(d.Files, o.Dir, st.marker); err != nil {
		return err
	}
	st.set(PieceDriver, StatusOK)
	st.set(PieceChromium, StatusOK)
	return nil
}

func (st *state) status(piece string) Status {
	for _, p := range st.report {
		if p.Piece == piece {
			return p.Status
		}
	}
	return ""
}

const aptCommand = "apt-get install -y ffmpeg"

func installFFmpeg(ctx context.Context, d Deps, o Options, st *state) error {
	if !d.Packages.HasApt() {
		return fmt.Errorf("ffmpeg and ffprobe are not on PATH and apt-get is not available: install them with your package manager (Debian/Ubuntu: %s)", aptCommand)
	}
	_, _ = fmt.Fprintf(o.Out, "ffmpeg: running %s\n", aptCommand)
	if err := d.Packages.InstallFFmpeg(ctx, o.Out); err != nil {
		return fmt.Errorf("install ffmpeg (%s): %w", aptCommand, err)
	}
	if got := checkFFmpeg(d.Host); got.Status != StatusOK {
		return fmt.Errorf("install ffmpeg (%s): still missing afterwards: %s", aptCommand, got.Detail)
	}
	st.set(PieceFFmpeg, StatusOK)
	return nil
}
