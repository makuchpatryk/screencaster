package setup

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// rootDockerfile and goMod are the other homes of the Playwright pins
// (decision 82): the root image's playwright stage and the module file.
const (
	rootDockerfile = "../../../Dockerfile"
	goMod          = "../../../go.mod"
)

// dockerfile is the other home of the pins (decision 77): the piper stage of
// the Docker image. This test is the guard for that duplication.
const dockerfile = "../../../providers/piper/Dockerfile"

func TestPins_matchProviderDockerfile(t *testing.T) {
	b, err := os.ReadFile(dockerfile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	pins := defaultPins(t)

	args := []struct{ name, want string }{
		{"PIPER_VERSION", pins.PiperVersion},
		{"PIPER_SHA256", pins.PiperSHA256},
		{"VOICES_REV", pins.VoicesRev},
	}
	for _, a := range args {
		t.Run(a.name, func(t *testing.T) {
			m := regexp.MustCompile(`(?m)^ARG ` + a.name + `=(\S+)$`).FindStringSubmatch(text)
			if m == nil {
				t.Fatalf("no ARG %s= line in %s", a.name, dockerfile)
			}
			if m[1] != a.want {
				t.Errorf("Dockerfile has %s, Go pin is %s", m[1], a.want)
			}
		})
	}

	// "<sha256>  <file>" lines of the voices' sha256sum check.
	docker := map[string]string{}
	for _, m := range regexp.MustCompile(`"([0-9a-f]{64})  (\S+)"`).FindAllStringSubmatch(text, -1) {
		docker[m[2]] = m[1]
	}
	if len(docker) != len(pins.Voices) {
		t.Errorf("Dockerfile pins %d voice files, Go pins %d", len(docker), len(pins.Voices))
	}
	for _, v := range pins.Voices {
		if got := docker[v.Name]; got != v.SHA256 {
			t.Errorf("%s: Dockerfile has %q, Go pin is %s", v.Name, got, v.SHA256)
		}
	}
}

func TestPins_voiceURLsMatchTheDockerfileDownloads(t *testing.T) {
	b, err := os.ReadFile(dockerfile)
	if err != nil {
		t.Fatal(err)
	}
	pins := defaultPins(t)
	// The Dockerfile downloads `$base/<path>` for each file; the Go URL builder
	// must produce the same path under the same revision.
	for _, v := range pins.Voices {
		if !regexp.MustCompile(`\$base/` + regexp.QuoteMeta(v.Path) + `[\s;]`).Match(b) {
			t.Errorf("Dockerfile does not download $base/%s", v.Path)
		}
	}
	// The URL bases in pins.env are the ones the Dockerfile builds its URLs from.
	text := string(b)
	for _, want := range []string{
		pins.PiperURLBase + "${PIPER_VERSION}/piper_linux_x86_64.tar.gz",
		"base=" + strings.TrimSuffix(pins.VoicesURLBase, "/") + "/${VOICES_REV}",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("Dockerfile lacks %q", want)
		}
	}
}

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// defaultPins is what a bare `setup` uses: the embedded pins.env alone.
func defaultPins(t *testing.T) Pins {
	t.Helper()
	p, err := LoadPins(envOf(nil))
	if err != nil {
		t.Fatalf("embedded pins.env: %v", err)
	}
	return p
}

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestLoadPins_embeddedFileGivesTheDefaults(t *testing.T) {
	p := defaultPins(t)
	if p.PiperVersion == "" || len(p.Voices) != 4 || !strings.HasSuffix(p.PiperURLBase, "/") {
		t.Errorf("pins %+v: want a version, 4 voices and a slash-terminated base", p)
	}
}

