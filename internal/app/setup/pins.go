package setup

import (
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Pins are the downloads `setup` makes: which Piper release, voice files and
// Playwright pieces (Node.js, the playwright-core driver package, Chromium, its
// headless shell and Playwright's ffmpeg), where from, and the sha256 each must
// have. The values are not in Go: they are read from pins.env (embedded), then
// from any --pins-file, then from the environment, each layer winning over the
// one before (LoadPins). So a new release, revision or mirror is a one-file or
// one-variable change. Every download is sha256-checked, none is unpinned
// (security audit H2).
type Pins struct {
	PiperVersion  string
	PiperSHA256   string
	PiperURLBase  string // the release folder; the tag and asset name follow
	VoicesRev     string
	VoicesURLBase string // the Hugging Face resolve folder; the revision and path follow
	Voices        []VoiceFile

	// The playwright-go version the pins were taken from; it must equal go.mod's
	// (pins_test.go), since the driver files below are what that version expects.
	PlaywrightGoVersion string
	NodeVersion         string
	NodeURLBase         string // nodejs.org/dist folder; v<version>/<archive> follows
	NodeSHA256          string
	CoreVersion         string // playwright-core npm package, the driver's package/ folder
	CoreURLBase         string // the npm registry; playwright-core/-/<file> follows
	CoreSHA256          string
	ChromiumRevision    string // names the folders under the browsers dir (chromium-<rev>)
	ChromiumVersion     string // Chrome for Testing version, part of the download path
	ChromiumURLBase     string // the Playwright CDN; builds/... follows
	ChromiumSHA256      string
	HeadlessShellSHA256 string // same revision and version as Chromium
	FFmpegRevision      string // Playwright's own ffmpeg, used by video recording
	FFmpegSHA256        string
}

// VoiceFile is one file of a built-in voice: its name in the voices folder,
// its path below the Hugging Face revision, and its sha256.
type VoiceFile struct {
	Name   string
	Path   string
	SHA256 string
}

// The keys of pins.env, also the names of the environment variables that
// override them.
const (
	EnvPiperVersion  = "SCREENCASTER_PIPER_VERSION"
	EnvPiperSHA256   = "SCREENCASTER_PIPER_SHA256"
	EnvPiperURLBase  = "SCREENCASTER_PIPER_URL_BASE"
	EnvVoicesRev     = "SCREENCASTER_VOICES_REV"
	EnvVoicesURLBase = "SCREENCASTER_VOICES_URL_BASE"

	EnvPlaywrightGoVersion = "SCREENCASTER_PLAYWRIGHT_GO_VERSION"
	EnvNodeVersion         = "SCREENCASTER_NODE_VERSION"
	EnvNodeURLBase         = "SCREENCASTER_NODE_URL_BASE"
	EnvNodeSHA256          = "SCREENCASTER_NODE_SHA256"
	EnvCoreVersion         = "SCREENCASTER_PLAYWRIGHT_CORE_VERSION"
	EnvCoreURLBase         = "SCREENCASTER_PLAYWRIGHT_CORE_URL_BASE"
	EnvCoreSHA256          = "SCREENCASTER_PLAYWRIGHT_CORE_SHA256"
	EnvChromiumRevision    = "SCREENCASTER_CHROMIUM_REVISION"
	EnvChromiumVersion     = "SCREENCASTER_CHROMIUM_VERSION"
	EnvChromiumURLBase     = "SCREENCASTER_CHROMIUM_URL_BASE"
	EnvChromiumSHA256      = "SCREENCASTER_CHROMIUM_SHA256"
	EnvHeadlessShellSHA256 = "SCREENCASTER_HEADLESS_SHELL_SHA256"
	EnvFFmpegRevision      = "SCREENCASTER_FFMPEG_REVISION"
	EnvFFmpegSHA256        = "SCREENCASTER_FFMPEG_SHA256"
	// KeyVoiceFile is the repeatable key of the file layers: one `name=path=sha256`
	// per line. EnvVoiceFiles is its environment form, entries joined by `;`.
	KeyVoiceFile  = "SCREENCASTER_VOICE_FILE"
	EnvVoiceFiles = "SCREENCASTER_VOICE_FILES"
)

// piperAsset is the one Piper build setup supports (amd64 only).
const piperAsset = "piper_linux_x86_64.tar.gz"

// Playwright assets setup supports (linux amd64 only, decision 77).
const (
	nodeArchive         = "linux-x64.tar.gz"
	chromiumPath        = "linux64/chrome-linux64.zip"
	headlessShellPath   = "linux64/chrome-headless-shell-linux64.zip"
	ffmpegAsset         = "ffmpeg-linux.zip"
	playwrightCoreAsset = "playwright-core"
)

// scalarKeys are the single-valued keys, in the order buildPins reports a
// missing one. pins.env, a --pins-file and the environment all accept exactly
// these (plus the repeatable KeyVoiceFile).
var scalarKeys = []string{
	EnvPiperVersion, EnvPiperSHA256, EnvPiperURLBase, EnvVoicesRev, EnvVoicesURLBase,
	EnvPlaywrightGoVersion,
	EnvNodeVersion, EnvNodeURLBase, EnvNodeSHA256,
	EnvCoreVersion, EnvCoreURLBase, EnvCoreSHA256,
	EnvChromiumRevision, EnvChromiumVersion, EnvChromiumURLBase, EnvChromiumSHA256, EnvHeadlessShellSHA256,
	EnvFFmpegRevision, EnvFFmpegSHA256,
}

// shaGroups ties a version (or revision) key to the sha256 keys of the bytes it
// names: a layer that sets the first must set all of the second, because a
// sha256 belongs to one version.
var shaGroups = []struct {
	version string
	shas    []string
}{
	{EnvPiperVersion, []string{EnvPiperSHA256}},
	{EnvNodeVersion, []string{EnvNodeSHA256}},
	{EnvCoreVersion, []string{EnvCoreSHA256}},
	{EnvChromiumVersion, []string{EnvChromiumSHA256, EnvHeadlessShellSHA256}},
	{EnvFFmpegRevision, []string{EnvFFmpegSHA256}},
}

// pathSafe is what a version or revision may look like: it ends up in a URL and
// in a folder name under the install dir.
var pathSafe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]*$`)

//go:embed pins.env
var embeddedPins string

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// layer is the pin values one source sets; empty means "not set here".
type layer struct {
	name   string
	vals   map[string]string
	voices []string // raw name=path=sha256 entries; nil means "not set here"
}

// LoadPins merges the embedded pins.env, then each of files (the contents of
// --pins-file files, in order), then the environment. Bad values fail before
// any work. A layer that sets a Piper version must set its sha256 too.
func LoadPins(getenv func(string) string, files ...string) (Pins, error) {
	layers := make([]layer, 0, len(files)+2)
	emb, err := parseEnvFile("pins.env", embeddedPins)
	if err != nil {
		return Pins{}, err
	}
	layers = append(layers, emb)
	for i, text := range files {
		l, err := parseEnvFile(fmt.Sprintf("pins file %d", i+1), text)
		if err != nil {
			return Pins{}, err
		}
		layers = append(layers, l)
	}
	layers = append(layers, envLayer(getenv))

	vals := map[string]string{}
	var voices []string
	for _, l := range layers {
		for _, g := range shaGroups {
			if _, ok := l.vals[g.version]; !ok {
				continue
			}
			for _, sha := range g.shas {
				if _, ok := l.vals[sha]; !ok {
					return Pins{}, fmt.Errorf("%s: %s is set without %s: a sha256 belongs to one version", l.name, g.version, sha)
				}
			}
		}
		for k, v := range l.vals {
			vals[k] = v
		}
		if l.voices != nil {
			voices = l.voices
		}
	}
	return buildPins(vals, voices)
}

func envLayer(getenv func(string) string) layer {
	l := layer{name: "environment", vals: map[string]string{}}
	for _, k := range scalarKeys {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			l.vals[k] = v
		}
	}
	if v := strings.TrimSpace(getenv(EnvVoiceFiles)); v != "" {
		l.voices = []string{}
		for _, e := range strings.Split(v, ";") {
			if e = strings.TrimSpace(e); e != "" {
				l.voices = append(l.voices, e)
			}
		}
		if len(l.voices) == 0 {
			l.voices = []string{""} // reported as an empty list by buildPins
		}
	}
	return l
}

// parseEnvFile reads KEY=VALUE lines. Blank lines and # comments are skipped,
// values may be wrapped in one pair of quotes, and an unknown key is an error
// (a typo would otherwise silently keep the old pin).
func parseEnvFile(name, text string) (layer, error) {
	l := layer{name: name, vals: map[string]string{}}
	known := map[string]bool{KeyVoiceFile: true}
	for _, k := range scalarKeys {
		known[k] = true
	}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(key), "export "))
		if !ok || key == "" {
			return layer{}, fmt.Errorf("%s:%d: want KEY=VALUE, got %q", name, i+1, line)
		}
		if !known[key] {
			return layer{}, fmt.Errorf("%s:%d: unknown key %q", name, i+1, key)
		}
		val = unquote(strings.TrimSpace(val))
		if key == KeyVoiceFile {
			l.voices = append(l.voices, val)
			continue
		}
		l.vals[key] = val
	}
	return l, nil
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

func buildPins(vals map[string]string, voiceEntries []string) (Pins, error) {
	p := Pins{
		PiperVersion:  vals[EnvPiperVersion],
		PiperSHA256:   vals[EnvPiperSHA256],
		PiperURLBase:  vals[EnvPiperURLBase],
		VoicesRev:     vals[EnvVoicesRev],
		VoicesURLBase: vals[EnvVoicesURLBase],

		PlaywrightGoVersion: vals[EnvPlaywrightGoVersion],
		NodeVersion:         vals[EnvNodeVersion],
		NodeURLBase:         vals[EnvNodeURLBase],
		NodeSHA256:          vals[EnvNodeSHA256],
		CoreVersion:         vals[EnvCoreVersion],
		CoreURLBase:         vals[EnvCoreURLBase],
		CoreSHA256:          vals[EnvCoreSHA256],
		ChromiumRevision:    vals[EnvChromiumRevision],
		ChromiumVersion:     vals[EnvChromiumVersion],
		ChromiumURLBase:     vals[EnvChromiumURLBase],
		ChromiumSHA256:      vals[EnvChromiumSHA256],
		HeadlessShellSHA256: vals[EnvHeadlessShellSHA256],
		FFmpegRevision:      vals[EnvFFmpegRevision],
		FFmpegSHA256:        vals[EnvFFmpegSHA256],
	}
	// Every scalar key must be set. The sha256 keys are format-checked below,
	// the version-like ones for path safety.
	for _, k := range scalarKeys {
		if vals[k] == "" {
			return Pins{}, fmt.Errorf("%s is not set", k)
		}
	}
	for _, f := range []struct{ key, val string }{
		{EnvPiperSHA256, p.PiperSHA256}, {EnvNodeSHA256, p.NodeSHA256}, {EnvCoreSHA256, p.CoreSHA256},
		{EnvChromiumSHA256, p.ChromiumSHA256}, {EnvHeadlessShellSHA256, p.HeadlessShellSHA256},
		{EnvFFmpegSHA256, p.FFmpegSHA256},
	} {
		if !sha256Hex.MatchString(f.val) {
			return Pins{}, fmt.Errorf("%s: want 64 lowercase hex digits, got %q", f.key, f.val)
		}
	}
	for _, f := range []struct{ key, val string }{
		{EnvPlaywrightGoVersion, p.PlaywrightGoVersion}, {EnvNodeVersion, p.NodeVersion},
		{EnvCoreVersion, p.CoreVersion}, {EnvChromiumRevision, p.ChromiumRevision},
		{EnvChromiumVersion, p.ChromiumVersion}, {EnvFFmpegRevision, p.FFmpegRevision},
	} {
		if !pathSafe.MatchString(f.val) {
			return Pins{}, fmt.Errorf("%s: %q is not a plain version or revision", f.key, f.val)
		}
	}
	p.PiperURLBase = withSlash(p.PiperURLBase)
	p.VoicesURLBase = withSlash(p.VoicesURLBase)
	p.NodeURLBase = withSlash(p.NodeURLBase)
	p.CoreURLBase = withSlash(p.CoreURLBase)
	p.ChromiumURLBase = withSlash(p.ChromiumURLBase)
	voices, err := parseVoiceFiles(voiceEntries)
	if err != nil {
		return Pins{}, err
	}
	p.Voices = voices
	return p, nil
}

func withSlash(s string) string {
	if strings.HasSuffix(s, "/") {
		return s
	}
	return s + "/"
}

// parseVoiceFiles checks the `name=path=sha256` entries.
func parseVoiceFiles(entries []string) ([]VoiceFile, error) {
	var out []VoiceFile
	for _, entry := range entries {
		parts := strings.Split(entry, "=")
		if len(parts) != 3 {
			return nil, fmt.Errorf("%s: entry %q is not name=path=sha256", KeyVoiceFile, entry)
		}
		v := VoiceFile{Name: strings.TrimSpace(parts[0]), Path: strings.TrimSpace(parts[1]), SHA256: strings.TrimSpace(parts[2])}
		if v.Name == "" || strings.ContainsAny(v.Name, `/\`) || v.Name == "." || v.Name == ".." {
			return nil, fmt.Errorf("%s: %q is not a plain file name", KeyVoiceFile, v.Name)
		}
		if v.Path == "" {
			return nil, fmt.Errorf("%s: entry %q has no path", KeyVoiceFile, entry)
		}
		if !sha256Hex.MatchString(v.SHA256) {
			return nil, fmt.Errorf("%s: %s: want 64 lowercase hex digits for the sha256, got %q", KeyVoiceFile, v.Name, v.SHA256)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, errors.New("no voice is listed (" + KeyVoiceFile + ")")
	}
	return out, nil
}

// PiperURL is the Piper release tarball.
func (p Pins) PiperURL() string {
	return p.PiperURLBase + p.PiperVersion + "/" + piperAsset
}

// VoiceURL is the download URL of one voice file.
func (p Pins) VoiceURL(v VoiceFile) string {
	return p.VoicesURLBase + p.VoicesRev + "/" + v.Path
}

// piperTag is what the marker records for an installed Piper.
func (p Pins) piperTag() string { return p.PiperVersion + "/" + p.PiperSHA256 }

// NodeURL is the Node.js release archive; only its bin/node is used.
func (p Pins) NodeURL() string {
	return p.NodeURLBase + "v" + p.NodeVersion + "/" + p.nodeArchiveDir() + ".tar.gz"
}

// nodeArchiveDir is the single folder the Node.js archive extracts into.
func (p Pins) nodeArchiveDir() string {
	return "node-v" + p.NodeVersion + "-" + strings.TrimSuffix(nodeArchive, ".tar.gz")
}

// CoreURL is the playwright-core package tarball on the npm registry.
func (p Pins) CoreURL() string {
	return p.CoreURLBase + playwrightCoreAsset + "/-/" + playwrightCoreAsset + "-" + p.CoreVersion + ".tgz"
}

// ChromiumURL, HeadlessShellURL and FFmpegURL are the Playwright CDN zips.
func (p Pins) ChromiumURL() string {
	return p.ChromiumURLBase + "builds/cft/" + p.ChromiumVersion + "/" + chromiumPath
}

func (p Pins) HeadlessShellURL() string {
	return p.ChromiumURLBase + "builds/cft/" + p.ChromiumVersion + "/" + headlessShellPath
}

func (p Pins) FFmpegURL() string {
	return p.ChromiumURLBase + "builds/ffmpeg/" + p.FFmpegRevision + "/" + ffmpegAsset
}

// playwrightTag is what the marker records for the driver and the browsers:
// the playwright-go version and the sha256 of every downloaded piece, so any
// pin change reinstalls them.
func (p Pins) playwrightTag() string {
	return strings.Join([]string{
		p.PlaywrightGoVersion, p.NodeSHA256, p.CoreSHA256,
		p.ChromiumSHA256, p.HeadlessShellSHA256, p.FFmpegSHA256,
	}, "/")
}
