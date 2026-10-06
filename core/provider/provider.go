// Package provider is the seam between the render pipeline and a TTS engine.
// Engine is the contract, FromEnv picks the adapter once at startup. Adding a
// provider means one adapter package under core/provider, one case in FromEnv
// and one image layer (ARCHITECTURE §15).
package provider

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"screencaster/core/provider/piper"
	"screencaster/core/voices"
)

// Engine is a TTS provider: it lists its voices and speaks text into a PCM
// WAV at outPath, returning the clip's duration (FR-003). Adapters that get
// another format convert it themselves. Voice names are opaque IDs from the
// provider's own Catalog.
type Engine interface {
	Catalog() (voices.Catalog, error)
	Synthesize(ctx context.Context, voice, text, outPath string) (time.Duration, error)
}

// FromEnv builds the provider named by SCREENCASTER_TTS. An unset or unknown
// name is an error, so the entry points fail before any work. The adapter's
// own settings come from its own variables, set by the image; no path lives in
// code. workDir is the project folder whose voices/ extends the catalog.
func FromEnv(getenv func(string) string, workDir string) (Engine, error) {
	switch name := getenv("SCREENCASTER_TTS"); name {
	case "piper":
		p, err := piper.New(getenv("SCREENCASTER_PIPER_BIN"),
			getenv("SCREENCASTER_PIPER_VOICES"), filepath.Join(workDir, "voices"))
		if err != nil {
			return nil, err
		}
		return p, nil // not `return piper.New(...)`: a nil *Piper would be a non-nil Engine
	case "":
		return nil, errors.New("no TTS provider: set SCREENCASTER_TTS (available: piper)")
	default:
		return nil, fmt.Errorf("unknown TTS provider %q (available: piper)", name)
	}
}
