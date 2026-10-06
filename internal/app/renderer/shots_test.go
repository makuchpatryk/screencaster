package renderer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"screencaster/internal/domain/failure"
	"screencaster/internal/domain/script"
	"screencaster/internal/domain/shooter"
	"screencaster/internal/domain/voices"
)

const shotsScript = `name: shots
type: screenshots
baseUrl: http://host.docker.internal:3000
storageState:
  cookies:
    - {name: session, value: abc, domain: host.docker.internal, path: /}
steps:
  - goto: /projects
  - screenshot: true
  - click: "#new"
  - screenshot: { selector: "#form" }
`

// shotsOut is where shotsScript publishes: <outputDir>/<name>/screenshots.
const shotsOut = work + "/demos/output/shots/screenshots"

// fakeShooter writes n PNGs like the real one (01.png, 02.png, ...) into the
// request's dir and records the request. The fake clock advances 3 s per run.
type fakeShooter struct {
	n   int
	err error // returned instead of PNGs

	in         []ShootRequest
	dirExisted bool // the temp dir existed when Shoot was called

	files *memFS
	clock *fakes
}

func (s *fakeShooter) Shoot(ctx context.Context, r ShootRequest) ([]string, error) {
	s.in = append(s.in, r)
	s.dirExisted = s.files.exists(r.Dir)
	s.clock.advance(3 * time.Second)
	if s.err != nil {
		return nil, s.err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	for i := range r.Steps {
		if r.OnStep != nil {
			r.OnStep(i)
		}
	}
	var paths []string
	for k := 1; k <= s.n; k++ {
		p := filepath.Join(r.Dir, fmt.Sprintf("%02d.png", k))
		s.files.write(p, fmt.Sprintf("png%d", k))
		paths = append(paths, p)
	}
	return paths, nil
}

// shotsDeps wires a fakeShooter next to the video fakes, so a test can also
// see that no video tool ran (f.log stays empty).
func shotsDeps(f *fakes, sh *fakeShooter, files *memFS) Deps {
	d := f.deps(files)
	sh.files, sh.clock = files, f
	d.Shots = sh
	return d
}

func TestPrepare_screenshotsNeedNoVoicesLanguagesOrCards(t *testing.T) { // decision 72
	dir, files := workDir(shotsScript)

	// An empty catalog would fail every video script (BR-011).
	plan, err := Prepare(request(dir), voices.Catalog{}, files)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if plan.Type != script.TypeScreenshots {
		t.Errorf("Type = %q, want %q", plan.Type, script.TypeScreenshots)
	}
	if plan.Languages != nil || plan.Voices != nil {
		t.Errorf("Languages, Voices = %v, %v; want none", plan.Languages, plan.Voices)
	}
	if plan.Intro != (Card{}) || plan.Outro != (Card{}) {
		t.Errorf("Intro, Outro = %+v, %+v; want zero cards", plan.Intro, plan.Outro)
	}
	if want := filepath.Join(dir, "demos", "output"); plan.OutputDir != want {
		t.Errorf("OutputDir = %q, want %q", plan.OutputDir, want)
	}
}

func TestPrepare_screenshotsRejectALanguageOverride(t *testing.T) { // decision 72
	dir, files := workDir(shotsScript)
	req := request(dir)
	req.LangOverride = []string{"pl"}

	_, err := Prepare(req, stock, files)
	var ve failure.ValidationErrors
	if !errors.As(err, &ve) {
		t.Fatalf("Prepare() error = %v, want ValidationErrors", err)
	}
	want := failure.ValidationErrors{{Pointer: "/languages", Message: "languages are not used by a screenshots script"}}
	if !reflect.DeepEqual(ve, want) {
		t.Errorf("errors = %v, want %v", ve, want)
	}
}

func TestPrepare_screenshotsKeepTheOutputDirCheck(t *testing.T) {
	dir, files := workDir(strings.Replace(shotsScript, "type: screenshots\n", "type: screenshots\noutputDir: ../../elsewhere\n", 1))

	_, err := Prepare(request(dir), stock, files)
	var ve failure.ValidationErrors
	if !errors.As(err, &ve) || len(ve) != 1 || ve[0].Pointer != "/outputDir" {
		t.Errorf("Prepare() error = %v, want one error at /outputDir", err)
	}
}

func TestPrepare_videoPlanTypeIsVideo(t *testing.T) {
	dir, files := workDir(enOnly)

	plan, err := Prepare(request(dir), stock, files)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Type != script.TypeVideo {
		t.Errorf("Type = %q, want %q", plan.Type, script.TypeVideo)
	}
}

func TestRender_screenshotsPublishToAStableFolder(t *testing.T) { // decision 73
	dir, files := workDir(shotsScript)
	f, sh := &fakes{}, &fakeShooter{n: 2}

	outs, err := Render(context.Background(), shotsDeps(f, sh, files), request(dir))
	if err != nil {
		t.Fatal(err)
	}
	want := []Output{{Path: shotsOut + "/01.png"}, {Path: shotsOut + "/02.png"}}
	if !reflect.DeepEqual(outs, want) {
		t.Errorf("outputs = %+v, want %+v", outs, want)
	}
	for i, content := range []string{"png1", "png2"} {
		if got, _ := files.read(outs[i].Path); got != content {
			t.Errorf("%s = %q, want %q", outs[i].Path, got, content)
		}
	}
	if len(f.log) != 0 {
		t.Errorf("video tools ran for a screenshots script: %v", f.log)
	}
	if files.exists(filepath.Join(dir, ".screencaster", "tmp", "run1")) {
		t.Error("temp dir still exists")
	}
}

func TestRender_screenshotsRequestCarriesTheScript(t *testing.T) {
	dir, files := workDir(shotsScript)
	f, sh := &fakes{}, &fakeShooter{n: 2}

	if _, err := Render(context.Background(), shotsDeps(f, sh, files), request(dir)); err != nil {
		t.Fatal(err)
	}
	if len(sh.in) != 1 {
		t.Fatalf("Shoot called %d times, want 1", len(sh.in))
	}
	in := sh.in[0]
	if want := filepath.Join(dir, ".screencaster", "tmp", "run1", "shots"); in.Dir != want {
		t.Errorf("Dir = %q, want %q", in.Dir, want)
	}
	if !sh.dirExisted {
		t.Error("the temp dir did not exist when Shoot was called")
	}
	if in.BaseURL != "http://host.docker.internal:3000" || len(in.Steps) != 4 ||
		in.StorageState == nil || in.StorageState.Cookies[0].Name != "session" {
		t.Errorf("request = %+v, want the script's base URL, 4 steps and the storage state", in)
	}
}

func TestRender_screenshotsProgressHasNoLanguage(t *testing.T) { // CLI prints `step i/n` for lang ""
	dir, files := workDir(shotsScript)
	f, sh := &fakes{}, &fakeShooter{n: 2}
	req := request(dir)
	var lines []string
	req.Progress = func(lang string, i, n int, action, target string) {
		lines = append(lines, fmt.Sprintf("[%s] %d/%d %s %s", lang, i, n, action, target))
	}

	if _, err := Render(context.Background(), shotsDeps(f, sh, files), req); err != nil {
		t.Fatal(err)
	}
	want := []string{"[] 1/4 goto /projects", "[] 2/4 screenshot ", "[] 3/4 click #new", "[] 4/4 screenshot #form"}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("progress = %q, want %q", lines, want)
	}
}

