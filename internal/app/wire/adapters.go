package wire

import (
	"context"
	"errors"

	"screencaster/internal/adapters/assembler"
	"screencaster/internal/adapters/browser"
	"screencaster/internal/app/renderer"
	"screencaster/internal/domain/recorder"
)

// The adapters below map the renderer's port types onto the tool wrappers,
// field by field, so app/renderer names no tool package (ARCHITECTURE §3).

// recorderPort is renderer.Recorder over domain/recorder.
type recorderPort struct{ rec recorder.Recorder }

func (p recorderPort) Record(ctx context.Context, r renderer.RecordRequest) (renderer.Recording, error) {
	out, err := p.rec.Record(ctx, recordInput(r))
	if err != nil {
		return renderer.Recording{}, err
	}
	return renderer.Recording{Video: out.WebmPath, Offsets: out.Offsets}, nil
}

func recordInput(r renderer.RecordRequest) recorder.Input {
	return recorder.Input{
		Steps:        r.Steps,
		Clips:        r.Clips,
		Lang:         r.Lang,
		Dir:          r.Dir,
		BaseURL:      r.BaseURL,
		StorageState: r.StorageState,
		StartImage:   r.StartImage,
		OnStep:       r.OnStep,
	}
}

// assemblerPort is renderer.Assembler over adapters/assembler.
type assemblerPort struct{ ffmpeg assembler.FFmpeg }

func (p assemblerPort) Assemble(ctx context.Context, r renderer.AssembleRequest) (renderer.Assembly, error) {
	out, err := p.ffmpeg.Assemble(ctx, assembleInput(r))
	if errors.Is(err, assembler.ErrNoMarker) {
		return renderer.Assembly{}, renderer.ErrNoMarker
	}
	if err != nil {
		return renderer.Assembly{}, err
	}
	return renderer.Assembly{DurationMs: out.DurationMs, MarkerEnd: out.MarkerEnd}, nil
}

func assembleInput(r renderer.AssembleRequest) assembler.Input {
	in := assembler.Input{
		Webm:    r.Video,
		Intro:   still(r.Intro),
		Outro:   still(r.Outro),
		Title:   r.Title,
		Comment: r.Comment,
		Out:     r.Out,
	}
	for _, c := range r.Clips {
		in.Clips = append(in.Clips, assembler.Clip{Path: c.Path, Offset: c.Offset})
	}
	return in
}

// still maps a card picture; nil (card off) stays nil.
func still(s *renderer.Still) *assembler.Still {
	if s == nil {
		return nil
	}
	return &assembler.Still{Path: s.Path, Duration: s.Duration}
}

// cardsPort is renderer.Cards over the real browser.
type cardsPort struct{}

func (cardsPort) Screenshot(ctx context.Context, shots []renderer.Shot) error {
	bs := make([]browser.Shot, len(shots))
	for i, s := range shots {
		bs[i] = browser.Shot{HTML: s.HTML, Out: s.Out}
	}
	return browser.Launcher{}.Screenshot(ctx, bs)
}
