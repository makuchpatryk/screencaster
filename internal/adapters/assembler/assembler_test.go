package assembler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	head = "-hide_banner -nostdin -loglevel error -y -ss 1.52 -i rec.webm"
	enc  = "-c:v libx264 -preset veryfast -crf 20 -c:a aac -b:a 160k"
	vid  = "[0:v]fps=30,scale=1920:1080,format=yuv420p[v]"
	main = "[0:v]fps=30,scale=1920:1080,format=yuv420p,setsar=1[main]"
)

// stillGraph is the filter that fits input idx on the dark background.
func stillGraph(idx int, label string) string {
	return fmt.Sprintf("[%d:v]scale=1920:1080:force_original_aspect_ratio=decrease,pad=1920:1080:(ow-iw)/2:(oh-ih)/2:color=0x0f172a,fps=30,format=yuv420p,setsar=1[%s]", idx, label)
}

func TestBuildArgs_golden(t *testing.T) { // FR-009
	tests := []struct {
		name string
		in   Input
		want []string
	}{
		{
			name: "two clips at their offsets, with meta",
			in: Input{
				Webm:    "rec.webm",
				Clips:   []Clip{{"c1.wav", 1234 * time.Millisecond}, {"c3.wav", 5678 * time.Millisecond}},
				Title:   "Create a project",
				Comment: "How to start",
				Out:     "out.mp4",
			},
			want: slices.Concat(fields(head+" -i c1.wav -i c3.wav -filter_complex"),
				[]string{vid + ";[1:a]adelay=1234:all=1[a1];[2:a]adelay=5678:all=1[a2];[a1][a2]amix=inputs=2:normalize=0:dropout_transition=0[a]"},
				fields("-map [v] -map [a] "+enc),
				[]string{"-metadata", "title=Create a project", "-metadata", "comment=How to start"},
				fields("-movflags +faststart out.mp4")),
		},
		{
			name: "no narration uses a silent source",
			in:   Input{Webm: "rec.webm", Out: "out.mp4"},
			want: fields(head + " -f lavfi -i anullsrc=r=22050:cl=mono -filter_complex " +
				vid + " -map [v] -map 1:a -shortest " + enc + " -movflags +faststart out.mp4"),
		},
		{
			name: "missing meta writes no tags; sub-ms offset truncates",
			in:   Input{Webm: "rec.webm", Clips: []Clip{{"c.wav", 1500*time.Microsecond + 999}}, Out: "out.mp4"},
			want: fields(head + " -i c.wav -filter_complex " +
				vid + ";[1:a]adelay=1:all=1[a1];[a1]amix=inputs=1:normalize=0:dropout_transition=0[a] " +
				"-map [v] -map [a] " + enc + " -movflags +faststart out.mp4"),
		},
		{
			name: "intro only: still first, offsets shifted by its length",
			in: Input{
				Webm:  "rec.webm",
				Clips: []Clip{{"c1.wav", 1234 * time.Millisecond}},
				Intro: &Still{"intro.png", 3 * time.Second},
				Out:   "out.mp4",
			},
			want: slices.Concat(fields(head+" -loop 1 -framerate 30 -t 3 -i intro.png -i c1.wav -filter_complex"),
				[]string{strings.Join([]string{main, stillGraph(1, "intro"), "[intro][main]concat=n=2:v=1:a=0[v]",
					"[2:a]adelay=4234:all=1[a1]", "[a1]amix=inputs=1:normalize=0:dropout_transition=0[a]"}, ";")},
				fields("-map [v] -map [a] "+enc+" -movflags +faststart out.mp4")),
		},
		{
			name: "both stills with clips: outro leaves offsets alone",
			in: Input{
				Webm:  "rec.webm",
				Clips: []Clip{{"c1.wav", 0}, {"c3.wav", 2500 * time.Millisecond}},
				Intro: &Still{"intro.png", 3 * time.Second},
				Outro: &Still{"outro.jpg", 4500 * time.Millisecond},
				Out:   "out.mp4",
			},
			want: slices.Concat(fields(head+" -loop 1 -framerate 30 -t 3 -i intro.png -loop 1 -framerate 30 -t 4.5 -i outro.jpg -i c1.wav -i c3.wav -filter_complex"),
				[]string{strings.Join([]string{main, stillGraph(1, "intro"), stillGraph(2, "outro"), "[intro][main][outro]concat=n=3:v=1:a=0[v]",
					"[3:a]adelay=3000:all=1[a1]", "[4:a]adelay=5500:all=1[a2]", "[a1][a2]amix=inputs=2:normalize=0:dropout_transition=0[a]"}, ";")},
				fields("-map [v] -map [a] "+enc+" -movflags +faststart out.mp4")),
		},
		{
			name: "outro only, no clips: silent source follows the stills",
			in:   Input{Webm: "rec.webm", Outro: &Still{"outro.png", 3 * time.Second}, Out: "out.mp4"},
			want: slices.Concat(fields(head+" -loop 1 -framerate 30 -t 3 -i outro.png -f lavfi -i anullsrc=r=22050:cl=mono -filter_complex"),
				[]string{strings.Join([]string{main, stillGraph(1, "outro"), "[main][outro]concat=n=2:v=1:a=0[v]"}, ";")},
				fields("-map [v] -map 2:a -shortest "+enc+" -movflags +faststart out.mp4")),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildArgs(tt.in, 1520*time.Millisecond); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("args =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

var fields = strings.Fields

func TestTail_keepsLastLines(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 25; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	got := strings.Split(tail(b.String(), 20), "\n")
	if len(got) != 20 || got[0] != "line 6" || got[19] != "line 25" {
		t.Errorf("tail = %q..%q (%d lines), want line 6..line 25", got[0], got[len(got)-1], len(got))
	}
}

// fakeBin writes a shell script standing in for ffmpeg or ffprobe.
func fakeBin(t *testing.T, name, body string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// markerScanOutput is what the fake ffmpeg prints for the marker scan: one
// marker frame, then a white one at 40 ms.
const markerScanOutput = `case "$*" in *rawvideo*)
	printf '\377\000\377\377\377\377'
	echo "[Parsed_showinfo_1 @ 0x1] n:   0 pts:      0 pts_time:0       duration:40" >&2
	echo "[Parsed_showinfo_1 @ 0x1] n:   1 pts:     40 pts_time:0.04    duration:40" >&2;;
esac`

func TestAssemble_returnsProbedDurationAndMarkerEnd(t *testing.T) {
	f := FFmpeg{
		Bin:   fakeBin(t, "ffmpeg", markerScanOutput+"\nexit 0"),
		Probe: fakeBin(t, "ffprobe", "echo 12.3456"),
	}
	got, err := f.Assemble(context.Background(), Input{Webm: "rec.webm", Out: "out.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Output{DurationMs: 12346, MarkerEnd: 40 * time.Millisecond}); got != want {
		t.Errorf("Assemble = %+v, want %+v", got, want)
	}
}

func TestAssemble_noMarkerIsErrNoMarker(t *testing.T) { // decision 69
	f := FFmpeg{
		Bin:   fakeBin(t, "ffmpeg", "exit 0"), // the scan finds no frames
		Probe: fakeBin(t, "ffprobe", "echo 1"),
	}
	_, err := f.Assemble(context.Background(), Input{Webm: "rec.webm", Out: "out.mp4"})
	if !errors.Is(err, ErrNoMarker) {
		t.Errorf("err = %v, want ErrNoMarker", err)
	}
}

func TestFindMarkerEnd(t *testing.T) { // decision 69
	const (
		magenta = "\xff\x00\xff"
		near    = "\xe0\x20\xf0" // the cursor and the encoder shift the average a little
		white   = "\xff\xff\xff"
		card    = "\x0f\x17\x2a"
	)
	showinfo := func(pts ...string) string {
		var b strings.Builder
		b.WriteString("Input #0, matroska,webm, from 'rec.webm':\n")
		for i, p := range pts {
			fmt.Fprintf(&b, "[Parsed_showinfo_1 @ 0x55d] n:%4d pts:%7d pts_time:%-8s duration:40\n", i, i*40, p)
			fmt.Fprintf(&b, "[Parsed_showinfo_1 @ 0x55d]  color_range:tv color_space:unknown\n")
		}
		return b.String()
	}
	tests := []struct {
		name    string
		rgb     string
		log     string
		want    time.Duration
		wantErr error
	}{
		{"marker then card", magenta + near + card, showinfo("0", "0.04", "0.08"), 80 * time.Millisecond, nil},
		{"white page before the marker is skipped", white + magenta + white, showinfo("0", "0.521", "0.561"), 561 * time.Millisecond, nil},
		{"no marker", white + card, showinfo("0", "0.04"), 0, ErrNoMarker},
		{"marker never ends", magenta + magenta, showinfo("0", "0.04"), 0, ErrNoMarker},
		{"no frames", "", "", 0, ErrNoMarker},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := findMarkerEnd([]byte(tt.rgb), tt.log)
			if !errors.Is(err, tt.wantErr) || got != tt.want {
				t.Errorf("findMarkerEnd = %v, %v; want %v, %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestFindMarkerEnd_pixelAndFrameCountMustAgree(t *testing.T) {
	_, err := findMarkerEnd([]byte("\xff\x00\xff"), "[Parsed_showinfo_1 @ 0x1] n: 0 pts: 0 pts_time:0 \n[Parsed_showinfo_1 @ 0x1] n: 1 pts: 40 pts_time:0.04 \n")
	if err == nil || errors.Is(err, ErrNoMarker) {
		t.Errorf("err = %v, want a count mismatch", err)
	}
}

// The real scan over a generated clip: a white page, the marker for 1.2 s,
// then white. Needs ffmpeg with libvpx, as in the dev image.
func TestMarkerEnd_generatedClip(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH; runs in the dev image")
	}
	webm := filepath.Join(t.TempDir(), "rec.webm")
	gen := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=white:s=320x180:r=25:d=0.2",
		"-f", "lavfi", "-i", "color=c=0xFF00FF:s=320x180:r=25:d=1.2",
		"-f", "lavfi", "-i", "color=c=white:s=320x180:r=25:d=1",
		"-filter_complex", "[0][1][2]concat=n=3:v=1", "-c:v", "libvpx", "-b:v", "1M", webm)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate clip: %v\n%s", err, out)
	}

	got, err := FFmpeg{Bin: "ffmpeg"}.markerEnd(context.Background(), webm)
	if err != nil {
		t.Fatal(err)
	}
	if want := 1400 * time.Millisecond; got != want {
		t.Errorf("marker ends at %v, want %v", got, want)
	}
}

func TestAssemble_failureKeepsStderrTail(t *testing.T) { // FR-009 edge case
	f := FFmpeg{
		Bin:   fakeBin(t, "ffmpeg", `i=1; while [ $i -le 30 ]; do echo "err $i" >&2; i=$((i+1)); done; exit 1`),
		Probe: fakeBin(t, "ffprobe", "exit 0"),
	}
	_, err := f.Assemble(context.Background(), Input{Webm: "rec.webm", Out: "out.mp4"})
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want *assembler.Error", err)
	}
	lines := strings.Split(e.Error(), "\n")
	if len(lines) != 20 || lines[0] != "err 11" || lines[19] != "err 30" {
		t.Errorf("tail = %q, want err 11..err 30", e.Error())
	}
}
