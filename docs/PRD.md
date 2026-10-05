# screencaster — PRD

**Version:** 1.3 | **Date:** 2026-10-02 | **Status:** Ready for implementation

**Related docs:** [ARCHITECTURE.md](ARCHITECTURE.md) · [CODE_QUALITY.md](CODE_QUALITY.md)

# Part I — Business

## 1. Problem & Goal
- **Who has the problem:** The user's own team, which ships its own product(s) with irregular / continuous deploys.
- **Current solution / workaround:** None. Demo videos are not produced at all because manual production (recording + voiceover + editing) costs too much.
- **Cost of the problem:** Release notes ship without video and are not read. Sales and marketing also have no demo videos.
- **Goal / target outcome:** A developer describes a demo in natural language in a Claude Code chat and receives a narrated 1080p MP4 (English by default, plus Polish when asked). When the UI changes, the demo is re-rendered from its stored script with ≤ 10 minutes of developer work.
- **Why now:** Not stated by user.

## 2. Value Proposition
- **For:** Developers on the user's team.
- **Who need:** Narrated product demo videos for release notes, sales and marketing.
- **The product:** Turns a natural-language description into a deterministic, re-renderable demo script (YAML). It then renders that script into narrated videos (English by default, Polish on request) using Playwright, local TTS and ffmpeg.
- **Unlike current solution:** Manual recording and voiceover is the only alternative, and it isn't done because of cost. Here a video costs one chat message, and a UI change costs a script fix and a re-render instead of a re-recording.

## 3. Users & Roles
| Role / Persona | Goals | Pays / Uses | Permissions |
|----------------|-------|-------------|-------------|
| Developer (single role) | Produce and re-render demo videos via Claude Code chat or CLI | Uses (internal tool, nobody pays) | Full access to all features and files in the mounted project directory |

Consumers of the videos (prospects, release-note readers, marketing audience) never interact with the tool.

## 4. Business Model & Monetization
N/A — confirmed by user. The MVP is an internal tool. A future commercial version is planned but excluded from this PRD, and no multi-tenant foundations are built.
- **Cost drivers:** $0 recurring. TTS runs locally (Piper), and Claude Code usage is covered by the user's existing plan.

## 5. Business Rules

### BR-001: Deterministic rendering
- **Rule:** A render must replay the stored YAML script exactly. No LLM may be called during rendering.
- **Applies to:** Every render (CLI and MCP).
- **Exceptions:** None.

### BR-002: Language selection
- **Rule:** A render must produce one MP4 per selected language. English (`en`) is the default; any other language is rendered only when explicitly selected. A language is supported when a Piper voice for it is installed (EN and PL voices are built in; others are added per FR-016). The selection is, in priority order: the render-time override (CLI `--lang`, MCP `languages` parameter), then the script's `languages` field, then `["en"]`. Every narrated step must have narration text for every selected language.
- **Applies to:** Scripts, renders.
- **Exceptions:** None.

### BR-003: Narration–action synchronization
- **Rule:** For a narrated step, the narration clip must start at the same moment the step's action starts. The next step must start only after the later of two events: the clip ending, or the action completing. An un-narrated step must let the next step start as soon as its action completes.
- **Applies to:** Step execution.
- **Exceptions:** None.

### BR-004: Abort on step failure
- **Rule:** If any step's action does not complete within 30 seconds (e.g. selector not found), the whole job must abort immediately. No MP4 may be written for any language, and the failure must report the step index (1-based), the action, the selector/URL and the underlying error message.
- **Applies to:** Every render.
- **Exceptions:** None.

### BR-005: No review gate
- **Rule:** A successfully rendered MP4 is final. No approval step exists. The developer reviews by watching the video and requests changes via chat.
- **Applies to:** Outputs.
- **Exceptions:** None.

### BR-006: Outputs are never overwritten
- **Rule:** Every render must write new files named `<demo>.<lang>.<timestamp>.mp4`. Existing files must not be overwritten or deleted by the tool.
- **Applies to:** Output folder.
- **Exceptions:** None.

### BR-007: Unlimited retention
- **Rule:** The tool must keep all MP4s and all job records forever. Cleanup is manual and outside the tool.
- **Applies to:** Outputs, job records.
- **Exceptions:** None.

### BR-008: Sequential rendering
- **Rule:** Only one job may render at a time. Additional `render_video` requests must be queued and run in FIFO order.
- **Applies to:** MCP server.
- **Exceptions:** None.

### BR-009: Interrupted jobs
- **Rule:** When the MCP server starts, every job with status `queued` or `running` must be set to `failed` with error message `interrupted`. Jobs must not resume automatically.
- **Applies to:** Job records.
- **Exceptions:** None.

### BR-010: Self-contained demo file
- **Rule:** A demo is one self-contained YAML file. Its steps must use URL paths relative to the script's own `baseUrl`. Authentication must come only from the Playwright storage state in the script's optional inline `storageState`. Scripts must not contain login steps. Retargeting a demo at another environment means editing its `baseUrl`.
- **Applies to:** Scripts.
- **Exceptions:** A `goto` step with an absolute URL (`http://` or `https://`) is used as-is.

### BR-011: Voice selection
- **Rule:** The Piper voice for each selected language must be resolved in priority order: script `voices.<lang>`, then the built-in default (only `en` → `en_US-ryan-high` and `pl` → `pl_PL-darkman-medium` have one). A selected language with no resolved voice, or with a resolved voice that is not installed, must fail validation before any browser or TTS work starts.
- **Applies to:** Scripts, renders.
- **Exceptions:** None.

## 6. Business Processes

### BP-001: Create a demo via chat
1. Developer writes a natural-language description in Claude Code (e.g. "show how to create a project and invite a user").
2. Claude Code explores the running app with the `explore_page` tool (UF-003) to find real selectors.
3. Claude Code writes `demos/<demo>.yaml` following the schema embedded in the `render_video` tool description. If the developer asked for Polish, it sets `languages: [en, pl]`.
4. Claude Code calls `render_video` with the script path (optionally `languages`) and receives a job ID, or a validation error to fix and retry.
5. Claude Code polls `get_render_status` until the status is `succeeded` or `failed`.
6. On `succeeded`, Claude Code reports the MP4 path(s). On `failed`, it reports the failing step, fixes the YAML and re-submits.
- **Statuses & allowed transitions:** `queued → running → succeeded`, `queued → running → failed`, `queued → failed` (interrupted), `running → failed` (interrupted).
- **Exceptions:** Container stops mid-job → BR-009.
- **Related rules:** BR-001…BR-010.

