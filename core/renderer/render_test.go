package renderer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"screencaster/core/assembler"
	"screencaster/core/failure"
	"screencaster/core/recorder"
)

const twoStepsEnPl = `name: demo
baseUrl: http://host.docker.internal:3000
storageState:
  cookies:
    - {name: session, value: abc, domain: host.docker.internal, path: /}
languages: [en, pl]
meta:
  title: Demo title
  description: Demo description
steps:
  - action: goto
    url: /projects
    narration:
      en: Hello.
      pl: Cześć.
  - action: click
    selector: "#new"
  - action: click
    selector: "#save"
    narration:
      en: Saved.
      pl: Zapisane.
`

// fakes records every call in log, in order, and writes the files the real
// tools would, so publish has something to move.
type fakes struct {
	log []string

	ttsErr   error  // returned by every Synthesize
	failRec  string // language whose recording fails
	asmErr   error
	cardsErr error
	cancel   context.CancelFunc // called during the first Record, if set

	recIn []recorder.Input
	asmIn []assembler.Input
	shots [][]Shot // one entry per Screenshot call

	now time.Time // fake clock; each tool call advances it
}

// advance moves the fake clock, so phase lines get a known elapsed time.
func (f *fakes) advance(d time.Duration) {
	if f.now.IsZero() {
		f.now = jobStart
	}
	f.now = f.now.Add(d)
}

func (f *fakes) clock() time.Time {
	f.advance(0)
	return f.now
}

func (f *fakes) Screenshot(_ context.Context, shots []Shot) error {
	var names []string
	for _, sh := range shots {
		names = append(names, filepath.Base(sh.Out))
	}
	f.log = append(f.log, "cards "+strings.Join(names, ","))
	f.advance(time.Second)
	f.shots = append(f.shots, shots)
	if f.cardsErr != nil {
		return f.cardsErr
	}
	for _, sh := range shots {
		if err := os.WriteFile(sh.Out, []byte(sh.HTML), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakes) Synthesize(_ context.Context, voice, text, out string) (time.Duration, error) {
	f.log = append(f.log, fmt.Sprintf("tts %s %s", voice, filepath.Base(out)))
	f.advance(500 * time.Millisecond)
	if f.ttsErr != nil {
		return 0, f.ttsErr
	}
	return time.Duration(len(text)) * time.Second, os.WriteFile(out, []byte(text), 0o644)
}

func (f *fakes) Record(ctx context.Context, in recorder.Input) (recorder.Output, error) {
	f.log = append(f.log, "record "+in.Lang)
	f.advance(15 * time.Second)
	f.recIn = append(f.recIn, in)
	if f.cancel != nil {
		f.cancel()
		return recorder.Output{}, ctx.Err()
	}
	if in.Lang == f.failRec {
		step := 2
		return recorder.Output{}, &failure.Failure{Step: &step, Lang: in.Lang, Action: "click", Target: "#new", Message: "timeout"}
	}
	for i := range in.Steps {
		if in.OnStep != nil {
			in.OnStep(i)
		}
	}
	webm := filepath.Join(in.Dir, "rec.webm")
	offsets := make([]time.Duration, len(in.Steps))
	for i := range offsets {
		offsets[i] = time.Duration(i) * time.Second
	}
	return recorder.Output{WebmPath: webm, Offsets: offsets}, os.WriteFile(webm, []byte("webm"), 0o644)
}

func (f *fakes) Assemble(_ context.Context, in assembler.Input) (int64, error) {
	f.log = append(f.log, "assemble "+filepath.Base(filepath.Dir(in.Out)))
	f.asmIn = append(f.asmIn, in)
	f.advance(2 * time.Second)
	if f.asmErr != nil {
		return 0, f.asmErr
	}
	return 4200, os.WriteFile(in.Out, []byte("mp4 "+in.Webm), 0o644)
}

var jobStart = time.Date(2026, 10, 3, 10, 15, 0, 0, time.UTC)

func (f *fakes) deps() Deps {
	return Deps{
		TTS: f, Rec: f, Asm: f, Cards: f,
		Voices: stock,
		Now:    f.clock,
		RunID:  func() string { return "run1" },
	}
}

func request(dir string) Request {
	return Request{WorkDir: dir, ScriptPath: "demos/demo.yaml"}
}

// assertNoLeftovers checks the temp dir is gone and outputDir holds only want.
func assertNoLeftovers(t *testing.T, dir string, want ...string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, ".screencaster", "tmp", "run1")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temp dir still exists (stat err = %v)", err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "demos", "output"))
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("output dir = %v, want %v", got, want)
	}
}

