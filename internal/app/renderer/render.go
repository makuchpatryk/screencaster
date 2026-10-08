package renderer

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"screencaster/internal/domain/card"
	"screencaster/internal/domain/failure"
	"screencaster/internal/domain/script"
	"screencaster/internal/domain/voices"
)

// The ports below name no tool package: app/wire maps them onto the browser,
// TTS and ffmpeg wrappers (ARCHITECTURE §3). The mapping repeats a few struct
// shapes on purpose, so the renderer stays free of tool types.

// Synthesizer turns text into a WAV at outPath and returns its duration.
// voice is a name from the provider's catalog, opaque to the
// renderer.
type Synthesizer interface {
	Synthesize(ctx context.Context, voice, text, outPath string) (time.Duration, error)
}

// RecordRequest is one language's recording. Clips maps a step index
// (0-based, into Steps) to the duration of its narration clip. StartImage is
// the picture the page shows until the first goto paints ("" for the plain
// card colour). OnStep, when set, is called with the 0-based index right
// before each step runs.
type RecordRequest struct {
	Steps        []script.Step
	Clips        map[int]time.Duration
	Lang         string
	Dir          string               // video output directory
	StorageState *script.StorageState // nil: logged-out session
	StartImage   string
	OnStep       func(i int)
}

// Recording is a finished recording: the video file and, per step, where its
// narration is placed in the video (the step start plus the recorder's lag).
type Recording struct {
	Video   string
	Offsets []time.Duration
}

// Recorder records one language. A step failure comes back as
// *failure.Failure with Lang set; a cancelled ctx as ctx.Err().
type Recorder interface {
	Record(ctx context.Context, r RecordRequest) (Recording, error)
}

// Clip is one narration WAV and where it starts in the recording.
type Clip struct {
	Path   string
	Offset time.Duration
}

// Still is a card picture shown full-frame for Duration.
type Still struct {
	Path     string
	Duration time.Duration
}

// AssembleRequest is one language's assembly. Intro and Outro are nil for a
// card that is off; clip offsets are relative to the recording. Title and
// Comment become MP4 tags when not empty. Out must not exist yet.
type AssembleRequest struct {
	Video          string
	Clips          []Clip
	Intro, Outro   *Still
	Title, Comment string
	Out            string
}

// Assembly is a written MP4: its duration and where the recording was cut, at
// the end of the sync marker.
type Assembly struct {
	DurationMs int64
	MarkerEnd  time.Duration
}

// ErrNoMarker is the Assembler's error for a recording without the sync
// marker; the render fails with failure.SyncMarker (decision 69).
var ErrNoMarker = errors.New("sync marker not found")

// Assembler cuts the recording at the end of its sync marker, mixes the clips
// in and writes the MP4. Its error text is the tool's stderr tail, or
// ErrNoMarker.
type Assembler interface {
	Assemble(ctx context.Context, r AssembleRequest) (Assembly, error)
}

// Shot is one card picture to take: an HTML page and the PNG path it goes to.
type Shot struct{ HTML, Out string }

// Cards takes the screenshots of the built-in start and end cards, all in one
// browser launch.
type Cards interface {
	Screenshot(ctx context.Context, shots []Shot) error
}

// ShootRequest is one screenshots run (repeats domain/shooter.Input on
// purpose, ARCHITECTURE §3). OnStep, when set, is called with the 0-based index
// right before each step runs.
type ShootRequest struct {
	Steps        []script.Step
	Dir          string               // PNGs are written here
	StorageState *script.StorageState // nil: logged-out session
	OnStep       func(i int)
}

// Shooter runs a screenshots script and returns the PNG paths in step order.
// A step failure comes back as *failure.Failure; a cancelled ctx as ctx.Err().
type Shooter interface {
	Shoot(ctx context.Context, r ShootRequest) ([]string, error)
}

// Deps are the collaborators and the environment of a render, wired by
// app.NewDeps.
// Now and RunID are injected so tests get fixed names (CODE_QUALITY, SOLID).
type Deps struct {
	TTS    Synthesizer
	Rec    Recorder
	Asm    Assembler
	Cards  Cards
	Shots  Shooter
	Files  Files
	Voices voices.Catalog
	Now    func() time.Time
	RunID  func() string
}

// Output is one published file: a video per language, or a PNG of a
// screenshots script (Lang empty, DurationMs 0).
type Output struct {
	Lang       string
	Path       string // absolute
	DurationMs int64
}

