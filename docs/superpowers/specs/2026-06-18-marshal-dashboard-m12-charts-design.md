# M12 — Metric Charts (Dashboard) — Design

**Date:** 2026-06-18
**Status:** approved (brainstorming) — pending implementation plan
**Predecessor:** M11 web dashboard (`docs/superpowers/specs/2026-06-18-marshal-dashboard-m11-design.md`)

## Goal

Turn the dashboard's static CPU/mem numbers into live charts. Each process row in the
fleet table gets two inline SVG **sparklines** (CPU%, memory). Clicking a process row
expands a **detail panel** with larger time-series charts and a selectable time window
(5m / 1h / 6h / 24h). Charts are hand-rolled SVG — zero runtime charting dependencies,
keeping Marshal's self-contained, committed-`dist` ethos.

No new storage is required: the central server already persists per-agent CPU/mem history
in SQLite (`internal/metricstore`, per-agent stores managed by `internal/server/stores.go`)
and exposes `*Server.FleetMetricsHistory` over gRPC. M12 surfaces that data to the browser.

## Scope (decided in brainstorming)

- **Visual shape:** inline sparklines (always visible) **and** click-to-expand detail panel.
- **Metrics:** CPU% and memory (RSS), both.
- **Rendering:** hand-rolled SVG components (no charting library).
- **Detail window:** selectable buttons (5m / 1h / 6h / 24h).
- **Sparkline feed:** one batched endpoint, polled on a slower (~10s) cadence, independent
  of the existing 2s fleet poll.

## Backend

### Server refactor — extract history query to `*stores`

The metrics-history query logic currently lives inside `*Server.FleetMetricsHistory`
(label matching → `metricstore.AutoBucketMs` → per-label `Query` → `metricstore.MergeBuckets`).
Extract it to a method on `*stores`, where the per-agent stores live:

```go
// internal/server/stores.go
// History returns merged CPU/mem buckets for an agent's selector (app name or
// "app#instance"). Matches the selector exactly or as a "selector#" prefix, merges
// across instances, oldest first. A missing agent returns (nil, nil).
func (s *stores) History(agent, selector string, sinceMs, bucketMs int64) ([]metricstore.Bucket, error)
```

- `sinceMs <= 0` → caller-side default applies before the call (see endpoint); `History`
  treats `sinceMs` as a window width in ms and computes `lowerMs = now - sinceMs`.
- `bucketMs` passed through `metricstore.AutoBucketMs(sinceMs, bucketMs)`.
- Missing agent (`!s.has(agent)`) → `(nil, nil)` (not an error). The proto wrapper keeps
  its existing `NotFound` behavior by checking `has` itself.

`*Server.FleetMetricsHistory` becomes a thin proto wrapper: keep the `NotFound` guard, call
`s.stores.History(...)`, map `[]metricstore.Bucket` → `[]*pb.MetricBucket`. Behavior is
unchanged; the existing proto-level test is the regression guard.

### Dashboard dependency interface

Follow the existing `FleetLister` pattern: the dashboard package **defines** the interface,
the server type **satisfies** it. No import cycle (server imports dashboard, never the
reverse). The dashboard imports the leaf `metricstore` package only.

```go
// internal/dashboard/metrics.go
type MetricsHistory interface {
    History(agent, selector string, sinceMs, bucketMs int64) ([]metricstore.Bucket, error)
}
```

`*server.stores` satisfies `MetricsHistory`.

### New HTTP route: `GET /api/metrics`

Session-guarded via the existing `requireSession` middleware, exactly like `/api/fleet`.

- `?since=<ms>` (default 300000 = 5min) → **batched**: iterate the lister's agents and each
  agent's proc names, call `History(agent, procName, since, 0)` (auto bucket) for each.
- `?agent=X&selector=Y[&since=<ms>][&bucket=<ms>]` → restrict to a single series for the
  detail panel. `since` default 3600000 (1h) when only a single series is requested.

One JSON response shape for both (single-series simply returns one agent / one proc):

```json
[{"agent":"dev-1","procs":[{"name":"ticker","buckets":[
  {"ts":1781770000,"cpu_avg":0.03,"cpu_max":0.05,"mem_avg":3244032,"mem_max":3300000}
]}]}]
```

`buckets` may be empty for a proc with no stored history; that is normal, not an error.

A small `metricsView` helper maps `[]metricstore.Bucket` → the JSON `buckets` structs
(mirrors `fleetView`).

### Wiring

