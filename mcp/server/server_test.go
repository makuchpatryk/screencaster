package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"screencaster/core/executor"
	"screencaster/core/explorer"
	"screencaster/core/script"
	"screencaster/core/voices"
	"screencaster/mcp/queue"
)

const validScript = `name: demo-one
steps:
  - action: goto
    url: /projects
    narration:
      en: Hello.
`

// fakePage is the explorer's browser: goto and click only, click "#missing"
// fails.
type fakePage struct{ executor.Page }

func (fakePage) Goto(string) error { return nil }
func (fakePage) Click(sel string) error {
	if sel == "#missing" {
		return errors.New("element not found")
	}
	return nil
}
func (fakePage) Start() error                  { return nil }
func (fakePage) Abort()                        {}
func (fakePage) AriaSnapshot() (string, error) { return `- button "New project"`, nil }
func (fakePage) Count(string) (int, error)     { return 1, nil }
func (fakePage) Title() (string, error)        { return "Projects", nil }
func (fakePage) URL() string                   { return "http://app/projects" }

type env struct {
	work   string
	store  *queue.Store
	worker *queue.Worker
	cs     *mcp.ClientSession
	inst   voices.Installed
}

// newEnv starts the server over the in-memory transport with a project dir
// holding a valid config. The returned inst can be changed before the first
// call that reads voices.
func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{work: t.TempDir(), inst: voices.Installed{
		"en_US-ryan-high":      "/v/en",
		"pl_PL-darkman-medium": "/v/pl",
	}}
	e.writeFile("auth/state.json", "{}")
	e.writeFile("screencaster.yaml", "baseUrl: http://host.docker.internal:3000\nstorageState: auth/state.json\n")

	var err error
	e.store, err = queue.Open(filepath.Join(e.work, ".screencaster", "jobs.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.store.Close() })
	e.worker = queue.NewWorker(e.store)

	var n atomic.Int32
	srv := New(Deps{
		WorkDir: e.work, Store: e.store, Worker: e.worker,
		Explorer: explorer.Explorer{Launch: func(context.Context, explorer.LaunchOptions) (explorer.Session, error) {
			return fakePage{}, nil
		}},
		Voices: func() (voices.Installed, error) { return e.inst, nil },
		NewID:  func() string { return "job-" + string(rune('a'+n.Add(1)-1)) },
	}, "test")

	ctx, cancel := context.WithCancel(context.Background())
	t1, t2 := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.cs, err = mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.cs.Close(); _ = ss.Wait(); cancel() })
	return e
}

func (e *env) writeFile(rel, content string) {
	p := filepath.Join(e.work, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		panic(err)
	}
}

func (e *env) call(t *testing.T, tool string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := e.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", tool, err)
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// decode reads the structured output of a successful call into T.
func decode[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %s", text(res))
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return v
}

// FR-016 AC1: the names a client sees.
func TestServer_listsToolsAndPrompt(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	var tools []string
	var renderDesc string
	for tool, err := range e.cs.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		tools = append(tools, tool.Name)
		if tool.Name == "render_video" {
			renderDesc = tool.Description
		}
	}
	slices.Sort(tools)
	if want := []string{"explore_page", "get_options", "get_render_status", "render_video"}; !slices.Equal(tools, want) {
		t.Errorf("tools = %v, want %v", tools, want)
	}

	// FR-012: schema, example and the three rules, derived from core/script.
	for _, want := range []string{
		strings.TrimSpace(string(script.SchemaJSON()))[:20],
		"name: create-project",
		"relative to baseUrl",
		"narration text for every selected language",
		"No login steps",
	} {
		if !strings.Contains(renderDesc, want) {
			t.Errorf("render_video description lacks %q", want)
		}
	}

	var prompts []string
	for p, err := range e.cs.Prompts(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		prompts = append(prompts, p.Name)
	}
	if !slices.Equal(prompts, []string{"create_demo"}) {
		t.Errorf("prompts = %v, want [create_demo]", prompts)
	}
}

