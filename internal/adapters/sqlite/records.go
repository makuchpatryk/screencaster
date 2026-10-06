package sqlite

import (
	"screencaster/internal/app/renderer"
	"screencaster/internal/domain/failure"
)

// The stored JSON of outputs_json and error_json (PRD §13). The tags are the
// format every jobs.db on disk already holds, so they never change; the
// domain types they map from carry no tags (ARCHITECTURE §9).

type outputRecord struct {
	Lang       string `json:"lang"`
	Path       string `json:"path"`
	DurationMs int64  `json:"durationMs"`
}

type failureRecord struct {
	Step    *int   `json:"step,omitempty"`
	Lang    string `json:"lang,omitempty"`
	Action  string `json:"action,omitempty"`
	Target  string `json:"target,omitempty"`
	Message string `json:"message"`
}

func toOutputRecords(outs []renderer.Output) []outputRecord {
	recs := make([]outputRecord, len(outs))
	for i, o := range outs {
		recs[i] = outputRecord{Lang: o.Lang, Path: o.Path, DurationMs: o.DurationMs}
	}
	return recs
}

func fromOutputRecords(recs []outputRecord) []renderer.Output {
	outs := make([]renderer.Output, len(recs))
	for i, r := range recs {
		outs[i] = renderer.Output{Lang: r.Lang, Path: r.Path, DurationMs: r.DurationMs}
	}
	return outs
}

func toFailureRecord(f *failure.Failure) failureRecord {
	return failureRecord{Step: f.Step, Lang: f.Lang, Action: f.Action, Target: f.Target, Message: f.Message}
}

func (r failureRecord) failure() *failure.Failure {
	return &failure.Failure{Step: r.Step, Lang: r.Lang, Action: r.Action, Target: r.Target, Message: r.Message}
}
