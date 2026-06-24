# M-C · Host system metrics — Handoff

**Date:** 2026-06-24
**Branch:** `mC-host-metrics` (off `dev` @ `9b7698e`). Reviewed + live-demo-verified;
**ready to merge → `dev` (`--no-ff`)**. `main` unchanged (still v0.2.0).

To resume: read this file. Spec: `docs/superpowers/specs/2026-06-24-mC-host-system-metrics-design.md`;
plan: `docs/superpowers/plans/2026-06-24-mC-host-system-metrics.md`; program roadmap:
`docs/superpowers/specs/2026-06-23-dashboard-program-roadmap.md`; SDD ledger (git-ignored):
`.superpowers/sdd/progress.md`.

## What M-C does

Adds per-host **current-value** gauges per agent — collected on the agent, pushed with the
existing periodic `StateSnapshot`, stored on `AgentState`, and served via `/api/fleet`:

| Field                  | Source (gopsutil/v3)                     |
|------------------------|------------------------------------------|
| `cpu_percent`          | `cpu.Percent(0,false)[0]` (delta-based)  |
| `load1/5/15`           | `load.Avg()`                             |
| `mem_total/used/used_pct` | `mem.VirtualMemory()`                 |
| `net_rx_bps/net_tx_bps` | `net.IOCounters(false)[0]` counter deltas ÷ elapsed |

Point-in-time only — no time-series, no new endpoint. Network I/O is a **rate** (bytes/sec),
aggregate (whole-host CPU, summed NICs).

## What changed (8 commits, `3147b85..67dbee7`; plus spec 8cab6e9 + plan 3147b85)

- `proto/marshal/v1/fleet.proto` — new `HostMetrics` message; `StateSnapshot.host = 2`;
  `AgentState.host = 11`; `internal/pb` regenerated via `make proto`. (8439e5b)
- `internal/hostmetrics/sampler.go` (new) — host gauge sampler; pure `rate`/`perSec` helpers
  (0 on first read / reset / non-positive elapsed); best-effort (failing subsystem → zero
  field; `Sample()` never nil); `NewSampler` primes `cpu.Percent`. (b237967)
- `internal/fleet/client.go` — `HostFunc` type + `WithHost` option; `pushSnapshot` sets
  `StateSnapshot.Host` when configured. (04cf51c)
- `internal/server/registry.go` + `server.go` — `agentEntry.host`; `Update(name, procs,
  host)`; `List()` emits `AgentState.Host`; snapshot call passes `GetHost()`. (51ce972)
- `internal/daemon/server.go` — construct one `hostmetrics.Sampler`; wire
  `fleet.WithHost(func() *pb.HostMetrics { return hostSampler.Sample() })`. (c309a70)
- `internal/dashboard/fleet.go` — `hostView` + `agentView.Host` (`host,omitempty`); mapped
  from `a.GetHost()` (omitted when nil). (66a6e6d)
- `web/src/api.ts` + `web/src/Overview.tsx` — `AgentHost` type + `host?`; `hostMeta`/`fmtBps`
  render a host line on the agent band; embedded bundle rebuilt (`make ui`). (1726292)
- `CHANGELOG.md` `[Unreleased]`. (67dbee7)

## Quality gates

- TDD throughout (subagent-driven: fresh implementer + spec/quality reviewer per task). All
  per-task reviews clean. Final whole-branch review (opus): **READY TO MERGE**, no
  Critical/Important.
- `go test ./... -race -count=1` green (incl. `hostmetrics`); plain suite ALL PASS; `go vet`
  clean; `gofmt -l .` empty; `make build` ok (`v0.1.0-84`).
- **Live demo PASS:** real agent enrolled to a real server (scratch `/tmp/marshal-mC-demo`,
  :9000/:9001). `/api/fleet` returned a fully real host object (cpu 22.2%, load 3.90/4.65/4.08,
  mem 23.3/38.7GB @ 60.2%, **net 160 KB/s rx / 389 KB/s tx — non-zero, delta confirmed**).
  In-browser the agent band rendered the host line. Scratch torn down by data dir; standing
  launchd daemon (PID 899) preserved; no orphans.

## Deferred / notes (not bugs)

- **Net rate is 0 only on the genuine first read.** It is NOT reset on reconnect: after a
  reconnect gap the next rate is a true wall-clock average over the gap (counter delta ÷ real
  elapsed), not a spike and not zero. This is more correct than the spec's "reconnect → 0"
  note (design line ~184); the implementation wins, the spec wording is the stale one.
- **First `cpu_percent` after connect** can be a noisy tiny-window delta (prime-to-first-sample
  gap is sub-second); self-corrects on the next tick. Matches the per-process sampler's
  priming approach.
- `prevNet` is advanced only on a successful `net.IOCounters` read — so a transient failure
  yields a correct longer-window average next tick (intentional, good property).
- **Load average** is Unix-only → zeros off darwin/linux (Marshal targets those).
- UI is **minimal transitional** surfacing (agent-band line). **M-A** delivers the real
  cluster-cell visual treatment.
- Trivial (no fix): `hostMeta(a)` is evaluated twice in the render gate (cheap pure fn).

## Next step

Merge `mC-host-metrics` → `dev` (`--no-ff`), delete the branch. Per the roadmap the remaining
data milestones are **M-E (restart history — timestamped restart events in a new event store)**
and **M-F (errors/exceptions subsystem — the largest)**; **M-G (control additions)** is small.
The **M-A full redesign** ships last, backed by all the data from B–F. Recommend M-E next
(unblocks "restarts 24h"/velocity), then M-G, then M-F, then M-A.

## Build / run / test

```bash
make proto   # regenerate internal/pb from proto/marshal/v1
make ui      # rebuild embedded SPA bundle (commit it)
make build
go test ./... -race -count=1 && go vet ./... && gofmt -l .
```
