// Package failure holds the error types every layer passes on: the
// single Failure shape for step, TTS, assembly, card and interruption errors, and
// ValidationErrors for input that is rejected before any work starts
// (ARCHITECTURE §9).
package failure

import (
	"fmt"
	"strings"
)

// Failure is the one error shape shared by CLI output, MCP tool errors and
// jobs.error_json (BR-004, FR-008). It is a domain type: the stored and the
// tool JSON shapes live with their adapters (adapters/sqlite,
// adapters/mcpserver).
type Failure struct {
	Step    *int // 1-based; nil for non-step failures
	Lang    string
	Action  string
	Target  string // selector or URL
	Message string

	cause error
}

// Error is the full Message for failures that carry their own wording (TTS,
// assembly, interrupted) and a one-line BR-004 report for step failures.
func (f *Failure) Error() string {
	if f.Action == "" {
		return f.Message
	}
	var b strings.Builder
	if f.Step != nil {
		fmt.Fprintf(&b, "step %d ", *f.Step)
	}
	if f.Lang != "" {
		fmt.Fprintf(&b, "(%s) ", f.Lang)
	}
	b.WriteString(f.Action)
	if f.Target != "" {
		b.WriteString(" " + f.Target)
	}
	b.WriteString(": " + f.Message)
	return b.String()
}

// Unwrap exposes the underlying error so callers can use errors.Is, for
// example against context.Canceled.
func (f *Failure) Unwrap() error { return f.cause }

// Step reports that the action of step i (1-based) failed or timed out.
func Step(i int, lang, action, target string, err error) *Failure {
	return &Failure{Step: &i, Lang: lang, Action: action, Target: target, Message: err.Error(), cause: err}
}

// TTS reports that the TTS provider failed for the narration of step i.
func TTS(i int, lang, stderr string) *Failure {
	return &Failure{Step: &i, Lang: lang, Message: fmt.Sprintf("tts failed at step %d (%s): %s", i, lang, stderr)}
}

// Assembly reports that ffmpeg failed; stderrTail is its last 20 lines.
func Assembly(lang, stderrTail string) *Failure {
	return &Failure{Lang: lang, Message: fmt.Sprintf("assembly failed (%s): %s", lang, stderrTail)}
}

// SyncMarker reports that the recording has no sync marker to align the
// narration on (decision 69). There is no fallback alignment.
func SyncMarker(lang string) *Failure {
	return &Failure{Lang: lang, Message: fmt.Sprintf("sync marker not found in recording (%s)", lang)}
}

// Cards reports that the start or end card could not be built (page or
// screenshot); err is the cause.
func Cards(lang string, err error) *Failure {
	return &Failure{Lang: lang, Message: fmt.Sprintf("build cards (%s): %s", lang, err), cause: err}
}

// Interrupted marks a job that was queued or running when the server stopped
// (BR-009).
func Interrupted() *Failure {
	return &Failure{Message: "interrupted"}
}

// ValidationError is one rejected input. Pointer is a JSON pointer into the
// script ("/steps/3/narration"), or empty when the error is not tied to one.
type ValidationError struct {
	Pointer string `json:"pointer"`
	Message string `json:"message"`
}

func (e ValidationError) Error() string {
	if e.Pointer == "" {
		return e.Message
	}
	return e.Pointer + ": " + e.Message
}

// ValidationErrors lists every problem found, one per line when printed.
type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	lines := make([]string, len(e))
	for i, v := range e {
		lines[i] = v.Error()
	}
	return strings.Join(lines, "\n")
}
