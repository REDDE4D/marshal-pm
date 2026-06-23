# Marshal — `dev` status & next steps — Handoff

**Date:** 2026-06-23
**Branch:** `dev` (integration), pushed to `origin/dev` at `835cfcd`. `main` is unchanged
(still at `v0.2.0`); no release cut this session.

This is an overarching status/next-steps handoff. The per-milestone handoffs
(`2026-06-22-m28-notification-hardening.md`, `2026-06-22-m29-agent-connect-command.md`) have
the deep detail; read this first to see where things stand and what to do next.

---

## Where we are

The user asked to tackle four deferred items from the M27 handoff. We scoped them into
sequential milestones (each its own spec → plan → subagent-driven TDD → opus whole-branch
review → live demo → handoff → `--no-ff` merge). Two are **done and merged to `dev`**; a
user-requested feature was **inserted** ahead of the rest; two remain.

Completed & merged to `dev` this session:

1. **M28 — Notification hardening** (merge `609f611`)
   - Per-event-type cooldown overrides: `Settings.CooldownOverrides map[EventType]int` +
     `cooldownFor` helper; map-presence semantics (absent = inherit global; present incl. `0` =
     use that). Dispatcher `allow()` looks up per-type rate.
   - Bounded cooldown map: `Dispatcher.last` is pruned lazily inside `allow()`'s locked
     section (`map[string]cooldownEntry`), closing the unbounded-growth carryover.
   - Six-row per-event override block in the settings UI (function-first).
   - Opus review: READY TO MERGE (one doc-only wording fix re: prune vs a runtime cooldown
     *increase* — benign, never suppresses an alert).

2. **M29 — Agent connect-command generator** (merge `583ceb2`) — *inserted at user request*
   - Dashboard **"+ connect agent"** (Overview) → `POST /api/fleet/connect-token`
     (session-gated) mints a fresh enroll token by rotating the single shared token through the
     running in-memory `AuthStore` (immediately effective), returns it **once** with the cert
     fingerprint + a default address; the modal renders a copy-paste `marshal.yaml` +
     `marshal start` one-liner. `EnrollMinter` interface (dashboard) + adapter (server).
   - Opus review caught a **Critical**: a server-only `marshal.yaml` (no `apps:`) was rejected
     by `marshal start` ("config has no apps"), so the generated command was broken. Fixed
     (`bc3e454`): `validate()` allows zero apps when a `server:` block is present (a fleet agent
     legitimately starts with no apps). Live demo then enrolled **two real agents** end-to-end
     and proved an old (rotated-out) token is rejected.

3. **Connect-modal overflow fix** (merge `835cfcd`) — user spotted the generated command
   running off the modal edge. The command `<pre>` was unstyled (CSS deferred to the UI pass),
   so long token/fingerprint lines didn't wrap. Added `.connect-cmd` (wrap + contain +
   code-block look) and `.warn` to `styles.css`, regenerated the embedded bundle; verified in a
   live browser render.

State checks (all green this session): `go test ./... -race -count=1`, `go vet ./...`,
`gofmt -l .`, `make build`. `CHANGELOG.md` `[Unreleased]` holds all of the above (Added:
per-event cooldown, connect-an-agent; Fixed: bounded cooldown map, server-only config,
modal overflow).

## Conventions reminder (so the next session doesn't trip)

- Work on a branch off **`dev`**; `main` only moves on a release. Merge features back with
  `--no-ff`. Update `CHANGELOG.md` `[Unreleased]` as you go.
- After each milestone: write a handoff in `docs/handoffs/`, then run a **live demo** (scratch
  `XDG_DATA_HOME`, server on `:9000`/`:9001`, webhook sink on `:9099`), tear it down by
  data-dir + PID, and confirm `pgrep -fl marshal` shows only the user's standing launchd daemon
  (**pid 3119** — never kill it).
- Auth setup happens while the server is **down** for the CLI path; but note M29 proved that
  rotating the enroll token *through the running server* (the dashboard endpoint) is immediately
  effective, unlike the CLI on-disk `token --rotate` (a running server only picks that up via
  its 3 s `ReloadLoop`).
- SDD ledger lives at `.superpowers/sdd/progress.md` (git-ignored); it currently reflects M29.

## Next steps (in order)

1. **M30 — Alert/recovery coalescing / digest.** The architecturally heavier notification item:
   buffer/delay delivery so transient `online→restarting→online` pairs (crash + recovered) are
   merged/coalesced instead of sent as two notifications. This breaks the current synchronous
   fire-and-forget `Emit` model — start with a fresh **brainstorm** (debounce-suppress vs
   buffer-and-flush, the time window, scope). Spec → plan → TDD → review → demo → handoff.

2. **M31 — Signal / M19 UI styling pass.** The deliberate dashboard redesign (the
   "Signal" identity). Restyle everything that's been built function-first, specifically
   including the **M28 per-event cooldown rows** and the **M29 connect-agent modal**, and clear
   the deferred Minor UI items logged against M29:
   - `connectToken()` sends `address:""`/`name:""` rather than omitting them;
   - the copy button has no `.catch()` on `navigator.clipboard.writeText`;
   - `dashboard.Serve`'s doc-comment doesn't mention the new `enroll` param;
   - the connect modal's `connectTokenReq{Address,Name}` body fields are decoded but unused.

3. **Cut v0.3.0** once M30 (and optionally M31) land: move `CHANGELOG.md` `[Unreleased]` into a
   dated `## [0.3.0]` section, update the compare links, merge `dev → main` (`--no-ff`), tag
   `v0.3.0`, and push `main`, `dev`, and the tag. (CI cross-builds release binaries on the
   `v*` tag.)

## Immediate concrete next action

Start **M30** with the brainstorming skill (coalescing design), branching off `dev`.
