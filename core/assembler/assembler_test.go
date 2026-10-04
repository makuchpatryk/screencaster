package assembler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	head = "-hide_banner -nostdin -loglevel error -y -i rec.webm"
	enc  = "-c:v libx264 -preset veryfast -crf 20 -c:a aac -b:a 160k"
	vid  = "[0:v]fps=30,scale=1920:1080,format=yuv420p[v]"
)

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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildArgs(tt.in); !reflect.DeepEqual(got, tt.want) {
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

func TestAssemble_returnsProbedDurationMs(t *testing.T) {
	f := FFmpeg{
		Bin:   fakeBin(t, "ffmpeg", "exit 0"),
		Probe: fakeBin(t, "ffprobe", "echo 12.3456"),
	}
	got, err := f.Assemble(context.Background(), Input{Webm: "rec.webm", Out: "out.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if got != 12346 {
		t.Errorf("durationMs = %d, want 12346", got)
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