func TestLoadPins_environmentOverridesEveryValue(t *testing.T) {
	got, err := LoadPins(envOf(map[string]string{
		EnvPiperVersion:  "2030.1.1",
		EnvPiperSHA256:   shaA,
		EnvPiperURLBase:  "https://mirror.example/piper", // no trailing slash on purpose
		EnvVoicesRev:     "rev9",
		EnvVoicesURLBase: "https://mirror.example/voices/",
		EnvVoiceFiles:    "x.onnx=en/x.onnx=" + shaA + "; x.onnx.json=en/x.onnx.json=" + shaB + ";",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://mirror.example/piper/2030.1.1/piper_linux_x86_64.tar.gz"; got.PiperURL() != want {
		t.Errorf("PiperURL = %s, want %s", got.PiperURL(), want)
	}
	if got.PiperSHA256 != shaA {
		t.Errorf("PiperSHA256 = %s", got.PiperSHA256)
	}
	if len(got.Voices) != 2 || got.Voices[1] != (VoiceFile{"x.onnx.json", "en/x.onnx.json", shaB}) {
		t.Fatalf("voices = %+v, want the two from the env only", got.Voices)
	}
	if want := "https://mirror.example/voices/rev9/en/x.onnx"; got.VoiceURL(got.Voices[0]) != want {
		t.Errorf("VoiceURL = %s, want %s", got.VoiceURL(got.Voices[0]), want)
	}
}

// Layers: embedded < --pins-file (in order) < environment.
func TestLoadPins_layersApplyInOrder(t *testing.T) {
	file1 := "# a mirror\nSCREENCASTER_VOICES_REV=\"fromfile1\"\nSCREENCASTER_PIPER_URL_BASE='https://one.example/'\n"
	file2 := "SCREENCASTER_VOICES_REV=fromfile2\n"
	got, err := LoadPins(envOf(map[string]string{EnvPiperURLBase: "https://env.example/"}), file1, file2)
	if err != nil {
		t.Fatal(err)
	}
	if got.VoicesRev != "fromfile2" {
		t.Errorf("VoicesRev = %s, want the later file's", got.VoicesRev)
	}
	if got.PiperURLBase != "https://env.example/" {
		t.Errorf("PiperURLBase = %s, want the environment's", got.PiperURLBase)
	}
	if def := defaultPins(t); got.PiperSHA256 != def.PiperSHA256 || len(got.Voices) != len(def.Voices) {
		t.Error("values no layer set must keep the embedded ones")
	}
}

func TestLoadPins_voiceListInALayerReplacesTheLowerOne(t *testing.T) {
	file := "SCREENCASTER_VOICE_FILE=a.onnx=p/a.onnx=" + shaA + "\nSCREENCASTER_VOICE_FILE=b.onnx=p/b.onnx=" + shaB + "\n"
	got, err := LoadPins(envOf(nil), file)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Voices) != 2 || got.Voices[0].Name != "a.onnx" {
		t.Errorf("voices = %+v, want the file's two", got.Voices)
	}
	got, err = LoadPins(envOf(map[string]string{EnvVoiceFiles: "c.onnx=p/c.onnx=" + shaA}), file)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Voices) != 1 || got.Voices[0].Name != "c.onnx" {
		t.Errorf("voices = %+v, want the environment's one", got.Voices)
	}
}

// playwrightEnv sets every Playwright key, so a test does not depend on the
// embedded pins.env for them. Values are shaped like the real ones.
func playwrightEnv() map[string]string {
	return map[string]string{
		EnvPlaywrightGoVersion: "v0.9999.1",
		EnvNodeVersion:         "99.1.0",
		EnvNodeURLBase:         "https://node.example/dist", // no trailing slash on purpose
		EnvNodeSHA256:          shaA,
		EnvCoreVersion:         "9.9.9",
		EnvCoreURLBase:         "https://npm.example/",
		EnvCoreSHA256:          shaB,
		EnvChromiumRevision:    "4321",
		EnvChromiumVersion:     "199.0.1.2",
		EnvChromiumURLBase:     "https://cdn.example/",
		EnvChromiumSHA256:      shaA,
		EnvHeadlessShellSHA256: shaB,
		EnvFFmpegRevision:      "2022",
		EnvFFmpegSHA256:        shaA,
	}
}

