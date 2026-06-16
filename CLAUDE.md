# Marshal — project guide for Claude

**Marshal** is a free, self-hosted process manager — an open alternative to PM2 and the
paywalled PM2 Plus insights. Written in Go. The long-term goal is a self-hosted **fleet**
manager (agents per host → central server → web dashboard); we build it bottom-up.

## Handoff convention (IMPORTANT)

**Every time a task or milestone is finished, write a handoff document** to
`docs/handoffs/YYYY-MM-DD-<topic>.md` before ending. A handoff must let a brand-new
session resume with zero prior context. Include:
- Current state (what's done, what's merged, what branch).
- What changed this session and why (key decisions, anything non-obvious).
- How to build, run, and test.
- Deferred / known issues.
- The concrete next step.

**To resume a fresh session: read the most recent file in `docs/handoffs/` first.**

## Where things live

- `docs/superpowers/specs/` — design specs (fleet architecture; agent-core design).
- `docs/superpowers/plans/` — implementation plans (one per milestone).
- `docs/handoffs/` — session handoffs (read the latest to resume).
- `internal/` — Go packages: `config`, `proc`, `supervisor`, `manager`, `version`.
- `cmd/marshal/` — the CLI entry point.

## Build / run / test

```bash
go build -o marshal ./cmd/marshal      # build the CLI
./marshal run marshal.yaml             # foreground supervisor (M1)
go test ./...                          # all tests
go test ./... -race -count=1           # race check (do this before finishing work)
go vet ./... && gofmt -l .             # lint/format (gofmt should list nothing)
```

## Conventions

- TDD: write the failing test first, then the implementation. Keep packages small and
  focused (one clear responsibility each).
- Commit messages: imperative subject; co-author trailer
  `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Do feature work on a branch, not directly on `main`.

## Environment notes

- Go was installed via Homebrew (`/opt/homebrew/bin/go`, currently 1.26.4).
- No git remote is configured (local-only repo) — PRs aren't available until one is added.
- Module path is the local `marshal`; imports are `marshal/internal/...`.
