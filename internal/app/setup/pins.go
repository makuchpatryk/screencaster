package setup

import (
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Pins are the downloads `setup` makes: which Piper release and which voice
// files, where from, and the sha256 each must have. The values are not in Go:
// they are read from pins.env (embedded), then from any --pins-file, then from
// the environment, each layer winning over the one before (LoadPins). So a new
// Piper release, voice revision or mirror is a one-file or one-variable change.
// Every download is sha256-checked, none is unpinned.
type Pins struct {
	PiperVersion  string
	PiperSHA256   string
	PiperURLBase  string // the release folder; the tag and asset name follow
	VoicesRev     string
	VoicesURLBase string // the Hugging Face resolve folder; the revision and path follow
	Voices        []VoiceFile
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
	// KeyVoiceFile is the repeatable key of the file layers: one `name=path=sha256`
	// per line. EnvVoiceFiles is its environment form, entries joined by `;`.
	KeyVoiceFile  = "SCREENCASTER_VOICE_FILE"
	EnvVoiceFiles = "SCREENCASTER_VOICE_FILES"
)

// piperAsset is the one Piper build setup supports (amd64 only).
const piperAsset = "piper_linux_x86_64.tar.gz"

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
		if _, ok := l.vals[EnvPiperVersion]; ok {
			if _, ok := l.vals[EnvPiperSHA256]; !ok {
				return Pins{}, fmt.Errorf("%s: %s is set without %s: a sha256 belongs to one Piper version", l.name, EnvPiperVersion, EnvPiperSHA256)
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
	for _, k := range []string{EnvPiperVersion, EnvPiperSHA256, EnvPiperURLBase, EnvVoicesRev, EnvVoicesURLBase} {
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
	known := map[string]bool{
		EnvPiperVersion: true, EnvPiperSHA256: true, EnvPiperURLBase: true,
		EnvVoicesRev: true, EnvVoicesURLBase: true, KeyVoiceFile: true,
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
	}
	for _, f := range []struct{ key, val string }{
		{EnvPiperVersion, p.PiperVersion}, {EnvPiperURLBase, p.PiperURLBase},
		{EnvVoicesRev, p.VoicesRev}, {EnvVoicesURLBase, p.VoicesURLBase},
	} {
		if f.val == "" {
			return Pins{}, fmt.Errorf("%s is not set", f.key)
		}
	}
	if !sha256Hex.MatchString(p.PiperSHA256) {
		return Pins{}, fmt.Errorf("%s: want 64 lowercase hex digits, got %q", EnvPiperSHA256, p.PiperSHA256)
	}
	p.PiperURLBase = withSlash(p.PiperURLBase)
	p.VoicesURLBase = withSlash(p.VoicesURLBase)
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