func TestLoadPins_playwrightKeysOverrideAndBuildTheURLs(t *testing.T) {
	got, err := LoadPins(envOf(playwrightEnv()))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ name, got, want string }{
		{"node", got.NodeURL(), "https://node.example/dist/v99.1.0/node-v99.1.0-linux-x64.tar.gz"},
		{"node folder", got.nodeArchiveDir(), "node-v99.1.0-linux-x64"},
		{"core", got.CoreURL(), "https://npm.example/playwright-core/-/playwright-core-9.9.9.tgz"},
		{"chromium", got.ChromiumURL(), "https://cdn.example/builds/cft/199.0.1.2/linux64/chrome-linux64.zip"},
		{"headless shell", got.HeadlessShellURL(), "https://cdn.example/builds/cft/199.0.1.2/linux64/chrome-headless-shell-linux64.zip"},
		{"ffmpeg", got.FFmpegURL(), "https://cdn.example/builds/ffmpeg/2022/ffmpeg-linux.zip"},
	} {
		if tt.got != tt.want {
			t.Errorf("%s URL = %s, want %s", tt.name, tt.got, tt.want)
		}
	}
	if got.ChromiumRevision != "4321" || got.FFmpegRevision != "2022" || got.PlaywrightGoVersion != "v0.9999.1" {
		t.Errorf("pins %+v: revisions or go version not taken from the environment", got)
	}
}

// Any pin change must reinstall the driver and browsers (decision 82), so the
// marker tag has to move with every sha256 and the playwright-go version.
func TestPlaywrightTag_changesWithEveryPin(t *testing.T) {
	base, err := LoadPins(envOf(playwrightEnv()))
	if err != nil {
		t.Fatal(err)
	}
	const shaC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	changes := map[string]func(*Pins){
		"playwright-go": func(p *Pins) { p.PlaywrightGoVersion = "v0.1" },
		"node":          func(p *Pins) { p.NodeSHA256 = shaC },
		"core":          func(p *Pins) { p.CoreSHA256 = shaC },
		"chromium":      func(p *Pins) { p.ChromiumSHA256 = shaC },
		"headless":      func(p *Pins) { p.HeadlessShellSHA256 = shaC },
		"ffmpeg":        func(p *Pins) { p.FFmpegSHA256 = shaC },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			p := base
			change(&p)
			if p.playwrightTag() == base.playwrightTag() {
				t.Errorf("tag %q did not change", p.playwrightTag())
			}
		})
	}
}

func TestLoadPins_rejectsBadValues(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		file string
		want string
	}{
		{"env version without sha256", map[string]string{EnvPiperVersion: "2030.1.1"}, "", EnvPiperSHA256},
		{"file version without sha256", nil, "SCREENCASTER_PIPER_VERSION=2030.1.1\n", EnvPiperSHA256},
		{"short sha256", map[string]string{EnvPiperSHA256: "abc"}, "", "64 lowercase hex"},
		{"unknown key in a file", nil, "SCREENCASTER_PIPER_VERSON=1\n", `unknown key "SCREENCASTER_PIPER_VERSON"`},
		{"line without =", nil, "just words\n", "want KEY=VALUE"},
		{"voice entry shape", map[string]string{EnvVoiceFiles: "x.onnx=only-two"}, "", "name=path=sha256"},
		{"voice name with a slash", map[string]string{EnvVoiceFiles: "../x.onnx=p=" + shaA}, "", "plain file name"},
		{"voice sha256", map[string]string{EnvVoiceFiles: "x.onnx=p=zz"}, "", "64 lowercase hex"},
		{"empty voice list", map[string]string{EnvVoiceFiles: " ; "}, "", "name=path=sha256"},
		{"node version without sha256", map[string]string{EnvNodeVersion: "99.1.0"}, "", EnvNodeSHA256},
		{"core version without sha256", nil, "SCREENCASTER_PLAYWRIGHT_CORE_VERSION=9.9.9\n", EnvCoreSHA256},
		{"chromium version without its shell sha256", map[string]string{EnvChromiumVersion: "199.0.1.2", EnvChromiumSHA256: shaA}, "", EnvHeadlessShellSHA256},
		{"ffmpeg revision without sha256", map[string]string{EnvFFmpegRevision: "2022"}, "", EnvFFmpegSHA256},
		{"playwright sha256 shape", map[string]string{EnvChromiumSHA256: "abc"}, "", EnvChromiumSHA256 + ": want 64 lowercase hex"},
		{"revision with a slash", map[string]string{EnvChromiumRevision: "../1234"}, "", "plain version or revision"},
		{"empty-looking version", map[string]string{EnvNodeVersion: "v 1", EnvNodeSHA256: shaA}, "", "plain version or revision"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var files []string
			if tt.file != "" {
				files = append(files, tt.file)
			}
			_, err := LoadPins(envOf(tt.env), files...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %v, want one containing %q", err, tt.want)
			}
		})
	}
}

