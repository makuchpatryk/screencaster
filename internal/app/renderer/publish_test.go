package renderer

import (
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOutputName(t *testing.T) {
	tests := []struct {
		name string
		ts   time.Time
		want string
	}{
		{"FR-010 format", time.Date(2026, 10, 3, 10, 15, 0, 0, time.UTC), "create-project.en.20261003T101500Z.mp4"},
		{
			"timestamp is always UTC",
			time.Date(2026, 10, 3, 12, 15, 0, 0, time.FixedZone("CEST", 2*60*60)),
			"create-project.en.20261003T101500Z.mp4",
		},
		{"sub-second part is dropped", time.Date(2026, 1, 2, 3, 4, 5, 999_000_000, time.UTC), "create-project.en.20260102T030405Z.mp4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OutputName("create-project", "en", tt.ts); got != tt.want {
				t.Errorf("OutputName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMove_crossDeviceCopiesThenRenames(t *testing.T) { // ARCHITECTURE §4 rule 5
	files := newMemFS()
	files.write("/tmp/run/en/out.mp4", "video")
	if err := files.MkdirAll("/work/output", 0o755); err != nil {
		t.Fatal(err)
	}
	files.mount = "/work" // the output folder is on another file system

	if err := move(files, "/tmp/run/en/out.mp4", "/work/output/demo.en.mp4"); err != nil {
		t.Fatal(err)
	}
	if b, _ := files.read("/work/output/demo.en.mp4"); b != "video" {
		t.Errorf("dst = %q, want video", b)
	}
	if got := files.list("/work/output"); !reflect.DeepEqual(got, []string{"demo.en.mp4"}) {
		t.Errorf("output dir = %v, want only the video (no .part)", got)
	}
}

func TestCopyThenRename_publishesWithoutPartLeft(t *testing.T) { // cross-filesystem publish
	files := newMemFS()
	files.write("/tmp/run/src.mp4", "video")
	if err := files.MkdirAll("/work/out", 0o755); err != nil {
		t.Fatal(err)
	}

	if err := copyThenRename(files, "/tmp/run/src.mp4", "/work/out/demo.en.mp4"); err != nil {
		t.Fatal(err)
	}
	if b, _ := files.read("/work/out/demo.en.mp4"); b != "video" {
		t.Errorf("dst = %q, want video", b)
	}
	if files.exists("/work/out/demo.en.mp4.part") {
		t.Error(".part left behind")
	}
}

func TestCopyThenRename_refusesExistingPart(t *testing.T) { // BR-006
	files := newMemFS()
	files.write("/work/src.mp4", "video")
	files.write("/work/demo.en.mp4.part", "someone else's")

	if err := copyThenRename(files, "/work/src.mp4", "/work/demo.en.mp4"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("copy over an existing .part: error = %v, want fs.ErrExist", err)
	}
	if b, _ := files.read("/work/demo.en.mp4.part"); b != "someone else's" {
		t.Errorf(".part was touched: %q", b)
	}
}

func TestPublish_movesUnderFinalNames(t *testing.T) { // FR-010
	files := newMemFS()
	files.write("/tmp/en.mp4", "en")
	files.write("/tmp/pl.mp4", "pl")

	outs, err := publish(files, "/work/output", "demo", jobStart, []Output{
		{Lang: "en", Path: "/tmp/en.mp4", DurationMs: 1},
		{Lang: "pl", Path: "/tmp/pl.mp4", DurationMs: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Output{
		{Lang: "en", Path: "/work/output/demo.en.20261003T101500Z.mp4", DurationMs: 1},
		{Lang: "pl", Path: "/work/output/demo.pl.20261003T101500Z.mp4", DurationMs: 2},
	}
	if !reflect.DeepEqual(outs, want) {
		t.Errorf("outputs = %+v, want %+v", outs, want)
	}
	if got := files.list("/tmp"); len(got) != 0 {
		t.Errorf("sources left: %v", got)
	}
}

func TestPublish_failedMoveRemovesEarlierOutputs(t *testing.T) { // BR-004
	files := newMemFS()
	files.write("/tmp/en.mp4", "en")

	_, err := publish(files, "/work/output", "demo", jobStart, []Output{
		{Lang: "en", Path: "/tmp/en.mp4"},
		{Lang: "pl", Path: "/tmp/missing.mp4"},
	})
	if err == nil {
		t.Fatal("publish with a missing source succeeded")
	}
	if got := files.list("/work/output"); len(got) != 0 {
		t.Errorf("output dir not rolled back: %v", got)
	}
}

func TestPublish_existingTargetStopsBeforeAnyMove(t *testing.T) { // BR-006
	files := newMemFS()
	files.write("/tmp/en.mp4", "en")
	files.write("/tmp/pl.mp4", "pl")
	files.write("/work/output/demo.pl.20261003T101500Z.mp4", "old")

	_, err := publish(files, "/work/output", "demo", jobStart, []Output{
		{Lang: "en", Path: "/tmp/en.mp4"},
		{Lang: "pl", Path: "/tmp/pl.mp4"},
	})
	if err == nil || !strings.Contains(err.Error(), "output already exists") {
		t.Fatalf("publish() error = %v, want output already exists", err)
	}
	if got := files.list("/work/output"); !reflect.DeepEqual(got, []string{"demo.pl.20261003T101500Z.mp4"}) {
		t.Errorf("output dir = %v, want only the old file", got)
	}
	if b, _ := files.read("/work/output/demo.pl.20261003T101500Z.mp4"); b != "old" {
		t.Errorf("existing output changed to %q", b)
	}
}

func TestPublish_crossDeviceRollsBack(t *testing.T) { // BR-004, rule 5
	files := newMemFS()
	files.write("/tmp/en.mp4", "en")
	files.mount = "/work"

	_, err := publish(files, "/work/output", "demo", jobStart, []Output{
		{Lang: "en", Path: "/tmp/en.mp4"},
		{Lang: "pl", Path: "/tmp/missing.mp4"},
	})
	if err == nil {
		t.Fatal("publish() error = nil")
	}
	if got := files.list("/work/output"); len(got) != 0 {
		t.Errorf("output dir = %v, want empty: the copied en and any .part removed", got)
	}
}
