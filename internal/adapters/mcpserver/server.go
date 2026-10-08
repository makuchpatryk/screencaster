// Package mcpserver registers the MCP tools and the create_demo prompt
// (FR-012, FR-013, FR-017, FR-018, FR-019). It maps use-case results to tool
// results and holds no render logic or SQL (CODE_QUALITY, Separation).
package mcpserver

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"screencaster/internal/adapters/sqlite"
	"screencaster/internal/app/explorer"
	"screencaster/internal/app/jobs"
	"screencaster/internal/app/renderer"
	"screencaster/internal/domain/failure"
	"screencaster/internal/domain/script"
	"screencaster/internal/domain/voices"
)

//go:embed create_demo.md
var createDemoPrompt string

const (
	serverName    = "screencaster"
	demosDir      = "demos"
	noDescription = "(none given)"
)

// Deps are the collaborators of the server, wired in main.
type Deps struct {
	WorkDir  string
	Store    *sqlite.Store
	Worker   *jobs.Worker
	Explorer explorer.Explorer
	// Files reads the script and card images for render_video's check.
	Files renderer.Files
	// Voices lists the active TTS provider's voices.
	Voices func() (voices.Catalog, error)
	// NewID returns a fresh job ID (UUID v4).
	NewID func() string
}

type handlers struct{ Deps }

// New returns the MCP server with its five tools and one prompt.
func New(d Deps, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: version},
		&mcp.ServerOptions{Logger: slog.Default()})
	h := handlers{d}

	mcp.AddTool(s, &mcp.Tool{Name: "render_video", Description: renderDescription()}, h.renderVideo)
	mcp.AddTool(s, &mcp.Tool{Name: "take_screenshots", Description: screenshotsDescription()}, h.takeScreenshots)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_render_status",
		Description: "Status of a render job: queued (with position), running, succeeded (with output paths) or failed (with the failing step).",
	}, h.renderStatus)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "explore_page",
		InputSchema: exploreSchema,
		Description: "Open a page of the app in a fresh browser and return its accessibility tree. " +
			"Every interactive line ends with `-> <selector>`, a selector that is unique on the page and works unchanged in a script step. " +
			"`url` must be an absolute http(s) URL, and pass its storageState if the app needs a login. " +
			"`actions` (script steps without narration) are replayed first to reach a deeper page state; a goto there must be absolute too. Every call starts from scratch. " +
			"A failing action returns an error naming the step plus the snapshot at that point.",
	}, h.explorePage)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_options",
		Description: "Installed languages and voices (with defaults), audiences, names of existing demos. Call before asking the developer what to render.",
	}, h.options)

	s.AddPrompt(&mcp.Prompt{
		Name:        "create_demo",
		Description: "Guided creation of a narrated demo video",
		Arguments: []*mcp.PromptArgument{
			{Name: "description", Description: "What the demo should show"},
		},
	}, createDemo)
	return s
}

// renderDescription is built from the embedded schema and example, so the
// tool text cannot drift from what validation accepts (CODE_QUALITY, DRY).
func renderDescription() string {
	return "Queue a narrated demo video. `script` is the path of a YAML script relative to the project directory; " +
		"`languages` (e.g. [\"en\",\"pl\"]) overrides the script's `languages` (default [\"en\"]). " +
		"The script is validated now; on success the job is queued and its id and queue position are returned. " +
		"Poll `get_render_status` with the id.\n\n" +
		"Rules:\n" +
		"- Every goto is an absolute http(s) URL, for example http://172.17.0.1:3000/projects. A relative path is rejected.\n" +
		"- Every narrated step needs narration text for every selected language.\n" +
		"- No login steps: authentication comes from the script's optional `storageState`.\n\n" +
		"Script format (JSON Schema):\n" + string(script.SchemaJSON()) + "\n\n" +
		"Example script:\n" + string(script.ExampleYAML())
}

// screenshotsDescription points at render_video's schema instead of repeating
// it, and shows the screenshots example (a test keeps it valid).
func screenshotsDescription() string {
	return "Queue PNG screenshots of the app instead of a video. `script` is the path of a YAML script with `type: screenshots`, " +
		"relative to the project directory. Each `screenshot` step captures a PNG (the viewport, the full page, one element or a clip region, " +
		"optionally annotated with a box, arrow, label or dim) into <outputDir>/<name>/screenshots/01.png, 02.png, ... in step order. " +
		"A rerun overwrites those files and removes numbered shots it no longer produces. There is no narration, language, voice or card. " +
		"The script is validated now; on success the job is queued and its id and queue position are returned. " +
		"Poll `get_render_status` with the id: its outputs list the PNG paths. " +
		"The full script schema is in render_video's description.\n\n" +
		"Example script:\n" + string(script.ExampleScreenshotsYAML())
}

// scriptTools names the tool that queues each script type, for the message a
// script sent to the wrong one gets.
var scriptTools = map[string]string{
	script.TypeVideo:       "render_video",
	script.TypeScreenshots: "take_screenshots",
}

