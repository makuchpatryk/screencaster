package mcpserver

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"screencaster/internal/app/jobs"
	"screencaster/internal/app/renderer"
	"screencaster/internal/domain/script"
)

const shotsScript = `name: shots-one
type: screenshots
steps:
  - goto: http://host.docker.internal:3000/projects
  - screenshot: true
`

func TestTakeScreenshots_descriptionPointsAtRenderVideoAndShowsTheExample(t *testing.T) {
	e := newEnv(t)
	var desc string
	for tool, err := range e.cs.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Name == "take_screenshots" {
			desc = tool.Description
		}
	}
	for _, want := range []string{
		"type: screenshots",
		"<outputDir>/<name>/screenshots/01.png",
		"overwrites",
		"get_render_status",
		"render_video's description",
		strings.SplitN(string(script.ExampleScreenshotsYAML()), "\n", 2)[0], // the example's name line
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("take_screenshots description lacks %q", want)
		}
	}
}

// The example in the tool description is what an LLM copies, so the tool must
// accept it as it stands.
func TestTakeScreenshots_theExampleScriptQueues(t *testing.T) {
	e := newEnv(t)
	e.writeFile("demos/example.yaml", string(script.ExampleScreenshotsYAML()))

	out := decode[renderOut](t, e.call(t, "take_screenshots", map[string]any{"script": "demos/example.yaml"}))
	if out.Status != "queued" || out.Position != 1 || out.JobID == "" {
		t.Errorf("example job = %+v, want queued at position 1", out)
	}
}

// The worker passes a job's languages back as the override, which a screenshots
// script rejects, so the job stores an empty list.
func TestTakeScreenshots_jobStoresNoLanguages(t *testing.T) {
	e := newEnv(t)
	e.writeFile("demos/shots.yaml", shotsScript)

	out := decode[renderOut](t, e.call(t, "take_screenshots", map[string]any{"script": "demos/shots.yaml"}))
	j, err := e.store.Get(context.Background(), out.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if j.Languages == nil || len(j.Languages) != 0 {
		t.Errorf("Languages = %#v, want an empty list", j.Languages)
	}
	if j.DemoName != "shots-one" || j.ScriptPath != "demos/shots.yaml" || string(j.Script) != shotsScript {
		t.Errorf("job = %+v, want the submitted script", j)
	}
}

func TestTakeScreenshots_needsNoInstalledVoices(t *testing.T) { // decision 72
	e := newEnv(t)
	e.cat = catalog() // no voices at all fails every video script (BR-011)
	e.writeFile("demos/shots.yaml", shotsScript)

	out := decode[renderOut](t, e.call(t, "take_screenshots", map[string]any{"script": "demos/shots.yaml"}))
	if out.Position != 1 {
		t.Errorf("job = %+v, want queued at position 1", out)
	}
}

func TestTakeScreenshots_videoScriptIsRefusedWithNoRow(t *testing.T) {
	e := newEnv(t)
	e.writeFile("demos/video.yaml", validScript)
	e.writeFile("demos/shots.yaml", shotsScript)

	res := e.call(t, "take_screenshots", map[string]any{"script": "demos/video.yaml"})
	if want := "script type is video; use render_video"; !res.IsError || text(res) != want {
		t.Errorf("video script: %v %q, want error %q", res.IsError, text(res), want)
	}
	out := decode[renderOut](t, e.call(t, "take_screenshots", map[string]any{"script": "demos/shots.yaml"}))
	if out.Position != 1 {
		t.Errorf("next job = %+v, want position 1 (the refused call left no row)", out)
	}
}

func TestRenderVideo_screenshotsScriptIsRefusedWithNoRow(t *testing.T) {
	e := newEnv(t)
	e.writeFile("demos/video.yaml", validScript)
	e.writeFile("demos/shots.yaml", shotsScript)

	res := e.call(t, "render_video", map[string]any{"script": "demos/shots.yaml"})
	if want := "script type is screenshots; use take_screenshots"; !res.IsError || text(res) != want {
		t.Errorf("screenshots script: %v %q, want error %q", res.IsError, text(res), want)
	}
	out := decode[renderOut](t, e.call(t, "render_video", map[string]any{"script": "demos/video.yaml"}))
	if out.Position != 1 {
		t.Errorf("next job = %+v, want position 1 (the refused call left no row)", out)
	}
}

// The type check runs before the tool's own validation: a video script that
// no installed voice can narrate, sent to take_screenshots, is told which tool
// to use instead of getting voice errors.
func TestTakeScreenshots_wrongToolHintComesBeforeVoiceErrors(t *testing.T) {
	e := newEnv(t)
	e.cat = catalog() // no voices at all fails every video script (BR-011)
	e.writeFile("demos/video.yaml", validScript)

	res := e.call(t, "take_screenshots", map[string]any{"script": "demos/video.yaml"})
	if want := "script type is video; use render_video"; !res.IsError || text(res) != want {
		t.Errorf("video script: %v %q, want error %q", res.IsError, text(res), want)
	}
}

// A language override is rejected by a screenshots script, but the wrong tool
// is the first thing to report.
func TestRenderVideo_wrongToolHintComesBeforeLanguageError(t *testing.T) {
	e := newEnv(t)
	e.writeFile("demos/shots.yaml", shotsScript)

	res := e.call(t, "render_video", map[string]any{"script": "demos/shots.yaml", "languages": []string{"en"}})
	if want := "script type is screenshots; use take_screenshots"; !res.IsError || text(res) != want {
		t.Errorf("screenshots script: %v %q, want error %q", res.IsError, text(res), want)
	}
}

func TestTakeScreenshots_invalidScriptListsEveryProblem(t *testing.T) {
	e := newEnv(t)
	e.writeFile("demos/bad.yaml", `name: bad
type: screenshots
languages: [en]
steps:
  - goto: http://host.docker.internal:3000/
    narration: {en: Hello.}
`)

	res := e.call(t, "take_screenshots", map[string]any{"script": "demos/bad.yaml"})
	msg := text(res)
	if !res.IsError {
		t.Fatalf("want tool error, got %s", msg)
	}
	for _, want := range []string{
		"/languages: languages is not allowed in a screenshots script",
		"/steps/0/narration: narration is not allowed in a screenshots script",
		"/steps: a screenshots script needs at least one screenshot step",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error lacks %q, got %q", want, msg)
		}
	}
}

