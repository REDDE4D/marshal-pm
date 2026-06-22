# Marshal — M29 Agent Connect-Command Generator — Handoff

**Date:** 2026-06-22
**Branch:** `m29-agent-connect-command` (off `dev`), all tasks implemented + reviewed + a
Critical fixed + live-demoed end-to-end; **ready to merge `--no-ff` into `dev`**.

This milestone was inserted ahead of the previously-planned coalescing (now M30) and Signal UI
(now M31) at the user's request, after they asked for a dashboard way to link new agents.

---

## TL;DR

The dashboard can now **generate a copy-paste command to connect a new agent**. An
authenticated operator opens **Overview → "+ connect agent"**, optionally edits the agent name
and address, clicks **Generate**, and gets a shell one-liner that writes a `marshal.yaml`
(`server:` block with address + name + a freshly minted enroll token + the server fingerprint)
and runs `marshal start`. Running it on a new host enrolls that host into the fleet.

Because the server stores only the **hash** of the enroll token, "generate" **mints a fresh
token** by rotating the single shared enroll token — done through the running server's
in-memory `AuthStore`, so it is immediately effective (no restart, unlike the CLI on-disk
`token --rotate` path). Generating again supersedes a prior unused command; already-enrolled
agents are unaffected (they use per-agent tokens).

Built spec → plan → 3 subagent-driven TDD tasks (each reviewed clean) → opus whole-branch
review (**found 1 Critical**) → C1 fix → live fleet demo (enrolled two real agents).
Spec: `docs/superpowers/specs/2026-06-22-m29-agent-connect-command-design.md`.
Plan: `docs/superpowers/plans/2026-06-22-m29-agent-connect-command.md`.

## What changed (by file)

- **`internal/dashboard/connect.go`** (new) — `EnrollMinter` interface
  (`RotateEnrollToken() (string, error)`, `Fingerprint() string`, `FleetAddress() string`);
  `connectToken` handler for `POST /api/fleet/connect-token` (mint via rotate, return
  `{token, fingerprint, default_address}` once, **never log the token**, `nil` minter → 503);
  `defaultConnectAddress(reqHost, fleetAddr)` (request host + fleet port via
  `net.SplitHostPort`/`JoinHostPort`).
- **`internal/dashboard/handlers.go`** — `handler.enroll EnrollMinter` field; route
  `POST /api/fleet/connect-token` registered behind `requireSession`.
- **`internal/dashboard/server.go`** — `Serve` gains a trailing `enroll EnrollMinter` param;
  `h.enroll = enroll` set alongside `h.notifs`.
- **`internal/server/enroll.go`** (new) — `enrollMinter` adapter: `RotateEnrollToken()` calls
  the unexported in-memory `auth.rotate("enroll")`; `Fingerprint()`/`FleetAddress()` return the
  values captured at wiring time.
- **`internal/server/server.go`** — builds `enrollMinter{auth, fp, lis.Addr().String()}` and
  passes it into `dashboard.Serve` in `ServeDir`.
- **`internal/config/config.go`** — **C1 fix**: `validate()` now errors on zero apps only when
  there is *also* no server block (`len(c.Apps)==0 && c.Server==nil`). A server-only config is
  valid (a fleet agent starts with no apps and receives them later).
- **`web/src/api.ts`** — `ConnectInfo` type + `connectToken(address?, name?)`.
- **`web/src/ConnectAgentModal.tsx`** (new) — the modal (name/address inputs, Generate,
  rendered one-liner, copy button, show-once/rotation warning). Function-first styling.
- **`web/src/Overview.tsx`** — `+ connect agent` topbar button + modal wiring.
- **`internal/dashboard/dist`** — regenerated embedded bundle (committed).
- **`CHANGELOG.md`** — `[Unreleased]` Added (Connect-an-agent) + Fixed (server-only config).
- Tests: `connect_test.go` (200 fields + RotateEnrollToken-called-once, 401 no session, 503
  nil minter, `defaultConnectAddress` table); `enroll_test.go` (adapter: fresh token each
  rotate, new token verifies, rotated-out token rejected); `config_test.go`
  (`TestServerOnlyConfigIsValid`, `TestNoServerNoAppsIsInvalid`).

## Key decisions / non-obvious

