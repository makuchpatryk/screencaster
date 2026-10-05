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

	ttsErr  error  // returned by every Synthesize
	failRec string // language whose recording fails
	asmErr  error
	cancel  context.CancelFunc // called during the first Record, if set

	recIn []recorder.Input
	asmIn []assembler.Input
}

func (f *fakes) Synthesize(_ context.Context, voice, text, out string) (time.Duration, error) {
	f.log = append(f.log, fmt.Sprintf("tts %s %s", filepath.Base(voice), filepath.Base(out)))
	if f.ttsErr != nil {
		return 0, f.ttsErr
	}
	return time.Duration(len(text)) * time.Second, os.WriteFile(out, []byte(text), 0o644)
}

func (f *fakes) Record(ctx context.Context, in recorder.Input) (recorder.Output, error) {
	f.log = append(f.log, "record "+in.Lang)
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
	if f.asmErr != nil {
		return 0, f.asmErr
	}
	return 4200, os.WriteFile(in.Out, []byte("mp4 "+in.Webm), 0o644)
}

var jobStart = time.Date(2026, 10, 3, 10, 15, 0, 0, time.UTC)

func (f *fakes) deps() Deps {
	return Deps{
		TTS: f, Rec: f, Asm: f,
		Voices: stock,
		Now:    func() time.Time { return jobStart },
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
	entries, _ := os.ReadDir(filepath.Join(dir, "output"))
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
		"tts en_US-ryan-high.onnx 1.wav", "tts en_US-ryan-high.onnx 3.wav", "record en", "assemble en",
		"tts pl_PL-darkman-medium.onnx 1.wav", "tts pl_PL-darkman-medium.onnx 3.wav", "record pl", "assemble pl",
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
		{Lang: "en", Path: filepath.Join(dir, "output", "demo.en.20261003T101500Z.mp4"), DurationMs: 4200},
		{Lang: "pl", Path: filepath.Join(dir, "output", "demo.pl.20261003T101500Z.mp4"), DurationMs: 4200},
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
	existing := filepath.Join(dir, "output", "demo.pl.20261003T101500Z.mp4")
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
