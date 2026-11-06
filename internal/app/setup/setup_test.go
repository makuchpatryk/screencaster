package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

const (
	testDir     = "/opt/sc"
	testVersion = "1.2.3"
)

// memFS is an in-memory Files. Directories are implicit, as in fstest.MapFS.
type memFS struct {
	m        fstest.MapFS
	made     []string
	readable []string
}

func newMemFS() *memFS { return &memFS{m: fstest.MapFS{}} }

func key(name string) string { return strings.TrimPrefix(name, "/") }

func (f *memFS) put(name, data string) { f.m[key(name)] = &fstest.MapFile{Data: []byte(data)} }

func (f *memFS) has(name string) bool { _, ok := f.m[key(name)]; return ok }

func (f *memFS) Open(name string) (io.ReadCloser, error)    { return f.m.Open(key(name)) }
func (f *memFS) ReadFile(name string) ([]byte, error)       { return f.m.ReadFile(key(name)) }
func (f *memFS) Stat(name string) (fs.FileInfo, error)      { return fs.Stat(f.m, key(name)) }
func (f *memFS) ReadDir(name string) ([]fs.DirEntry, error) { return fs.ReadDir(f.m, key(name)) }

func (f *memFS) WriteFile(name string, data []byte, _ fs.FileMode) error {
	f.m[key(name)] = &fstest.MapFile{Data: data}
	return nil
}

func (f *memFS) MkdirAll(name string, _ fs.FileMode) error {
	f.made = append(f.made, name)
	return nil
}

func (f *memFS) Remove(name string) error {
	delete(f.m, key(name))
	return nil
}

func (f *memFS) MakeReadable(paths ...string) error {
	f.readable = append(f.readable, paths...)
	return nil
}

// fakeFetch writes what a real download would: the voice content, an archive
// file, and on extract what the archive holds (the Piper binary, the driver
// package, a browser folder's files).
type fakeFetch struct {
	fs      *memFS
	fetched []fetchCall
	failURL string
	extract int           // Extract calls: the tar.gz of Piper and of playwright-core
	zips    []extractCall // ExtractZip calls: the archive and where it went
	files   []string      // ExtractFile destinations
	zipErr  error         // returned by ExtractZip, after it records the call
}

type extractCall struct{ archive, dstDir string }

type fetchCall struct{ url, dst, sha string }

func (f *fakeFetch) Fetch(_ context.Context, url, dst, sha string) error {
	f.fetched = append(f.fetched, fetchCall{url, dst, sha})
	if url == f.failURL {
		return errors.New("boom")
	}
	f.fs.put(dst, voiceContent[filepath.Base(dst)])
	return nil
}

func (f *fakeFetch) Extract(_ context.Context, tgz, dstDir string) error {
	f.extract++
	if filepath.Base(tgz) == "playwright-core.tgz" {
		f.fs.put(filepath.Join(dstDir, "package", "index.js"), "js")
		f.fs.put(filepath.Join(dstDir, "package", "lib", "coreBundle.js"), unpatchedBundle)
		return nil
	}
	f.fs.put(PiperBin(dstDir), "piper")
	return nil
}

func (f *fakeFetch) ExtractZip(_ context.Context, zipPath, dstDir string) error {
	f.zips = append(f.zips, extractCall{zipPath, dstDir})
	if f.zipErr != nil {
		return f.zipErr
	}
	f.fs.put(filepath.Join(dstDir, "binary"), "bin")
	return nil
}

func (f *fakeFetch) ExtractFile(_ context.Context, _, _, dst string, _ fs.FileMode) error {
	f.files = append(f.files, dst)
	f.fs.put(dst, "node")
	return nil
}

// The two driver bundle texts: what playwright-core ships and what playwright-go
// turns it into (setup applies the same edits).
const (
	unpatchedBundle = "a(pageError.location.url, pageError.location.lineNumber, pageError.location.columnNumber)"
	patchedBundle   = `a(pageError.location?.url || "", pageError.location?.lineNumber || 0, pageError.location?.columnNumber || 0)`
)

