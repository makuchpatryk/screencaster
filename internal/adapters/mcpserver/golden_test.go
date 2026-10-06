package mcpserver

import (
	"context"
	"testing"
	"time"

	"screencaster/internal/app/jobs"
	"screencaster/internal/app/renderer"
	"screencaster/internal/domain/failure"
)

// get_render_status is a contract with MCP clients: these goldens were captured
// before the status types stopped reusing the domain types, and must not
// change (FR-013). The SDK writes the text content with sorted keys.
const (
	goldenSucceeded = `{"createdAt":"2026-10-03T10:15:00Z","finishedAt":"2026-10-03T10:15:00Z","jobId":"job-a","outputs":[{"durationMs":900,"lang":"en","path":"/work/output/x.mp4"}],"script":"demos/ok.yaml","startedAt":"2026-10-03T10:15:00Z","status":"succeeded"}`
	goldenFailed    = `{"createdAt":"2026-10-03T10:15:00Z","error":{"action":"click","lang":"en","message":"timeout","step":2,"target":"#new"},"finishedAt":"2026-10-03T10:15:00Z","jobId":"job-b","script":"demos/ok.yaml","startedAt":"2026-10-03T10:15:00Z","status":"failed"}`
	goldenQueued    = `{"createdAt":"2026-10-03T10:15:00Z","jobId":"job-c","position":1,"script":"demos/ok.yaml","status":"queued"}`
)

func TestGetRenderStatus_jsonIsByteForByteStable(t *testing.T) {
	at := time.Date(2026, 10, 3, 10, 15, 0, 0, time.UTC)
	e := newEnvAt(t, func() time.Time { return at })
	e.writeFile("demos/ok.yaml", validScript)

	e.render = func(_ context.Context, j jobs.Job) ([]renderer.Output, error) {
		if j.ID == "job-b" {
			step := 2
			return nil, &failure.Failure{Step: &step, Lang: "en", Action: "click", Target: "#new", Message: "timeout"}
		}
		return []renderer.Output{{Lang: "en", Path: "/work/output/x.mp4", DurationMs: 900}}, nil
	}
	for range 2 {
		decode[renderOut](t, e.call(t, "render_video", map[string]any{"script": "demos/ok.yaml"}))
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { e.worker.Run(ctx); close(stopped) }()
	for _, id := range []string{"job-a", "job-b"} {
		for decode[statusOut](t, e.call(t, "get_render_status", map[string]any{"jobId": id})).FinishedAt == nil {
			time.Sleep(5 * time.Millisecond)
		}
	}
	cancel()
	<-stopped
	decode[renderOut](t, e.call(t, "render_video", map[string]any{"script": "demos/ok.yaml"}))

	for _, c := range []struct{ id, want string }{
		{"job-a", goldenSucceeded}, {"job-b", goldenFailed}, {"job-c", goldenQueued},
	} {
		res := e.call(t, "get_render_status", map[string]any{"jobId": c.id})
		if got := text(res); res.IsError || got != c.want {
			t.Errorf("%s status =\n%s\nwant\n%s", c.id, got, c.want)
		}
	}
}