func TestRender_screenshotsLogStartAndEnd(t *testing.T) { // decision 64
	dir, files := workDir(shotsScript)
	f, sh := &fakes{}, &fakeShooter{n: 2}
	req := request(dir)
	var lines []string
	req.Log = func(msg string) { lines = append(lines, msg) }

	if _, err := Render(context.Background(), shotsDeps(f, sh, files), req); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"render start: demos/demo.yaml name=shots type=screenshots steps=4 output=" + filepath.Join(dir, "demos", "output"),
		"screenshots: 2 captured in 3s",
		"render done in 3s",
		shotsOut + "/01.png",
		shotsOut + "/02.png",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("log =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestRender_screenshotsOverwriteTheSameNamesOnRerun(t *testing.T) { // decision 73
	dir, files := workDir(shotsScript)
	files.write(shotsOut+"/01.png", "old1")
	files.write(shotsOut+"/02.png", "old2")
	f, sh := &fakes{}, &fakeShooter{n: 2}

	if _, err := Render(context.Background(), shotsDeps(f, sh, files), request(dir)); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"01.png": "png1", "02.png": "png2"} {
		if got, _ := files.read(shotsOut + "/" + name); got != want {
			t.Errorf("%s = %q, want the new shot %q", name, got, want)
		}
	}
}

// Only NN.png files this run did not write are stale; anything else in the
// folder is the user's.
func TestRender_screenshotsRemoveStaleShotsAndKeepOtherFiles(t *testing.T) { // decision 73
	dir, files := workDir(shotsScript)
	for _, name := range []string{"01.png", "02.png", "03.png", "004.png", "cover.png", "notes.txt", "1.png", "03.png.part"} {
		files.write(shotsOut+"/"+name, "old")
	}
	files.write(shotsOut+"/sub/05.png", "nested") // a file in a subfolder is not ours
	f, sh := &fakes{}, &fakeShooter{n: 2}

	if _, err := Render(context.Background(), shotsDeps(f, sh, files), request(dir)); err != nil {
		t.Fatal(err)
	}
	want := []string{"01.png", "02.png", "03.png.part", "1.png", "cover.png", "notes.txt", "sub"}
	if got := files.list(shotsOut); !reflect.DeepEqual(got, want) {
		t.Errorf("folder = %v, want %v", got, want)
	}
	if got, _ := files.read(shotsOut + "/sub/05.png"); got != "nested" {
		t.Errorf("subfolder file = %q, want it untouched", got)
	}
}

