// Package assembler wraps ffmpeg and ffprobe: it places narration clips at
// their step offsets, transcodes the recording and muxes both into one MP4
// (FR-009). It knows nothing about steps, languages or jobs.
package assembler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	// FPS is forced during transcode so video time equals wall time and the
	// recorded offsets stay valid (ARCHITECTURE §5, ADR-50).
	FPS = 30
	// stderrLines is how much ffmpeg output a failure keeps (FR-009 edge case).
	stderrLines = 20
	// padColor fills the frame around a still that is not 16:9. It is the
	// built-in card's background (domain/card), so a custom picture and a card
	// look alike.
	padColor = "0x0f172a"

	// markerScan is how much of the recording is searched for the sync
	// marker: its 2 s hold plus the latest video start seen (1.5 s,
	// ARCHITECTURE §17.1), rounded up.
	markerScan = 4 * time.Second
	// markerTolerance is how far each channel of a frame's average colour may
	// be from the marker's #FF00FF. The cursor overlay and the encoder move it
	// a little; the start card and white page are far off.
	markerTolerance = 40
)

// ErrNoMarker means the recording does not show the sync marker followed by
// another frame within markerScan, so there is nothing to align the narration
// on (decision 69).
var ErrNoMarker = errors.New("sync marker not found")

// FFmpeg runs the ffmpeg and ffprobe binaries at Bin and Probe.
type FFmpeg struct{ Bin, Probe string }

// Clip is one narration WAV and where it starts in the video.
type Clip struct {
	Path   string
	Offset time.Duration
}

// Still is a picture shown full-frame for Duration, scaled to fit on a dark
// background (decision 63). Path is a PNG or JPEG.
type Still struct {
	Path     string
	Duration time.Duration
}

// Input is one language's assembly. Intro and Outro, when not nil, are stills
// placed before and after the recording; clip offsets are relative to the
// recording and the assembler shifts them by the intro. Title and Comment
// become MP4 tags when not empty (FR-009.5). Out must not exist yet.
type Input struct {
	Webm    string
	Clips   []Clip
	Intro   *Still
	Outro   *Still
	Title   string
	Comment string
	Out     string
}

// Output is a finished MP4: its duration and where the recording was cut, the
// end of the sync marker in the WebM (logged for drift checks).
type Output struct {
	DurationMs int64
	MarkerEnd  time.Duration
}

// Error is a failed ffmpeg run. Error() is the last 20 lines of stderr, so the
// renderer can put it into failure.Assembly unchanged.
type Error struct {
	StderrTail string
	err        error
}

func (e *Error) Error() string {
	if e.StderrTail == "" {
		return e.err.Error()
	}
	return e.StderrTail
}

func (e *Error) Unwrap() error { return e.err }

// Assemble finds the end of the sync marker in the recording, cuts the
// recording there and writes in.Out; it returns the MP4's duration in ms, read
// back with ffprobe. A recording without the marker is ErrNoMarker.
// Subprocess output is captured, never inherited (ADR-48).
func (f FFmpeg) Assemble(ctx context.Context, in Input) (Output, error) {
	cut, err := f.markerEnd(ctx, in.Webm)
	if err != nil {
		return Output{}, err
	}
	if _, _, err := run(ctx, f.Bin, buildArgs(in, cut)...); err != nil {
		return Output{}, err
	}
	out, _, err := run(ctx, f.Probe, "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", in.Out)
	if err != nil {
		return Output{}, fmt.Errorf("ffprobe %s: %w", in.Out, err)
	}
	sec, err := strconv.ParseFloat(strings.TrimSpace(out), 64)
	if err != nil {
		return Output{}, fmt.Errorf("ffprobe %s: parse duration %q: %w", in.Out, out, err)
	}
	return Output{DurationMs: int64(math.Round(sec * 1000)), MarkerEnd: cut}, nil
}

// markerEnd returns the time of the first frame after the sync marker in the
// first markerScan of webm. Each frame is averaged down to one RGB pixel on
// stdout; showinfo logs its pts_time on stderr in the same order, and
// passthrough keeps ffmpeg from dropping or repeating frames between the two.
func (f FFmpeg) markerEnd(ctx context.Context, webm string) (time.Duration, error) {
	rgb, log, err := run(ctx, f.Bin, "-hide_banner", "-nostdin", "-nostats",
		"-t", seconds(markerScan), "-i", webm,
		"-vf", "scale=1:1:flags=area,showinfo", "-fps_mode", "passthrough",
		"-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1")
	if err != nil {
		return 0, err
	}
	return findMarkerEnd([]byte(rgb), log)
}

// findMarkerEnd pairs each frame's pixel (3 bytes in rgb) with its pts_time in
// the showinfo log and returns the pts of the first non-marker frame that
// follows a marker frame. Frames before the marker (a blank page before the
// start document paints) are skipped.
func findMarkerEnd(rgb []byte, log string) (time.Duration, error) {
	var pts []time.Duration
	for line := range strings.Lines(log) {
		_, rest, ok := strings.Cut(line, " pts_time:")
		if !ok || !strings.Contains(line, "showinfo") {
			continue
		}
		v, _, _ := strings.Cut(strings.TrimSpace(rest), " ")
		sec, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, fmt.Errorf("sync marker scan: parse pts_time %q: %w", v, err)
		}
		pts = append(pts, time.Duration(math.Round(sec*float64(time.Second))))
	}
	if len(rgb) != 3*len(pts) {
		return 0, fmt.Errorf("sync marker scan: %d bytes of pixels for %d frames", len(rgb), len(pts))
	}
	seen := false
	for i, t := range pts {
		if isMarker(rgb[3*i], rgb[3*i+1], rgb[3*i+2]) {
			seen = true
		} else if seen {
			return t, nil
		}
	}
	return 0, ErrNoMarker
}

