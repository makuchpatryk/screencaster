# screencaster

Turn a natural-language description into a narrated demo video (MP4) with re-renderable YAML scripts.

## Quick start

1. **Clone and build** (Go 1.22+):
   ```bash
   go work use ./core ./cli ./mcp
   go build -o screencaster ./cli
   go build -o screencaster-mcp ./mcp
   ```

2. **Docker** (recommended for rendering):
   ```bash
   docker build -t screencaster .
   docker run -i --rm --add-host=host.docker.internal:host-gateway \
     -v $(pwd):/work screencaster screencaster-mcp
   ```

3. **Configure** your project (`screencaster.yaml`):
   ```yaml
   baseUrl: http://host.docker.internal:3000
   storageState: auth/storageState.json
   outputDir: output
   ```

4. **Write a demo** (`demos/my-demo.yaml`):
   ```yaml
   name: my-demo
   languages: [en, pl]
   meta:
     title: How to create a project
     audience: release-notes
   steps:
     - action: goto
       url: /projects
     - action: click
       selector: role=button[name="New project"]
       narration:
         en: Click the New project button.
         pl: Kliknij przycisk Nowy projekt.
     - action: fill
       selector: input[name="name"]
       value: My Project
   ```

5. **Render**:
   - CLI: `screencaster render demos/my-demo.yaml`
   - MCP (in Claude Code): describe what you want, Claude writes the YAML and renders

## Docs

- **[PRD](docs/PRD.md)** — what it does, requirements, business logic
- **[ARCHITECTURE](docs/ARCHITECTURE.md)** — how it is built, pipeline, concurrency, data model
- **[CODE_QUALITY](docs/CODE_QUALITY.md)** — rules for writing and reviewing code

## Project layout

```
core/          shared library (no MCP, no SQLite)
cli/           screencaster render ... CLI
mcp/           screencaster-mcp MCP server
schema/        script.schema.json
testdata/      fixture HTML app for e2e
Dockerfile
```

## Development

```bash
# Test all modules
go test -race ./...

# Lint
golangci-lint run ./...

# Build image
docker build -t screencaster .

# E2E in the image
docker run --rm -v $(pwd):/work screencaster go test -race ./...
```

## Scripts

- `demos/*.yaml` — stored demo scripts (created via chat or CLI)
- `output/*.mp4` — rendered videos, never overwritten (BR-006, FR-010)
- `auth/storageState.json` — logged-in session state for the target app (add to `.gitignore`)

## Key constraints

- One render at a time (BR-008, FR-014)
- 30 s timeout per step (BR-004, FR-008)
- 1920×1080 30 fps H.264 MP4 (FR-004, FR-009)
- Deterministic: no LLM at render time (BR-001)
- Offline TTS via Piper (§9)

## Notes

- Add extra Piper voices to `/work/voices/*.onnx` (FR-018)
- Add `.screencaster/` to `.gitignore`
- The MCP server runs inside Docker. Configure in Claude Code with:
  ```json
  {
    "name": "screencaster",
    "command": "docker",
    "args": ["run", "-i", "--rm", "--add-host=host.docker.internal:host-gateway", "-v", "<project>:/work", "screencaster", "screencaster-mcp"]
  }
  ```

## Author

Built by one developer. Not production software, no support.
