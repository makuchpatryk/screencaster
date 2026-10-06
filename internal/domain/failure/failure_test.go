package failure

import (
	"context"
	"errors"
	"testing"
)

func TestFailure_messageFormats(t *testing.T) {
	tests := []struct {
		name string
		f    *Failure
		want string
	}{
		{"tts (FR-003)", TTS(3, "pl", "boom"), "tts failed at step 3 (pl): boom"},
		{"assembly (FR-009)", Assembly("en", "line1\nline2"), "assembly failed (en): line1\nline2"},
		{"sync marker (decision 69)", SyncMarker("pl"), "sync marker not found in recording (pl)"},
		{"cards (decision 63)", Cards("pl", errors.New("chromium crashed")), "build cards (pl): chromium crashed"},
		{"interrupted (BR-009)", Interrupted(), "interrupted"},
		{
			"step carries BR-004 fields",
			Step(4, "en", "click", `role=button[name="Save"]`, errors.New("timeout 30000ms exceeded")),
			`step 4 (en) click role=button[name="Save"]: timeout 30000ms exceeded`,
		},
		{"step without target", Step(1, "en", "press", "", errors.New("bad key")), "step 1 (en) press: bad key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.f.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStep_unwrapsCause(t *testing.T) {
	var err error = Step(2, "en", "goto", "/x", context.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Error("errors.Is(step failure, context.Canceled) = false")
	}
	var f *Failure
	if !errors.As(err, &f) || *f.Step != 2 {
		t.Errorf("errors.As failed or wrong step: %+v", f)
	}
}

func TestValidationErrors_errorJoinsPointerAndMessage(t *testing.T) {
	tests := []struct {
		name string
		errs ValidationErrors
		want string
	}{
		{"one", ValidationErrors{{"/steps/0", "bad"}}, "/steps/0: bad"},
		{"many, one per line", ValidationErrors{{"/a", "x"}, {"/b", "y"}}, "/a: x\n/b: y"},
		{"no pointer prints message only", ValidationErrors{{"", "voice not installed: v"}}, "voice not installed: v"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.errs.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}