func (f *fakeFetch) urls() []string {
	var u []string
	for _, c := range f.fetched {
		u = append(u, c.url)
	}
	return u
}

// fakeBrowser records the driver dirs InstallDeps ran for.
type fakeBrowser struct {
	calls []string
	err   error
}

func (b *fakeBrowser) InstallDeps(_ context.Context, driverDir string, _ io.Writer) error {
	b.calls = append(b.calls, driverDir)
	return b.err
}

type fakePackages struct {
	host     *fakeHost
	apt      bool
	installs int
	err      error
}

func (p *fakePackages) HasApt() bool { return p.apt }

func (p *fakePackages) InstallFFmpeg(context.Context, io.Writer) error {
	p.installs++
	if p.err != nil {
		return p.err
	}
	p.host.onPath["ffmpeg"], p.host.onPath["ffprobe"] = true, true
	return nil
}

type fakeHost struct {
	root   bool
	arch   string
	onPath map[string]bool
}

func (h *fakeHost) IsRoot() bool            { return h.root }
func (h *fakeHost) Arch() string            { return h.arch }
func (h *fakeHost) HasOnPath(n string) bool { return h.onPath[n] }

// fakeSandbox defaults to an unrestricted kernel (the common case, and most
// dev/CI machines), so the sandbox piece is ok with no work unless a test
// sets restricted.
type fakeSandbox struct {
	restricted    bool
	restrictedErr error
	loaded        bool
	installs      []string // chromePath per Install call
	installErr    error
}

func (s *fakeSandbox) Restricted() (bool, error) { return s.restricted, s.restrictedErr }
func (s *fakeSandbox) Loaded(string) bool        { return s.loaded }

func (s *fakeSandbox) Install(_ context.Context, chromePath string, _ io.Writer) error {
	s.installs = append(s.installs, chromePath)
	if s.installErr != nil {
		return s.installErr
	}
	s.loaded = true
	return nil
}

// voiceContent is what the fake downloads write; testPins pins its sha256.
var voiceContent = map[string]string{"a.onnx": "aaa", "b.onnx.json": "bbb"}

// testPins are the default pins with two small voices instead of the real
// (large) ones, so Check can hash content the tests control.
func testPins() Pins {
	p, err := LoadPins(func(string) string { return "" })
	if err != nil {
		panic(err) // the embedded pins.env; pins_test.go would already have failed
	}
	p.Voices = nil
	for _, name := range []string{"a.onnx", "b.onnx.json"} {
		sum := sha256.Sum256([]byte(voiceContent[name]))
		p.Voices = append(p.Voices, VoiceFile{Name: name, Path: "x/" + name, SHA256: hex.EncodeToString(sum[:])})
	}
	return p
}

// rig is a bare machine: root, amd64, apt, an unrestricted kernel, nothing
// installed.
type rig struct {
	pins    Pins
	fs      *memFS
	fetch   *fakeFetch
	br      *fakeBrowser
	pk      *fakePackages
	host    *fakeHost
	sandbox *fakeSandbox
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{pins: testPins(), fs: newMemFS(), host: &fakeHost{root: true, arch: "amd64", onPath: map[string]bool{}}}
	r.fetch = &fakeFetch{fs: r.fs}
	r.br = &fakeBrowser{}
	r.pk = &fakePackages{host: r.host, apt: true}
	r.sandbox = &fakeSandbox{}
	return r
}

func (r *rig) deps() Deps {
	return Deps{Fetch: r.fetch, Browser: r.br, Packages: r.pk, Files: r.fs, Host: r.host, Sandbox: r.sandbox}
}

func (r *rig) opts() Options { return Options{Dir: testDir, Pins: r.pins, Version: testVersion} }