func TestTakeScreenshots_scriptOutsideWorkDirIsRefused(t *testing.T) { // decision 58
	e := newEnv(t)
	res := e.call(t, "take_screenshots", map[string]any{"script": "../other.yaml"})
	if want := "script must stay inside the working directory: ../other.yaml"; !res.IsError || text(res) != want {
		t.Errorf("outside script: %v %q, want error %q", res.IsError, text(res), want)
	}
}

// A screenshot output has no language and no duration, so the status JSON
// carries only the path; a video's output keeps both (the goldens pin that).
func TestGetRenderStatus_screenshotOutputsListPathsOnly(t *testing.T) {
	e := newEnv(t)
	e.writeFile("demos/shots.yaml", shotsScript)
	paths := []string{"/work/demos/output/shots-one/screenshots/01.png", "/work/demos/output/shots-one/screenshots/02.png"}
	e.render = func(context.Context, jobs.Job) ([]renderer.Output, error) {
		return []renderer.Output{{Path: paths[0]}, {Path: paths[1]}}, nil
	}
	out := decode[renderOut](t, e.call(t, "take_screenshots", map[string]any{"script": "demos/shots.yaml"}))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.worker.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	deadline := time.Now().Add(2 * time.Second)
	for {
		res := e.call(t, "get_render_status", map[string]any{"jobId": out.JobID})
		st := decode[statusOut](t, res)
		if st.Status == "succeeded" {
			var got []string
			for _, o := range st.Outputs {
				got = append(got, o.Path)
			}
			if !slices.Equal(got, paths) {
				t.Errorf("output paths = %v, want %v", got, paths)
			}
			if raw := text(res); strings.Contains(raw, `"lang"`) || strings.Contains(raw, "durationMs") {
				t.Errorf("status JSON = %s, want no lang or durationMs on a screenshot", raw)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never succeeded: %+v", st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestGetRenderStatus_jobIDDescriptionNamesBothTools(t *testing.T) {
	e := newEnv(t)
	for tool, err := range e.cs.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Name != "get_render_status" {
			continue
		}
		raw, _ := tool.InputSchema.(map[string]any)["properties"].(map[string]any)["jobId"].(map[string]any)["description"].(string)
		if raw != "id returned by render_video or take_screenshots" {
			t.Errorf("jobId description = %q", raw)
		}
	}
}

func TestPrompt_createDemoMentionsScreenshots(t *testing.T) {
	if !strings.Contains(createDemoPrompt, "take_screenshots") || !strings.Contains(createDemoPrompt, "type: screenshots") {
		t.Error("create_demo prompt does not say how to ask for screenshots")
	}
}
