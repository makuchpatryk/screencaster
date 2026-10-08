package wire

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"screencaster/internal/adapters/assembler"
	"screencaster/internal/app/renderer"
	"screencaster/internal/domain/recorder"
	"screencaster/internal/domain/script"
	"screencaster/internal/domain/shooter"
)

// Every field of a port request reaches the wrapper; a field added to one side
// only would show up here as a zero value.
func TestRecordInput_everyFieldCrosses(t *testing.T) {
	called := -1
	r := renderer.RecordRequest{
		Steps:        []script.Step{{Action: script.Goto{URL: "http://app/"}}},
		Clips:        map[int]time.Duration{0: time.Second},
		Lang:         "pl",
		Dir:          "/tmp/v",
		StorageState: &script.StorageState{Cookies: []script.Cookie{{Name: "s"}}},
		StartImage:   "/tmp/intro.png",
		OnStep:       func(i int) { called = i },
	}
	got := recordInput(r)
	got.OnStep(3)
	got.OnStep = nil
	want := recorder.Input{
		Steps: r.Steps, Clips: r.Clips, Lang: "pl", Dir: "/tmp/v",
		StorageState: r.StorageState, StartImage: "/tmp/intro.png",
	}
	if !reflect.DeepEqual(got, want) || called != 3 {
		t.Errorf("recordInput() = %+v (OnStep called with %d), want %+v", got, called, want)
	}
	assertNoZeroField(t, got, "OnStep")
}

func TestShootInput_everyFieldCrosses(t *testing.T) {
	called := -1
	r := renderer.ShootRequest{
		Steps:        []script.Step{{Action: script.Screenshot{}}},
		Dir:          "/tmp/shots",
		StorageState: &script.StorageState{Cookies: []script.Cookie{{Name: "s"}}},
		OnStep:       func(i int) { called = i },
	}
	got := shootInput(r)
	got.OnStep(2)
	got.OnStep = nil
	want := shooter.Input{
		Steps: r.Steps, Dir: "/tmp/shots", StorageState: r.StorageState,
	}
	if !reflect.DeepEqual(got, want) || called != 2 {
		t.Errorf("shootInput() = %+v (OnStep called with %d), want %+v", got, called, want)
	}
	assertNoZeroField(t, got, "OnStep")
}

func TestAssembleInput_everyFieldCrosses(t *testing.T) {
	got := assembleInput(renderer.AssembleRequest{
		Video:   "/tmp/rec.webm",
		Clips:   []renderer.Clip{{Path: "/tmp/1.wav", Offset: 2 * time.Second}},
		Intro:   &renderer.Still{Path: "/tmp/intro.png", Duration: 3 * time.Second},
		Outro:   &renderer.Still{Path: "/tmp/outro.png", Duration: 4 * time.Second},
		Title:   "Title",
		Comment: "Comment",
		Out:     "/tmp/out.mp4",
	})
	want := assembler.Input{
		Webm:    "/tmp/rec.webm",
		Clips:   []assembler.Clip{{Path: "/tmp/1.wav", Offset: 2 * time.Second}},
		Intro:   &assembler.Still{Path: "/tmp/intro.png", Duration: 3 * time.Second},
		Outro:   &assembler.Still{Path: "/tmp/outro.png", Duration: 4 * time.Second},
		Title:   "Title",
		Comment: "Comment",
		Out:     "/tmp/out.mp4",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("assembleInput() = %+v, want %+v", got, want)
	}
	assertNoZeroField(t, got)
}

func TestAssembleInput_cardOffStaysNil(t *testing.T) { // decision 63
	if got := assembleInput(renderer.AssembleRequest{}); got.Intro != nil || got.Outro != nil {
		t.Errorf("stills = %+v, %+v, want nil", got.Intro, got.Outro)
	}
}

// assertNoZeroField fails for any zero field of struct v, except those named.
func assertNoZeroField(t *testing.T, v any, except ...string) {
	t.Helper()
	rv := reflect.ValueOf(v)
	for i := range rv.NumField() {
		name := rv.Type().Field(i).Name
		if rv.Field(i).IsZero() && !slices.Contains(except, name) {
			t.Errorf("field %s is not mapped", name)
		}
	}
}

// The port reports a recording without the sync marker as the renderer's own
// sentinel (decision 69). The stand-in ffmpeg prints nothing, so the marker
// scan finds no frames.
func TestAssemblerPort_noMarkerIsRendererErrNoMarker(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := assemblerPort{ffmpeg: assembler.FFmpeg{Bin: bin, Probe: bin}}
	_, err := p.Assemble(context.Background(), renderer.AssembleRequest{Video: "rec.webm", Out: "out.mp4"})
	if !errors.Is(err, renderer.ErrNoMarker) {
		t.Errorf("err = %v, want renderer.ErrNoMarker", err)
	}
}
