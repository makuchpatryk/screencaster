# Claude Code Guidelines for screencaster

## Commits

- **Subject line:** Short (≤50 chars), imperative mood
- **Body:** Wrap at 72 chars (standard Git)
- **No co-author line** — skip `Co-Authored-By: ...`

Examples:
- `Add scroll action to executor`
- `Check script type before validation for tool errors`

## Skills

Use the three screencaster-* skills:
- `screencaster-planning` — spec before coding
- `screencaster-implement` — turn plan into Go code
- `screencaster-review` — check code against docs and patterns

Skills output changes only; user decides when to commit.

