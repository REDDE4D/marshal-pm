# Dashboard redesign — brainstorm handoff

**Date:** 2026-06-23
**Branch:** `ui-redesign` (off `dev`). `main`/`dev` unchanged. No app code changed this session.

## Current state

Design exploration for the dashboard redesign is **done and approved**; implementation has **not
started**. The validated design is written up at
`docs/superpowers/specs/2026-06-23-dashboard-redesign-design.md` — **read it first.**

This supersedes the earlier conformance-only "UI consistency pass"
(`2026-06-23-ui-consistency-production-readiness.md`). The user upgraded the scope to a **full
redesign** + **full hardening** + **Notifications rewrite from scratch**.

## What happened this session

Ran the brainstorming skill with the **visual companion** (browser mockups). Explored directions;
the user steered to the **app.pm2.io** dashboard as the reference (screenshots in `ref/`), but
explicitly rejected the generic "AI app" look (colored-edge cards, gradient logos, bento grids).
Landed on a distinctive **"Marshal Instrument"** language: hairline-ruled continuous surfaces,
dense tabular ledgers + metric clusters, a **semantic muted multi-hue palette** (each metric type
owns a colour), a **left icon rail**, big light-weight stat numbers, per-row **quick actions**, a
**live-log modal** (with filtering), and a **dedicated Errors page**.

**The interactive prototype is the source of truth:**
`.superpowers/brainstorm/46891-1782222731/content/demo3.html` — a clickable multi-page demo
(Fleet → Detail+Files → Logs → Notifications → Credentials → Login, + Errors, + modals). Open it
in a browser when implementing. (`.superpowers/` is gitignored; the file persists locally.)

Key decisions are all captured in the spec: tokens, fonts (introduce **Inter** for chrome, keep
JetBrains Mono for data), shell, components, per-page layouts, **new data needs**, hardening
checklist, and implementation sequencing.

## Deferred / open issues (decide during planning)

- **Errors page** needs error-**signature grouping** the backend doesn't have yet (`getLogStats`
  is per-process counts only). Decide: UI shell on existing counts now / full backend first / defer.
- **"Show only real data":** several mockup metrics (host CPU/mem, net I/O, threads/fds, OS/arch)
  may not be collected. Confirm the agent's real metric surface and prune clusters before building.
- Fold in the four minor cleanups from the prior handoff (connect-modal clipboard `.catch()`,
  empty `address/name` in `connectToken()`, `dashboard.Serve` doc-comment, dead `connectTokenReq`
  fields).

## Concrete next step

Get user decisions on the two open questions (Errors scope; which metrics are real), then run the
**writing-plans** skill to turn the spec into an implementation plan, and execute on `ui-redesign`
(TDD where logic changes; `make ui` + `make build` + `go test ./... -race` before finishing).

## Build / run / test

```bash
make ui      # rebuild SPA into internal/dashboard/dist (embedded) — commit the bundle
make build
go test ./... -race -count=1
go vet ./... && gofmt -l .
```