`newHandler` / `NewHandler` / `Serve` gain a `MetricsHistory` parameter. In
`internal/server/server.go` `ServeDir`, the `*stores` value `ss` and the registry `reg`
are both already in scope at the `dashboard.Serve` call site (the `*Server` is not — it is
built later inside the inner `Serve`). Pass `ss`:

```go
go dashboard.Serve(ctx, httpAddr, reg, ss, auth, cert)
```

Empty `httpAddr` preserves identical prior behavior.

## Frontend (`web/src/`)

### `Sparkline.tsx` — presentational, ~30 lines

Props: `points: number[]`, `width`, `height`, `color`. Renders a single `<polyline>` in a
normalized viewBox; autoscale min→max; a flat line when all values are equal or the series
is empty. No axes, labels, or interaction.

### `MetricChart.tsx` — detail chart, ~80 lines

Props: `buckets`, `metric: 'cpu' | 'mem'`. Larger SVG with: min/max Y gridlines + value
labels, a few X-axis time tick labels, the **avg** series as a solid line and the **max**
series as a fainter line above it. Memory values formatted to a readable unit (e.g. MB).

### `api.ts`

- `getMetrics(sinceMs)` → batched array (sparklines).
- `getMetricsForProc(agent, selector, sinceMs, bucketMs)` → single series (detail panel).

Both throw on 401, matching the existing `getFleet`.

### `Fleet.tsx` integration

- **Sparklines:** a second poll loop calls `getMetrics(300000)` every **10s**, independent
  of the existing 2s fleet poll. Results keyed `agent → procName → buckets`. Each process
  row renders two small `<Sparkline>`s (CPU, mem) beside the existing numeric cells. A proc
  with no history renders an empty/placeholder sparkline.
- **Expand:** clicking a process row toggles an expanded panel beneath it. A single
  `expanded` key in component state → one open panel at a time. The panel shows window
  buttons **5m / 1h / 6h / 24h**, two `<MetricChart>`s (CPU, mem), and polls
  `getMetricsForProc` for that proc on the selected window every **10s** while open.
  Changing the window refetches immediately.

### Data flow

```
agent → gRPC batch → server stores (SQLite)
                          │
        GET /api/metrics  ▼   (dashboard, session-guarded)
  sparklines: batched, since=5m, poll 10s
  detail:     agent+selector, since=window, poll 10s while open
                          │
                          ▼
   Fleet.tsx ── Sparkline (rows) / MetricChart (expanded panel)
```

## Testing (TDD)

- **`stores.History`** — table tests: selector matches `app` and `app#instance`; bucketing
  via `AutoBucketMs`; merge across instances; missing agent → `(nil, nil)`.
- **`/api/metrics` handler** — `httptest`: 401 without cookie; batched shape across a fake
  lister + fake `MetricsHistory`; single-series filter via `agent`+`selector`; default vs
  explicit `since`/`bucket`.
- **`FleetMetricsHistory`** — existing proto-level test stays green after the refactor
  (regression guard).
- **SPA rebuild:** `make ui` regenerates the committed `internal/dashboard/dist/`; Go tests
  remain Node-free.
- **Gate before finishing:** `go test ./... -race -count=1`, `gofmt -l .`, `go vet ./...`.
- **Live demo** (per CLAUDE.md convention): start a scratch server with `--http-listen`,
  enroll a demo agent, confirm a sparkline populates and the detail panel renders in the
  browser; tear down and verify no orphan `marshal` processes.

## Deferred (not in M12)

- Hover tooltips with exact point values.
- Chart zoom/pan.
- Y-unit formatting beyond basic bytes→MB.
- Remembering the expanded panel / selected window across reloads.
- Process controls, log tailing, multi-user accounts (later milestones, per M11 handoff).

## Files touched

- `internal/server/stores.go` — add `History`.
- `internal/server/server.go` — refactor `FleetMetricsHistory` to call `History`; pass `ss`
  to `dashboard.Serve`.
- `internal/dashboard/metrics.go` (new) — `MetricsHistory` interface, `metricsView`,
  `GET /api/metrics` handler.
- `internal/dashboard/handlers.go`, `server.go` — thread the `MetricsHistory` dependency.
- `web/src/Sparkline.tsx`, `web/src/MetricChart.tsx` (new); `web/src/api.ts`,
  `web/src/Fleet.tsx` (edits).
- `internal/dashboard/dist/` — rebuilt committed artifact.
- Tests alongside each new/changed unit.