// installAll puts every piece in place, as a finished setup leaves it.
func (r *rig) installAll() {
	r.fs.put(PiperBin(testDir), "piper")
	for _, v := range r.pins.Voices {
		r.fs.put(filepath.Join(VoicesDir(testDir), v.Name), voiceContent[v.Name])
	}
	r.fs.put(filepath.Join(DriverDir(testDir), "node"), "node")
	r.fs.put(filepath.Join(DriverDir(testDir), "package", "index.js"), "js")
	r.fs.put(filepath.Join(DriverDir(testDir), "package", "lib", "coreBundle.js"), patchedBundle)
	chromium, shell, ffmpegDir := browserDirs(BrowsersDir(testDir), r.pins)
	for _, d := range []string{chromium, shell, ffmpegDir} {
		r.fs.put(filepath.Join(d, installComplete), "")
	}
	tag := testVersion + "/" + r.pins.playwrightTag()
	r.fs.put(markerPath(testDir), `{"piper":"`+r.pins.piperTag()+`","driver":"`+tag+`","chromium":"`+tag+`"}`)
	r.host.onPath["ffmpeg"], r.host.onPath["ffprobe"] = true, true
}

func (r *rig) noWork(t *testing.T) {
	t.Helper()
	if len(r.fetch.fetched) != 0 || r.fetch.extract != 0 || len(r.fetch.zips) != 0 || len(r.fetch.files) != 0 ||
		len(r.br.calls) != 0 || r.pk.installs != 0 || len(r.sandbox.installs) != 0 {
		t.Errorf("work done: fetched %v, extracts %d, zips %v, files %v, browser %v, apt %d, sandbox %v",
			r.fetch.urls(), r.fetch.extract, r.fetch.zips, r.fetch.files, r.br.calls, r.pk.installs, r.sandbox.installs)
	}
}

func TestCheck_allOKChangesNothing(t *testing.T) {
	r := newRig(t)
	r.installAll()
	r.host.root = false // --check needs no root
	rep, err := Check(context.Background(), r.deps(), r.opts())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.AllOK() {
		t.Errorf("report %v, want all ok", rep)
	}
	r.noWork(t)
	if len(r.fs.made) != 0 || len(r.fs.readable) != 0 {
		t.Errorf("Check touched the file system: made %v, readable %v", r.fs.made, r.fs.readable)
	}
}

func TestCheck_emptyDirReportsEveryPieceInOrder(t *testing.T) {
	r := newRig(t)
	rep, err := Check(context.Background(), r.deps(), r.opts())
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, p := range rep {
		order = append(order, p.Piece)
		// Sandbox is a property of the kernel, not the install dir: the rig's
		// kernel is unrestricted by default, so it is ok here unlike every
		// other piece on an empty dir.
		if p.Piece != PieceSandbox && p.Status == StatusOK {
			t.Errorf("%s is ok on an empty dir", p.Piece)
		}
	}
	if want := []string{"piper", "voices", "driver", "chromium", "sandbox", "ffmpeg"}; !slices.Equal(order, want) {
		t.Errorf("order %v, want %v", order, want)
	}
}

func TestRun_allOKDownloadsAndInstallsNothing(t *testing.T) {
	r := newRig(t)
	r.installAll()
	rep, err := Run(context.Background(), r.deps(), r.opts())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.AllOK() {
		t.Errorf("report %v, want all ok", rep)
	}
	r.noWork(t)
}