### BP-002: Re-render after a UI change
1. Developer asks Claude Code to re-render `demos/<demo>.yaml`, or runs the CLI.
2. If the render fails at a step, Claude Code updates only the affected step(s) and re-submits.
3. New timestamped MP4s are written next to the old ones.
- **Related rules:** BR-004, BR-006.

### BP-003: Render via CLI
1. Developer runs `screencaster render demos/<demo>.yaml` inside the Docker image (add `--lang en,pl` to include Polish).
2. The CLI renders synchronously (no queue, no job record), prints progress per step, and exits 0 with the output path(s), or exits 1 with the failure report from BR-004.
- **Related rules:** BR-001…BR-004, BR-006, BR-010.

### BP-004: Guided creation via slash command
1. Developer runs `/mcp__screencaster__create_demo [description]` in Claude Code.
2. Claude Code calls `get_options` to learn the installed voices, defaults and existing demo names.
3. Claude Code asks the developer, in a single message, only what the description does not already answer: languages, voice per selected language, audience, title.
4. Claude Code explores the app (`explore_page`), writes the YAML with the answers (`languages`, `voices`, `meta`), and runs BP-001 steps 4–6.
- **Exceptions:** No description given → Claude Code first asks what the demo should show. No approval of the YAML is requested (BR-005).
- **Related rules:** BR-002, BR-005, BR-011.

## 7. Assumptions & Risks
### Assumptions (stated by user)
None stated.
### Risks
None identified by user.

## 8. Success Metrics
| KPI | Target | Timeframe | Measurement |
|-----|--------|-----------|-------------|
| Developer work to re-render a demo after a UI change | ≤ 10 minutes | Per occurrence, from MVP release | Time from starting the fix to a successful render, self-reported |

## 9. Constraints
- Built and maintained by 1 developer. No deadline.
- TTS/AI API budget: $0 (local Piper).
- Rendering runs on Linux, inside Docker.
- Volume target: ≤ 10 videos/month. Do not design for higher scale.
- The engine must be environment-agnostic (BR-010).

# Part II — Product