// reporter is how a render talks to its caller: step progress and log lines,
// with the clock that times the phases.
type reporter struct {
	progress Progress
	log      func(msg string)
	now      func() time.Time
}

// logf sends one line to Request.Log, if the caller set one.
func (r reporter) logf(format string, a ...any) {
	if r.log != nil {
		r.log(fmt.Sprintf(format, a...))
	}
}

// since is the time elapsed since t, to a tenth of a second.
func (r reporter) since(t time.Time) string { return round(r.now().Sub(t)) }

func round(d time.Duration) string { return d.Round(100 * time.Millisecond).String() }

// Render validates the request and renders by the script's type. A video
// script renders every selected language in order and publishes the MP4s only
// when all of them succeeded (BR-004, FR-010); a screenshots script takes its
// PNGs and publishes them (decision 73). Work happens under
// <work>/.screencaster/tmp/<runId>, which is removed on every path. A step,
// TTS or assembly problem comes back as *failure.Failure; a cancelled ctx as
// ctx.Err(). Once the script is accepted, Request.Log gets a start line, one
// line per phase and language, and an end line (decision 64).
func Render(ctx context.Context, d Deps, req Request) (_ []Output, err error) {
	start := d.Now() // FR-010: the job start time names every output
	plan, err := Prepare(req, d.Voices, d.Files)
	if err != nil {
		return nil, err
	}

	rep := reporter{progress: req.Progress, log: req.Log, now: d.Now}
	if plan.Type == script.TypeScreenshots {
		rep.logf("render start: %s name=%s type=screenshots steps=%d output=%s",
			req.ScriptPath, plan.Script.Name, len(plan.Script.Steps), plan.OutputDir)
	} else {
		rep.logf("render start: %s name=%s languages=%s voices=%s steps=%d output=%s",
			req.ScriptPath, plan.Script.Name, strings.Join(plan.Languages, ","), voiceNames(plan), len(plan.Script.Steps), plan.OutputDir)
	}
	defer func() {
		if err != nil {
			rep.logf("render failed after %s", rep.since(start))
		}
	}()

	runDir := filepath.Join(req.WorkDir, ".screencaster", "tmp", d.RunID())
	defer func() { _ = d.Files.RemoveAll(runDir) }()

	if plan.Type == script.TypeScreenshots {
		return renderShots(ctx, d, plan, runDir, start, rep)
	}
	return renderVideo(ctx, d, plan, runDir, start, rep)
}

// renderVideo renders every language, then publishes the MP4s.
func renderVideo(ctx context.Context, d Deps, plan Plan, runDir string, start time.Time, rep reporter) ([]Output, error) {
	outs := make([]Output, 0, len(plan.Languages))
	for _, lang := range plan.Languages {
		out, err := renderLanguage(ctx, d, plan, lang, filepath.Join(runDir, lang), rep)
		if err != nil {
			return nil, err
		}
		outs = append(outs, out)
	}
	outs, err := publish(d.Files, plan.OutputDir, plan.Script.Name, start, outs)
	if err != nil {
		return nil, err
	}
	rep.logf("render done in %s", rep.since(start))
	for _, o := range outs {
		rep.logf("[%s] %s (%s)", o.Lang, o.Path, round(time.Duration(o.DurationMs)*time.Millisecond))
	}
	return outs, nil
}

// shotsSubdir is where a screenshots script's PNGs are published:
// <outputDir>/<name>/shotsSubdir (decision 73).
const shotsSubdir = "screenshots"

// renderShots takes the screenshots into the temp dir, then publishes them
// over the previous run's. Nothing reaches outputDir when a step fails
// (BR-004).
func renderShots(ctx context.Context, d Deps, plan Plan, runDir string, start time.Time, rep reporter) ([]Output, error) {
	shotDir := filepath.Join(runDir, "shots")
	if err := d.Files.MkdirAll(shotDir, 0o755); err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	steps := plan.Script.Steps
	in := ShootRequest{Steps: steps, Dir: shotDir, StorageState: plan.Script.StorageState}
	if rep.progress != nil {
		in.OnStep = func(i int) { rep.progress("", i+1, len(steps), steps[i].Action.Name(), steps[i].Action.Target()) }
	}
	t := d.Now()
	pngs, err := d.Shots.Shoot(ctx, in)
	if err != nil {
		return nil, err
	}
	rep.logf("screenshots: %d captured in %s", len(pngs), rep.since(t))

	paths, err := publishShots(d.Files, filepath.Join(plan.OutputDir, plan.Script.Name, shotsSubdir), pngs)
	if err != nil {
		return nil, err
	}
	rep.logf("render done in %s", rep.since(start))
	outs := make([]Output, len(paths))
	for i, p := range paths {
		rep.logf("%s", p)
		outs[i] = Output{Path: p}
	}
	return outs, nil
}