func TestRun_emptyMachineInstallsEverything(t *testing.T) {
	r := newRig(t)
	rep, err := Run(context.Background(), r.deps(), r.opts())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.AllOK() {
		t.Errorf("report %v, want all ok", rep)
	}
	if got := r.fetch.fetched[0]; got != (fetchCall{r.pins.PiperURL(), filepath.Join(testDir, "piper.tgz"), r.pins.PiperSHA256}) {
		t.Errorf("first fetch %+v, want the pinned piper tarball", got)
	}
	if r.fs.has(filepath.Join(testDir, "piper.tgz")) {
		t.Error("piper tarball not removed after extract")
	}
	if want := []string{DriverDir(testDir)}; !slices.Equal(r.br.calls, want) {
		t.Errorf("install-deps calls %v, want %v (one, with apt)", r.br.calls, want)
	}
	if r.pk.installs != 1 {
		t.Errorf("apt installs %d, want 1", r.pk.installs)
	}
	if !slices.Equal(r.fs.readable, []string{testDir}) {
		t.Errorf("made readable %v, want [%s]", r.fs.readable, testDir)
	}
	// The second run is a no-op (success criterion).
	r.fetch.fetched, r.br.calls, r.pk.installs, r.fetch.extract = nil, nil, 0, 0
	r.fetch.zips, r.fetch.files = nil, nil
	if _, err := Run(context.Background(), r.deps(), r.opts()); err != nil {
		t.Fatal(err)
	}
	r.noWork(t)
}

func TestRun_pinnedVoiceURLsAndShas(t *testing.T) {
	r := newRig(t)
	r.installAll()
	_ = r.fs.Remove(filepath.Join(VoicesDir(testDir), "a.onnx"))
	if _, err := Run(context.Background(), r.deps(), r.opts()); err != nil {
		t.Fatal(err)
	}
	v := r.pins.Voices[0]
	want := fetchCall{r.pins.VoiceURL(v), filepath.Join(VoicesDir(testDir), v.Name), v.SHA256}
	if len(r.fetch.fetched) != 1 || r.fetch.fetched[0] != want {
		t.Errorf("fetched %+v, want only %+v", r.fetch.fetched, want)
	}
}

func TestRun_badVoiceRefetchesOnlyThatVoice(t *testing.T) {
	r := newRig(t)
	r.installAll()
	r.fs.put(filepath.Join(VoicesDir(testDir), "b.onnx.json"), "corrupt")
	rep, err := Run(context.Background(), r.deps(), r.opts())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{r.pins.VoiceURL(r.pins.Voices[1])}; !slices.Equal(r.fetch.urls(), want) {
		t.Errorf("fetched %v, want %v", r.fetch.urls(), want)
	}
	if !rep.AllOK() {
		t.Errorf("report %v, want all ok", rep)
	}
}

func TestCheck_badVoiceIsReportedByName(t *testing.T) {
	r := newRig(t)
	r.installAll()
	r.fs.put(filepath.Join(VoicesDir(testDir), "b.onnx.json"), "corrupt")
	rep, err := Check(context.Background(), r.deps(), r.opts())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rep[1].String(), "voices: bad (b.onnx.json)"; got != want {
		t.Errorf("voices line %q, want %q", got, want)
	}
}

func TestRun_missingVoiceIsReportedAsMissing(t *testing.T) {
	r := newRig(t)
	r.installAll()
	_ = r.fs.Remove(filepath.Join(VoicesDir(testDir), "a.onnx"))
	rep, err := Check(context.Background(), r.deps(), r.opts())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rep[1].String(), "voices: missing (a.onnx)"; got != want {
		t.Errorf("voices line %q, want %q", got, want)
	}
}

func TestRun_corruptMarkerKeepsGoodVoicesAndRepairsTheRest(t *testing.T) {
	r := newRig(t)
	r.installAll()
	r.fs.put(markerPath(testDir), "{not json")
	rep, err := Run(context.Background(), r.deps(), r.opts())
	if err != nil {
		t.Fatal(err)
	}
	reinstalled := append([]string{r.pins.PiperURL()}, playwrightURLs(r.pins)...)
	for _, u := range r.fetch.urls() {
		if !slices.Contains(reinstalled, u) {
			t.Errorf("refetched %s; voices on disk are good and must be kept", u)
		}
	}
	// Piper and the playwright-core package are unpacked again, and the browser
	// pieces are reinstalled (nothing recorded, so re-verified by reinstall).
	if r.fetch.extract != 2 || len(r.fetch.zips) != 3 || len(r.br.calls) != 1 {
		t.Errorf("extracts %d, zips %d, install-deps %d; want 2, 3 and 1", r.fetch.extract, len(r.fetch.zips), len(r.br.calls))
	}
	if !rep.AllOK() {
		t.Errorf("report %v, want all ok", rep)
	}
}

