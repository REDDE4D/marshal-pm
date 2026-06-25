# Unified host agent — design

**Date:** 2026-06-25
**Status:** Approved (brainstorm), pending spec review
**Branch:** `unified-host-agent` (off `dev`)

## Problem

A single host runs up to three independent supervision contexts, each with its
own store and socket, none aware of the others:

1. The **standing local daemon** — default store (`store.New()` →
   `$XDG_DATA_HOME/marshal`), auto-spawned by `marshal start`/`list`/… or run at
   boot by `marshal startup` (launchd/systemd). **Not enrolled** with any server.
2. The **self-enroll agent** — a *separate* store at `<serverData>/agent`, run
   in-process by `marshal server startup --self-enroll <yaml>`
   (`cmd/marshal/selfenroll.go`). Enrolled, reports to the server, shows in the
   dashboard.
3. (`marshal fleet …` operates on the server, a fourth surface.)

Consequences observed in the field:

- Apps started with `marshal start marshal.yaml` land on daemon (1). They appear
  in `marshal list` but **never in the dashboard** (daemon 1 isn't enrolled).
- An app started via `--self-enroll` lands on daemon (2). It appears in the
  **dashboard but not in `marshal list`** (different store/socket).
- The fleet client is wired **once at daemon startup** from `st.LoadServer()`
  (`internal/daemon/server.go:327`); there is no live reload, so enrolling a
  running daemon requires a restart.

## Goals

1. `marshal list` always shows **all** apps managed by Marshal on the host.
2. Apps started with `marshal start` automatically appear in the dashboard **when
   the host is enrolled** — no per-app step.
3. **One** daemon/agent per host, not three.

## Non-goals

- Auto-migrating the existing `<serverData>/agent` store. Pre-1.0, single known
  host → **clean cutover** (documented manual re-add), no migration code.
- Changing the server itself. This work is entirely on the agent side + a new
  enroll entry point.
- Multi-agent-per-host topologies. Exactly one local agent per host.

## The model

**One local daemon per host, keyed to the single default store, is the agent.**

- Every `start`/`stop`/`list`/`restart`/… talks to it. `marshal startup` runs it
  at boot. It supervises **all** host apps.
- It **optionally enrolls** with a server (server config + token live in its
  store). When enrolled it reports its full app list automatically — the existing
  `fleetSnapshot` already returns `s.mgr.List()`
  (`internal/daemon/fleet.go:18`), so nothing per-app is needed.
- The **server stays a separate process** (same box or remote). The agent
  connects to it over the existing fleet gRPC stream, localhost or remote — one
  code path.

Because there is exactly one daemon, Goals 1–3 hold by construction: one daemon
to list, one daemon that reports, one agent.

### Data flow (after)

```
marshal start app ─┐
                   ├─► local daemon (default store) ──fleet client (if enrolled)──► server ──► dashboard
marshal list ◄─────┘        supervises ALL host apps
marshal enroll ───► writes server config to default store ──► daemon's fleet watcher reconnects live
```

## Components / changes

### 1. `marshal enroll` / `marshal unenroll` (new commands)

`cmd/marshal/` — new file (e.g. `enroll.go`).

- `marshal enroll <server-addr> --token <enroll-token> (--fingerprint <fp> | --ca <file>)`
  - Builds `config.ServerConfig{Address, Name: hostname, Token, Fingerprint, CA}`
    and writes it to the **default** store via `st.SaveServer(...)`.
  - `--name` optional override for the agent's reported name (default
    `os.Hostname()`).
  - Prints confirmation and the current enrollment target.
  - If a daemon is already running it picks the change up live (see #2);
    otherwise it enrolls when next started.
- `marshal unenroll`
  - Clears the server config (and fleet token) from the default store; the
    daemon drops its fleet connection live.
- `marshal enroll --status` (or fold into `marshal list` header — see #5):
  reports whether enrolled, the target, and connection state.

Validation mirrors the existing fleet TLS requirements: a fingerprint or CA is
required (reuse `fleetauth.ClientTLS` semantics) so we fail fast on a bad pin.

### 2. Live fleet (re)connection in the daemon

`internal/daemon/server.go` (~`Serve`, currently lines 327–350).

Replace the once-at-startup wiring with a **supervisor goroutine** that owns the
fleet client lifecycle:

- Reads the desired state from the store: server config (`LoadServer`) + fleet
  token (`LoadFleetToken`).
- On change (config appears / address|token|fingerprint changes / config
  cleared), tears down the current fleet client and starts a new one (or none).
- Detection: **poll the store on a short interval** (e.g. 2 s) and diff a small
  fingerprint of `(address, token, fleetToken, fingerprint, ca)`. Polling one
  small file is cheap and dependency-free; fsnotify is an unnecessary dependency
  here.
- The fleet client retry/backoff already handles a server that isn't up yet, so
  the watcher only restarts on *config* change, not on transient disconnects.

This removes the restart requirement for enrollment and retires the recurring
"daemon doesn't pick up on-disk changes until restart" footgun.

**Isolation:** put the supervisor in its own unit (e.g.
`internal/daemon/fleetsupervisor.go`) with a clear interface: given a store and
the snapshot/logs/metrics adapters, it runs until ctx is cancelled, maintaining
at most one live fleet client to match the store's current config. Testable
without a real server by injecting a fake "connect" function.

### 3. `--self-enroll` refactored onto the unified agent

`cmd/marshal/selfenroll.go`.

New behavior of `marshal server startup --self-enroll <yaml>`:

1. Prepare cert + fingerprint, ensure a dashboard password, mint a fresh enroll
   token — all while the server is down (unchanged).
2. Write the localhost server-block (`Address: localhost:<port>`, `Token`,
   `Fingerprint`, `Name: hostname`) into the **default** store (not a new agent
   store).
3. Start the server in-process (foreground; this process **is** the server +
   dashboard).
4. Start the yaml's apps on the **one local daemon** via the normal client path
   (auto-spawning the standing daemon if needed) — exactly what `marshal start`
   does. The daemon, now enrolled, connects to localhost and reports.

The separate `<serverData>/agent` store and the in-process `daemon.Run` agent are
removed from this path.

**Behavior change (called out):** today Ctrl-C on `--self-enroll` stops the
single process — server *and* apps. In the unified model Ctrl-C stops the
**server** (dashboard); the apps keep running under the persistent local agent.
Stop apps with `marshal stop`, the daemon with `marshal kill`. This matches the
pm2/fleet model (the dashboard going down should not kill your processes) and is
intentional.

### 4. Clean cutover (migration)

- The old `<serverData>/agent` store is abandoned. Document removing it
  (handoff + CHANGELOG note).
- One-time manual step for the existing host: re-run apps under the unified agent
  (`--self-enroll` or `marshal start` + `marshal enroll`).
- No migration code.

### 5. `marshal list` / `marshal start` — no functional change

They already use the default-store daemon; once it's the enrolled agent the goals
are met. Optional nicety: `marshal list` prints a one-line enrollment header
(`enrolled → <server> (connected)` / `not enrolled`) so the unified state is
visible at a glance. Low risk, additive; include if cheap.

## Testing

- **Unit:** `marshal enroll` writes the correct `ServerConfig` (address, token,
  fingerprint, hostname) to the store; `--fingerprint`/`--ca` validation; bad
  input rejected.
- **Unit:** `unenroll` clears server config + fleet token.
- **Integration (fleet supervisor):** with an injected connect function, the
  supervisor starts a client when config appears, restarts on change, stops on
  clear, and keeps at most one client. No real server needed.
- **E2E:** start daemon with no server → `list` shows apps, not enrolled →
  `enroll` against a test server → apps appear via the fleet stream within the
  poll interval → `unenroll` → reporting stops. Extends/parallels
  `cmd/marshal/daemon_e2e_test.go`.
- **Update** the existing self-enroll e2e for the new lifecycle (default store;
  Ctrl-C stops server, agent persists).

## Phasing (for the implementation plan)

- **Phase 1 — `enroll`/`unenroll` + live fleet supervisor.** Delivers the core
  user need: a standing daemon can join a server and report all its apps with no
  restart. `marshal start` apps appear in the dashboard.
- **Phase 2 — `--self-enroll` refactor + drop the agent store** (incl. the Ctrl-C
  lifecycle change and clean-cutover docs).
- **Phase 3 (optional) — enrollment header in `marshal list`; handoff + docs.**

## Open risks

- **Socket ownership:** exactly one daemon may own the default-store socket. The
  unified model assumes the standing/auto-spawned daemon is that owner;
  `--self-enroll` must not spawn a competing in-process agent (it relies on the
  normal client path instead). Verify the auto-spawn/connect path is idempotent
  when a launchd daemon already owns the socket.
- **Poll interval vs. responsiveness:** 2 s is a deliberate trade. Configurable
  later if needed; not for v1.
