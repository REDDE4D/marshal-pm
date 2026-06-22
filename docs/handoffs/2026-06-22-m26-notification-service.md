# Marshal — M26: Notification Service — Handoff

**Date:** 2026-06-22
**Branch:** `m26-notification-service` (all work done, every task reviewed, whole-branch
reviewed clean, live-demoed end-to-end; **ready to merge `--no-ff` to `main`**).
**Read the M22 handoff `2026-06-19-m22-managed-git-credentials.md` first** (the credstore +
master-key sealing this reuses) and the M25 handoff `2026-06-21-m25-ssh-deploy-keys.md`
(the credstore type model and the live-demo/teardown conventions this follows).

---

## TL;DR

Marshal can now **push fleet alerts** to user-configured channels when something goes
wrong. A **server-side detector** polls the existing fleet registry every 2s, diffs
snapshots into **events** (`crash`, `restart_loop`, `agent_down`, `agent_up`,
`deploy_fail`), a **dispatcher** gates each event by a per-`(agent,process,type)`
**cooldown** (default 300s), matches it against a **rules engine** (event-type + agent +
process → channels), and **fans out** to one or more channels behind a `Sender`
interface: **generic webhook (HMAC-signed), Telegram, Slack, email (SMTP)**. Channels +
rules + **sealed per-channel secrets** persist in `notifications.json`, reusing the
credstore's AES-256-GCM master key via a new shared **`internal/secretbox`** package. A
**dashboard page** (`#/notifications`) manages channels/rules/cooldown with a **Test**
button; secrets are **write-only** over the API. **No proto or agent changes** — detection
is purely server-side over `registry.List()`.

Built spec → plan → 13 subagent-driven TDD tasks (each task-reviewed; Tasks 4 and 7 had a
one-pass fix loop) → whole-branch review (opus, "ready to merge — clean") → 2 hardening
fixes → live fleet demo. Spec: `docs/superpowers/specs/2026-06-21-notification-service-design.md`.
Plan: `docs/superpowers/plans/2026-06-21-notification-service.md`.

## What changed this session (by package)

- **`internal/secretbox/`** (new): shared AES-256-GCM seal/open extracted from credstore.
  `Load(dir) (*Box, error)` (resolves `$MARSHAL_MASTER_KEY` or `<dir>/master.key`, 0600),
  `FromKey([32]byte)`, `(*Box).Seal([]byte) (nonceB64, cipherB64, err)`, `(*Box).Open(...)`.
- **`internal/credstore/credstore.go`**: refactored onto `secretbox` (struct now holds a
  `*secretbox.Box`; `seal`/`openCipher` delegate). **On-disk format and public API
  unchanged** — the existing credstore tests pass unmodified (that is the compat proof).
- **`internal/notify/`** (new):
  - `model.go` — `EventType` consts, `Event`, `Channel`, `Rule`, `Settings`, `Message`,
    `Sender` interface, and `Rule.Matches` (disabled→false; empty Events→any; agent/process
    `""`/`"*"`→any else exact).
  - `store.go` — `notifications.json` persistence (`Open(dir, box)`, channel/rule/settings
    CRUD). Per-channel secrets are a JSON `map[string]string` sealed into
    `secret_nonce`/`secret_cipher`; `Channels()` returns metadata only; `Config` maps are
    `maps.Clone`d at both boundaries (no aliasing). Empty/nil secrets on update preserve the
    existing sealed blob. Default cooldown 300.
  - `detector.go` — pure `diff(prev, next []*pb.AgentState, now) []Event` (seeds silently on
    unknown agent/process/nil-prev → no replay on restart/enroll) + `Detector.Run(ctx)` 2s
    poll loop, `Lister`/`Emitter` interfaces.
  - `render.go` — `render(Event) Message` (single-line Title + Body).
  - `dispatcher.go` — `Emit(e)`: **cooldown gate** (`(agent,process,type)` key, mutex'd
    `last` map) → `matchChannels` (dedup across rules, enabled-only, live store read) →
    fan-out (goroutine per channel by default; `WithSyncDelivery()` for tests). Each
    delivery **rebuilds the Sender from the live store + freshly-decrypted secrets** (so
    dashboard edits are hot — no restart), best-effort with ≤3 retries; failures logged,
    never fatal, never block detection.
