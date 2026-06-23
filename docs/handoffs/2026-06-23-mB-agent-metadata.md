# M-B · Agent & host metadata — Handoff

**Date:** 2026-06-23
**Branch:** `mB-agent-metadata` (off `dev` @ `630a066`). Live-demo-verified; **ready to merge → `dev` (`--no-ff`)**. `main` unchanged.

To resume: read this file. Spec: `docs/superpowers/specs/2026-06-23-mB-agent-host-metadata-design.md`;
plan: `docs/superpowers/plans/2026-06-23-mB-agent-host-metadata.md`; program roadmap:
`docs/superpowers/specs/2026-06-23-dashboard-program-roadmap.md`; SDD ledger (git-ignored):
`.superpowers/sdd/progress.md`.

## What M-B does

Carries per-agent host metadata — hostname, IP, OS, arch, marshal version, host uptime — from the
agent's `Hello` handshake → server registry → dashboard `/api/fleet` JSON → the SPA agent header.
**IP is server-derived from the gRPC peer** (not self-reported, unspoofable). Host uptime is carried
as a static **boot time** (`host_boot_unix`) and computed client-side. This is the first data
milestone of the dashboard program (data-first; the M-A redesign ships last).

## What changed (8 commits, `630a066..3dfa4c5`)

- `scripts/gen-proto.sh` + `make proto` — reproducible proto regeneration (was missing).
- `proto/marshal/v1/fleet.proto` — `Hello` gains `hostname/os/arch/host_boot_unix`; `AgentState`
  gains `hostname/ip/os/arch/marshal_version/host_boot_unix` (additive field numbers); `internal/pb`
  regenerated.
- `internal/server/registry.go` — `AgentMeta` + `SetMeta`; `List()` emits the fields.
- `internal/server/server.go` — `peerIP(ctx)` helper; Hello handler records metadata (IP from peer).
- `internal/fleet/client.go` — `buildHello()` fills host facts (best-effort; failures leave zero).
- `internal/dashboard/fleet.go` — `agentView` JSON gains the six fields.
- `web/src/api.ts` + `web/src/Overview.tsx` — `Agent` type + `agentMeta()`/`fmtUptime()` render the
  header line. Embedded bundle rebuilt (`make ui`). `CHANGELOG.md` `[Unreleased]` updated.

## Quality gates

- TDD throughout; per-task spec+quality reviews all clean; final whole-branch review (opus):
  **READY TO MERGE**, no Critical/Important.
- `go test ./... -race -count=1` green (24 pkgs), `go vet` clean, `gofmt -l .` empty, `make build` ok.
- **Live demo PASS:** real agent enrolled to a real server; authenticated `/api/fleet` returned real
  `hostname=mac.local, ip=127.0.0.1 (peer), os=darwin, arch=arm64, marshal_version=v0.1.0-61-g3dfa4c5,
  host_boot_unix=…`. Scratch torn down; standing daemon preserved; no orphans.

## Deferred / notes (not bugs)

- Accepted Minors (final review): redundant `.PHONY: proto`; `TestAgentStateMetaFields` asserts 3 of
  6 getters (covered elsewhere); empty `.seen` span when an agent has zero metadata (unreachable in
  practice — agents always send os/arch/hostname). All cosmetic; revisit in M-A if desired.
- `Open` and `SetMeta` take the registry mutex separately → a `List()` between them could show
  `connected:true` with empty meta for one tick. Benign, self-heals, display-only.
- Browser screenshot of the header deferred: minimal transitional UI; M-A delivers the real visual
  treatment with a proper in-browser demo.

## Next step

Merge `mB-agent-metadata` → `dev` (`--no-ff`), delete the branch. Then the next milestone per the
roadmap is **M-D (extended per-process metrics: threads, fds, exit code)** — design → plan →
subagent-driven execution, same as M-B.

## Build / run / test

```bash
make proto   # regenerate internal/pb from proto/marshal/v1
make ui      # rebuild embedded SPA bundle (commit it)
make build
go test ./... -race -count=1 && go vet ./... && gofmt -l .
```