// voiceNames lists the voice per language as lang=name, in language order.
func voiceNames(plan Plan) string {
	names := make([]string, len(plan.Languages))
	for i, lang := range plan.Languages {
		names[i] = lang + "=" + plan.Voices[lang]
	}
	return strings.Join(names, ",")
}

// renderLanguage runs TTS, builds the cards, records and assembles one
// language (ARCHITECTURE §4, rule 3: clip durations decide step timing). The
// cards come before the slow recording so a card problem fails early. The
// returned Path is the temp MP4.
func renderLanguage(ctx context.Context, d Deps, plan Plan, lang, dir string, rep reporter) (Output, error) {
	clipDir, videoDir, cardDir := filepath.Join(dir, "clips"), filepath.Join(dir, "video"), filepath.Join(dir, "cards")
	for _, p := range []string{clipDir, videoDir, cardDir} {
		if err := d.Files.MkdirAll(p, 0o755); err != nil {
			return Output{}, fmt.Errorf("create temp dir: %w", err)
		}
	}

	steps := plan.Script.Steps
	t := d.Now()
	nar, err := synthesizeAll(ctx, d, plan.Voices[lang], lang, steps, clipDir, ttsWorkers)
	if err != nil {
		return Output{}, err
	}
	durations, clipPaths := nar.durations, nar.paths
	// The synthesis time is the sum over clips: against the wall time it shows
	// what the pool saved (decision 71).
	rep.logf("[%s] narration: %d clips in %s (%d at a time, %s of synthesis)",
		lang, len(clipPaths), rep.since(t), ttsWorkers, round(nar.work))

	intro, outro, err := buildCards(ctx, d, plan, lang, cardDir, rep)
	if err != nil {
		return Output{}, err
	}

	in := RecordRequest{
		Steps:        steps,
		Clips:        durations,
		Lang:         lang,
		Dir:          videoDir,
		StorageState: plan.Script.StorageState,
	}
	if intro != nil {
		in.StartImage = intro.Path // the picture carries on until the first page paints
	}
	if rep.progress != nil {
		in.OnStep = func(i int) { rep.progress(lang, i+1, len(steps), steps[i].Action.Name(), steps[i].Action.Target()) }
	}
	rep.logf("[%s] recording", lang)
	t = d.Now()
	rec, err := d.Rec.Record(ctx, in)
	if err != nil {
		return Output{}, err
	}
	rep.logf("[%s] recorded in %s", lang, rep.since(t))

	// Offsets are clip placements, relative to the recording; the assembler adds
	// the intro.
	asm := AssembleRequest{Video: rec.Video, Intro: intro, Outro: outro, Out: filepath.Join(dir, "out.mp4")}
	for i := range steps { // step order keeps the ffmpeg inputs stable
		if p, ok := clipPaths[i]; ok {
			asm.Clips = append(asm.Clips, Clip{Path: p, Offset: rec.Offsets[i]})
		}
	}
	if m := plan.Script.Meta; m != nil {
		asm.Title, asm.Comment = m.Title, m.Description
	}
	t = d.Now()
	done, err := d.Asm.Assemble(ctx, asm)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return Output{}, ctx.Err()
		case errors.Is(err, ErrNoMarker):
			return Output{}, failure.SyncMarker(lang)
		}
		return Output{}, failure.Assembly(lang, err.Error())
	}
	// The marker end is logged in ms: it drifting toward the 2 s hold warns of
	// a late video start before a render fails on it (decision 69).
	rep.logf("[%s] assembled %s video in %s (sync marker ends at %s)", lang,
		round(time.Duration(done.DurationMs)*time.Millisecond), rep.since(t), done.MarkerEnd.Round(time.Millisecond))
	return Output{Lang: lang, Path: asm.Out, DurationMs: done.DurationMs}, nil
}