- **Generate = rotate**, forced by the hash-only token storage: the server can't show an
  existing token, so a new one is minted. Rotating via the **running** `AuthStore` is what
  makes it immediately usable — the CLI `token --rotate` path writes on-disk state a running
  server only picks up via its 3 s `ReloadLoop`.
- **`EnrollMinter` interface in `dashboard`, adapter in `server`** — keeps the dashboard
  decoupled and unit-testable with a fake; the adapter (same package as `AuthStore`) reaches
  the unexported `rotate`.
- **C1 was a real product gap, not just a demo bug** — a fleet agent legitimately runs with
  zero apps; `validate()` rejecting that blocked the entire feature. The whole-branch review
  caught it by building and running the generated command; the live demo then proved the fix.
- **Security**: token returned once in the TLS response body, never logged; endpoint
  session-gated (401 unauth); rotation never touches `data.Agents`; the heredoc is
  single-quoted so interpolated name/address can't trigger shell expansion.

## Whole-branch review (opus) — verdict: CHANGES REQUIRED → resolved

- **Critical C1**: generated server-only `marshal.yaml` was rejected by `marshal start`
  ("config has no apps"). **Fixed** in `bc3e454` (validate allows zero apps with a server
  block) + tests; verified by the live demo below.
- **Important**: none. Token exposure, rotation safety, `AuthStore` locking (rotate/verify
  serialize on `a.mu`; `-race` green), `defaultConnectAddress` edge cases, and heredoc
  injection were all reviewed and hold up.
- **Minor (deferred, no fix)**: unused decoded `connectTokenReq` body fields (frontend-owned by
  design); handler test asserts `:9000` suffix not exact host (table test covers exact);
  `Serve` doc-comment doesn't mention `enroll`; frontend sends `""` vs omitting name/address;
  copy button lacks `.catch()`. None affect correctness; candidates for the M31 UI pass.

## Live demo result (2026-06-22, scratch `/tmp/marshal-m29-demo`, server `:9000`/`:9001`)

- `POST /api/fleet/connect-token` (authenticated) returned `{token, fingerprint,
  default_address}`. Assembled the generated **server-only** `marshal.yaml` and ran
  `marshal start` on a fresh agent data dir → **`web-prod-1` enrolled + `connected=True`**
  (the exact case C1 fixed).
- **Rotation/invalidation**: re-generating produced a different token; enrolled **`web-prod-2`**
  with the new token; a fresh agent using the **old** token was **rejected** (absent from
  `server agent ls`). Final roster: `web-prod-1` + `web-prod-2` only.
- **UI** (Playwright): the **"+ connect agent"** modal rendered the full command (server block
  + entered name + token + fingerprint + `marshal start`) and the show-once/rotation warning;
  Overview showed 2/2 agents. Screenshot `/tmp/m29-connect-modal.png` (removed with scratch).
- **Teardown** by data dir + PID: three demo agent daemons + the server stopped; standing
  launchd daemon (pid 3119) preserved; `pgrep -fl marshal` clean; ports freed; scratch removed.

## How to build / run / test

```bash
go test ./... -race -count=1     # all 25 pkgs green (verified)
go vet ./... ; gofmt -l .        # clean / silent (verified)
make ui                          # web/ → internal/dashboard/dist (tracked, embedded)
make build                       # version-stamped binary
```

## Known issues / deferred

- The Minor review items above (doc comment, frontend `.catch()`, omit-vs-empty) — fold into
  the M31 UI pass.
- UI is function-first (Signal/M31 styling pending).
- The generated command assumes the operator can reach the server at `default_address`
  (request host + fleet port); for hosts behind NAT/other interfaces the operator edits the
  address field before generating.

## Concrete next step

1. **Merge `m29-agent-connect-command` → `dev` (`--no-ff`)** via finishing-a-development-branch.
2. Resume the original "do all four" plan, now shifted by this insertion:
   - **M30 — Alert/recovery coalescing/digest** (buffering / delayed delivery to merge
     transient alert+recovery pairs).
   - **M31 — Signal/M19 UI styling pass** (restyle the dashboard, incl. the M28 cooldown rows
     and this M29 modal; clear the deferred Minor UI items).
3. Cut a release (v0.3.0) when ready: move `CHANGELOG.md` `[Unreleased]` into a dated section,
   merge `dev → main` (`--no-ff`), tag, push.

The full SDD ledger is in `.superpowers/sdd/progress.md` (git-ignored).
