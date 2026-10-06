package wire

import (
	"context"
	"testing"
	"time"

	"screencaster/internal/domain/voices"
)

// stubEngine is a tts.Engine with an empty catalog.
type stubEngine struct{}

func (stubEngine) Catalog() (voices.Catalog, error) { return voices.Catalog{}, nil }
func (stubEngine) Synthesize(context.Context, string, string, string) (time.Duration, error) {
	return 0, nil
}

// A port left nil would panic mid-render, so every one NewDeps promises is
// set, including the screenshots shooter.
func TestNewDeps_wiresEveryPort(t *testing.T) {
	d, err := NewDeps(stubEngine{}, func() string { return "run" })
	if err != nil {
		t.Fatal(err)
	}
	ports := map[string]bool{
		"TTS": d.TTS != nil, "Rec": d.Rec != nil, "Asm": d.Asm != nil, "Cards": d.Cards != nil,
		"Shots": d.Shots != nil, "Files": d.Files != nil, "Now": d.Now != nil, "RunID": d.RunID != nil,
	}
	for name, set := range ports {
		if !set {
			t.Errorf("Deps.%s is not wired", name)
		}
	}
}