func TestRun_newReleaseReinstallsTheBrowserOnly(t *testing.T) {
	r := newRig(t)
	r.installAll()
	o := r.opts()
	o.Version = "2.0.0"
	if _, err := Run(context.Background(), r.deps(), o); err != nil {
		t.Fatal(err)
	}
	if want := playwrightURLs(r.pins); !slices.Equal(r.fetch.urls(), want) || len(r.br.calls) != 1 || r.pk.installs != 0 {
		t.Errorf("fetched %v, install-deps %d, apt %d; want only the Playwright files %v", r.fetch.urls(), len(r.br.calls), r.pk.installs, want)
	}
}

// playwrightURLs are the five pinned Playwright downloads, in install order.
func playwrightURLs(p Pins) []string {
	return []string{p.NodeURL(), p.CoreURL(), p.ChromiumURL(), p.HeadlessShellURL(), p.FFmpegURL()}
}

func TestRun_installsThePinnedPlaywrightFiles(t *testing.T) {
	r := newRig(t)
	r.installAll()
	_ = r.fs.Remove(markerPath(testDir)) // driver and chromium rows get reinstalled
	if _, err := Run(context.Background(), r.deps(), r.opts()); err != nil {
		t.Fatal(err)
	}
	p := r.pins
	var got []fetchCall
	for _, c := range r.fetch.fetched {
		if c.url != p.PiperURL() {
			got = append(got, c)
		}
	}
	want := []fetchCall{
		{p.NodeURL(), filepath.Join(testDir, "node.tgz"), p.NodeSHA256},
		{p.CoreURL(), filepath.Join(testDir, "playwright-core.tgz"), p.CoreSHA256},
		{p.ChromiumURL(), filepath.Join(testDir, "chromium.zip"), p.ChromiumSHA256},
		{p.HeadlessShellURL(), filepath.Join(testDir, "chromium-headless-shell.zip"), p.HeadlessShellSHA256},
		{p.FFmpegURL(), filepath.Join(testDir, "ffmpeg.zip"), p.FFmpegSHA256},
	}
	if !slices.Equal(got, want) {
		t.Errorf("fetched %+v, want %+v", got, want)
	}
	if want := []string{filepath.Join(DriverDir(testDir), "node")}; !slices.Equal(r.fetch.files, want) {
		t.Errorf("node extracted to %v, want %v", r.fetch.files, want)
	}
	chromium, shell, ffmpegDir := browserDirs(BrowsersDir(testDir), p)
	var dirs []string
	for _, z := range r.fetch.zips {
		dirs = append(dirs, z.dstDir)
	}
	if want := []string{chromium, shell, ffmpegDir}; !slices.Equal(dirs, want) {
		t.Errorf("zips extracted to %v, want %v", dirs, want)
	}
	for _, d := range []string{chromium, shell, ffmpegDir} {
		for _, marker := range []string{installComplete, depsValidated} {
			if !r.fs.has(filepath.Join(d, marker)) {
				t.Errorf("%s lacks the %s marker", d, marker)
			}
		}
	}
	for _, f := range []string{"node.tgz", "playwright-core.tgz", "chromium.zip", "chromium-headless-shell.zip", "ffmpeg.zip"} {
		if r.fs.has(filepath.Join(testDir, f)) {
			t.Errorf("%s not removed after extract", f)
		}
	}
}