// FR-012 AC1: all validation errors in one tool error, no job row.
func TestRenderVideo_invalidScriptIsToolErrorAndNoRow(t *testing.T) {
	e := newEnv(t)
	e.writeFile("demos/bad.yaml", "name: Bad Name\nsteps:\n  - action: dance\n")
	e.writeFile("demos/ok.yaml", validScript)

	res := e.call(t, "render_video", map[string]any{"script": "demos/bad.yaml"})
	if !res.IsError {
		t.Fatalf("want tool error, got %s", text(res))
	}
	msg := text(res)
	if !strings.Contains(msg, "/name:") || !strings.Contains(msg, "/steps/0/action:") {
		t.Errorf("error should list every problem as pointer: message, got %q", msg)
	}

	out := decode[renderOut](t, e.call(t, "render_video", map[string]any{"script": "demos/ok.yaml"}))
	if out.Position != 1 || out.Status != "queued" || out.JobID == "" {
		t.Errorf("first valid job = %+v, want queued at position 1 (the invalid call left no row)", out)
	}
}

func TestRenderVideo_missingScriptAndConfigAreToolErrors(t *testing.T) {
	e := newEnv(t)
	if res := e.call(t, "render_video", map[string]any{"script": "demos/none.yaml"}); !res.IsError || !strings.Contains(text(res), "script not found") {
		t.Errorf("missing script: %v %q", res.IsError, text(res))
	}
	if err := os.Remove(filepath.Join(e.work, "screencaster.yaml")); err != nil {
		t.Fatal(err)
	}
	e.writeFile("demos/ok.yaml", validScript)
	want := "config not found: " + filepath.Join(e.work, "screencaster.yaml")
	if res := e.call(t, "render_video", map[string]any{"script": "demos/ok.yaml"}); !res.IsError || text(res) != want {
		t.Errorf("missing config: %v %q, want %q", res.IsError, text(res), want)
	}
}

func TestRenderVideo_languagesOverrideIsResolvedAtSubmit(t *testing.T) {
	e := newEnv(t)
	e.writeFile("demos/ok.yaml", validScript)

	// narration has no pl text: the override makes the script invalid.
	res := e.call(t, "render_video", map[string]any{"script": "demos/ok.yaml", "languages": []string{"en", "pl"}})
	if !res.IsError || !strings.Contains(text(res), "missing narration for language pl") {
		t.Errorf("override pl: %v %q", res.IsError, text(res))
	}
}