func TestRender_validationFailsBeforeAnyWork(t *testing.T) { // FR-002, BR-011
	dir := workDir(t, enPlMissingPl)
	f := &fakes{}

	_, err := Render(context.Background(), f.deps(), request(dir))
	var verrs failure.ValidationErrors
	if !errors.As(err, &verrs) {
		t.Fatalf("err = %v, want ValidationErrors", err)
	}
	if len(f.log) != 0 {
		t.Errorf("work started before validation passed: %v", f.log)
	}
}

func TestRender_ttsRunsBeforeRecordingPerLanguage(t *testing.T) { // FR-003, FR-004, ARCHITECTURE §4
	dir := workDir(t, twoStepsEnPl)
	f := &fakes{}

	if _, err := Render(context.Background(), f.deps(), request(dir)); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"tts en_US-ryan-high 1.wav", "tts en_US-ryan-high 3.wav", "cards intro.png,outro.png", "record en", "assemble en",
		"tts pl_PL-darkman-medium 1.wav", "tts pl_PL-darkman-medium 3.wav", "cards intro.png,outro.png", "record pl", "assemble pl",
	}
	if !reflect.DeepEqual(f.log, want) {
		t.Errorf("calls =\n%q\nwant\n%q", f.log, want)
	}
}

func TestRender_clipsReachRecorderAndAssembler(t *testing.T) { // FR-007, FR-009.1, FR-009.5
	dir := workDir(t, twoStepsEnPl)
	f := &fakes{}

	if _, err := Render(context.Background(), f.deps(), request(dir)); err != nil {
		t.Fatal(err)
	}
	en := f.recIn[0]
	if want := map[int]time.Duration{0: 6 * time.Second, 2: 6 * time.Second}; !reflect.DeepEqual(en.Clips, want) {
		t.Errorf("recorder clips = %v, want %v (keyed by step, unnarrated step absent)", en.Clips, want)
	}
	if en.BaseURL != "http://host.docker.internal:3000" || en.StorageState == nil || len(en.StorageState.Cookies) != 1 || en.StorageState.Cookies[0].Name != "session" {
		t.Errorf("recorder context = %q, %+v", en.BaseURL, en.StorageState)
	}

	asm := f.asmIn[0]
	var offsets []time.Duration
	for _, c := range asm.Clips {
		offsets = append(offsets, c.Offset)
	}
	if want := []time.Duration{0, 2 * time.Second}; !reflect.DeepEqual(offsets, want) {
		t.Errorf("clip offsets = %v, want the recorded offsets of steps 1 and 3 %v", offsets, want)
	}
	if asm.Title != "Demo title" || asm.Comment != "Demo description" {
		t.Errorf("meta = %q, %q", asm.Title, asm.Comment)
	}
}

func TestRender_publishesWithSharedTimestamp(t *testing.T) { // FR-010
	dir := workDir(t, twoStepsEnPl)
	f := &fakes{}

	outs, err := Render(context.Background(), f.deps(), request(dir))
	if err != nil {
		t.Fatal(err)
	}
	want := []Output{
		{Lang: "en", Path: filepath.Join(dir, "demos", "output", "demo.en.20261003T101500Z.mp4"), DurationMs: 4200},
		{Lang: "pl", Path: filepath.Join(dir, "demos", "output", "demo.pl.20261003T101500Z.mp4"), DurationMs: 4200},
	}
	if !reflect.DeepEqual(outs, want) {
		t.Errorf("outputs = %+v, want %+v", outs, want)
	}
	assertNoLeftovers(t, dir, "demo.en.20261003T101500Z.mp4", "demo.pl.20261003T101500Z.mp4")
}