// The driver bundle must carry playwright-go's three edits, since its `Run`
// does not apply them and setup no longer calls its installer.
func TestRun_patchesTheDriverBundle(t *testing.T) {
	r := newRig(t)
	if _, err := Run(context.Background(), r.deps(), r.opts()); err != nil {
		t.Fatal(err)
	}
	b, err := r.fs.ReadFile(filepath.Join(DriverDir(testDir), "package", "lib", "coreBundle.js"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != patchedBundle {
		t.Errorf("bundle %q, want %q", b, patchedBundle)
	}
}

func TestPatchDriver_failsWithoutThePattern(t *testing.T) {
	r := newRig(t)
	r.fs.put(filepath.Join(DriverDir(testDir), "package", "lib", "coreBundle.js"), "unrelated")
	err := patchDriver(r.fs, DriverDir(testDir))
	if err == nil || !strings.Contains(err.Error(), "pageError.location.url") {
		t.Errorf("error %v, want one naming the missing pattern", err)
	}
}

// A new pin (here a Chromium sha256) must reinstall the driver and browsers
// and nothing else, even on the same release (decision 82).
func TestRun_playwrightPinChangeReinstalls(t *testing.T) {
	r := newRig(t)
	r.installAll()
	o := r.opts()
	o.Pins.ChromiumSHA256 = shaB
	rep, err := Check(context.Background(), r.deps(), o)
	if err != nil {
		t.Fatal(err)
	}
	if rep[2].Status != StatusBad || rep[3].Status != StatusBad {
		t.Errorf("driver %v, chromium %v; want both bad after a pin change", rep[2], rep[3])
	}
	if _, err := Run(context.Background(), r.deps(), o); err != nil {
		t.Fatal(err)
	}
	if want := playwrightURLs(o.Pins); !slices.Equal(r.fetch.urls(), want) || r.pk.installs != 0 {
		t.Errorf("fetched %v, apt %d; want the Playwright files %v only", r.fetch.urls(), r.pk.installs, want)
	}
	if !r.fetch.fetchedSHA(shaB) {
		t.Error("the new Chromium pin was not the one fetched")
	}
}

func (f *fakeFetch) fetchedSHA(sha string) bool {
	for _, c := range f.fetched {
		if c.sha == sha {
			return true
		}
	}
	return false
}

// A zip that fails to unpack leaves no marker, so Check reports the browser as
// missing and the next run redoes it.
func TestRun_failedBrowserUnpackLeavesNoMarker(t *testing.T) {
	r := newRig(t)
	r.installAll()
	_ = r.fs.Remove(markerPath(testDir))
	r.fetch.zipErr = errors.New("disk full")
	_, err := Run(context.Background(), r.deps(), r.opts())
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("error %v, want one with the unpack failure", err)
	}
	chromium, _, _ := browserDirs(BrowsersDir(testDir), r.pins)
	if r.fs.has(filepath.Join(chromium, installComplete)) {
		t.Error("the marker of a folder whose unpack failed is still there")
	}
	rep, err := Check(context.Background(), r.deps(), r.opts())
	if err != nil {
		t.Fatal(err)
	}
	if rep[3].Status != StatusMissing {
		t.Errorf("chromium %v, want missing after a failed unpack", rep[3])
	}
}

func TestRun_markerIsWrittenAfterEachPiece(t *testing.T) {
	r := newRig(t)
	r.installAll()
	_ = r.fs.Remove(PiperBin(testDir))
	_ = r.fs.Remove(markerPath(testDir))
	r.fetch.failURL = r.pins.VoiceURL(r.pins.Voices[0])
	_ = r.fs.Remove(filepath.Join(VoicesDir(testDir), "a.onnx"))
	_, err := Run(context.Background(), r.deps(), r.opts())
	if err == nil {
		t.Fatal("want the voice download error")
	}
	b, rerr := r.fs.ReadFile(markerPath(testDir))
	if rerr != nil {
		t.Fatalf("no marker after piper succeeded: %v", rerr)
	}
	if !strings.Contains(string(b), r.pins.piperTag()) {
		t.Errorf("marker %s does not record piper", b)
	}
}

func TestRun_failedPieceStopsTheRun(t *testing.T) {
	r := newRig(t)
	r.fetch.failURL = r.pins.VoiceURL(r.pins.Voices[0])
	_, err := Run(context.Background(), r.deps(), r.opts())
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"download voice a.onnx", r.pins.VoiceURL(r.pins.Voices[0]), "boom"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if len(r.br.calls) != 0 || r.pk.installs != 0 {
		t.Errorf("later pieces ran after a failure: browser %d, apt %d", len(r.br.calls), r.pk.installs)
	}
	if !r.fs.has(markerPath(testDir)) {
		t.Error("marker for the finished piper piece was lost")
	}
	if len(r.fs.readable) == 0 {
		t.Error("what did install was not made readable")
	}
}

func TestRun_errorsNameThePieceAndTheCommand(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*rig)
		want  []string
	}{
		{"piper download", func(r *rig) { r.fetch.failURL = r.pins.PiperURL() }, []string{"download piper", testPins().PiperURL()}},
		{"chromium libraries", func(r *rig) { r.br.err = errors.New("apt down") }, []string{"install chromium system libraries", "apt down"}},
		{"node download", func(r *rig) { r.fetch.failURL = testPins().NodeURL() }, []string{"download node.tgz", testPins().NodeURL()}},
		{"chromium download", func(r *rig) { r.fetch.failURL = testPins().ChromiumURL() }, []string{"download chromium.zip", testPins().ChromiumURL()}},
		{"apt", func(r *rig) { r.pk.err = errors.New("no network") }, []string{"install ffmpeg", "apt-get install -y ffmpeg", "no network"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			tt.setup(r)
			_, err := Run(context.Background(), r.deps(), r.opts())
			if err == nil {
				t.Fatal("want an error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}
}

func TestRun_ffmpegMissingWithAptInstallsIt(t *testing.T) {
	r := newRig(t)
	r.installAll()
	delete(r.host.onPath, "ffprobe")
	rep, err := Run(context.Background(), r.deps(), r.opts())
	if err != nil {
		t.Fatal(err)
	}
	if r.pk.installs != 1 || !rep.AllOK() {
		t.Errorf("apt installs %d, report %v; want 1 and all ok", r.pk.installs, rep)
	}
}

func TestRun_ffmpegMissingWithoutAptNamesTheCommand(t *testing.T) {
	r := newRig(t)
	r.installAll()
	r.pk.apt = false
	delete(r.host.onPath, "ffmpeg")
	_, err := Run(context.Background(), r.deps(), r.opts())
	if err == nil || !strings.Contains(err.Error(), "apt-get install -y ffmpeg") {
		t.Fatalf("error %v, want one naming the apt command", err)
	}
	if r.pk.installs != 0 {
		t.Error("InstallFFmpeg called without apt")
	}
	if len(r.fs.readable) == 0 {
		t.Error("the downloaded pieces were not made readable before the error")
	}
}

func TestRun_withoutAptChromiumHasNoSystemDeps(t *testing.T) {
	r := newRig(t)
	r.installAll()
	r.pk.apt = false
	chromium, _, _ := browserDirs(BrowsersDir(testDir), r.pins)
	_ = r.fs.Remove(filepath.Join(chromium, installComplete))
	if _, err := Run(context.Background(), r.deps(), r.opts()); err != nil {
		t.Fatal(err)
	}
	if len(r.br.calls) != 0 || len(r.fetch.zips) != 3 {
		t.Errorf("install-deps calls %v, zips %d; want none and 3 (browsers installed, no system libraries)", r.br.calls, len(r.fetch.zips))
	}
	if r.fs.has(filepath.Join(chromium, depsValidated)) {
		t.Error("DEPENDENCIES_VALIDATED written although install-deps did not run")
	}
}

func TestCheck_chromiumNotesUncheckedLibrariesWithoutApt(t *testing.T) {
	r := newRig(t)
	r.installAll()
	r.pk.apt = false
	rep, err := Check(context.Background(), r.deps(), r.opts())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rep[3].String(), "chromium: ok (system libraries not checked on this distro)"; got != want {
		t.Errorf("chromium line %q, want %q", got, want)
	}
}

func TestRun_refusedBeforeAnyWork(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*fakeHost)
		wantErr string
	}{
		{"not root", func(h *fakeHost) { h.root = false }, "must run as root"},
		{"not amd64", func(h *fakeHost) { h.arch = "arm64" }, "unsupported architecture arm64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			tt.mutate(r.host)
			_, err := Run(context.Background(), r.deps(), r.opts())
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %v, want one containing %q", err, tt.wantErr)
			}
			r.noWork(t)
			if len(r.fs.made) != 0 || len(r.fs.readable) != 0 || r.fs.has(markerPath(testDir)) {
				t.Errorf("file system touched: made %v, readable %v", r.fs.made, r.fs.readable)
			}
		})
	}
}