// FR-012 AC2 and FR-013: a job is running, the next one waits at position 1.
func TestRenderVideo_secondJobWhileRunningHasPositionOne(t *testing.T) {
	e := newEnv(t)
	e.writeFile("demos/ok.yaml", validScript)

	running, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.worker.Run(ctx,
			func(context.Context, queue.Job) ([]queue.Output, error) {
				running <- struct{}{}
				<-release
				return []queue.Output{{Lang: "en", Path: "/work/output/x.mp4", DurationMs: 900}}, nil
			},
			func(context.Context) (func(), error) { return func() {}, nil })
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })

	first := decode[renderOut](t, e.call(t, "render_video", map[string]any{"script": "demos/ok.yaml"}))
	<-running
	second := decode[renderOut](t, e.call(t, "render_video", map[string]any{"script": "demos/ok.yaml"}))
	if second.Position != 1 {
		t.Errorf("second job position = %d, want 1", second.Position)
	}

	st := decode[statusOut](t, e.call(t, "get_render_status", map[string]any{"jobId": second.JobID}))
	if st.Status != "queued" || st.Position != 1 || st.Script != "demos/ok.yaml" {
		t.Errorf("queued status = %+v", st)
	}
	if st := decode[statusOut](t, e.call(t, "get_render_status", map[string]any{"jobId": first.JobID})); st.Status != "running" || st.StartedAt == nil {
		t.Errorf("running status = %+v", st)
	}

	release <- struct{}{} // finish the first job; the second then starts
	<-running
	release <- struct{}{}
	deadline := time.Now().Add(2 * time.Second)
	for {
		st := decode[statusOut](t, e.call(t, "get_render_status", map[string]any{"jobId": first.JobID}))
		if st.Status == "succeeded" {
			if len(st.Outputs) != 1 || st.Outputs[0].DurationMs != 900 || st.FinishedAt == nil {
				t.Errorf("succeeded status = %+v", st)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never succeeded: %+v", st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// FR-013 AC.
func TestGetRenderStatus_unknownJob(t *testing.T) {
	e := newEnv(t)
	res := e.call(t, "get_render_status", map[string]any{"jobId": "nope"})
	if !res.IsError || text(res) != "job not found: nope" {
		t.Errorf("unknown job: %v %q", res.IsError, text(res))
	}
}

// FR-018 acceptance criteria.
func TestGetOptions(t *testing.T) {
	voicesDir := func(t *testing.T, names ...string) voices.Installed {
		t.Helper()
		dir := t.TempDir()
		for _, n := range names {
			if err := os.WriteFile(filepath.Join(dir, n+".onnx"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		inst, err := voices.Discover(dir)
		if err != nil {
			t.Fatal(err)
		}
		return inst
	}
	stock := []string{"en_US-ryan-high", "pl_PL-darkman-medium"}
	find := func(o optionsOut, code string) languageOut {
		for _, l := range o.Languages {
			if l.Code == code {
				return l
			}
		}
		t.Fatalf("language %s not listed in %+v", code, o.Languages)
		return languageOut{}
	}

	t.Run("stock image", func(t *testing.T) {
		e := newEnv(t)
		e.inst = voicesDir(t, stock...)
		e.writeFile("demos/ok.yaml", validScript)
		e.writeFile("demos/broken.yaml", "name: [")
		o := decode[optionsOut](t, e.call(t, "get_options", map[string]any{}))
		en, pl := find(o, "en"), find(o, "pl")
		if en.DefaultVoice == nil || *en.DefaultVoice != "en_US-ryan-high" || !en.SelectedByDefault {
			t.Errorf("en = %+v", en)
		}
		if pl.DefaultVoice == nil || *pl.DefaultVoice != "pl_PL-darkman-medium" || pl.SelectedByDefault {
			t.Errorf("pl = %+v", pl)
		}
		if !slices.Equal(o.Audiences, []string{"release-notes", "sales", "marketing"}) {
			t.Errorf("audiences = %v", o.Audiences)
		}
		if !slices.Equal(o.ExistingDemo, []string{"demo-one"}) {
			t.Errorf("existingDemos = %v, want only the valid demo", o.ExistingDemo)
		}
		if o.BaseURL != "http://host.docker.internal:3000" {
			t.Errorf("baseUrl = %q", o.BaseURL)
		}
	})

	t.Run("extra voices", func(t *testing.T) {
		e := newEnv(t)
		e.inst = voicesDir(t, append(slices.Clone(stock), "pl_PL-gosia-medium", "de_DE-thorsten-medium")...)
		o := decode[optionsOut](t, e.call(t, "get_options", map[string]any{}))
		if pl := find(o, "pl"); !slices.Contains(pl.Voices, "pl_PL-gosia-medium") {
			t.Errorf("pl voices = %v, want gosia listed", pl.Voices)
		}
		if de := find(o, "de"); de.DefaultVoice != nil || !slices.Equal(de.Voices, []string{"de_DE-thorsten-medium"}) {
			t.Errorf("de = %+v, want null default and its voice", de)
		}
	})

	t.Run("no voices at all", func(t *testing.T) {
		e := newEnv(t)
		e.inst = voices.Installed{}
		o := decode[optionsOut](t, e.call(t, "get_options", map[string]any{}))
		if en := find(o, "en"); len(en.Voices) != 0 || en.DefaultVoice != nil {
			t.Errorf("en = %+v, want empty voices", en)
		}
	})

	t.Run("config missing", func(t *testing.T) {
		e := newEnv(t)
		if err := os.Remove(filepath.Join(e.work, "screencaster.yaml")); err != nil {
			t.Fatal(err)
		}
		res := e.call(t, "get_options", map[string]any{})
		if want := "config not found: " + filepath.Join(e.work, "screencaster.yaml"); !res.IsError || text(res) != want {
			t.Errorf("got %v %q, want %q", res.IsError, text(res), want)
		}
	})
}

// FR-017 acceptance criteria 1 and 2 (3 is the e2e test).
func TestExplorePage(t *testing.T) {
	e := newEnv(t)

	out := decode[exploreOut](t, e.call(t, "explore_page", map[string]any{"url": "/projects"}))
	if !strings.Contains(out.Snapshot, `role=button[name="New project"]`) || out.Title != "Projects" {
		t.Errorf("explore = %+v", out)
	}

	res := e.call(t, "explore_page", map[string]any{
		"url":     "/projects",
		"actions": []map[string]any{{"action": "click", "selector": "#missing"}},
	})
	msg := text(res)
	if !res.IsError || !strings.Contains(msg, "step 1") || !strings.Contains(msg, "click #missing") || !strings.Contains(msg, "New project") {
		t.Errorf("failing action: %v %q, want step, action, target and snapshot", res.IsError, msg)
	}

	res = e.call(t, "explore_page", map[string]any{"url": "/", "actions": []map[string]any{{"action": "click"}}})
	if !res.IsError || !strings.Contains(text(res), "/steps/0") {
		t.Errorf("invalid action: %v %q", res.IsError, text(res))
	}
}

// FR-019 acceptance criteria.
func TestPrompt_createDemo(t *testing.T) {
	e := newEnv(t)
	res, err := e.cs.GetPrompt(context.Background(), &mcp.GetPromptParams{
		Name: "create_demo", Arguments: map[string]string{"description": "invite a user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, m := range res.Messages {
		b.WriteString(m.Content.(*mcp.TextContent).Text)
	}
	got := b.String()
	if !strings.Contains(got, "invite a user") {
		t.Error("prompt lacks the description")
	}
	// One marker per instruction 1-7 of FR-019.
	for i, want := range []string{
		"1. Call `get_options`",
		"2. Ask the developer once, in a single message",
		"3. Explore the running app with `explore_page`",
		"4. Write `demos/<name>.yaml`",
		"5. Call `render_video`",
		"6. Do not ask the developer to approve the YAML",
		"7. If the description above is empty",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("instruction %d missing: %q", i+1, want)
		}
	}

	res, err = e.cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "create_demo"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Messages[0].Content.(*mcp.TextContent).Text, noDescription) {
		t.Error("prompt without description should say none was given")
	}
}

// ARCHITECTURE §12: nothing but protocol frames may reach stdout. The
// protocol runs over the in-memory transport here, so any byte on os.Stdout
// came from the handlers.
func TestHandlers_writeNothingToStdout(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	captured := make(chan []byte)
	go func() { b, _ := io.ReadAll(r); captured <- b }()

	e := newEnv(t)
	e.writeFile("demos/ok.yaml", validScript)
	e.call(t, "render_video", map[string]any{"script": "demos/ok.yaml"})
	e.call(t, "render_video", map[string]any{"script": "demos/none.yaml"})
	e.call(t, "get_render_status", map[string]any{"jobId": "job-a"})
	e.call(t, "get_options", map[string]any{})
	e.call(t, "explore_page", map[string]any{"url": "/"})
	if _, err := e.cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "create_demo"}); err != nil {
		t.Fatal(err)
	}

	os.Stdout = orig
	_ = w.Close()
	if b := <-captured; len(b) != 0 {
		t.Errorf("handlers wrote %d bytes to stdout: %q", len(b), b)
	}
}
