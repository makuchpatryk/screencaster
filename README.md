# screencaster

Write a YAML script, run one command, get a narrated MP4 of a website.

![screencaster rendering a demo from the terminal, then playing the video](docs/demo.gif)

## Install

Docker (run from the project folder):

```bash
docker run --rm -v $(pwd):/work ghcr.io/makuchpatryk/screencaster screencaster render demos/my-demo.yaml
```

Native (Debian/Ubuntu, linux amd64), from a [release](https://github.com/makuchpatryk/screencaster/releases):

```bash
tar xzf screencaster_X.Y.Z_linux_amd64.tar.gz
sudo ./screencaster setup
./screencaster render demos/my-demo.yaml
```

## Demo

`demos/my-demo.yaml`:

```yaml
name: my-demo
languages: [en]
steps:
  - goto: http://172.17.0.1:3000/projects
  - click: role=button[name="New project"]
    narration:
      en: Click the New project button.
  - fill: { selector: 'input[name="name"]', value: My Project }
```

- Paths are relative to the demo file. `goto` must be an absolute URL.
- Docker: `172.17.0.1` is the host on Linux, `host.docker.internal` on Docker Desktop. The app must listen on `0.0.0.0`.
- `narration` is optional per step.

More (login cookies, intro/outro cards, screenshots, voices): [docs/PRD.md](docs/PRD.md).

## Claude Code (MCP)

Add to the project's `.mcp.json`, with `<project>` the absolute path of the folder that holds `demos/`:

```json
{
  "mcpServers": {
    "screencaster": {
      "command": "docker",
      "args": ["run", "-i", "--rm", "--init",
               "-v", "<project>:/work", "screencaster", "screencaster-mcp"]
    }
  }
}
```

The `screencaster` image name comes from `make image`, or `docker tag ghcr.io/makuchpatryk/screencaster screencaster`.

Prompt: `/mcp__screencaster__create_demo [description]`.

## Docs

[PRD](docs/PRD.md) · [ARCHITECTURE](docs/ARCHITECTURE.md) · [CODE_QUALITY](docs/CODE_QUALITY.md)

## Development

Host needs only Docker.

```bash
make image    # build the image
make test     # go test -race ./...
make lint     # golangci-lint
make e2e      # real Chromium, Piper, ffmpeg
```