func TestRun_notRootIsErrNotRoot(t *testing.T) {
	r := newRig(t)
	r.host.root = false
	if _, err := Run(context.Background(), r.deps(), r.opts()); !errors.Is(err, ErrNotRoot) {
		t.Errorf("error %v, want ErrNotRoot", err)
	}
}

func TestCheck_refusesNonAMD64(t *testing.T) {
	r := newRig(t)
	r.host.arch = "arm64"
	if _, err := Check(context.Background(), r.deps(), r.opts()); err == nil {
		t.Error("want an architecture error")
	}
}

func TestRun_cancelledContextStopsBeforeInstalling(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, r.deps(), r.opts())
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error %v, want context.Canceled", err)
	}
	r.noWork(t)
}

// SCREENCASTER_HOME is free text: a dir that was already there (a home or data
// folder) must not be chmodded wholesale, only the pieces setup owns.
func TestRun_existingDirIsNotMadeReadableWholesale(t *testing.T) {
	r := newRig(t)
	r.fs.put(filepath.Join(testDir, "notes.txt"), "private") // the dir exists
	if _, err := Run(context.Background(), r.deps(), r.opts()); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(r.fs.readable, testDir) {
		t.Errorf("made readable %v: includes the whole dir", r.fs.readable)
	}
	for _, want := range []string{DriverDir(testDir), BrowsersDir(testDir), filepath.Join(testDir, "piper"), markerPath(testDir)} {
		if !slices.Contains(r.fs.readable, want) {
			t.Errorf("made readable %v, lacks %s", r.fs.readable, want)
		}
	}
}