func TestRender_plFailureDiscardsEn(t *testing.T) { // BR-004, FR-008
	dir := workDir(t, twoStepsEnPl)
	f := &fakes{failRec: "pl"}

	_, err := Render(context.Background(), f.deps(), request(dir))
	var fl *failure.Failure
	if !errors.As(err, &fl) || fl.Lang != "pl" || *fl.Step != 2 {
		t.Fatalf("err = %v, want the pl step 2 failure", err)
	}
	assertNoLeftovers(t, dir)
}

func TestRender_neverOverwrites(t *testing.T) { // BR-006
	dir := workDir(t, twoStepsEnPl)
	existing := filepath.Join(dir, "demos", "output", "demo.pl.20261003T101500Z.mp4")
	if err := os.MkdirAll(filepath.Dir(existing), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakes{}

	_, err := Render(context.Background(), f.deps(), request(dir))
	if err == nil || !strings.Contains(err.Error(), "output already exists") {
		t.Fatalf("err = %v, want output already exists", err)
	}
	if b, _ := os.ReadFile(existing); string(b) != "old" {
		t.Errorf("existing output changed to %q", b)
	}
	assertNoLeftovers(t, dir, "demo.pl.20261003T101500Z.mp4") // en not published either
}

func TestRender_cancelRemovesTempDir(t *testing.T) { // FR-008
	dir := workDir(t, twoStepsEnPl)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakes{cancel: cancel}

	_, err := Render(ctx, f.deps(), request(dir))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	assertNoLeftovers(t, dir)
}

func TestRender_toolFailuresBecomeFailures(t *testing.T) { // FR-003, FR-009 edge cases
	tests := []struct {
		name string
		f    *fakes
		want string
	}{
		{"tts", &fakes{ttsErr: errors.New("Model file doesn't exist")}, "tts failed at step 1 (en): Model file doesn't exist"},
		{"assembly", &fakes{asmErr: errors.New("line 1\nline 2")}, "assembly failed (en): line 1\nline 2"},
		{"cards", &fakes{cardsErr: errors.New("chromium crashed")}, "build cards (en): chromium crashed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := workDir(t, twoStepsEnPl)

			_, err := Render(context.Background(), tt.f.deps(), request(dir))
			var fl *failure.Failure
			if !errors.As(err, &fl) || fl.Error() != tt.want {
				t.Fatalf("err = %v, want Failure %q", err, tt.want)
			}
			assertNoLeftovers(t, dir)
		})
	}
}

func TestRender_progressNamesEachStep(t *testing.T) { // FR-011 progress on stderr
	dir := workDir(t, twoStepsEnPl)
	f := &fakes{}
	req := request(dir)
	req.LangOverride = []string{"en"}
	var lines []string
	req.Progress = func(lang string, i, n int, action, target string) {
		lines = append(lines, fmt.Sprintf("[%s] %d/%d %s %s", lang, i, n, action, target))
	}

	if _, err := Render(context.Background(), f.deps(), req); err != nil {
		t.Fatal(err)
	}
	want := []string{"[en] 1/3 goto /projects", "[en] 2/3 click #new", "[en] 3/3 click #save"}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("progress = %q, want %q", lines, want)
	}
}