// playwrightArgs are the Playwright pins as the root Dockerfile's ARG names
// (the pins.env key without its SCREENCASTER_ prefix) and their values.
func playwrightArgs(p Pins) map[string]string {
	return map[string]string{
		"PLAYWRIGHT_GO_VERSION":    p.PlaywrightGoVersion,
		"NODE_VERSION":             p.NodeVersion,
		"NODE_URL_BASE":            p.NodeURLBase,
		"NODE_SHA256":              p.NodeSHA256,
		"PLAYWRIGHT_CORE_VERSION":  p.CoreVersion,
		"PLAYWRIGHT_CORE_URL_BASE": p.CoreURLBase,
		"PLAYWRIGHT_CORE_SHA256":   p.CoreSHA256,
		"CHROMIUM_REVISION":        p.ChromiumRevision,
		"CHROMIUM_VERSION":         p.ChromiumVersion,
		"CHROMIUM_URL_BASE":        p.ChromiumURLBase,
		"CHROMIUM_SHA256":          p.ChromiumSHA256,
		"HEADLESS_SHELL_SHA256":    p.HeadlessShellSHA256,
		"FFMPEG_REVISION":          p.FFmpegRevision,
		"FFMPEG_SHA256":            p.FFmpegSHA256,
	}
}

func TestPins_playwrightMatchRootDockerfile(t *testing.T) {
	b, err := os.ReadFile(rootDockerfile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for name, want := range playwrightArgs(defaultPins(t)) {
		t.Run(name, func(t *testing.T) {
			m := regexp.MustCompile(`(?m)^ARG ` + name + `=(\S+)$`).FindStringSubmatch(text)
			if m == nil {
				t.Fatalf("no ARG %s= line in %s", name, rootDockerfile)
			}
			if m[1] != want {
				t.Errorf("Dockerfile has %s, pins.env has %s", m[1], want)
			}
		})
	}
}

// The Dockerfile builds its URLs from the ARGs; they must have the shape the Go
// builders (NodeURL, CoreURL, ChromiumURL, ...) produce.
func TestPins_playwrightURLsMatchTheDockerfileDownloads(t *testing.T) {
	b, err := os.ReadFile(rootDockerfile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{
		"${NODE_URL_BASE}v${NODE_VERSION}/node-v${NODE_VERSION}-linux-x64.tar.gz",
		"${PLAYWRIGHT_CORE_URL_BASE}playwright-core/-/playwright-core-${PLAYWRIGHT_CORE_VERSION}.tgz",
		"${CHROMIUM_URL_BASE}builds/cft/${CHROMIUM_VERSION}/linux64",
		"/" + chromiumPath[strings.LastIndex(chromiumPath, "/")+1:],
		"/" + headlessShellPath[strings.LastIndex(headlessShellPath, "/")+1:],
		"${CHROMIUM_URL_BASE}builds/ffmpeg/${FFMPEG_REVISION}/" + ffmpegAsset,
		"chromium-${CHROMIUM_REVISION}",
		"chromium_headless_shell-${CHROMIUM_REVISION}",
		"ffmpeg-${FFMPEG_REVISION}",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("Dockerfile lacks %q", want)
		}
	}
}

// The driver files are what this playwright-go version expects, so the pin, the
// module requirement and the image's ARG are one version.
func TestPins_playwrightGoVersionMatchesGoMod(t *testing.T) {
	b, err := os.ReadFile(goMod)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s*github\.com/mxschmitt/playwright-go (v\S+)`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatal("no github.com/mxschmitt/playwright-go requirement in go.mod")
	}
	if got := defaultPins(t).PlaywrightGoVersion; got != m[1] {
		t.Errorf("pins.env has playwright-go %s, go.mod requires %s", got, m[1])
	}
}
