# Handoff: CLI "update available" terminal hint

**Date:** 2026-06-26
**Branch:** `cli-update-hint`
**Status:** All tasks done, tests green, changelog updated, ready for live demo then merge.

---

## Current state

Branch `cli-update-hint` off `dev`. All implementation tasks (1–6) complete:

- Proto: `UpdateInfo` message + `UpdateStatus` RPC added to the daemon service definition (Task 1).
- `updatecheck` interval constant changed from 24h to 6h, shared by daemon and server (Task 2).
- Daemon runs `updatecheck.Checker`; `UpdateStatus` RPC handler returns current/latest/outdated/checked-at (Task 3).
- `client.ConnectExisting` — non-spawning dial to an already-running daemon (Task 4).
- CLI `PersistentPostRunE` banner: calls `ConnectExisting`, fetches `UpdateStatus`, formats and prints to stderr when outdated + interactive TTY + `MARSHAL_NO_UPDATE_CHECK` not set (Task 5).
- Changelog entry + handoff (this file) — Task 6.

All tests pass (`go test ./... -race -count=1`), vet clean, gofmt clean (one stray `.claude/worktrees/` path appears in gofmt output but is not project code).

---

## What changed and why

### 1. `UpdateInfo` proto message + `UpdateStatus` RPC

A new `UpdateInfo` protobuf message carries `current`, `latest`, `outdated` (bool), and `checked_at_unix` fields. The daemon service definition gains a `UpdateStatus(Empty) → UpdateInfo` RPC so the CLI can poll the daemon for update state without re-doing the GitHub network call itself.

### 2. Update check interval: 24h → 6h

The shared constant in `internal/updatecheck` was 24h. Halving it to 6h means new releases surface within a day for users who keep their daemon running. Both daemon and server read the same constant so the change is consistent.

### 3. Daemon runs the checker

`daemon.Run` now constructs an `updatecheck.Checker` (the same type the server dashboard uses) and starts it. The `UpdateStatus` RPC handler reads state from the checker — if the checker hasn't run yet it returns `outdated: false` gracefully.

### 4. `client.ConnectExisting`

A new entry point `ConnectExisting(*store.Store) (pb.DaemonClient, *grpc.ClientConn, error)` dials the daemon only if it is already running (reads the socket path from the store, returns an error if the socket doesn't exist). It never spawns a new daemon. This is important for the banner: we want a best-effort check after a command, not a side-effecting daemon start.

### 5. CLI `PersistentPostRunE` banner

`cmd/marshal` registers a `PersistentPostRunE` on the root command. After every command that successfully talks to a running daemon it calls `maybePrintUpdateBanner`, which:
- Calls `ConnectExisting` — no-ops if no daemon is running.
- Calls `UpdateStatus` — no-ops on any RPC error.
- Formats a one-line message if `outdated == true`: `marshal: update available — vX.Y.Z (current vA.B.C) → <releases URL>`.
- Prints to stderr only if stdout is an interactive TTY (uses `term.IsTerminal`).
- Is silenced by `MARSHAL_NO_UPDATE_CHECK=1` (or any non-empty value).

### Key design decisions

- **stderr, not stdout** — so it never contaminates piped/scripted output.
- **TTY-gated** — CI, scripts, and log captures never see the hint.
- **Non-spawning** — the daemon is already running for any command that reaches `PostRun`; no extra process is created.
- **Best-effort** — any error (no daemon, RPC fail, network) is silently swallowed.

---

## How to build, run, and test

```bash
# Build (version stamped from git tags)
make build

# All tests with race detector
go test ./... -race -count=1

# Vet + format
go vet ./... && gofmt -l .

# Quick manual check (requires a running daemon)
./marshal list                         # banner appears if outdated
MARSHAL_NO_UPDATE_CHECK=1 ./marshal list  # silent
./marshal list | cat                   # piped — no banner
```

To force-show the banner for testing, build with a fake old version:

```bash
go build -ldflags "-X github.com/REDDE4D/marshal-pm/internal/version.Version=v0.0.1" \
    -o /tmp/marshal-test ./cmd/marshal
/tmp/marshal-test list   # should print update banner to stderr
```

---

## Deferred / known issues

- **Auto-update (`marshal update` command)** — out of scope for this milestone. Downloading and replacing the binary raises security and permission questions; deferred.
- **Configurable interval** — the 6h constant is hard-coded. A config-file option (`update_check_interval`) is a reasonable future addition but not needed now.
- **Per-instance restart on `max_memory_restart`** — noted as a deferred refinement in v0.12.0 changelog; unrelated to this branch.

---

## Next step

1. **Live demo** (Step 5 of the brief): build a binary stamped `v0.0.1`, start a daemon + demo app, confirm the banner appears on `marshal list` in a terminal, confirm silence when piped and with `MARSHAL_NO_UPDATE_CHECK=1`. Tear down by data dir.
2. Merge `cli-update-hint` → `dev` (`--no-ff`).
3. Cut release v0.13.0: move `[Unreleased]` entries into `[0.13.0]`, update compare links, merge `dev` → `main` (`--no-ff`), tag `main` as `v0.13.0`, push `main`, `dev`, and the tag.