func TestRender_logsStartPhasesAndEnd(t *testing.T) { // decision 64
	dir := workDir(t, twoStepsEnPl)
	f := &fakes{}
	req := request(dir)
	var lines []string
	req.Log = func(msg string) { lines = append(lines, msg) }

	if _, err := Render(context.Background(), f.deps(), req); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "demos", "output")
	want := []string{
		"render start: demos/demo.yaml name=demo languages=en,pl voices=en=en_US-ryan-high,pl=pl_PL-darkman-medium steps=3 output=" + out,
		"[en] narration: 2 clips in 1s",
		"[en] cards: 2 built in 1s",
		"[en] recording",
		"[en] recorded in 15s",
		"[en] assembled 4.2s video in 2s",
		"[pl] narration: 2 clips in 1s",
		"[pl] cards: 2 built in 1s",
		"[pl] recording",
		"[pl] recorded in 15s",
		"[pl] assembled 4.2s video in 2s",
		"render done in 38s",
		"[en] " + filepath.Join(out, "demo.en.20261003T101500Z.mp4") + " (4.2s)",
		"[pl] " + filepath.Join(out, "demo.pl.20261003T101500Z.mp4") + " (4.2s)",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("log =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestRender_logsEndLineOnFailure(t *testing.T) { // decision 64
	dir := workDir(t, twoStepsEnPl)
	f := &fakes{failRec: "pl"}
	req := request(dir)
	var lines []string
	req.Log = func(msg string) { lines = append(lines, msg) }

	if _, err := Render(context.Background(), f.deps(), req); err == nil {
		t.Fatal("Render() error = nil")
	}
	if last := lines[len(lines)-1]; last != "render failed after 36s" { // en 19 s, then pl narration, cards and the failing recording 17 s
		t.Errorf("last line = %q, want the failed end line", last)
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "render done") {
			t.Errorf("a failed render logged %q", l)
		}
	}
}

func TestRender_validationFailureLogsNothing(t *testing.T) { // the CLI prints the error itself
	dir := workDir(t, enPlMissingPl)
	req := request(dir)
	req.Log = func(msg string) { t.Errorf("logged %q before validation passed", msg) }

	_, _ = Render(context.Background(), (&fakes{}).deps(), req)
}

func TestRender_nilLogIsFine(t *testing.T) {
	dir := workDir(t, twoStepsEnPl)
	req := request(dir) // neither Log nor Progress set

	if _, err := Render(context.Background(), (&fakes{}).deps(), req); err != nil {
		t.Fatal(err)
	}
}

func TestRender_cardsBuiltBeforeRecording(t *testing.T) { // FR-002 spirit: fail early
	dir := workDir(t, twoStepsEnPl)
	f := &fakes{cardsErr: errors.New("chromium crashed")}

	_, err := Render(context.Background(), f.deps(), request(dir))
	var fl *failure.Failure
	if !errors.As(err, &fl) || fl.Lang != "en" || fl.Error() != "build cards (en): chromium crashed" {
		t.Fatalf("err = %v, want a Failure for en with the card message", err)
	}
	for _, c := range f.log {
		if strings.HasPrefix(c, "record") || strings.HasPrefix(c, "assemble") {
			t.Errorf("work went on after the card failed: %v", f.log)
		}
	}
	assertNoLeftovers(t, dir) // BR-004
}

func TestRender_cardsGetTheirTextAndAssemblerGetsStills(t *testing.T) { // decision 63
	dir := workDir(t, twoStepsEnPl)
	f := &fakes{}

	if _, err := Render(context.Background(), f.deps(), request(dir)); err != nil {
		t.Fatal(err)
	}
	if len(f.shots) != 2 || len(f.shots[0]) != 2 {
		t.Fatalf("screenshots = %d calls, want one call with two cards per language", len(f.shots))
	}
	en, pl := f.shots[0], f.shots[1]
	for _, c := range []struct{ got, want string }{
		{en[0].HTML, "Demo title"}, {en[0].HTML, "Demo description"}, // intro: title and description
		{en[1].HTML, "Thank you for watching"}, {en[1].HTML, "Demo title"}, // outro: closing line, then the title
		{pl[1].HTML, "Dziękujemy za uwagę"},
	} {
		if !strings.Contains(c.got, c.want) {
			t.Errorf("card page lacks %q:\n%s", c.want, c.got)
		}
	}

	asm := f.asmIn[0]
	if asm.Intro == nil || asm.Intro.Path != en[0].Out || asm.Intro.Duration != 3*time.Second ||
		asm.Outro == nil || asm.Outro.Path != en[1].Out || asm.Outro.Duration != 3*time.Second {
		t.Errorf("stills = %+v, %+v; want the two screenshots for 3 s each", asm.Intro, asm.Outro)
	}
}

