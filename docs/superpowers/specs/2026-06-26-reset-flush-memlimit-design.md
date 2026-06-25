# Design: `reset`, `flush`, and `max_memory_restart`

**Date:** 2026-06-26
**Status:** Approved (brainstorming → ready for implementation plan)
**Target release:** v0.12.0 (minor bump — three user-facing features)

## Summary

Four additions, all framed for PM2 parity so migrating users keep their muscle memory:

1. **`marshal reset <name|id|all>`** — zero an app's restart counter (mirrors `pm2 reset`).
2. **`marshal flush [name|id|all]`** — clear an app's captured logs; no argument = all (mirrors `pm2 flush`).
3. **`max_memory_restart: 300M`** — per-app config: auto-restart an app when its RSS exceeds a
   threshold for a sustained period (mirrors PM2's `max_memory_restart`).
4. **Color-coded merged log tail** — colorize the per-app `name#idx` prefix in `logs … -f` so an
   interleaved multi-app stream is readable (foreman/docker-compose style).

The first two are small, self-contained operational utilities; the fourth is a ~15-line CLI polish.
The third is near-free because the daemon already samples per-instance RSS every tick; it adds a
debounced comparison + restart trigger.

A larger "live observability" set — a full-screen TUI monitor (`marshal monit`) and true live fleet
log streaming — was deliberately split into its own follow-up spec (target v0.13.0) rather than
bloating this release. See *Out of scope* below.

## Motivation

Marshal is positioned as a free alternative to PM2. PM2 already has `reset`, `flush`, and
`max_memory_restart`; Marshal users coming from PM2 expect them. The restart counter is currently a
monotonic value with no reset path, and logs can only be cleared by manually deleting files under
`~/.marshal/logs/`. Memory-limit auto-restart is the single most-requested production guardrail a
process manager provides and the data to implement it is already being collected.

---

## Feature 1 — `marshal reset <name|id|all>`

### Behavior

Reset zeroes the restart accounting for the selected apps. It does **not** restart the process and
does **not** reset uptime/`StartedAt` (same as `pm2 reset`). After reset, both the CLI `Restarts`
column and the dashboard `restarts_24h` read zero.

The restart count lives in three places; a correct reset clears all three:

| Place | Location | Action |
|---|---|---|
| `Instance.restarts` — lifetime total | `internal/supervisor/instance.go:43` | set to 0 |
| `Instance.unstable` — consecutive sub-`MinUptime` restarts vs `max_restarts` | `internal/supervisor/instance.go:44` | set to 0 |
| `restarts.db` rows for the label | `internal/eventstore/store.go` | delete rows `WHERE label = ?` |

Resetting `unstable` matters for a **running** instance: it restores the crash-loop headroom before
`max_restarts` is hit again. For an **errored** instance the supervisor loop has already exited, so
reset is purely cosmetic (counters and `restarts_24h` go to zero); a subsequent `restart` recreates
the instance fresh regardless. Reset never changes process state or restarts anything.

### New code, by layer

- **supervisor** (`internal/supervisor/instance.go`): add
  ```go
  // ResetCounters zeroes the lifetime and crash-loop restart counters.
  func (i *Instance) ResetCounters() {
      i.mu.Lock()
      defer i.mu.Unlock()
      i.restarts = 0
      i.unstable = 0
  }
  ```
- **manager** (`internal/manager/manager.go`): add
  ```go
  // ResetCounters zeroes the restart counters of the selected apps' instances.
  func (m *Manager) ResetCounters(sel string) ([]InstanceSnapshot, error)
  ```
  Follows the `Stop`/`Restart` locking pattern: take `opMu`+`mu`, `resolve(sel)`, iterate
  instances calling `inst.ResetCounters()`, unlock, return `m.Describe(sel)`.
- **eventstore** (`internal/eventstore/store.go`): add
  ```go
  // DeleteLabels removes all restart events for the given labels.
  func (s *Store) DeleteLabels(labels []string) (int64, error)
  ```
  One `DELETE FROM restarts WHERE label = ?` per label (the DB is tiny and serialized at
  `SetMaxOpenConns(1)`), summing `RowsAffected`.
- **daemon** (`internal/daemon/server.go`): add the RPC handler
  ```go
  func (s *Server) Reset(_ context.Context, sel *pb.Selector) (*pb.ProcList, error)
  ```
  Call `s.mgr.ResetCounters(sel.GetTarget())` (map not-found → `codes.NotFound`, like `mutate`);
  collect the `Label`s from the returned snapshots; if `s.estore != nil`, call
  `s.estore.DeleteLabels(labels)`; return `s.procList(snaps)`.
- **proto** (`proto/marshal/v1/daemon.proto`): add to the `Daemon` service
  ```protobuf
  rpc Reset(Selector) returns (ProcList);
  ```
  Regenerate `internal/pb`.

### CLI

`cmd/marshal/` gains a `reset` subcommand mirroring `restart`: resolve the selector argument,
dial the daemon, call `Reset`, render the returned `ProcList` (same table as `restart`).

---

## Feature 2 — `marshal flush [name|id|all]`

### Behavior

Flush clears all captured output for the selected apps so `marshal logs -f` and the dashboard log
pane start fresh. With no argument it defaults to `all` (matching `pm2 flush`). Logs live in three
places per label (`internal/logs/sink.go`):

- active rotated files `<label>.out.log` / `<label>.err.log` → truncate to zero
- rotated backups (`<label>.out-*.log`, `.gz`, and the `.err` equivalents) → delete
- the in-memory 1000-line ring + partial-line buffers → reset to empty

### New code, by layer

- **logs.Sink** (`internal/logs/sink.go`): add
  ```go
  // Truncate empties the active log files, deletes rotated backups, and clears the ring.
  func (s *Sink) Truncate() error
  ```
  Under `s.mu`: `os.Truncate(filename, 0)` on `outFile.Filename` and `errFile.Filename`;
  glob-delete rotated siblings in the same dir; `s.ring = newRing(ringCap)`; `s.outPart = nil`,
  `s.errPart = nil`. Lumberjack keeps writing to the same path after an external truncate, so no
  reopen is needed. (No subscriber disruption — followers just see no new backfill.)
- **logs.Registry** (`internal/logs/registry.go`): add
  ```go
  // Truncate clears the logs of the existing sinks for the given labels (unknown labels skipped).
  func (r *Registry) Truncate(labels []string) error
  ```
  Looks up existing sinks only (like `ResolveLabeled`), calls `Truncate()` on each, joins errors.
- **daemon** (`internal/daemon/server.go`): add
  ```go
  func (s *Server) Flush(_ context.Context, sel *pb.Selector) (*pb.Ack, error)
  ```
  Resolve labels by calling `s.mgr.Describe(sel.GetTarget())` (reuses existing selector logic;
  not-found → `codes.NotFound`), collect `Label`s, call `s.logs.Truncate(labels)`, return
  `&pb.Ack{Ok: true, Message: "flushed"}`.
- **proto**: add to the `Daemon` service
  ```protobuf
  rpc Flush(Selector) returns (Ack);
  ```

### CLI

`cmd/marshal/` gains a `flush` subcommand. The selector argument is **optional** and defaults to
`"all"`. Print a short confirmation (e.g. `flushed logs for <n> apps`).

---

## Feature 3 — `max_memory_restart`

### Behavior

A per-app config field. When set, the daemon restarts the app once its sampled RSS exceeds the
limit for **3 consecutive metric samples** (~10–15s at the default 5s tick). The consecutive-sample
debounce prevents a momentary spike from triggering a restart. Zero/unset = disabled.

Accepted syntax (PM2-compatible): `300M`, `1G`, `512K`, or a plain byte count. Suffixes are
1024-based (`K`=1024, `M`=1024², `G`=1024³); `KB`/`MB`/`GB` accepted as aliases.

### New code, by layer

- **config** (`internal/config/config.go`): add a `ByteSize` type mirroring the existing `Duration`
  pattern (`UnmarshalYAML`, `UnmarshalJSON`, `MarshalJSON`):
  ```go
  // ByteSize is a byte count that unmarshals from "300M"/"1G"/"512K" or a plain integer.
  type ByteSize struct{ Bytes uint64 }
  ```
  Add the field to `App`:
  ```go
  MaxMemoryRestart ByteSize `yaml:"max_memory_restart" json:"max_memory_restart,omitempty"`
  ```
  No default (zero = disabled). No validation needed beyond parse errors (uint can't be negative).
- **new package `internal/memguard`**: a focused, independently testable guard.
  ```go
  type Guard struct {
      mu        sync.Mutex
      limits    map[string]uint64 // by app name; 0/absent = no limit
      breach    map[string]int    // by instance label; consecutive over-limit ticks
      threshold int               // const default 3
      restart   func(name string) // fires a restart for an app
      logf      func(format string, args ...any)
  }

  func New(restart func(name string), logf func(string, ...any)) *Guard
  func (g *Guard) SetLimit(app string, bytes uint64) // bytes==0 removes the limit
  func (g *Guard) Remove(app string)                 // on delete; also drops the app's breach counters
  func (g *Guard) Check(samples map[string]metrics.Sample)
  ```
  `Check` logic per sampled label: derive app name (strip `#idx`); if no limit, skip; if
  `sample.Mem > limit` increment `breach[label]`, and when it reaches `threshold`, call
  `restart(name)`, log the reason, and clear the breach counts for that app's labels; if
  `sample.Mem <= limit`, reset `breach[label]` to 0. Clearing on trigger prevents an immediate
  re-fire on the next tick while the restart is still in flight (a fresh process starts well under
  the limit, so no cooldown timer is required).
- **daemon wiring** (`internal/daemon/server.go`):
  - Construct the guard in `Run`: `guard := memguard.New(func(name string){ go mgr.Restart(name) }, log.Printf)`.
  - In the existing `sampler.SetOnTick` closure (server.go:297), after building `samples`, call
    `guard.Check(m)`.
  - Register limits where apps are admitted: in `launchApp` (server.go:52), call
    `guard.SetLimit(app.Name, app.MaxMemoryRestart.Bytes)`.
  - In `Delete`, after removal, call `guard.Remove(name)` for each deleted app. (The manager's
    `Delete` already returns the removed snapshots; the daemon `Delete` handler uses `mutate`, so
    this needs the daemon handler to learn the deleted names — collect from the returned `ProcList`.)
  - `Restart` is async (`go mgr.Restart`) so a slow restart never blocks the sampler tick.

### Known limitation (documented, not fixed here)

Memory is sampled per instance (`name#idx`), but the manager only restarts by app selector, so a
multi-instance app restarts **all** its instances when any one exceeds the limit. Per-instance
restart is a future refinement (would need a manager API that restarts a single instance slot).
Most apps are single-instance, so this is acceptable for v0.12.0. The handoff/CHANGELOG will note it.

---

## Feature 4 — Color-coded merged log tail

### Behavior

When following logs (`marshal logs <sel> -f`), each line is already prefixed `name#idx | line`
(`cmd/marshal/control.go:517`). This feature colorizes the `name#idx` prefix so a merged stream
of multiple apps is easy to scan, like `foreman`/`docker compose`. Color is applied **only when the
destination is a terminal** (reusing the existing `isTerminal` check that `printProcs` uses at
`control.go:345`); piped/redirected output stays plain so logs remain greppable. The line text
itself is not colored — only the prefix. stderr lines keep going to stderr.

### New code

- **CLI** (`cmd/marshal/control.go`): add a small `labelColor(label string) string` helper that
  hashes the label to one of a fixed palette of ANSI colors (stable per app across the session), and
  update `printLogLine` (`control.go:511`) to wrap the `name#idx` prefix in that color when
  `isTerminal(w)` is true. Honor `NO_COLOR` consistency with the rest of the CLI (if a shared
  color gate exists, reuse it; otherwise `isTerminal` alone is sufficient and matches `printProcs`).

No proto, daemon, or fleet changes — this is purely client-side rendering of the existing stream.
(The same prefix colorization can later be applied to `fleet logs` rendering at `fleet.go:150`, but
that path is history-fetch, not live; it is optional polish, not required here.)

---

## Cross-cutting plumbing

### Wire propagation for `max_memory_restart`

The field must survive `start` (gRPC), `save`/`resurrect` (JSON — already covered by the struct
tag), and fleet deploys. Add to the wire `AppSpec`:

- **proto** (`proto/marshal/v1/daemon.proto`, message `AppSpec`): `int64 max_memory_restart = 12;`
- **config → AppSpec** (three construction sites): `cmd/marshal/control.go:42`,
  `internal/dashboard/apps.go:161`, `internal/dashboard/apps.go:178` — set
  `MaxMemoryRestart: int64(app.MaxMemoryRestart.Bytes)`.
- **AppSpec → config** (`internal/daemon/convert.go:16`, `appSpecToConfig`): set
  `app.MaxMemoryRestart = config.ByteSize{Bytes: uint64(s.GetMaxMemoryRestart())}`.

### Fleet + dashboard for `reset` / `flush`

`reset` and `flush` become routable control actions so they work across the fleet and from the web UI:

- **Fleet control**: add `reset` and `flush` to the `FleetControl` action set and the agent-side
  `handleFleetCommand` dispatch (routes to the daemon `Reset`/`Flush` paths).
- **Fleet CLI**: `marshal fleet reset <agent> <name|id|all>` and
  `marshal fleet flush <agent> [name|id|all]`.
- **Dashboard**: add `reset` and `flush` buttons to the per-app control menu (alongside
  stop/restart/delete/reload) and wire `/api/control` to accept the two new actions.

`max_memory_restart` flows through the existing app-create form / config path (one new optional
field in the create-app UI and `apps.go` spec builder); no new control action.

### Tests (TDD — failing test first, then implementation)

- `supervisor`: `ResetCounters` zeroes both counters (drive a couple of restarts first).
- `eventstore`: `DeleteLabels` removes only the named labels' rows; returns the count.
- `logs`: `Sink.Truncate` empties active files, removes rotated backups, clears the ring; a write
  after truncate still lands.
- `memguard`: `Check` fires `restart` exactly at the threshold, not before; resets the counter when
  a sample drops back under the limit; `Remove` drops limit + breach state.
- `config`: `ByteSize` parse table (`300M`, `1G`, `512K`, `1048576`, bad input → error); JSON
  round-trip through dump.json.
- `daemon`: e2e `Reset` and `Flush` RPCs over the test harness (counter → 0; log files emptied).
- `convert`: `appSpecToConfig` carries `max_memory_restart`.
- `cmd/marshal`: `labelColor` is stable per label and within the palette; `printLogLine` emits the
  colored prefix only when the writer is a terminal (plain when not).

### Docs & conventions

- `CHANGELOG.md` `[Unreleased]` → **Added**: the three features (note the multi-instance
  memory-restart limitation).
- Handoff doc at `docs/handoffs/2026-06-26-reset-flush-memlimit.md`.
- Live demo per the project convention: start a scratch daemon on standard ports, run demo apps,
  exercise `reset`, `flush`, and a deliberately memory-growing app to observe the auto-restart,
  confirm in CLI + dashboard, then tear down (kill demo daemon by data dir; no broad pkill).

## Out of scope (backlog)

**Next spec — "live observability" (target v0.13.0):** a full-screen TUI monitor (`marshal monit`,
`pm2 monit` equivalent — process list + live CPU/mem alongside a streaming log pane, keyboard
navigable) and **true live fleet log streaming** (today `fleet logs` fetches history; this would add
a streaming server→client path). Both deserve their own design — notably the TUI library choice
(bubbletea/tview), layout, keybindings, and refresh loop — so they are deliberately not in this spec.

**Further backlog:** HTTP health checks, `cron_restart`, `--watch`, `marshal prune`, per-instance
memory restart, and applying prefix colorization to the `fleet logs` render path.