func TestRender_screenshotsFailureLeavesOutputDirUntouched(t *testing.T) { // BR-004
	dir, files := workDir(shotsScript)
	files.write(shotsOut+"/01.png", "old1")
	files.write(shotsOut+"/02.png", "old2")
	step := 3
	boom := &failure.Failure{Step: &step, Action: "click", Target: "#new", Message: "timeout"}
	f, sh := &fakes{}, &fakeShooter{err: boom}
	req := request(dir)
	var lines []string
	req.Log = func(msg string) { lines = append(lines, msg) }

	_, err := Render(context.Background(), shotsDeps(f, sh, files), req)
	var fl *failure.Failure
	if !errors.As(err, &fl) || *fl.Step != 3 {
		t.Fatalf("err = %v, want the step 3 failure", err)
	}
	if got := files.list(shotsOut); !reflect.DeepEqual(got, []string{"01.png", "02.png"}) {
		t.Errorf("folder = %v, want the old shots", got)
	}
	if b, _ := files.read(shotsOut + "/01.png"); b != "old1" {
		t.Errorf("01.png = %q, want the old shot", b)
	}
	if files.exists(filepath.Join(dir, ".screencaster", "tmp", "run1")) {
		t.Error("temp dir still exists")
	}
	if last := lines[len(lines)-1]; last != "render failed after 3s" {
		t.Errorf("last line = %q, want the failed end line", last)
	}
}

func TestRender_screenshotsFailureCreatesNoFolder(t *testing.T) { // BR-004: nothing reaches outputDir
	dir, files := workDir(shotsScript)
	f, sh := &fakes{}, &fakeShooter{err: errors.New("no chromium")}

	if _, err := Render(context.Background(), shotsDeps(f, sh, files), request(dir)); err == nil {
		t.Fatal("Render() error = nil")
	}
	if files.exists(work + "/demos/output") {
		t.Error("output dir was created by a failed run")
	}
}

func TestRender_screenshotsCancelRemovesTempDir(t *testing.T) { // FR-008
	dir, files := workDir(shotsScript)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f, sh := &fakes{}, &fakeShooter{n: 2}

	_, err := Render(ctx, shotsDeps(f, sh, files), request(dir))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if files.exists(filepath.Join(dir, ".screencaster", "tmp", "run1")) {
		t.Error("temp dir still exists")
	}
}

func TestRender_screenshotsRejectALanguageOverrideBeforeAnyWork(t *testing.T) {
	dir, files := workDir(shotsScript)
	f, sh := &fakes{}, &fakeShooter{n: 2}
	req := request(dir)
	req.LangOverride = []string{"en"}

	if _, err := Render(context.Background(), shotsDeps(f, sh, files), req); err == nil {
		t.Fatal("Render() error = nil")
	}
	if len(sh.in) != 0 {
		t.Error("the shooter ran before validation passed")
	}
}

func TestPublishShots_crossDeviceReplacesWithoutAPartLeft(t *testing.T) { // decision 73, rule 5
	files := newMemFS()
	files.write("/tmp/01.png", "new")
	files.write("/work/shots/01.png", "old")
	files.mount = "/work"

	got, err := publishShots(files, "/work/shots", []string{"/tmp/01.png"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/work/shots/01.png"}; !reflect.DeepEqual(got, want) {
		t.Errorf("paths = %v, want %v", got, want)
	}
	if b, _ := files.read("/work/shots/01.png"); b != "new" {
		t.Errorf("01.png = %q, want the new shot", b)
	}
	if names := files.list("/work/shots"); !reflect.DeepEqual(names, []string{"01.png"}) {
		t.Errorf("folder = %v, want only 01.png (no .part)", names)
	}
}

func TestPublishShots_failedMoveNamesTheFile(t *testing.T) {
	files := newMemFS()
	files.write("/tmp/01.png", "new")
	files.write("/work/shots/03.png", "old")

	_, err := publishShots(files, "/work/shots", []string{"/tmp/01.png", "/tmp/missing.png"})
	if err == nil || !strings.Contains(err.Error(), "publish /work/shots/missing.png") {
		t.Fatalf("publishShots() error = %v, want it to name the failed file", err)
	}
	// Stale removal never runs after a failure: the folder is a mix, not emptied.
	if b, _ := files.read("/work/shots/03.png"); b != "old" {
		t.Errorf("03.png = %q, want it kept after a failed publish", b)
	}
}

// shotFile repeats the naming rule of shooter.ShotName so renderer needs no
// shooter import. Every name ShotName gives for a legal run (schema: at most
// 200 steps) must match, or stale shots of that width would never be cleaned up.
func TestShotFile_matchesEveryNameShotNameGives(t *testing.T) {
	for n := 1; n <= 200; n++ {
		for k := 1; k <= n; k++ {
			if name := shooter.ShotName(k, n); !shotFile.MatchString(name) {
				t.Fatalf("ShotName(%d, %d) = %q does not match %v", k, n, name, shotFile)
			}
		}
	}
}