func TestRender_customImageSkipsScreenshot(t *testing.T) { // decision 63
	script := strings.Replace(twoStepsEnPl, "steps:", "intro:\n  image: assets/logo.png\n  durationMs: 2000\nsteps:", 1)
	dir := workDir(t, script)
	writeFile(t, dir, "demos/assets/logo.png")
	f := &fakes{}
	req := request(dir)
	req.LangOverride = []string{"en"}

	if _, err := Render(context.Background(), f.deps(), req); err != nil {
		t.Fatal(err)
	}
	if len(f.shots) != 1 || len(f.shots[0]) != 1 || filepath.Base(f.shots[0][0].Out) != "outro.png" {
		t.Errorf("screenshots = %v, want only the outro, drawn", f.shots)
	}
	want := &assembler.Still{Path: filepath.Join(dir, "demos", "assets", "logo.png"), Duration: 2 * time.Second}
	if got := f.asmIn[0].Intro; !reflect.DeepEqual(got, want) {
		t.Errorf("Intro = %+v, want the custom image %+v", got, want)
	}
	if got := f.recIn[0].StartImage; got != want.Path {
		t.Errorf("StartImage = %q, want the intro picture %q", got, want.Path)
	}
}

func TestRender_cardsOffGiveNoStills(t *testing.T) { // decision 63
	script := strings.Replace(twoStepsEnPl, "steps:", "intro: false\noutro: false\nsteps:", 1)
	dir := workDir(t, script)
	f := &fakes{}
	req := request(dir)
	req.LangOverride = []string{"en"}

	if _, err := Render(context.Background(), f.deps(), req); err != nil {
		t.Fatal(err)
	}
	if len(f.shots) != 0 {
		t.Errorf("screenshots = %v, want none", f.shots)
	}
	if asm := f.asmIn[0]; asm.Intro != nil || asm.Outro != nil {
		t.Errorf("stills = %+v, %+v, want none", asm.Intro, asm.Outro)
	}
	if got := f.recIn[0].StartImage; got != "" {
		t.Errorf("StartImage = %q, want none without an intro", got)
	}
}

func TestCopyThenRename_publishesWithoutPartLeft(t *testing.T) { // cross-filesystem publish
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.mp4"), filepath.Join(dir, "out", "demo.en.mp4")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := copyThenRename(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "video" {
		t.Errorf("dst = %q, want video", b)
	}
	if _, err := os.Stat(dst + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".part left behind (stat err = %v)", err)
	}
}

func TestCopyThenRename_refusesExistingPart(t *testing.T) { // BR-006
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.mp4"), filepath.Join(dir, "demo.en.mp4")
	for p, c := range map[string]string{src: "video", dst + ".part": "someone else's"} {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := copyThenRename(src, dst); err == nil {
		t.Fatal("copy over an existing .part succeeded")
	}
	if b, _ := os.ReadFile(dst + ".part"); string(b) != "someone else's" {
		t.Errorf(".part was touched: %q", b)
	}
}

func TestPublish_failedMoveRemovesEarlierOutputs(t *testing.T) { // BR-004
	dir := t.TempDir()
	src := filepath.Join(dir, "en.mp4")
	if err := os.WriteFile(src, []byte("en"), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "output")

	_, err := publish(outDir, "demo", jobStart, []Output{
		{Lang: "en", Path: src},
		{Lang: "pl", Path: filepath.Join(dir, "missing.mp4")},
	})
	if err == nil {
		t.Fatal("publish with a missing source succeeded")
	}
	if entries, _ := os.ReadDir(outDir); len(entries) != 0 {
		t.Errorf("output dir not rolled back: %v", entries)
	}
}