// ---- render_video, take_screenshots ----

type renderIn struct {
	Script    string   `json:"script" jsonschema:"path of the YAML script relative to the project directory"`
	Languages []string `json:"languages,omitempty" jsonschema:"language codes overriding the script's languages"`
}

type renderOut struct {
	JobID    string `json:"jobId"`
	Status   string `json:"status"`
	Position int    `json:"position"`
}

func (h handlers) renderVideo(ctx context.Context, _ *mcp.CallToolRequest, in renderIn) (*mcp.CallToolResult, renderOut, error) {
	out, err := h.enqueue(ctx, in.Script, in.Languages, script.TypeVideo)
	return nil, out, err
}

type shotsIn struct {
	Script string `json:"script" jsonschema:"path of the YAML script (type: screenshots) relative to the project directory"`
}

func (h handlers) takeScreenshots(ctx context.Context, _ *mcp.CallToolRequest, in shotsIn) (*mcp.CallToolResult, renderOut, error) {
	out, err := h.enqueue(ctx, in.Script, nil, script.TypeScreenshots)
	return nil, out, err
}

// enqueue validates the script now and queues a job for it, or fails with a
// tool error and no job row (FR-012). kind is the script type the calling tool
// takes; a script of the other type is refused with the tool to use instead.
func (h handlers) enqueue(ctx context.Context, scriptPath string, languages []string, kind string) (renderOut, error) {
	// The tool input is LLM-written, so the script path is not trusted (decision 58).
	path, err := renderer.ScriptPath(h.WorkDir, scriptPath)
	if err != nil {
		return renderOut{}, err
	}
	installed, err := h.Voices()
	if err != nil {
		return renderOut{}, err
	}
	// Read once: the bytes validated here are the bytes the job renders, even
	// if the file changes while the job waits (ARCHITECTURE §10).
	data, err := renderer.ReadScript(h.Files, path)
	if err != nil {
		return renderOut{}, err
	}
	// The type check comes before Prepare, so a script sent to the wrong tool
	// is told so instead of getting that tool's voice or language errors. A
	// script that does not parse falls through to Prepare for its own errors.
	if s, err := script.Parse(data); err == nil && s.Kind() != kind {
		return renderOut{}, fmt.Errorf("script type is %s; use %s", s.Kind(), scriptTools[s.Kind()])
	}
	plan, err := renderer.Prepare(renderer.Request{WorkDir: h.WorkDir, ScriptPath: scriptPath, Script: data, LangOverride: languages}, installed, h.Files)
	if err != nil {
		return renderOut{}, err
	}
	// A screenshots job stores no languages: the worker passes them back as the
	// override, which a screenshots script rejects.
	langs := plan.Languages
	if langs == nil {
		langs = []string{}
	}
	id := h.NewID()
	pos, err := h.Store.Insert(ctx, jobs.NewJob{
		ID: id, ScriptPath: scriptPath, Script: data, DemoName: plan.Script.Name, Languages: langs,
	})
	if err != nil {
		return renderOut{}, err
	}
	h.Worker.Notify()
	return renderOut{JobID: id, Status: string(jobs.Queued), Position: pos}, nil
}

// ---- get_render_status ----

type statusIn struct {
	JobID string `json:"jobId" jsonschema:"id returned by render_video or take_screenshots"`
}

type statusOut struct {
	JobID      string      `json:"jobId"`
	Status     string      `json:"status"`
	Script     string      `json:"script"`
	Position   int         `json:"position,omitempty"`
	Outputs    []outputOut `json:"outputs,omitempty"`
	Error      *errorOut   `json:"error,omitempty"`
	CreatedAt  time.Time   `json:"createdAt"`
	StartedAt  *time.Time  `json:"startedAt,omitempty"`
	FinishedAt *time.Time  `json:"finishedAt,omitempty"`
}

