# Handoff — Unified host agent (2026-06-25)

## Current state

Branch **`unified-host-agent`** (off `dev`), all 9 plan tasks complete, reviewed,
and green — **not yet merged**. Full gate passes including the tagged e2e:
`go test ./... -tags e2e_fleet -race -count=1`, `go vet ./...`, `gofmt -l` clean.

Spec: `docs/superpowers/specs/2026-06-25-unified-host-agent-design.md`
Plan: `docs/superpowers/plans/2026-06-25-unified-host-agent.md`
SDD ledger: `.superpowers/sdd/progress.md` (git-ignored).

## What shipped (and why)

The host had up to three independent supervision contexts with separate stores;
apps started via `marshal start` (default store, unenrolled) never reached the
dashboard, while `--self-enroll` ran a *separate* agent under `<serverData>/agent`.
This branch makes **one local daemon per host the single agent**:

- **`marshal enroll <server> --token --fingerprint` / `marshal unenroll`**
  (`cmd/marshal/enroll.go`) write/clear the server config in the **default** store.
- **Live fleet supervisor** (`internal/daemon/fleetsupervisor.go`): the daemon
  watches its store (2 s poll, `daemon.WithFleetPollInterval` for tests) and
  connects / reconnects / drops the fleet client when config changes — **no
  restart to enroll**. Replaced the once-at-startup block in
  `internal/daemon/server.go`.
  - Load-bearing invariant: `fleetTarget` (the change key) **excludes the
    persisted per-agent token** — else the client restarts right after it enrolls
    and writes that token. Proven by `TestSuperviseFleetIgnoresPersistedToken`.
- **`--self-enroll` refactored** (`cmd/marshal/selfenroll.go`): enrolls the one
  default-store daemon (no more `<serverData>/agent`), serves the server in
  foreground, starts apps via `client.Connect` + `c.Start` + `c.Save`. **Ctrl-C
  now stops the server; apps keep running** under the persistent daemon.
- **`marshal list`** prints an `enrolled → <addr>` / `not enrolled` header
  (`cmd/marshal/control.go`, `enrollmentHeader`).
- **`store.ClearServer()`** (`internal/store/store.go`) for unenroll.

Once a host is enrolled, every app it supervises reports automatically
(`fleetSnapshot` = `mgr.List()`), so `marshal list` and the dashboard agree.

## Build / run / test

```
make build
go test ./... -tags e2e_fleet -race -count=1   # the e2e needs the tag (now in Makefile + CI)
go vet ./... && gofmt -l .
```

## Migration (clean cutover — no migration code)

A host that used the old self-enroll agent must remove `<serverData>/agent` and
re-add its apps under the unified daemon (re-run `--self-enroll` or
`marshal start` + `marshal enroll`).

## Deferred / known follow-ups

- **One-time "not enrolled" log** in `superviseFleet`: the old
  `log.Printf("fleet: disabled — no token and not enrolled")` startup breadcrumb
  was dropped; an unenrolled host now logs nothing about fleet. Deliberately
  deferred to avoid reopening the reviewed supervisor; small, OK to add later.
- The user's real adminbot fleet (the VPS) should, after this lands and a new
  binary is deployed: `marshal enroll <server> --token <t> --fingerprint <fp>`
  on the host, then plain `marshal start` for all apps — they appear in the
  dashboard automatically.

## Concrete next step

Live-demo verification (this session) → then merge `unified-host-agent` → `dev`
(`--no-ff`) via `superpowers:finishing-a-development-branch`. No release cut yet
(that's `dev` → `main` + tag, separately).
