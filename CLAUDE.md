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

## Live demo convention (IMPORTANT)

**After finishing a task/milestone and writing its handoff, spin up a live demo to test
it for real** — don't stop at green unit tests. Build the binary, run the actual app with
a few representative demo processes, exercise the new feature end-to-end (CLI and/or the
dashboard at `https://localhost:<http-port>`), and confirm it behaves. Report what you
observed. Tear the demo down afterward (stop processes + daemon + server, remove the
scratch dir) and confirm no orphan processes remain (`pgrep -fl marshal`).

Use a scratch data dir (`XDG_DATA_HOME=/tmp/marshal-demo/...`) so real state is never
touched. Dashboard auth setup must happen while the server is **down** (the running
server's in-memory `AuthStore` does not pick up on-disk `passwd`/`token --rotate` changes
until restart): set the password, rotate a fresh enroll token, capture the fingerprint,
*then* start the server with `--http-listen`, then enroll the agent.

## Versioning & release workflow (IMPORTANT)

The project uses **Semantic Versioning** (pre-1.0: minor bumps on features, patch on fixes)
and a **`CHANGELOG.md`** in [Keep a Changelog](https://keepachangelog.com/) format.

**Branching:** `main` is the **release** branch — it only moves when a release is cut.
Day-to-day work happens on **`dev`** (the integration branch): create milestone/feature
branches off `dev`, merge them back into `dev` (`--no-ff`), and only **merge `dev` → `main`
when a release is finished**. Never commit feature work directly to `main`.

**Every change records a changelog entry** under `## [Unreleased]` in `CHANGELOG.md` (Added /
Changed / Fixed / Removed) as part of the work — don't leave it for release time.

**Cutting a release** (on `dev`, then promote): move the `[Unreleased]` items into a new
`## [X.Y.Z] - YYYY-MM-DD` section, update the compare links at the bottom, merge `dev` → `main`
(`--no-ff`), then **tag** `main` (`git tag vX.Y.Z`) and push `main`, `dev`, and the tag.
The version is **git-tag-derived**: `make build` stamps `marshal --version` from
`git describe --tags` via `-ldflags` (see the Makefile); a plain `go build` reports the
in-source default `0.0.0-dev`.

Current baseline: **v0.1.0** (tagged on `main`, baselining the work through M26).

## Where things live

- `docs/superpowers/specs/` — design specs (fleet architecture; agent-core design).
- `docs/superpowers/plans/` — implementation plans (one per milestone).
- `docs/handoffs/` — session handoffs (read the latest to resume).
- `internal/` — Go packages: `config`, `proc`, `supervisor`, `manager`, `version`.
- `cmd/marshal/` — the CLI entry point.

## Build / run / test

```bash
make build                             # build the CLI, stamping the version from git tags
go build -o marshal ./cmd/marshal      # plain build (version reports 0.0.0-dev)
make version                           # print the version make build would stamp
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
- Do feature work on a branch off **`dev`**, never directly on `main`; `main` only moves on a
  release (see *Versioning & release workflow*). Update `CHANGELOG.md`'s `[Unreleased]` as you go.

## Environment notes

- Go was installed via Homebrew (`/opt/homebrew/bin/go`, currently 1.26.4).
- Published as a **public GitHub repo**: `REDDE4D/marshal-pm` (`origin`). PRs are available
  via `gh`. License: **GPL-3.0** (copyleft — forks must stay open source).
- Module path is the local `marshal`; imports are `marshal/internal/...`.