// outputOut and errorOut are the tool's JSON for renderer.Output and
// failure.Failure; the domain types carry no tags (ARCHITECTURE §9).
// A video output always has a language and a duration; a screenshot has
// neither, so both are omitted when empty.
type outputOut struct {
	Lang       string `json:"lang,omitempty"`
	Path       string `json:"path"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

type errorOut struct {
	Step    *int   `json:"step,omitempty"`
	Lang    string `json:"lang,omitempty"`
	Action  string `json:"action,omitempty"`
	Target  string `json:"target,omitempty"`
	Message string `json:"message"`
}

func toStatusOut(j jobs.Job) statusOut {
	out := statusOut{
		JobID: j.ID, Status: string(j.Status), Script: j.ScriptPath, Position: j.Position,
		CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt,
	}
	for _, o := range j.Outputs {
		out.Outputs = append(out.Outputs, outputOut{Lang: o.Lang, Path: o.Path, DurationMs: o.DurationMs})
	}
	if f := j.Error; f != nil {
		out.Error = &errorOut{Step: f.Step, Lang: f.Lang, Action: f.Action, Target: f.Target, Message: f.Message}
	}
	return out
}

func (h handlers) renderStatus(ctx context.Context, _ *mcp.CallToolRequest, in statusIn) (*mcp.CallToolResult, statusOut, error) {
	j, err := h.Store.Get(ctx, in.JobID)
	if errors.Is(err, sqlite.ErrNotFound) {
		return nil, statusOut{}, fmt.Errorf("job not found: %s", in.JobID)
	}
	if err != nil {
		return nil, statusOut{}, err
	}
	return nil, toStatusOut(j), nil
}

// ---- explore_page ----

type exploreIn struct {
	URL          string               `json:"url"`
	StorageState *script.StorageState `json:"storageState,omitempty"`
	Actions      []json.RawMessage    `json:"actions,omitempty"` // decoded by script.ParseSteps
}

// exploreSchema is explore_page's input schema. A step's action is an
// interface, so the schema is not inferred from exploreIn: actions and
// storageState point at the script schema's own definitions, and the tool
// accepts exactly the step form a script does (CODE_QUALITY, DRY).
var exploreSchema = mustExploreSchema()

func mustExploreSchema() json.RawMessage {
	var doc struct {
		Defs json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(script.SchemaJSON(), &doc); err != nil {
		panic(err) // the embedded schema is valid JSON (script tests)
	}
	b, err := json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"url"},
		"properties": map[string]any{
			"url": map[string]any{"type": "string", "description": "absolute http(s) URL of the page to open"},
			"storageState": map[string]any{
				"$ref":        "#/$defs/storageState",
				"description": "Playwright storage state (cookies, localStorage) to start logged in, same shape as the script's storageState; omit for a logged-out session",
			},
			"actions": map[string]any{
				"type":        "array",
				"items":       map[string]any{"$ref": "#/$defs/step"},
				"description": "script steps to replay before the snapshot, in the script's step form (narration is ignored)",
			},
		},
		"$defs": doc.Defs,
	})
	if err != nil {
		panic(err)
	}
	return b
}

type exploreOut struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	Snapshot  string `json:"snapshot"`
	Truncated bool   `json:"truncated"`
}

func (h handlers) explorePage(ctx context.Context, _ *mcp.CallToolRequest, in exploreIn) (*mcp.CallToolResult, exploreOut, error) {
	actions, err := script.ParseSteps(in.Actions)
	if err != nil {
		return nil, exploreOut{}, err
	}
	if !script.AbsoluteHTTP(in.URL) {
		return nil, exploreOut{}, fmt.Errorf("url must be an absolute http or https URL: %s", in.URL)
	}
	out, err := h.Explorer.Explore(ctx, explorer.Input{
		StorageState: in.StorageState, URL: in.URL, Actions: actions,
	})
	var f *failure.Failure
	if errors.As(err, &f) {
		// FR-017: name the step and show the page as it stood.
		return nil, exploreOut{}, fmt.Errorf("%w\n\nPage snapshot at the failure:\n%s", f, out.Snapshot)
	}
	if err != nil {
		return nil, exploreOut{}, err
	}
	return nil, exploreOut{URL: out.URL, Title: out.Title, Snapshot: out.Snapshot, Truncated: out.Truncated}, nil
}

// ---- get_options ----

type languageOut struct {
	Code              string   `json:"code"`
	SelectedByDefault bool     `json:"selectedByDefault"`
	DefaultVoice      *string  `json:"defaultVoice"`
	Voices            []string `json:"voices"`
}

type optionsOut struct {
	Languages    []languageOut `json:"languages"`
	Audiences    []string      `json:"audiences"`
	ExistingDemo []string      `json:"existingDemos"`
}

func (h handlers) options(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, optionsOut, error) {
	catalog, err := h.Voices()
	if err != nil {
		return nil, optionsOut{}, err
	}
	langs := []languageOut{}
	for _, o := range voices.Options(catalog) {
		l := languageOut{Code: o.Code, SelectedByDefault: o.SelectedByDefault, Voices: o.Voices}
		if o.DefaultVoice != "" {
			l.DefaultVoice = &o.DefaultVoice
		}
		langs = append(langs, l)
	}
	return nil, optionsOut{
		Languages:    langs,
		Audiences:    script.Audiences,
		ExistingDemo: existingDemos(h.WorkDir),
	}, nil
}

// existingDemos is the name of every valid demos/*.yaml; invalid or unreadable
// files are skipped (FR-018).
func existingDemos(workDir string) []string {
	paths, _ := filepath.Glob(filepath.Join(workDir, demosDir, "*.yaml")) // only fails on a bad pattern
	names := []string{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if s, err := script.Parse(data); err == nil {
			names = append(names, s.Name)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// ---- create_demo prompt ----

func createDemo(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	desc := strings.TrimSpace(req.Params.Arguments["description"])
	if desc == "" {
		desc = noDescription
	}
	text := strings.ReplaceAll(createDemoPrompt, "{{description}}", desc)
	return &mcp.GetPromptResult{
		Description: "Guided creation of a narrated demo video",
		Messages:    []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: text}}},
	}, nil
}
