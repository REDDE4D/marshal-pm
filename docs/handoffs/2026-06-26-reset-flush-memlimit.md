# Handoff — reset / flush / max_memory_restart / colored log tail (2026-06-26)

## Current state

- **Branch:** `reset-flush-memlimit` (off `dev`, base commit `9028874`). NOT yet merged.
- **All 17 plan tasks complete.** Full gate green: `go test ./... -race -count=1` (32 pkgs, 0 fail),
  `go vet ./...` clean, `gofmt -l .` clean (only a stray `.claude/worktrees/...` leftover shows, not
  our code), `make ui` builds with no TS errors.
- Plan: `docs/superpowers/plans/2026-06-26-reset-flush-memlimit.md`.
  Spec: `docs/superpowers/specs/2026-06-26-reset-flush-memlimit-design.md`.
- SDD ledger (per-task commits + findings): `.superpowers/sdd/progress.md`.

## What shipped this session (four features, PM2 parity)

1. **`marshal reset <name|id|all>`** — zeroes the lifetime restart counter, the crash-loop
   (`unstable`) counter, and prunes the app's rows from `restarts.db` (the 24h rollup). Does not
   restart the process or reset uptime. Path: supervisor `Instance.ResetCounters` →
   manager `ResetCounters` → daemon `Reset` RPC (also prunes eventstore) → CLI `reset` (via
   `selectorCmd`) + `fleet reset` + dashboard control `reset`.
2. **`marshal flush [name|id|all]`** (default all) — truncates active log files, deletes rotated
   backups, clears the in-memory ring. Path: `logs.Sink.Truncate` + `Registry.Truncate` →
   daemon `Flush` RPC (resolves labels via `mgr.Describe`) → CLI `flushCmd` + `fleet flush` +
   dashboard control `flush`.
3. **`max_memory_restart`** — new `config.ByteSize` type (K/M/G, 1024-based) + `App.MaxMemoryRestart`;
   propagated on the wire via `AppSpec.max_memory_restart` (proto field 12) through `appToSpec`
   (CLI), `appSpecToConfig` (daemon), and both dashboard builders (command + git). New
   `internal/memguard` package: a debounced guard that fires a restart once an app's sampled RSS
   exceeds the limit for 3 consecutive metric ticks. Wired into the daemon's existing
   `sampler.SetOnTick` in `Run`; limits registered in `launchApp`, dropped in `Delete`.
4. **Color-coded merged log tail** — `printLogLine` colorizes the `name#idx` prefix (stable FNV-1a →
   palette) when the writer is a TTY; plain otherwise. CLI-only.

## Key decisions / non-obvious bits

- **Generated proto names:** the `reset` oneof member generates `pb.ControlOp_Reset_` (wrapper) with
  inner field `Reset_` (trailing underscores, to avoid clashing with the generated `Reset()` method);
  `flush` is plain `pb.ControlOp_Flush` / `Flush`. Used throughout command.go, fleet.go, dashboard.
- **`gen-proto.sh` fix:** it was missing `--go-grpc_out`/`--go-grpc_opt`, so `make proto` wouldn't
  regenerate `*_grpc.pb.go` on a fresh checkout. Fixed in the proto commit (Task 7).
- **SetOnTick ordering:** in `daemon.Run` the metrics `SetOnTick` callback was moved to after `srv`
  and `srv.guard` are constructed (it now calls `srv.guard.Check(m)`), still before the sampler
  goroutine launches. `mdb.Append` logic unchanged.
- **Dashboard memory field UX:** the add-app form takes **MB** and converts to bytes on the wire
  (the backend `AppSpec.max_memory_restart` is int64 bytes). Both command and git apps support it.
- **Process note:** the subagent dispatch infra degraded mid-run (3 consecutive stalls). Tasks 1–8
  ran the normal implementer+reviewer loop (all clean). Task 9 and Tasks 10–17 were
  controller-implemented directly with per-task gates (build/vet/test/gofmt/commit). They have NOT
  had an independent task-level review — **the final whole-branch review is the key quality gate and
  should be run before merge.**

## Deferred / known issues

- **Multi-instance memory restart** restarts ALL instances of an app when any one exceeds the limit
  (manager restarts by app selector, not per instance). Documented in CHANGELOG. Future: per-instance
  restart needs a manager API for a single instance slot.
- Minor (logged in ledger, for final review): `Sink.Truncate` discards rotated-backup `os.Remove`
  errors (best-effort); `memguard.SetLimit(app,0)` removes the limit but leaves stale breach entries
  (never read; `Remove` clears both); `Flush` on a zero-instance selector returns ok "flushed 0"
  rather than NotFound.
- **Next observability spec (v0.13.0):** TUI monitor (`marshal monit`) + live fleet log streaming —
  split out during brainstorming; see the spec's "Out of scope".

## How to build / run / test

```bash
make build                       # stamps version from git tags
go test ./... -race -count=1     # full gate (used here)
go vet ./... && gofmt -l .       # lint/format
make ui                          # rebuild embedded dashboard (regenerates internal/dashboard/dist)
make proto                       # regenerate internal/pb after a .proto edit
```

## Concrete next step

1. Run the **final whole-branch review** (opus) over `9028874..HEAD` — focus on the
   controller-implemented Tasks 9–17 and the cross-cutting wiring; triage the ledger's minor findings.
2. Run the **live demo** per the project convention (scratch `XDG_DATA_HOME`, server on :9000/:9001):
   exercise colored `logs all -f`, `flush`, `reset` (drive restarts up first), and a memory-growing
   app with a low `max_memory_restart` to watch the auto-restart fire; confirm in CLI + dashboard;
   tear down by data dir (no broad pkill — a standing launchd daemon is running) and verify no orphans.
3. Merge `dev` ← `reset-flush-memlimit` (`--no-ff`), then cut **v0.12.0** (move `[Unreleased]` →
   `[0.12.0]`, update compare links, merge `dev` → `main`, tag).
```
