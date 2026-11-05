# Claude Code Guidelines for screencaster

## Commits

- **One line only:** subject line, short (≤50 chars), imperative mood, no body
- **No co-author line** — skip `Co-Authored-By: ...`

Examples:
- `Add scroll action to executor`
- `Check script type before validation for tool errors`

## Skills

Use the screencaster-* skills:
- `screencaster-planning` — spec before coding
- `screencaster-implement` — turn plan into Go code (asks where to work and which checks to run)
- `screencaster-review` — check code against the three docs and patterns
- `screencaster-e2e` — add one e2e test from a confirmed test contract (the only skill that writes or runs e2e)
- `screencaster-security` — read-only security audit

Skills output changes only; user decides when to commit and merge the worktree branch.

## Workflow rules

- **Ask where to work, every run**: implementation and e2e ask whether to use their own git worktree (`.claude/worktrees/<task-name>` on branch `feature/<task-name>`, `<task-name>` = spec file name without `.md`, created from the current `HEAD`; main checkout untouched, implementation ticks the spec copy there) or the current branch. Review and security ask which one to inspect. Never assume
- **Skills live in the main checkout** (`.claude/` is git-ignored, so read them by absolute path from the main checkout, also from inside a linked worktree)
- **Ask which checks to run, every run**: lint, vet, tests (implementation); the same plus image build and `make image-check` where relevant (review). Run only what the user selects. Go tooling runs in the dev image (`make dev-image` once)
- **Releases are the user's**: Claude never runs the `release` workflow, never pushes tags or images, never pushes to `main`
- **No e2e** in any skill except `screencaster-e2e`: planning, implementation, review and security neither run nor write e2e tests; CI runs the suite on the PR. Planning never runs tests
- **Handoffs are manual**: planning prints the implement command, implementation prints the review command, review prints the fix command; each runs in a fresh session and reads only the spec