// ttsWorkers is how many narration clips of one language are synthesized at
// once. Piper is already multi-threaded; a pool showed ~1.07× speedup and added
// complexity, so the sequential behaviour is kept (decision 71).
var ttsWorkers = 1

// narration is the synthesized clips of one language, keyed by step index
// (0-based): the duration and WAV path of each narrated step, and the summed
// time spent synthesizing.
type narration struct {
	durations map[int]time.Duration
	paths     map[int]string
	work      time.Duration
}

// synthesizeAll synthesizes the narration of every step that has one, with at
// most workers clips in flight, into dir/<step>.wav (step is 1-based). The
// result is the same as one clip after the other: same paths, same
// durations, and the failure reported is the one of the lowest failing step.
// Nothing is cancelled when a clip fails: only later steps are not started, so
// a lower step still in flight can fail and take over as the reported one.
// A cancelled ctx returns ctx.Err().
func synthesizeAll(ctx context.Context, d Deps, voice, lang string, steps []script.Step, dir string, workers int) (narration, error) {
	var (
		durs  = make([]time.Duration, len(steps)) // each worker writes its own index,
		work  = make([]time.Duration, len(steps)) // so no lock is needed
		paths = make([]string, len(steps))
		errs  = make([]error, len(steps))

		lowestFail atomic.Int64 // lowest failing step index so far
		sem        = make(chan struct{}, max(workers, 1))
		wg         sync.WaitGroup
	)
	lowestFail.Store(int64(len(steps)))

launch:
	for i, step := range steps {
		text := step.Narration[lang]
		if text == "" {
			continue
		}
		if ctx.Err() != nil { // select alone would pick at random when both are ready
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break launch
		}
		if int64(i) > lowestFail.Load() { // a lower step failed: this one's result is never used
			<-sem
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			path := filepath.Join(dir, strconv.Itoa(i+1)+".wav")
			t := d.Now()
			dur, err := d.TTS.Synthesize(ctx, voice, text, path)
			work[i] = d.Now().Sub(t)
			if err != nil {
				errs[i] = err
				lowerTo(&lowestFail, int64(i))
				return
			}
			durs[i], paths[i] = dur, path
		})
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return narration{}, err
	}
	for i, err := range errs {
		if err != nil {
			return narration{}, failure.TTS(i+1, lang, err.Error())
		}
	}
	nar := narration{durations: map[int]time.Duration{}, paths: map[int]string{}}
	for i, p := range paths {
		if p != "" {
			nar.durations[i], nar.paths[i] = durs[i], p
			nar.work += work[i]
		}
	}
	return nar, nil
}

// lowerTo sets v to n unless v is already lower.
func lowerTo(v *atomic.Int64, n int64) {
	for cur := v.Load(); n < cur; cur = v.Load() {
		if v.CompareAndSwap(cur, n) {
			return
		}
	}
}

// buildCards returns the intro and outro stills for lang (nil for a card that
// is off). A card with a custom image needs no work; the built-in cards of
// this language are drawn in one browser launch into dir. The outro's closing
// line follows the language (decision 63).
func buildCards(ctx context.Context, d Deps, plan Plan, lang, dir string, rep reporter) (intro, outro *Still, err error) {
	outroCard := plan.Outro
	outroCard.Title = cmp.Or(outroCard.Title, card.Outro(lang))

	var shots []Shot
	still := func(name string, c Card) (*Still, error) {
		if c.Off {
			return nil, nil
		}
		if c.Image != "" {
			return &Still{Path: c.Image, Duration: c.Duration}, nil
		}
		html, err := card.HTML(card.Text{Title: c.Title, Subtitle: c.Subtitle})
		if err != nil {
			return nil, failure.Cards(lang, err)
		}
		out := filepath.Join(dir, name+".png")
		shots = append(shots, Shot{HTML: html, Out: out})
		return &Still{Path: out, Duration: c.Duration}, nil
	}
	if intro, err = still("intro", plan.Intro); err != nil {
		return nil, nil, err
	}
	if outro, err = still("outro", outroCard); err != nil {
		return nil, nil, err
	}
	if len(shots) == 0 {
		return intro, outro, nil
	}

	t := d.Now()
	if err := d.Cards.Screenshot(ctx, shots); err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, failure.Cards(lang, err)
	}
	rep.logf("[%s] cards: %d built in %s", lang, len(shots), rep.since(t))
	return intro, outro, nil
}