## 10. Scope
### 10.1 MVP
- CLI render: YAML script → one MP4 per selected language (EN default; PL voice built in; other languages via extra voice files).
- MCP server with `render_video`, `get_render_status`, `explore_page` and `get_options`, plus the slash command `/mcp__screencaster__create_demo` (an MCP prompt), used from Claude Code.
- Step actions: `goto`, `click`, `fill`, `select`, `press`, `hover`, `scroll`, `wait`.
- Animated visible cursor and slowed-down typing.
- Piper TTS (EN and PL voices built in, other languages via extra voice files), narration synchronized per BR-003.
- SQLite-backed FIFO job queue.
- Docker image containing everything.
- A start card and an end card on every video (built in, replaceable by the developer's own picture, or off).
- Render log lines on stderr: start summary, one line per phase, end summary.

### 10.2 Post-MVP
- Slides interleaved between browser segments (the start and end cards are MVP, decision 63).
- Overlays (captions, callouts, element highlights).
- Commercial / multi-tenant version.

### 10.3 Out of Scope
- A schema/validation-only MCP tool or CLI command.
- Login steps in scripts, sensitive-data masking.
- Formats other than 16:9 1920×1080 MP4 (no vertical, no square).
- Multi-audio-track MP4s; languages without a Piper voice; automatic translation by the tool itself (Claude writes the narration text).
- Cloud TTS providers, voice cloning, human voiceover.
- CI-triggered or automatic re-renders.
- Web UI, user accounts, approval workflows.
- Automatic deletion of outputs.

## 11. User Flows

### UF-001: Chat → video (MCP)
1. Developer: "Make a demo of creating a project."
2. Claude Code explores the app with `explore_page`, writes `demos/create-project.yaml`, and calls `render_video({ "script": "demos/create-project.yaml" })`.
3. Server validates the script, inserts a job with status `queued`, and returns `{ "jobId": "<uuid>", "status": "queued", "position": <n> }`.
4. Claude Code polls `get_render_status({ "jobId": "<uuid>" })`.
5. Server returns `succeeded` with output paths and durations.
- **Error states:** Script file missing or schema invalid → `render_video` returns an MCP tool error listing every validation error (JSON pointer + message), and no job is created. Step failure → status `failed` with the error details from BR-004. Unknown job ID → tool error `job not found: <id>`.
- **Empty states:** `get_render_status` for a `queued` job returns `position` (1 = next to run).

### UF-002: CLI render
1. `screencaster render demos/create-project.yaml`
2. Progress output: `[en] step 3/12 click role=button[name="New project"]`.
3. Success: prints the absolute output path(s) and exits 0.
- **Error states:** Validation errors → printed and exits 1. Step failure → BR-004 report and exits 1. A `screencaster.yaml` in the working directory is not read; a warning `screencaster.yaml is ignored; move its fields into the demo script` is printed to stderr.

### UF-003: Explore a page
1. Claude Code calls `explore_page({ "url": "http://host.docker.internal:3000/projects" })`, plus `storageState` when the app needs a login.
2. Server returns the page title and an accessibility snapshot with a ready-to-use selector for each interactive element.
3. For pages deeper in a flow, Claude Code calls it again with `actions` that reach them (e.g. click "New project") and receives the snapshot of the resulting page.
- **Error states:** An action fails or times out → tool error naming the step, plus the snapshot of the page at that moment. A relative `url` → tool error naming the rule.
- **Empty states:** Page with no accessible elements → empty snapshot, no error.

### UF-004: Guided creation
1. Developer types `/mcp__screencaster__create_demo invite a user to a project`.
2. Claude Code calls `get_options`, then asks one message of questions, each with a marked default:
   - languages (EN selected; any language with an installed voice is optional),
   - voice for each selected language (installed voices),
   - audience (release notes / sales / marketing),
   - title (proposed from the description),
   - the app's base URL and, if it needs a login, the storage state (cookies and localStorage).
3. Developer answers, or replies "defaults" to accept all.
4. Claude Code explores the app, writes the script, renders, and reports the MP4 path(s).
- **Error states:** No voice installed for a requested language → Claude Code reports it and offers the languages that have voices.
- **Empty states:** No description passed → Claude Code asks what the demo should show before the other questions.

## 12. Functional Requirements

### FR-001: Demo target settings
- **Implements:** BR-010, Constraints
- **Description:** The system must read the target app, the login and the output location from the demo script itself. There is no project-level config file; a `screencaster.yaml` is never read.
- **Inputs / validation:** Top-level script fields:
  - `baseUrl` (string, required, absolute http/https URL; e.g. `http://host.docker.internal:3000`)
  - `storageState` (object, optional; Playwright's storage state: `cookies` and `origins` with `localStorage`, defined in the schema; omit for a fresh, logged-out session; it holds secrets, so the script stays out of git)
  - `outputDir` (string, optional, relative to the demo file's folder and inside the working dir, default `output`, so the videos of `demos/x.yaml` land in `demos/output/`)
  - `intro`, `outro` (optional; `false` for no card, or an object `{image?, title?, subtitle?, durationMs?}`; see FR-009). `image` is a path to a PNG or JPEG, relative to the demo file's folder and inside the working dir
  - `voices` (object, optional; keys are language codes, values are Piper voice names; built-in defaults: `en` → `en_US-ryan-high`, `pl` → `pl_PL-darkman-medium`)
- **Edge cases:** A voice not present in the image → validation error `voice not installed: <name>`. Only voices of selected languages are checked. A relative `baseUrl` → `/baseUrl: baseUrl must be an absolute http or https URL: <value>`. A `storageState` that is not an object of that shape (a path string, a cookie with neither `url` nor `domain` and `path`) → a schema validation error at `/storageState...`, reported together with the script and voice errors. An `outputDir` that resolves outside the working dir (`..` past it or an absolute path elsewhere) → `/outputDir: outputDir must stay inside the working directory: <value>`, because the script is written by an LLM; so is a script outside the working dir whose default `output` folder would be outside too. An `intro` or `outro` `image` follows the same rule (`/intro/image: intro.image must stay inside the working directory: <value>`), must exist (`/intro/image: image not found: <absolute path>`) and must be a regular file with a `.png`, `.jpg` or `.jpeg` name whose first bytes are those of a PNG or JPEG (`/intro/image: image is not a PNG or JPEG: <absolute path>`); `image` together with `title` or `subtitle`, `intro: true` and a `durationMs` outside 500–10000 are schema errors. All of it is checked before any browser or TTS work. If a `screencaster.yaml` exists in the working directory, the CLI prints `screencaster.yaml is ignored; move its fields into the demo script` to stderr on every render and the MCP server logs it once at startup.
- **Acceptance criteria:**
  - Given a script without `baseUrl`, when any render starts, then it fails schema validation (`/baseUrl`) before launching a browser.
  - Given a script without `voices`, when rendering `en`, then the audio uses `en_US-ryan-high`; when rendering `pl`, then it uses `pl_PL-darkman-medium`.
  - Given a work dir that holds only the demo file, when a public site is rendered, then it succeeds with no storageState.
  - Given `demos/x.yaml` with no `outputDir`, when it renders, then the MP4 is in `demos/output/`.

### FR-002: Script schema & validation
- **Implements:** BR-001, BR-002, BR-010
- **Description:** The system must parse the YAML script and validate it against a JSON Schema file (`core/script/script.schema.json`, embedded with `go:embed` in `core/script` and so compiled into both binaries) using `santhosh-tekuri/jsonschema`, before any browser or TTS work.
- **Inputs / validation:**
  - `name` (string, required, `^[a-z0-9-]{1,64}$`; used in output filenames)
  - `baseUrl` (string, required), `storageState` (object, optional), `outputDir` (string, optional): see FR-001
  - `languages` (array, optional, lowercase ISO 639-1 codes such as `en`, `pl`, `de`; default `["en"]`; overridable per render, BR-002)
  - `voices` (object, optional, keys are language codes, values Piper voice names; installed check per BR-011)
  - `meta` (object, optional: `title` ≤ 120 chars, `description` ≤ 500 chars, `audience` one of `release-notes | sales | marketing`)
  - `steps` (array, required, 1–200 items)
  - Each step:
    - `action` (required, one of `goto | click | fill | select | press | hover | scroll | wait`)
    - `narration` (optional object keyed by language code (`en`, `pl`, `de`, …), each a string ≤ 1000 chars, where an empty string keeps the step silent in that language; must contain an entry for every selected language; text for unselected languages is allowed and ignored; this cross-field rule is checked in code after schema validation)
    - Action-specific fields:
      - `goto`: `url` (required)
      - `click`, `hover`: `selector` (required)
      - `fill`: `selector` + `value` (required)
      - `select`: `selector` + `value` (required)
      - `press`: `key` (required), `selector` (optional)
      - `scroll`: exactly one of `selector` or `y` (integer px)
      - `wait`: exactly one of `ms` (integer 1–30000) or `selector`
  - Selectors are Playwright selector strings (CSS, `role=`, `text=`).
- **Edge cases:** Unknown fields → invalid. Empty `steps` → invalid.
- **Acceptance criteria:**
  - Given `languages: [en, pl]` and a step with `narration.en` but no `narration.pl`, when validating, then validation fails with a JSON pointer to that step.
  - Given no `languages` field and a step with only `narration.en`, when validating, then the script is valid.
  - Given a valid script, when validating, then no errors are returned.

### FR-003: TTS generation
- **Implements:** BR-002, BR-003
- **Description:** Before recording a language, the system must generate one WAV clip per narrated step with the Piper binary and that language's voice, and measure each clip's duration in milliseconds.
- **Edge cases:** Piper exits non-zero → job fails with `tts failed at step <n> (<lang>): <stderr>`.
- **Acceptance criteria:**
  - Given a script with 5 narrated steps, when rendering `pl`, then 5 WAV clips are generated with `pl_PL-darkman-medium` before the browser launches.

### FR-004: Browser recording
- **Implements:** Goal, NFR-002
- **Description:** For each selected language, the system must launch Chromium via playwright-go with a new context using:
  - viewport 1920×1080
  - `recordVideo` with size 1920×1080
  - `storageState` from the script, passed inline (omitted: no stored session)
  - `baseURL` from the script's `baseUrl`

  Each selected language is a separate recording, run sequentially in the order of `languages` (EN first by default), because narration lengths differ.
- **Acceptance criteria:**
  - Given a script with `languages: [en, pl]`, when rendering, then two independent browser recordings are made, each starting from a fresh context.
  - Given the default languages, when rendering, then one recording is made.

### FR-005: Step execution
- **Implements:** BR-003, BR-004, BR-010
- **Description:** The system must execute the steps in order with these semantics:
  - `goto`: navigate (relative URLs resolved against `baseUrl`) and wait for `load`.
  - `click`: click the element.
  - `fill`: type `value` into the element (FR-006).
  - `select`: select the option by value or label.
  - `press`: press the key, focused on `selector` if given.
  - `hover`: hover the element.
  - `scroll`: scroll the element into view, or scroll the window to `y`.
  - `wait`: sleep `ms`, or wait until `selector` is visible.
- **Acceptance criteria:**
  - Given `goto` with url `/projects` and baseUrl `http://host.docker.internal:3000`, when executed, then the page URL is `http://host.docker.internal:3000/projects`.

### FR-006: Human-like visuals
- **Implements:** Goal (demo quality)
- **Description:** The system must inject (via an init script) a visible cursor element that follows mouse movement. Before every `click`, `hover`, `fill` and `select`, the mouse must move to the element's center in 25 interpolated steps. `fill` must type character by character with a 60 ms delay per character.
- **Acceptance criteria:**
  - Given a `click` step, when recorded, then the video shows the cursor moving to the element before the click.

### FR-007: Narration synchronization
- **Implements:** BR-003
- **Description:** For each step, the system must record the step start offset in ms relative to the recording start, then run the action. For a narrated step it must wait until `max(action end, start + clip duration)` before the next step.
- **Acceptance criteria:**
  - Given a narrated step with a 4000 ms clip and an action taking 500 ms, when executed, then the next step starts 4000 ms (±100 ms) after this step started.
  - Given an un-narrated step, when its action completes, then the next step starts immediately.

### FR-008: Step timeout & abort
- **Implements:** BR-004
- **Description:** Every action must use a 30 000 ms timeout. On timeout or error, the system must:
  - stop the job,
  - close the browser,
  - delete the job's temp files,
  - write no MP4 for any language,
  - produce the error object `{ step, lang, action, target, message }`.
- **Acceptance criteria:**
  - Given step 4 clicks a non-existent selector, when rendering `en`, then the job fails within 31 s with `step: 4, lang: "en"`, and the output folder is unchanged.

### FR-009: Audio/video assembly
- **Implements:** BR-002, NFR-002
- **Description:** For each selected language, the system must use ffmpeg to:
  1. place every clip at its recorded step offset (`adelay`) and mix them into one track;
  2. transcode the recording to H.264 MP4, 30 fps, 1920×1080, AAC audio;
  3. mux them together;
  4. set the output duration to the video length;
  5. put a start card before the recording and an end card after it (unless `false`), each shown for `durationMs` (default 3000 ms), scaled to fit 1920×1080 at 30 fps. The built-in card is an HTML page drawn by Chromium: the `intro` shows `meta.title` (or `name`) and `meta.description`, the `outro` shows a closing line in the language of the video (`en`: Thank you for watching, `pl`: Dziękujemy za uwagę, other languages: English) and the title. `title` and `subtitle` replace the text; `image` shows the picture instead, on a dark background where it does not fill the frame. Clip offsets are placed after the start card; the cards carry no narration;
  6. write `meta.title` and `meta.description` as the MP4 `title` and `comment` tags when present.
- **Edge cases:** ffmpeg exits non-zero → job fails with `assembly failed (<lang>): <stderr last 20 lines>`. The card page cannot be drawn → the job fails with `build cards (<lang>): <message>` before the recording starts.
- **Acceptance criteria:**
  - Given a successful recording, when assembled, then `ffprobe` reports h264, 1920×1080, 30 fps, and an aac stream.
  - Given a script with no `intro` or `outro`, when rendered, then the video lasts the recording plus 6 s and its first and last 3 s are the built-in cards.
  - Given `intro: {image: assets/logo.png}` and `outro: false`, when rendered, then the first 3 s show that picture and the video ends with the recording.

### FR-010: Output naming
- **Implements:** BR-006, BR-007
- **Description:** Outputs must be written to `<outputDir>/<name>.<lang>.<timestamp>.mp4`, with `outputDir` resolved against the demo's folder (FR-001). `timestamp` is the job start time in UTC, format `YYYYMMDDTHHMMSSZ`, and is shared by all files of one job. Files are written to a temp path and moved into place only after all selected languages succeed.
- **Acceptance criteria:**
  - Given two default renders of `create-project`, when both succeed, then two EN files with different timestamps exist and none were overwritten.

### FR-011: CLI
- **Implements:** BP-003
- **Description:** The `screencaster` binary must provide `screencaster render <script-path> [--lang en,pl]` (`--lang` overrides the script's `languages`), which runs FR-001…FR-010 synchronously without touching the job queue. Exit code 0 on success, 1 on any failure. It prints on stderr a start summary (script, languages, voices, step count, output dir), the step progress, one line per phase and language with its elapsed time (narration, cards, recording, assembly) and an end summary (output paths, video durations, total time); a failed render prints `render failed after <time>` and then the error once. The paths of the videos go to stdout. The MCP server writes the same lines to its stderr log with the job id.
- **Acceptance criteria:**
  - Given a valid script, when `screencaster render` runs, then it prints the output path(s) and exits 0.

### FR-012: MCP tool `render_video`
- **Implements:** BP-001, BR-008
- **Description:** The `screencaster-mcp` binary (stdio transport, official `modelcontextprotocol/go-sdk`) must expose `render_video`.
  - **Input:** `{ "script": string, "languages"?: string[] }` (language codes); `script` is a path relative to the working dir (a path that resolves outside it → tool error `script must stay inside the working directory: <value>`), and `languages` overrides the script's `languages` (BR-002).
  - **Validation:** It must validate the script, its voices and its storageState (FR-001, FR-002) synchronously.
  - **On success:** It must insert a `queued` job and return `{ jobId, status: "queued", position }`.
  - **Tool description:** It must contain the full JSON Schema of the script format, an example script, and these rules: relative URLs only, narration text required for every selected language (`languages`, default `["en"]`), no login steps.
- **Acceptance criteria:**
  - Given an invalid script, when called, then a tool error lists all validation errors and no job row is created.
  - Given a job already running, when called with a valid script, then the new job is `queued` with `position: 1`.

### FR-013: MCP tool `get_render_status`
- **Implements:** BP-001
- **Description:** Input `{ "jobId": string }`. The tool must return `{ jobId, status, script, position?, outputs?, error?, createdAt, startedAt?, finishedAt? }`:
  - `outputs` is `[{ lang, path, durationMs }]` when `succeeded`.
  - `error` follows FR-008 when `failed`.
  - `position` is present when `queued`.
- **Acceptance criteria:**
  - Given an unknown jobId, when called, then a tool error `job not found: <id>` is returned.

### FR-014: Job queue
- **Implements:** BR-008
- **Description:** A single worker goroutine must take the oldest `queued` job, set it `running`, render it, and set it `succeeded` or `failed`. Job state is persisted in SQLite at `<working dir>/.screencaster/jobs.db`. Before calling the renderer, the worker must acquire an exclusive lock on `.screencaster/render.lock` to prevent a concurrent CLI render (Decision 45).
- **Acceptance criteria:**
  - Given three jobs submitted back-to-back, when processed, then they run strictly one at a time in submission order.
  - Given a CLI render holding the render lock, when a worker dequeues a job, then the job stays `queued` until the lock is released.

### FR-015: Startup recovery
- **Implements:** BR-009
- **Description:** On MCP server start, before accepting tool calls, the system must update all `queued` and `running` jobs to `failed` with error message `interrupted`.
- **Acceptance criteria:**
  - Given a job left in `running`, when the server starts, then its status is `failed` and its message is `interrupted`.

### FR-016: Docker image
- **Implements:** Constraints
- **Description:** One image must contain both binaries and their runtime dependencies:
  - binaries: `screencaster`, `screencaster-mcp`
  - playwright-go driver (Node) and Chromium with system deps
  - Piper with voices `en_US-ryan-high` and `pl_PL-darkman-medium` (extra voices can be added as `.onnx` + `.onnx.json` files in `/work/voices`)
  - ffmpeg/ffprobe

  The working dir is `/work`.

  The README must document this Claude Code `.mcp.json` entry:
  - command: `docker run -i --rm --add-host=host.docker.internal:host-gateway -v <project>:/work screencaster screencaster-mcp`
- **Acceptance criteria:**
  - Given the image, when started with the command above from Claude Code, then `render_video`, `get_render_status`, `explore_page` and `get_options` are listed as tools, and the prompt `create_demo` is listed.
  - Given a target app on host port 3000 and baseUrl `http://host.docker.internal:3000`, when rendering, then pages load.

### FR-017: MCP tool `explore_page`
- **Implements:** BP-001, BR-010
- **Description:** The `screencaster-mcp` server must expose `explore_page` so Claude Code can discover real selectors before writing a script.
  - **Input:** `{ "url": string, "storageState"?: object, "actions"?: Step[] }`. `url` must be an absolute http(s) URL; the caller passes the demo's `baseUrl` joined with the path. `storageState` has the same inline shape as the script's. `actions` use the script step schema without `narration` (FR-002). The `url` is also the base for relative `goto` URLs inside `actions` (BR-010).
  - **Behavior:** Launches a fresh, non-recorded Chromium context (1920×1080, the given storageState or none), navigates to `url`, runs `actions` in order with the FR-005 semantics and the 30 s timeout (no cursor animation, no typing delay), closes the browser, and returns the final page state.
  - **Output:** `{ url, title, snapshot, truncated }`. `snapshot` is the page's accessibility tree as indented text. Each interactive element line includes its role, accessible name and a ready-to-use Playwright selector (e.g. `role=button[name="New project"]`). Capped at 50 000 characters (`truncated: true` if cut).
  - **Stateless:** Every call starts from a fresh context. To inspect a page deeper in a flow, the caller passes the `actions` that reach it.
  - **Independence:** Runs immediately, outside the render queue. Concurrent explore calls run one at a time.
- **Edge cases:** A relative `url` → tool error `url must be an absolute http or https URL: <url>`. An action fails or times out → tool error with step index, action, target and message (as FR-008), plus the snapshot of the page at the failure. A page with no accessible elements → empty snapshot, no error.
- **Acceptance criteria:**
  - Given the fixture app with a "New project" button, when `explore_page` is called with the fixture's absolute `/projects` URL, then the snapshot contains `role=button[name="New project"]`.
  - Given `actions` that click a missing selector, when called, then a tool error names that step and includes the snapshot.
  - Given a selector returned by `explore_page`, when used in a script step, then the render executes that step successfully.

### FR-018: MCP tool `get_options`
- **Implements:** BP-004, BR-011
- **Description:** The `screencaster-mcp` server must expose `get_options` (no input) so Claude Code can offer real choices.
  - **Output:** `{ languages, audiences, existingDemos }`:
    - `languages` is `[{ code, selectedByDefault, defaultVoice, voices }]` for `en`, `pl` and every other language that has at least one installed voice. A voice's language code is the part of its name before the first `_` (`pl_PL-darkman-medium` → `pl`). `voices` lists the Piper voices for that language found in the image and in `/work/voices`. `defaultVoice` is resolved per BR-011 (built-in only), or `null` if none.
    - `audiences` is `["release-notes", "sales", "marketing"]`.
    - `existingDemos` is the `name` of every valid `demos/*.yaml` (invalid files are skipped).
- **Edge cases:** `en` and `pl` always appear; one with no installed voice has `voices: []`.
- **Acceptance criteria:**
  - Given the stock image, when called, then `en` lists `en_US-ryan-high` as `defaultVoice`, `pl` lists `pl_PL-darkman-medium`, and only `en` has `selectedByDefault: true`.
  - Given an extra voice file `pl_PL-gosia-medium.onnx` in `/work/voices`, when called, then `pl.voices` includes `pl_PL-gosia-medium`.
  - Given a voice file `de_DE-thorsten-medium.onnx` in `/work/voices`, when called, then `languages` includes `de` with that voice and `defaultVoice: null`.

### FR-019: MCP prompt `create_demo`
- **Implements:** BP-004
- **Description:** The `screencaster-mcp` server must expose an MCP prompt named `create_demo` (shown in Claude Code as `/mcp__screencaster__create_demo`) with one optional string argument `description`. The prompt text must instruct Claude Code to:
  1. call `get_options`;
  2. ask the developer in one message, skipping anything the description already answers: languages (default EN), a voice for each selected language (from `get_options`, default marked; a required choice when `defaultVoice` is `null`), audience, and a title (proposed from the description). Replying "defaults" accepts all defaults;
  3. explore the app with `explore_page`;
  4. write `demos/<name>.yaml` with `languages`, `voices` (only when different from the default) and `meta`, choosing a `name` not in `existingDemos`;
  5. call `render_video`, poll `get_render_status`, and report the output paths;
  6. not ask for approval of the YAML before rendering (BR-005);
  7. ask what the demo should show if no description was given.
- **Acceptance criteria:**
  - Given `prompts/get` for `create_demo` with `description: "invite a user"`, when called, then the returned messages include the description and instructions 1–7.
  - Given `prompts/list`, when called, then `create_demo` is listed.

# Part III — Technical

## 13. Data Model

### Job (SQLite table `jobs`)
| Field | Type | Required | Constraints | Notes |
|-------|------|----------|-------------|-------|
| id | TEXT | yes | UUID v4, PK | |
| script_path | TEXT | yes | relative to working dir | |
| demo_name | TEXT | yes | from script `name` | |
| languages | TEXT | yes | JSON array of language codes | resolved per BR-002 |
| status | TEXT | yes | `queued` \| `running` \| `succeeded` \| `failed` | |
| created_at | TEXT | yes | RFC 3339 UTC | FIFO order key |
| started_at | TEXT | no | RFC 3339 UTC | |
| finished_at | TEXT | no | RFC 3339 UTC | |
| outputs_json | TEXT | no | JSON `[{lang,path,durationMs}]` | set on success |
| error_json | TEXT | no | JSON `{step,lang,action,target,message}` | set on failure; `interrupted` uses `{message:"interrupted"}` |

### Files (no DB)
- `demos/*.yaml` (scripts, each with its own target settings), `<outputDir>/*.mp4` (default `demos/output/` for `demos/x.yaml`).
- **Relations:** One job → zero or more MP4 files (one per selected language).
- **Retention / deletion:** Forever; manual deletion only (BR-007).

## 14. Auth & Access Control
N/A — confirmed by user (local single-user tool). Target-app auth is storageState only (BR-010).

## 15. Integrations
| Service | Purpose | Data exchanged | Failure handling |
|---------|---------|----------------|------------------|
| Claude Code (MCP client) | Authoring via chat, triggering renders | MCP stdio tool calls | Standard MCP tool errors |
| Piper (local binary) | TTS | Text in, WAV out | FR-003 |
| ffmpeg / ffprobe (local) | Transcode, mux, durations | Media files | FR-009 |
| Target app (user's product) | Recorded subject | HTTP via Chromium | FR-008 |

## 16. Platforms & UI
- **Platforms:** Linux host, Docker. Interfaces: CLI and MCP (stdio). No GUI.
- **Responsiveness:** N/A — confirmed by user.
- **Languages:** Tool messages in English. Narration: any language with an installed Piper voice (EN and PL built in).
- **Accessibility:** N/A — confirmed by user.
- **Design reference:** N/A.

## 17. Non-Functional Requirements

### NFR-001: Render time
- **Requirement:** Total job time must be ≤ 2× the combined duration of all output videos (all selected languages), measured on a Linux Docker host.
- **Verification:** The e2e test logs job time and video durations and asserts the ratio.

### NFR-002: Video quality
- **Requirement:** 1920×1080, 30 fps, H.264 + AAC, MP4. Source is Playwright `recordVideo` (accepted trade-off: lower bitrate than CDP screencast).
- **Verification:** ffprobe assertion in the e2e test.

### NFR-003: Offline TTS
- **Requirement:** Rendering must make no network requests other than to the target app.
- **Verification:** Code review; no HTTP clients besides Chromium.

### NFR-004: Compliance / availability
- N/A — confirmed by user.

## 18. Tech Stack
| Layer | Choice | Version (if fixed) |
|-------|--------|--------------------|
| Language | Go | latest stable |
| Browser automation | playwright-go (community; bundles the Node driver) + Chromium | — |
| MCP | modelcontextprotocol/go-sdk, stdio | — |
| Script validation | JSON Schema + santhosh-tekuri/jsonschema; YAML parser | — |
| Database | SQLite via modernc.org/sqlite (no cgo) | — |
| TTS | Piper | voices en_US-ryan-high, pl_PL-darkman-medium |
| Media | ffmpeg, ffprobe | — |
| Hosting / Deployment | Docker image, run locally | — |
| CI/CD | GitHub Actions: golangci-lint, go vet, go test, docker build (from M4) | — |
| Testing | `go test` unit tests + 1 e2e render against a fixture HTML app (local, `make e2e`; not in CI) | — |

- **Repo structure:** Go workspace (`go.work`, committed) with four modules:
  - `core/` (script + `script.schema.json`, voices, TTS, recorder, assembler, renderer, explorer)
  - `cli/` (`screencaster`)
  - `mcp/` (`screencaster-mcp`, queue, SQLite)
  - `tests/e2e/` (end-to-end tests, run locally with `make e2e`)

  Also at the repo root: `testdata/` (fixture app, sample scripts), `Dockerfile`, `Makefile`, `.golangci.yml`.

## 19. Implementation Plan

### M1: Skeleton, config, schema
- **Includes:** FR-001, FR-002; go.work layout; CI.
- **Depends on:** —
- **Definition of done:** Valid and invalid sample scripts pass and fail as specified, and CI is green.

### M2: Recording without audio
- **Includes:** FR-004, FR-005, FR-006, FR-008.
- **Depends on:** M1
- **Definition of done:** A fixture-app script records a silent WebM with a visible cursor, and a missing selector aborts at the right step within 31 s.

### M3: TTS, sync, assembly, CLI
- **Includes:** FR-003, FR-007, FR-009, FR-010, FR-011.
- **Depends on:** M2
- **Definition of done:** `screencaster render` produces an EN MP4 by default and EN + PL MP4s with `--lang en,pl`, all passing the ffprobe assertion; the e2e test passes and NFR-001 is met.

### M4: Docker image
- **Includes:** FR-016 (image part).
- **Depends on:** M3
- **Definition of done:** The CLI e2e test passes inside the image, and a host app is reachable via host.docker.internal.

### M5: MCP server & queue
- **Includes:** FR-012, FR-013, FR-014, FR-015, FR-016 (README / `.mcp.json`), FR-017, FR-018, FR-019.
- **Depends on:** M4
- **Definition of done:** From Claude Code, a natural-language request produces an EN MP4, and asking for Polish adds a PL MP4. `explore_page` returns usable selectors, `/mcp__screencaster__create_demo` asks for languages and voices before rendering, three queued jobs run sequentially, and restart recovery is verified.

# Appendix

## 20. Decisions Log
| # | Decision | Alternatives considered | Rationale |
|---|----------|-------------------------|-----------|
| 1 | Internal tool now, sell later; commercial aspects N/A | Commercial from day one, agency | User choice |
| 2 | Demos of own products only | Clients' products | User choice |
| 3 | Audiences: release notes, sales, marketing | Onboarding/docs | User choice |
| 4 | Manual re-render trigger | CI, auto on breakage | User choice |
| 5 | Volume target ≤ 10/month | ≤ 50, 100+ | User choice; release cadence irregular |
| 6 | Developers author; AI (Claude Code) drafts from NL | Manual only, AI narration only | User choice |
| 7 | NL is input, YAML is the stored deterministic script | AI interprets NL on every render | Deterministic, cheap re-renders |
| 8 | No manual review; feedback via chat (supersedes "devs review the script") | Approve YAML before render | User choice |
| 9 | Piper local TTS | Google/Azure free tier, OpenAI, ElevenLabs | $0, offline; user accepted lower quality |
| 10 | Voices en_US-ryan-high, pl_PL-darkman-medium | Other Piper voices | User choice |
| 11 | EN by default, PL only on request; one script → one MP4 per selected language (updated v1.1) | Always both, separate scripts, multi-track | User choice |
| 12 | 16:9 1080p30 MP4 only | Vertical, square, 60 fps | User choice |
| 13 | Slides and overlays post-MVP | In MVP | User choice |
| 14 | Abort on step failure, 30 s timeout | Retry, skip | User choice |
| 15 | Configurable baseUrl + storageState in a per-project config (superseded by 58) | Fixed env, login steps | User choice |
| 16 | No sensitive-data masking in MVP | Masking | User choice |
| 17 | Actions goto/click/fill/select/press/hover/scroll/wait | Subset | User choice |
| 18 | Narration starts with action; next step waits for clip | Action first, configurable | User choice |
| 19 | Animated cursor + slow typing | Raw speed | User choice |
| 20 | Files for scripts/outputs; SQLite only for job state | Files only, in-memory, JSON, Redis | User choice; Redis rejected as extra service |
| 21 | Keep every render, timestamped, forever | Overwrite, last 5/10 | User choice |
| 22 | Render time ≤ 2× video length | ≤ 5 min, none | User choice |
| 23 | MCP exposes render_video, get_render_status and explore_page (updated v1.1; replaces reliance on Claude Code's Playwright MCP) | External Playwright MCP, separate validate/schema tools | User choice; the explorer shares the step executor, storageState and Docker network path with renders, so found selectors work in renders |
| 24 | Async jobs, FIFO sequential queue | Sync, reject concurrent, parallel | User choice |
| 25 | Interrupted jobs → failed | Auto-resume | User choice |
| 26 | MCP server in Docker via `docker run -i`, project mounted at /work | Host install | User choice |
| 27 | host.docker.internal in baseUrl (+ host-gateway flag on Linux) | --network host, remote only | User choice |
| 28 | Go + playwright-go | TypeScript (recommended), C#, Python, Go+chromedp | User choice; Node driver still bundled |
| 29 | Go workspace: core, cli, mcp modules | Single module | User choice |
| 30 | JSON Schema + santhosh-tekuri/jsonschema | Struct validation | Same schema reused in MCP tool description |
| 31 | Official MCP go-sdk | mark3labs/mcp-go | User choice |
| 32 | modernc.org/sqlite | mattn/go-sqlite3 | No cgo |
| 33 | Playwright recordVideo | CDP screencast | User choice; accepted lower bitrate |
| 34 | go test + 1 e2e; GitHub Actions lint/test/docker build | Unit only, no CI | User choice |
| 35 | Name: screencaster | demoforge, narrate, playreel | User choice |
| 36 | Implementation defaults (not discussed): YAML field names, cursor 25 move steps, 60 ms/char typing, timestamp format, EN rendered before PL, jobs.db path, CLI bypasses queue | — | Chosen by Claude while writing; change freely |
| 37 | explore_page: stateless, replays `actions` on each call, accessibility snapshot only (no screenshot), capped at 50 000 chars, runs outside the render queue | Stateful sessions, screenshots | Chosen by Claude; simplest, reuses the step executor |
| 38 | Language precedence: render-time override > script `languages` > `["en"]` | Script-only setting | Chosen by Claude; lets "also in Polish" be a render-time request |
| 39 | Slash command shipped as an MCP prompt (`create_demo`) inside screencaster-mcp (v1.2) | Per-project Claude Code skill file, plugin | User request; ships with the image and stays in sync with the schema; a plugin bundle can come later |
| 40 | `get_options` tool feeds real voices/defaults/existing demos to the prompt | Hardcoded options in the prompt | Chosen by Claude; options are never stale |
| 41 | Voice per script (`voices`), resolved script > built-in (config level dropped by 60); extra voices via `/work/voices` | Config-only voices | Chosen by Claude; per-demo voice choice from the guided flow |
| 42 | Guided flow asks once (batch, with defaults), no YAML approval; script `meta` also written to MP4 tags | Multi-step wizard, approval gate | Chosen by Claude; consistent with decision 8 |
| 43 | Any language with an installed Piper voice is supported; EN and PL voices built into the image, others as files in `/work/voices`; no built-in default voice for other languages (v1.3) | Fixed EN + PL only, bundling many voices in the image | User approved; keeps the image small |
| 44 | `explore_page` may overlap a running render (two Chromiums, CPU contention may jitter timing); no shared browser lock | Shared semaphore to serialize; reject explore during render | User choice. Offsets come from timestamps, so correctness holds. Revisit if timing jitter shows up. See ARCHITECTURE.md §6.2. |
| 45 | `flock` lock file `.screencaster/render.lock` guards CLI against simultaneous MCP worker render. CLI fails fast, worker waits | No lock; CLI enqueues via SQLite | User choice. Kernel releases on crash. Keeps PRD rule that CLI has no queue or job record. See ARCHITECTURE.md §6.3. |
| 46 | Recording t0 = monotonic timestamp just before recorded page creation; no trimming; fixed compensation only if M2 spike finds lead-in > ~100 ms | Trim lead-in with ffmpeg -ss | User choice. Fewer moving parts. See ARCHITECTURE.md §5. |
| 47 | `explore_page` snapshot = Playwright ARIA snapshot + derived selectors, with uniqueness check and `>> nth=N` for collisions | Screenshot + raw DOM; deprecated Accessibility snapshot | Supported API in playwright-go. Guarantees "selector found by explore works in render". Output format confirmed by spike S4 (ARCHITECTURE §17). |
| 48 | MCP server: logs to stderr only, subprocess stdout/stderr captured, never inherited | Logs to stdout | Required for MCP stdio hygiene. See ARCHITECTURE.md §12. |
| 49 | Narration clip duration read from WAV header, not ffprobe | Spawn ffprobe per clip | Avoids subprocess per clip. Piper emits PCM WAV, duration exact from header. See ARCHITECTURE.md §4. |
| 50 | Constant 30 fps forced during transcode with `fps=30` filter / `-r 30` | Pass VFR through | Keeps video time equal to wall time for adelay offsets. See ARCHITECTURE.md §5. |
| 51 | Commit `go.work` and `go.work.sum` | Ignore and generate in CI/Docker | One source of truth for CI, the image and contributors. |
| 52 | Dev Docker image from M1; every `make` target runs in it | Host Go install | The host has only Docker; one toolchain everywhere. |
| 53 | `spf13/cobra` for the CLI | std `flag` | User choice; documented deviation from the KISS rule in CODE_QUALITY.md. |
| 54 | e2e tests in their own module `tests/e2e`, run locally with `make e2e`, not in CI | In-module e2e; e2e in CI | User choice; keeps CI fast and cheap. Deviates from ARCHITECTURE.md §13. |
| 55 | Schema at `core/script/script.schema.json` | Root `schema/` | `go:embed` cannot reference parent directories. |
| 58 | Each demo is one self-contained YAML. `baseUrl` is required, `storageState` and `outputDir` are optional, and `screencaster.yaml` is removed (a warning is shown if one is present). Supersedes 15 | Optional `screencaster.yaml` fallback; paths relative to the demo file | User choice. No hidden project state; a public-site demo is one file and one command. Retargeting an environment means editing `baseUrl` (BR-010). |
| 59 | `explore_page` takes an absolute `url` plus an optional `storageState`. The url is also the base for relative gotos in `actions` | Relative path plus a config `baseUrl` | User choice. No config to read; the explorer needs no change. |
| 60 | No project-level voice defaults. Voice resolution is script, then built-in | Keep config `voices`; env-var defaults | Follows from 58. The script already carries per-demo `voices`, so a project level only added a second source. |
| 61 | `storageState` is an inline object in the script (and in `explore_page`), not a path to a file. Supersedes the file form of 15, 58 and 59 | Path to a Playwright JSON file | User choice. A demo is then truly one self-contained file, with no second file to mount, check or leak a path from. The schema defines the shape; secrets now live in the demo, so it stays out of git. |
| 62 | Every path in a demo resolves against the demo file's folder and must stay inside the working directory: `outputDir` (default `<demo dir>/output`) and a card `image`. Supersedes the "paths relative to the demo file" rejection in 58 | Paths relative to the working directory | User choice. A demo, its output and its pictures move together. The inside-the-work-dir rule stays because the script is LLM-written. A script outside the work dir has its default output outside too, so it needs an explicit absolute `outputDir` inside it. |
| 63 | Every video gets a 3 s start card and a 3 s end card by default. The built-in card is an HTML page screenshotted by Chromium, joined to the recording by ffmpeg; `intro`/`outro` set a picture, text, time, or `false`. Moves the start and end cards from 10.2 into the MVP; interleaved slides stay post-MVP | Opt-in cards; ffmpeg `drawtext`; Go image rendering; cards recorded as browser pages | User choice. The output looks finished without an editing step. Chromium is already in the image, wraps long text and has the Polish glyphs. |
| 64 | Render log lines are built in `core/renderer` and handed to `Request.Log`; the CLI prints them on stderr, the MCP worker writes them to `slog` with the job id | A `*slog.Logger` in the dependencies | One place for the wording, plain lines in the CLI (no `time=… level=…`), no stdout in the MCP server. |

## 21. Open Questions
None.

## 22. Glossary
| Term | Definition |
|------|------------|
| Script | YAML file in `demos/` describing steps and narration for one demo. Sets `languages` (default `["en"]`) and other metadata. |
| Step | One action plus optional `narration` (keyed by language code: `en`, `pl`, etc.). Narration text is required for every selected language. |
| Job | One queued/running/completed render of a script, producing one MP4 per selected language. (Not limited to EN and PL; any language with an installed voice is rendered.) |
| Card | The start (`intro`) or end (`outro`) picture of a video, 3 s by default: built in, or the developer's own `image`. Has no narration. |
| storageState | Playwright storage state (cookies/localStorage) used to start a logged-in session. Written inline in the demo script (optional). |
| Piper | Local open-source neural TTS engine. Voices and languages are pluggable via `/work/voices/*.onnx` files. |
