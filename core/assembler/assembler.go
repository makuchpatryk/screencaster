// Package assembler wraps ffmpeg and ffprobe: it places narration clips at
// their step offsets, transcodes the recording and muxes both into one MP4
// (FR-009). It knows nothing about steps, languages or jobs.
package assembler

import (
	"bytes"
	"context"
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
)

// FFmpeg runs the ffmpeg and ffprobe binaries at Bin and Probe.
type FFmpeg struct{ Bin, Probe string }

// Clip is one narration WAV and where it starts in the video.
type Clip struct {
	Path   string
	Offset time.Duration
}

// Input is one language's assembly. Title and Comment become MP4 tags when not
// empty (FR-009.5). Out must not exist yet.
type Input struct {
	Webm    string
	Clips   []Clip
	Title   string
	Comment string
	Out     string
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

// Assemble writes in.Out and returns its duration in ms, read back with
// ffprobe. Subprocess output is captured, never inherited (ADR-48).
func (f FFmpeg) Assemble(ctx context.Context, in Input) (int64, error) {
	if _, err := run(ctx, f.Bin, buildArgs(in)...); err != nil {
		return 0, err
	}
	out, err := run(ctx, f.Probe, "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", in.Out)
	if err != nil {
		return 0, fmt.Errorf("ffprobe %s: %w", in.Out, err)
	}
	sec, err := strconv.ParseFloat(strings.TrimSpace(out), 64)
	if err != nil {
		return 0, fmt.Errorf("ffprobe %s: parse duration %q: %w", in.Out, out, err)
	}
	return int64(math.Round(sec * 1000)), nil
}

// buildArgs is the whole ffmpeg command line, kept pure for golden tests.
//
//   - Each clip is delayed to its offset (adelay) and the clips are mixed
//     without level normalisation (FR-009.1).
//   - The output is as long as the video (FR-009.4): the recorder holds every
//     narrated step until its clip ends, so the mixed audio never outlasts
//     the video. An endless apad with -shortest would say the same, but it
//     never terminates in ffmpeg 5.1 (found in M3).
//   - With no narrated step a silent source stands in, cut by -shortest, so
//     every output has an aac stream (NFR-002).
func buildArgs(in Input) []string {
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y", "-i", in.Webm}
	graph := []string{fmt.Sprintf("[0:v]fps=%d,scale=1920:1080,format=yuv420p[v]", FPS)}
	audio := []string{"-map", "[a]"}

	if len(in.Clips) == 0 {
		args = append(args, "-f", "lavfi", "-i", "anullsrc=r=22050:cl=mono")
		audio = []string{"-map", "1:a", "-shortest"}
	} else {
		var mix strings.Builder
		for i, c := range in.Clips {
			args = append(args, "-i", c.Path)
			graph = append(graph, fmt.Sprintf("[%d:a]adelay=%d:all=1[a%d]", i+1, c.Offset.Milliseconds(), i+1))
			fmt.Fprintf(&mix, "[a%d]", i+1)
		}
		fmt.Fprintf(&mix, "amix=inputs=%d:normalize=0:dropout_transition=0[a]", len(in.Clips))
		graph = append(graph, mix.String())
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

// run executes bin and returns its stdout. On failure the error is an *Error
// with the stderr tail, or ctx.Err() when the context ended.
func run(ctx context.Context, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", &Error{StderrTail: tail(stderr.String(), stderrLines), err: err}
	}
	return stdout.String(), nil
}

// tail returns the last n lines of s, without trailing blank lines.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
