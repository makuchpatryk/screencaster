// Package server registers the MCP tools and the create_demo prompt
// (FR-012, FR-013, FR-017, FR-018, FR-019). It maps core results to tool
// results and holds no render logic or SQL (CODE_QUALITY, Separation).
package server

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"screencaster/core/explorer"
	"screencaster/core/failure"
	"screencaster/core/renderer"
	"screencaster/core/script"
	"screencaster/core/voices"
	"screencaster/mcp/queue"
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
	Store    *queue.Store
	Worker   *queue.Worker
	Explorer explorer.Explorer
	// Voices lists the installed Piper voices.
	Voices func() (voices.Installed, error)
	// NewID returns a fresh job ID (UUID v4).
	NewID func() string
}

type handlers struct{ Deps }

// New returns the MCP server with its four tools and one prompt.
func New(d Deps, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: version},
		&mcp.ServerOptions{Logger: slog.Default()})
	h := handlers{d}

	mcp.AddTool(s, &mcp.Tool{Name: "render_video", Description: renderDescription()}, h.renderVideo)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_render_status",
		Description: "Status of a render job: queued (with position), running, succeeded (with output paths) or failed (with the failing step).",
	}, h.renderStatus)
	mcp.AddTool(s, &mcp.Tool{
		Name: "explore_page",
		Description: "Open a page of the app in a fresh browser and return its accessibility tree. " +
			"Every interactive line ends with `-> <selector>`, a selector that is unique on the page and works unchanged in a script step. " +
			"`url` must be absolute: pass the demo's baseUrl joined with the path, and its storageState if the app needs a login. " +
			"`actions` (script steps without narration) are replayed first to reach a deeper page state; a relative goto there resolves against `url`. Every call starts from scratch. " +
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
		"- URLs are paths relative to the script's `baseUrl` (for example /projects). Only an absolute http(s) URL is used as-is.\n" +
		"- Every narrated step needs narration text for every selected language.\n" +
		"- No login steps: authentication comes from the script's optional `storageState`.\n\n" +
		"Script format (JSON Schema):\n" + string(script.SchemaJSON()) + "\n\n" +
		"Example script:\n" + string(script.ExampleYAML())
}

// ---- render_video ----

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
	// The tool input is LLM-written, so the script path is not trusted (decision 58).
	if _, err := renderer.ScriptPath(h.WorkDir, in.Script); err != nil {
		return nil, renderOut{}, err
	}
	installed, err := h.Voices()
	if err != nil {
		return nil, renderOut{}, err
	}
	plan, err := renderer.Prepare(renderer.Request{WorkDir: h.WorkDir, ScriptPath: in.Script, LangOverride: in.Languages}, installed)
	if err != nil {
		return nil, renderOut{}, err // a tool error, and no job row (FR-012)
	}
	id := h.NewID()
	pos, err := h.Store.Insert(ctx, queue.NewJob{
		ID: id, ScriptPath: in.Script, DemoName: plan.Script.Name, Languages: plan.Languages,
	})
	if err != nil {
		return nil, renderOut{}, err
	}
	h.Worker.Notify()
	return nil, renderOut{JobID: id, Status: string(queue.Queued), Position: pos}, nil
}

// ---- get_render_status ----

type statusIn struct {
	JobID string `json:"jobId" jsonschema:"id returned by render_video"`
}

type statusOut struct {
	JobID      string           `json:"jobId"`
	Status     string           `json:"status"`
	Script     string           `json:"script"`
	Position   int              `json:"position,omitempty"`
	Outputs    []queue.Output   `json:"outputs,omitempty"`
	Error      *failure.Failure `json:"error,omitempty"`
	CreatedAt  time.Time        `json:"createdAt"`
	StartedAt  *time.Time       `json:"startedAt,omitempty"`
	FinishedAt *time.Time       `json:"finishedAt,omitempty"`
}

func (h handlers) renderStatus(ctx context.Context, _ *mcp.CallToolRequest, in statusIn) (*mcp.CallToolResult, statusOut, error) {
	j, err := h.Store.Get(ctx, in.JobID)
	if errors.Is(err, queue.ErrNotFound) {
		return nil, statusOut{}, fmt.Errorf("job not found: %s", in.JobID)
	}
	if err != nil {
		return nil, statusOut{}, err
	}
	return nil, statusOut{
		JobID: j.ID, Status: string(j.Status), Script: j.ScriptPath, Position: j.Position,
		Outputs: j.Outputs, Error: j.Error,
		CreatedAt: j.CreatedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt,
	}, nil
}

// ---- explore_page ----

type exploreIn struct {
	URL          string        `json:"url" jsonschema:"absolute http(s) URL of the page to open"`
	StorageState string        `json:"storageState,omitempty" jsonschema:"Playwright storageState JSON relative to the project directory and inside it; omit for a logged-out session"`
	Actions      []script.Step `json:"actions,omitempty" jsonschema:"script steps to replay before the snapshot (narration is ignored)"`
}

type exploreOut struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	Snapshot  string `json:"snapshot"`
	Truncated bool   `json:"truncated"`
}

func (h handlers) explorePage(ctx context.Context, _ *mcp.CallToolRequest, in exploreIn) (*mcp.CallToolResult, exploreOut, error) {
	if err := script.ValidateSteps(in.Actions); err != nil {
		return nil, exploreOut{}, err
	}
	if !script.AbsoluteHTTP(in.URL) {
		return nil, exploreOut{}, fmt.Errorf("url must be an absolute http or https URL: %s", in.URL)
	}
	var storageState string
	if in.StorageState != "" {
		var err error
		if storageState, err = renderer.StorageStatePath(h.WorkDir, in.StorageState); err != nil {
			return nil, exploreOut{}, err
		}
	}
	// The url is also the base, so a relative goto in actions resolves against
	// the page being explored (BR-010, decision 59).
	out, err := h.Explorer.Explore(ctx, explorer.Input{
		BaseURL: in.URL, StorageState: storageState, URL: in.URL, Actions: in.Actions,
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
	installed, err := h.Voices()
	if err != nil {
		return nil, optionsOut{}, err
	}
	langs := []languageOut{}
	for _, o := range voices.Options(installed) {
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
