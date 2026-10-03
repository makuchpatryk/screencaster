package failure

import (
	"context"
	"encoding/json"
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

func TestFailure_jsonShapeMatchesErrorJSON(t *testing.T) {
	tests := []struct {
		name string
		f    *Failure
		want string
	}{
		{
			"step failure has all BR-004 keys",
			Step(4, "en", "click", "#x", errors.New("nope")),
			`{"step":4,"lang":"en","action":"click","target":"#x","message":"nope"}`,
		},
		{"interrupted is only a message (PRD §13)", Interrupted(), `{"message":"interrupted"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.f)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("json = %s, want %s", got, tt.want)
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