- **`internal/notify/channels/`** (new): `New(notify.Channel, secrets) (notify.Sender, err)`
  factory + an injectable `httpDoer` seam (`var httpClient`) and `doExpectOK`. Four
  transports: `webhook.go` (JSON `{type,agent,process,detail,time}` + optional
  `X-Marshal-Signature: sha256=<hmac>` over the exact body), `telegram.go`
  (`bot<token>/sendMessage`), `slack.go` (incoming webhook), `email.go` (`net/smtp`,
  `smtpSend` seam, `PlainAuth` only when username set, **Subject CRLF-stripped** against
  header injection).
- **`internal/dashboard/notifications.go`** (new) + `handlers.go`: `Notifications` interface
  (`*notify.Store` satisfies it) + 7 session-guarded routes — `GET /api/notifications`
  (channels redacted to `has_secret` bool, **never the secret**), `POST`/`DELETE`
  `…/channels[/{name}]`, `POST …/channels/{name}/test` (returns `{ok,error?}` 200, 404 if
  unknown), `POST …/rules`, `DELETE …/rules/{name}`, `PUT …/settings`. Handler gained
  `notifs Notifications` + `notifBuild notify.BuildFunc` fields.
- **`internal/dashboard/server.go`** + **`internal/server/server.go`**: `dashboard.Serve`
  gained two trailing params (`notifs`, `notifBuild`). Server startup now: `secretbox.Load`
  → `notify.Open` → `NewDispatcher(store, channels.New)` → `NewDetector(reg, disp, 2s)` →
  `go det.Run(ctx)`; failure logs "notifications disabled" and leaves the dashboard var nil
  (endpoints 503). Detector goroutine bound to the server ctx (stops on shutdown).
- **web** (`web/src/`): new `#/notifications` route (`router.ts`), 7 API fns + 4 types in
  `api.ts`, `Notifications.tsx` (Channels / Rules / Settings sections; type-aware channel
  form with write-only password inputs; rule builder with event checkboxes + agent/process
  matchers + channel multiselect; Test/Delete buttons), nav link in `Overview.tsx` topbar.
  `make ui` rebuilt the embedded `internal/dashboard/dist`.

## Key decisions / non-obvious

- **Server-side snapshot diffing, not agent-side events** — the server already aggregates
  the fleet, so detection is purely additive (no proto/agent change). Tradeoff (accepted,
  documented): a fast `online→restarting→online` flap that completes **between** two 2s
  polls is invisible — the detector samples, it doesn't subscribe. Durable states
  (`errored`=restart_loop, `failed`=deploy_fail, agent offline) are caught reliably.
- **`crash` (entering `restarting`) vs `restart_loop` (entering `errored`)** — a process
  that flaps with sub-`MinUptime` uptime racks up "unstable" restarts and eventually lands
  in `errored` (durable). A process that exits cleanly → `stopped` is **not** alerted.
- **One master key, two stores** — `secretbox` is shared so credstore and the notify store
  seal under the same `master.key`. The credstore refactor is behavior-identical.
- **Hot config** — the dispatcher reads the live store and rebuilds the Sender on **every**
  delivery, so dashboard edits to a channel/rule/cooldown take effect on the next event with
  no restart (single process). (An earlier per-channel Sender cache that broke this was
  caught in review and removed — Task 7.)
- **Secrets write-only** — `notify.Channel` has no secret field at all; the API view is a
  manual `channelView` projection exposing only `has_secret`. Redaction is structural, not
  policy.

## Whole-branch review (opus) — verdict: ready to merge (clean)

No Critical, no Important. Reviewer adversarially verified the two load-bearing invariants:
(1) **secret safety end-to-end** — sealed at rest (no plaintext in `notifications.json`),
structurally redacted from the API, never logged, and the Telegram bot token sits in the
URL **path** not Host so `doExpectOK`'s `"%s returned %d"` can't leak it; (2) **credstore
refactor byte-for-byte identical** (unmodified tests green). 2 hardening fixes taken
(commit `802e366`): `DeleteChannel`/`DeleteRule` now log flush errors; email Subject strips
CRLF (header-injection guard) + regression test.

## Live demo result (2026-06-22, scratch `/tmp/marshal-m26-demo`, server `:9000`/`:9001`)