// isMarker reports whether a frame's average colour is the marker's #FF00FF.
func isMarker(r, g, b byte) bool {
	return r >= 255-markerTolerance && g <= markerTolerance && b >= 255-markerTolerance
}

// buildArgs is the whole ffmpeg command line, kept pure for golden tests.
//
//   - Inputs: 0 is the recording, read from cut on (the end of the sync
//     marker, so video time 0 is the recorder's time 0, decision 69), then the intro and outro stills, then the
//     clips (or the silent source).
//   - Each clip is delayed to its offset plus the intro length (adelay) and the
//     clips are mixed without level normalisation (FR-009.1).
//   - Stills are looped for their duration, fitted to 1920x1080 and joined to
//     the recording with concat. Every concat input is finite, so it ends.
//   - The audio may end before the video (silent outro, or the gap after the
//     last clip); the output is as long as the video (FR-009.4). Padding it
//     with an endless apad and -shortest would never terminate in ffmpeg 5.1
//     (found in M3).
//   - With no narrated step a silent source stands in, cut by -shortest, so
//     every output has an aac stream (NFR-002).
func buildArgs(in Input, cut time.Duration) []string {
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y", "-ss", seconds(cut), "-i", in.Webm}
	next := 1 // index of the next -i

	var graph []string
	if in.Intro == nil && in.Outro == nil {
		graph = []string{fmt.Sprintf("[0:v]fps=%d,scale=1920:1080,format=yuv420p[v]", FPS)}
	} else {
		graph = []string{fmt.Sprintf("[0:v]fps=%d,scale=1920:1080,format=yuv420p,setsar=1[main]", FPS)}
		var seq []string
		if in.Intro != nil {
			args = append(args, stillInput(*in.Intro)...)
			graph = append(graph, stillFilter(next, "intro"))
			seq = append(seq, "[intro]")
			next++
		}
		seq = append(seq, "[main]")
		if in.Outro != nil {
			args = append(args, stillInput(*in.Outro)...)
			graph = append(graph, stillFilter(next, "outro"))
			seq = append(seq, "[outro]")
			next++
		}
		graph = append(graph, fmt.Sprintf("%sconcat=n=%d:v=1:a=0[v]", strings.Join(seq, ""), len(seq)))
	}

	var audio []string
	if len(in.Clips) == 0 {
		args = append(args, "-f", "lavfi", "-i", "anullsrc=r=22050:cl=mono")
		audio = []string{"-map", fmt.Sprintf("%d:a", next), "-shortest"}
	} else {
		var lead time.Duration
		if in.Intro != nil {
			lead = in.Intro.Duration
		}
		var mix strings.Builder
		for i, c := range in.Clips {
			args = append(args, "-i", c.Path)
			graph = append(graph, fmt.Sprintf("[%d:a]adelay=%d:all=1[a%d]", next+i, (lead+c.Offset).Milliseconds(), i+1))
			fmt.Fprintf(&mix, "[a%d]", i+1)
		}
		fmt.Fprintf(&mix, "amix=inputs=%d:normalize=0:dropout_transition=0[a]", len(in.Clips))
		graph = append(graph, mix.String())
		audio = []string{"-map", "[a]"}
	}

	args = append(args, "-filter_complex", strings.Join(graph, ";"), "-map", "[v]")
	args = append(args, audio...)
	args = append(args,
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "20",
		"-c:a", "aac", "-b:a", "160k",
	)
	if in.Title != "" {
		args = append(args, "-metadata", "title="+in.Title)
	}
	if in.Comment != "" {
		args = append(args, "-metadata", "comment="+in.Comment)
	}
	return append(args, "-movflags", "+faststart", in.Out)
}

// stillInput loops one picture at the output frame rate for its duration.
func stillInput(s Still) []string {
	return []string{"-loop", "1", "-framerate", strconv.Itoa(FPS), "-t", seconds(s.Duration), "-i", s.Path}
}

// seconds formats d for ffmpeg's time options, without trailing zeros.
func seconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64)
}

// stillFilter fits input idx into 1920x1080 on padColor and labels it.
func stillFilter(idx int, label string) string {
	return fmt.Sprintf("[%d:v]scale=1920:1080:force_original_aspect_ratio=decrease,pad=1920:1080:(ow-iw)/2:(oh-ih)/2:color=%s,fps=%d,format=yuv420p,setsar=1[%s]",
		idx, padColor, FPS, label)
}

// run executes bin and returns its stdout and stderr. On failure the error is
// an *Error with the stderr tail, or ctx.Err() when the context ended.
func run(ctx context.Context, bin string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		return "", "", &Error{StderrTail: tail(errOut.String(), stderrLines), err: err}
	}
	return out.String(), errOut.String(), nil
}

// tail returns the last n lines of s, without trailing blank lines.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
