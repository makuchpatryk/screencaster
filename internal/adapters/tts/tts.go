// Package tts is the seam between the render pipeline and a TTS engine:
// Engine is the contract every adapter satisfies. app/wire picks the adapter
// at startup. Adding a provider means one adapter package under adapters/tts,
// one case in wire.TTS and one image layer (ARCHITECTURE §15).
package tts

import (
	"context"
	"time"

	"screencaster/internal/domain/voices"
)

// Engine is a TTS provider: it lists its voices and speaks text into a PCM
// WAV at outPath, returning the clip's duration (FR-003). Adapters that get
// another format convert it themselves. Voice names are opaque IDs from the
// provider's own Catalog.
type Engine interface {
	Catalog() (voices.Catalog, error)
	Synthesize(ctx context.Context, voice, text, outPath string) (time.Duration, error)
}