Real fleet (demo server + agent `dev-1`) on the standard demo ports, scratch data dir, a
local Python webhook **sink** on `:9099`, a `stable` app + a `crasher` app:
- **Configured** a `webhook` channel (HMAC secret) + a rule via the dashboard API after
  logging in. `GET /api/notifications` returned `"has_secret":true` with **no** hmac value;
  on-disk `notifications.json` held `secret_cipher` and **zero** plaintext `shh-secret`.
- **`agent_down` + `agent_up`** delivered to the sink when the demo daemon was killed and
  restarted (`"agent stopped reporting"` / `"agent reconnected"`).
- **`restart_loop`** delivered once (`"gave up after 4 restarts"`) when the instant-crasher
  exhausted its restarts → `errored`.
- **Cooldown proven**: two crasher restarts inside the 300s window produced **exactly one**
  `restart_loop` delivery (the second was suppressed).
- **Test-send**: `POST …/channels/sink/test` delivered a `type:"test"` payload and returned
  `{ok:true}`; an unknown channel returned **404**.
- **Rendered UI** (Playwright, screenshot captured): the `#/notifications` page — Channels
  (sink, 🔒 redaction, Test button), the type-aware add-channel form, the Rules builder, and
  the cooldown Setting.
- **`crash` (fast-flap)** was intentionally NOT captured live — the 2s sampling misses
  sub-poll flaps (the documented tradeoff); the `crash` transition is covered by
  `detector_test.go`. **`deploy_fail`** not exercised live (needs a bad git deploy); its
  detection path (entering `failed`) is identical to the proven events and unit-tested.
- Teardown by data dir + pid only; the user's standing launchd daemon (pid 3119) preserved;
  `pgrep` shows no demo orphans; scratch dir removed.

## Known issues / deferred

- **email `Send` ignores its `ctx`** — `smtp.SendMail` takes no context, so the dispatcher's
  10s per-attempt timeout is illusory for SMTP. Isolated to its own fan-out goroutine, so a
  hung MX can't block detection — but the send goroutine could linger. Candidate fix: a
  `net.Dialer` with deadline + manual SMTP. (Non-blocking.)
- **Cooldown map (`dispatcher.last`) is never pruned** — bounded by fleet × processes × 5
  event-types (kilobytes) for normal fleets; add opportunistic pruning if you expect churny
  / ephemeral process names.
- **`crash` event delivery is sampling-limited** (see demo) — fast flaps between 2s polls are
  missed. If sub-2s crash visibility matters, revisit agent-side event capture (new proto).
- **Cosmetic UI** (function-first per the design memory): the notifications page reuses
  existing classes and has no dedicated styling; fold into the eventual Signal/M19 pass.
- **Thin handler tests**: only `GET` + `POST channel` have dashboard tests; the symmetric
  handlers (`testChannel`/`deleteChannel`/`putRule`/`deleteRule`/`putSettings`) follow the
  same pattern but are untested.
- **Out of scope (as designed)**: glob/pattern matchers (exact-or-`*` only); recovery/
  "resolved" notices; digest/coalescing; per-rule severity/escalation; agent-side capture.

## How to build / run / test

```bash
go build -o marshal ./cmd/marshal
go test ./... -race -count=1     # all 24 pkgs green (cmd/marshal SIGINT test can flake under heavy load — re-run in isolation)
gofmt -l . ; go vet ./...        # silent / clean
make ui                          # web/ → internal/dashboard/dist (tracked, embedded)
```

Dashboard endpoints (behind session): `GET /api/notifications`; `POST`/`DELETE`
`/api/notifications/channels[/{name}]`; `POST /api/notifications/channels/{name}/test`;
`POST /api/notifications/rules`; `DELETE /api/notifications/rules/{name}`;
`PUT /api/notifications/settings`.

## Concrete next step

1. **Merge `m26-notification-service` to `main`** (`--no-ff`), then update the project's
   next-steps. The full SDD ledger is in `.superpowers/sdd/progress.md` (git-ignored).
2. Candidate follow-ups (smallest → largest):
   - The deferred hardening: bound the SMTP send with a real deadline; prune the cooldown
     map; flesh out the dashboard handler tests.
   - **Provider polish**: Slack Block Kit formatting, Telegram message formatting, an
     auth-required HTTPS-remote demo (the last M22-era gap).
   - **Recovery notices** (send a "resolved" when a condition clears) — needs clear-transition
     state tracking; a natural M27.
   - The **Signal/M19** UI styling pass (now spans credentials + notifications).