func TestCheck_sandboxOKWhenUnrestricted(t *testing.T) {
	r := newRig(t)
	r.installAll()
	r.sandbox.restricted = false
	rep, err := Check(context.Background(), r.deps(), r.opts())
	if err != nil {
		t.Fatal(err)
	}
	if got := rep[4]; got.Status != StatusOK {
		t.Errorf("sandbox %v, want ok on an unrestricted kernel", got)
	}
	r.noWork(t)
}

func TestRun_installsAppArmorProfileWhenRestricted(t *testing.T) {
	r := newRig(t)
	r.installAll()
	r.sandbox.restricted = true
	rep, err := Check(context.Background(), r.deps(), r.opts())
	if err != nil {
		t.Fatal(err)
	}
	if rep[4].Status != StatusMissing {
		t.Errorf("sandbox %v, want missing before Run (kernel restricted, profile not loaded)", rep[4])
	}
	rep, err = Run(context.Background(), r.deps(), r.opts())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.AllOK() {
		t.Errorf("report %v, want all ok", rep)
	}
	want := chromiumExecutable(BrowsersDir(testDir), r.pins)
	if !slices.Equal(r.sandbox.installs, []string{want}) {
		t.Errorf("AppArmor Install calls %v, want [%s]", r.sandbox.installs, want)
	}
	// The second run is a no-op: the profile is now loaded.
	r.sandbox.installs = nil
	if _, err := Run(context.Background(), r.deps(), r.opts()); err != nil {
		t.Fatal(err)
	}
	if len(r.sandbox.installs) != 0 {
		t.Errorf("sandbox reinstalled on an already-loaded profile: %v", r.sandbox.installs)
	}
}
