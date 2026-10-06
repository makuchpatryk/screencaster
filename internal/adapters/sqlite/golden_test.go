package sqlite

import (
	"context"
	"testing"

	"screencaster/internal/app/renderer"
	"screencaster/internal/domain/failure"
)

// The stored JSON is a contract with every jobs.db on disk: these goldens were
// captured before the domain types lost their JSON tags and must not change.
const (
	goldenOutputs     = `[{"lang":"en","path":"/work/output/a.en.mp4","durationMs":1200},{"lang":"pl","path":"/work/output/a.pl.mp4","durationMs":1300}]`
	goldenStepError   = `{"step":3,"lang":"en","action":"click","target":"#x","message":"timeout"}`
	goldenPlainError  = `{"lang":"en","message":"assembly failed (en): boom"}`
	goldenInterrupted = `{"message":"interrupted"}`
)

// stored reads the raw JSON columns of job id.
func stored(t *testing.T, s *Store, id string) (outputs, errJSON string) {
	t.Helper()
	var out, errj *string
	if err := s.db.QueryRow(`SELECT outputs_json, error_json FROM jobs WHERE id = ?`, id).Scan(&out, &errj); err != nil {
		t.Fatal(err)
	}
	if out != nil {
		outputs = *out
	}
	if errj != nil {
		errJSON = *errj
	}
	return outputs, errJSON
}

func TestStoredJSON_isByteForByteStable(t *testing.T) { // PRD §13
	s := openStore(t)
	ctx := context.Background()
	insert(t, s, "ok", "step", "plain", "lost")

	step := 3
	for _, c := range []struct {
		id   string
		outs []renderer.Output
		f    *failure.Failure
	}{
		{"ok", []renderer.Output{
			{Lang: "en", Path: "/work/output/a.en.mp4", DurationMs: 1200},
			{Lang: "pl", Path: "/work/output/a.pl.mp4", DurationMs: 1300},
		}, nil},
		{"step", nil, &failure.Failure{Step: &step, Lang: "en", Action: "click", Target: "#x", Message: "timeout"}},
		{"plain", nil, failure.Assembly("en", "boom")},
	} {
		if err := s.Finish(ctx, c.id, c.outs, c.f); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Recover(ctx); err != nil { // "lost" was still queued
		t.Fatal(err)
	}

	for _, c := range []struct{ id, wantOut, wantErr string }{
		{"ok", goldenOutputs, ""},
		{"step", "", goldenStepError},
		{"plain", "", goldenPlainError},
		{"lost", "", goldenInterrupted},
	} {
		out, errJSON := stored(t, s, c.id)
		if out != c.wantOut || errJSON != c.wantErr {
			t.Errorf("%s: outputs_json = %s, error_json = %s; want %s, %s", c.id, out, errJSON, c.wantOut, c.wantErr)
		}
	}
}

// A row written by an earlier version decodes into the same job.
func TestStoredJSON_oldRowDecodes(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	insert(t, s, "ok", "step")
	if _, err := s.db.Exec(`UPDATE jobs SET status = 'succeeded', outputs_json = ? WHERE id = 'ok'`, goldenOutputs); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE jobs SET status = 'failed', error_json = ? WHERE id = 'step'`, goldenStepError); err != nil {
		t.Fatal(err)
	}

	ok, err := s.Get(ctx, "ok")
	if err != nil {
		t.Fatal(err)
	}
	if len(ok.Outputs) != 2 || ok.Outputs[1].Lang != "pl" || ok.Outputs[1].Path != "/work/output/a.pl.mp4" || ok.Outputs[1].DurationMs != 1300 {
		t.Errorf("outputs = %+v", ok.Outputs)
	}
	failed, err := s.Get(ctx, "step")
	if err != nil {
		t.Fatal(err)
	}
	if f := failed.Error; f == nil || f.Step == nil || *f.Step != 3 || f.Lang != "en" || f.Action != "click" || f.Target != "#x" || f.Message != "timeout" {
		t.Errorf("error = %+v", failed.Error)
	}
}
