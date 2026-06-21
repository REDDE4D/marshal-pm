# Marshal — Notification Service (M26) — design

**Date:** 2026-06-21
**Status:** approved design, pre-implementation
**Branch:** `m26-notification-service`

## Goal

Push **fleet alerts** to user-configured channels when something goes wrong in the
fleet. The central server already aggregates fleet state (agents + per-process
snapshots); this feature watches that state for trouble, applies user-defined routing
rules, and delivers a message to one or more channels — **Telegram, Slack, email, and
generic webhook**.

This is a server-side feature: detection, routing, and delivery all live on the central
server, which is the single point that already sees the whole fleet. **No proto or agent
changes** are required.

## Scope

**In scope (v1):**
- Four event types: process **crash**, **restart-loop / max-restarts exceeded**, **agent
  disconnect/reconnect**, **deploy/redeploy failure**.
- Four channel transports: **generic webhook**, **Telegram**, **Slack**, **email (SMTP)**.
- A **full rules engine**: route by event type + agent + process → set of channels.
- **Per-event-key cooldown** to suppress alert storms from flapping processes.
- Dashboard UI to manage channels + rules, with a **test-send** button.
- Per-channel secrets sealed at rest, reusing the credstore's AES-256-GCM master key.

**Out of scope (deferred):**
- Glob/pattern matchers for agent/process (v1 is exact-match-or-wildcard only).
- Recovery/"resolved" notices when a condition clears (plain cooldown only).
- Digest/coalescing windows (cooldown only).
- Provider-specific richness beyond a basic message (e.g. Slack interactive actions).
- Per-rule severity levels, escalation, on-call schedules, acknowledgement.
- Agent-side event capture (server-side snapshot diffing is sufficient for all four
  event types).

## Architecture

Single-purpose units (plus the shared `secretbox` and the domain model):

| Unit | Package | Responsibility |
|------|---------|----------------|
| Domain model | `internal/notify` | `Event`, `Channel`, `Rule`, `Settings` types |
| Store | `internal/notify` | persist `notifications.json`; seal/unseal secrets |
| Secretbox | `internal/secretbox` | shared AES-256-GCM seal/open (extracted from credstore) |
| Detector | `internal/notify` | poll registry snapshots, diff, emit `Event`s |
| Dispatcher | `internal/notify` | cooldown gate → rule match → fan out to channels |
| Channels | `internal/notify/channels` | one transport each behind a `Channel` interface |
| Dashboard | `internal/dashboard` | HTTP CRUD + test-send, secrets write-only |
| Web UI | `web/src` | `#/notifications` management page |

Data flow:

```
agents → server.Registry (existing 2s snapshots)
              │  (poll + diff)
              ▼
          Detector ──Event──▶ Dispatcher ──cooldown──▶ rule match ──▶ [Channel…]
                                                                         │
                                            Store (channels+rules+settings, sealed secrets)
```

## Domain model (`internal/notify`)

```go
type EventType string

const (
    EventCrash       EventType = "crash"
    EventRestartLoop EventType = "restart_loop"
    EventAgentDown   EventType = "agent_down"
    EventAgentUp     EventType = "agent_up"
    EventDeployFail  EventType = "deploy_fail"
)

type Event struct {
    Type    EventType
    Agent   string    // agent name
    Process string    // "" for agent-level events (agent_down/agent_up)
    Detail  string    // human message: "exited code 1", "gave up after 5 restarts", "build failed"
    Time    time.Time
}
// cooldown key = (Agent, Process, Type)

type Channel struct {
    Name    string            // unique id, validated like credstore names
    Type    string            // "webhook" | "telegram" | "slack" | "email"
    Enabled bool
    Config  map[string]string // non-secret target config (see per-type below)
    // secret material sealed at rest; never serialized to the dashboard API
}

type Rule struct {
    Name     string
    Enabled  bool
    Events   []EventType // empty = all event types
    Agent    string      // "" or "*" = any; else exact agent name
    Process  string      // "" or "*" = any; else exact process name
    Channels []string    // channel names to fan out to
}

type Settings struct {
    CooldownSeconds int // default 300
}
```

### Per-channel config + secret split

| Type | `Config` (plaintext) | sealed secret |
|------|----------------------|---------------|
| webhook | `url` | optional HMAC signing secret |
| telegram | `chat_id` | bot token |
| slack | (none) | incoming-webhook URL |
| email | `host`, `port`, `from`, `to`, `username`, `tls` | SMTP password |

Secrets are stored as a single sealed JSON blob per channel (a `map[string]string`), so
the model is uniform across types and adding a new type needs no schema change.

### Rule matching

An `Event` matches a `Rule` when **all** hold:
- `rule.Enabled`
- `event.Type ∈ rule.Events` (or `rule.Events` is empty → matches any type)
- `rule.Agent` is `""`/`"*"` **or** equals `event.Agent`
- `rule.Process` is `""`/`"*"` **or** equals `event.Process`

On match, the event is delivered to every channel named in `rule.Channels` (deduplicated
across all matching rules, so two rules naming the same channel send once). Matchers are
**exact string or wildcard** in v1 — no glob.

## Detector (`internal/notify/detector.go`)

A goroutine started at server boot. On a ~2s ticker it calls `registry.List()` and diffs
each agent/process against its own previously-seen snapshot (kept in an internal
`map[agentName]agentState`). Transition rules:

- process `online → restarting` from a non-clean exit ⇒ `EventCrash` (Detail = exit code)
- process `→ errored` (supervisor gave up after `> MaxRestarts`) ⇒ `EventRestartLoop`
- deploy phase `→ failed` ⇒ `EventDeployFail` (Detail = phase/error)
- agent `connected → !connected` ⇒ `EventAgentDown`; `!connected → connected` ⇒ `EventAgentUp`
- `StateStopped` (user-initiated stop) is **not** an event

Clean first-observation (server just started, or a brand-new agent/process) seeds the
previous-state map **without** emitting events, so a restart of the server does not
replay history as fresh alerts.

The detector is pure logic over snapshots: its core diff is a table-driven function
`diff(prev, next) []Event` that is unit-tested without any server.

## Dispatcher (`internal/notify/dispatcher.go`)

Receives `Event`s (channel/queue) and for each:
1. **Cooldown gate** — look up `(Agent, Process, Type)` in a `map[key]time.Time`. If the
   last send was within `Settings.CooldownSeconds`, drop the event. Otherwise record now
   and continue.
2. **Rule match** — evaluate all rules, collect the deduplicated set of channel names.
3. **Fan out** — for each channel, render a `Message` and `Send` in its own goroutine,
   best-effort with a small bounded retry (e.g. 3 attempts, backoff). Failures are
   **logged**, never fatal — a broken channel cannot crash the server or block detection.

The cooldown gate is applied at the event level (before fan-out) so a flapping process is
suppressed globally across all channels.

## Channels (`internal/notify/channels/`)

```go
type Message struct {
    Title string
    Body  string
    Event notify.Event
}

type Channel interface {
    Send(ctx context.Context, m Message) error
}
```

Four implementations, each built from a `notify.Channel` config with its secret decrypted.
Each takes an **injectable transport seam** (an `httpDoer` interface defaulting to
`http.DefaultClient`, or an SMTP send func) so `Send` is unit-tested against a fake — no
network in tests, mirroring credstore's `var genKeypair` pattern.

- **webhook** — `POST <url>` with `Content-Type: application/json` and a JSON body
  `{type, agent, process, detail, time}`. If an HMAC secret is set, add header
  `X-Marshal-Signature: sha256=<hex hmac of the raw body>`.
- **telegram** — `POST https://api.telegram.org/bot<token>/sendMessage` with
  `{chat_id, text}`.
- **slack** — `POST <incoming-webhook-url>` with a simple Block Kit / `{text}` payload.
- **email** — `net/smtp` with STARTTLS, `from → to`, subject = title, body = body.

A small factory `New(cfg notify.Channel, secrets map[string]string) (Channel, error)`
maps a config to its implementation (unknown type → error).

## Store (`internal/notify/store.go`) + `internal/secretbox`

`notifications.json` in the data dir holds `{channels, rules, settings}`. Each channel's
secret map is stored as a sealed base64 blob; everything else is plaintext metadata.

To avoid duplicating audited crypto, extract credstore's existing `seal`/`openCipher`
helpers (AES-256-GCM under the master key) into a new tiny package `internal/secretbox`
exposing roughly:

```go
func Load(dataDir string) (*Box, error)   // resolve master key (env or master.key), same as credstore today
func (b *Box) Seal(plaintext []byte) (string, error)
func (b *Box) Open(sealed string) ([]byte, error)
```

`credstore` is refactored to use `secretbox` internally with **identical on-disk behavior**
(its existing tests must still pass unchanged). `notify.Store` uses the same `Box` so both
stores share one master key.

Store API (sketch):

```go
func Open(dataDir string, box *secretbox.Box) (*Store, error)
func (s *Store) Channels() []Channel               // metadata only, secrets omitted
func (s *Store) PutChannel(c Channel, secrets map[string]string) error
func (s *Store) DeleteChannel(name string) bool
func (s *Store) ChannelSecrets(name string) (map[string]string, bool, error) // for send/test
func (s *Store) Rules() []Rule
func (s *Store) PutRule(r Rule) error
func (s *Store) DeleteRule(name string) bool
func (s *Store) Settings() Settings
func (s *Store) SetSettings(Settings) error
```

The store is **in-process and hot**: dashboard edits mutate memory + persist, and the
running dispatcher reads the live store on each event, so changes take effect immediately
(single process — no restart needed, unlike the AuthStore).

## Dashboard (`internal/dashboard/notifications.go`)

Session-guarded handlers, modeled on `credentials.go`:

- `GET /api/notifications` → `{channels, rules, settings}`. Channels include
  `{name, type, enabled, config, has_secret}` — **secrets never returned**.
- `POST /api/notifications/channels` `{name, type, enabled, config, secrets}` →
  create/update (201). Empty `secrets` on update keeps the existing sealed secret.
- `DELETE /api/notifications/channels/{name}` → 204.
- `POST /api/notifications/channels/{name}/test` → sends a test `Message` through the
  channel now, returns `{ok, error?}`.
- `POST /api/notifications/rules` `{…rule…}` → create/update (201).
- `DELETE /api/notifications/rules/{name}` → 204.
- `PUT /api/notifications/settings` `{cooldown_seconds}` → 200.

The `Notifications` interface the handler depends on is widened from the store
(mirroring how `credentials.go` defines a narrow `Credentials` interface).

## Web (`web/src/Notifications.tsx`, `api.ts`, `router.ts`)

New `#/notifications` route (extend the `Route` union + `parseHash`), modeled on
`Credentials.tsx`:
- **Channels** list: type, enabled toggle, **Test** button, delete. Add/edit form is
  **type-aware** (shows the right fields per transport; secret fields are write-only).
- **Rules** builder: name, enabled, event-type checkboxes, agent + process matchers
  (text, `*` = any), channel multi-select, delete.
- **Settings**: cooldown seconds.

`api.ts` gains `getNotifications`, `putChannel`, `deleteChannel`, `testChannel`,
`putRule`, `deleteRule`, `putSettings` following the existing non-throwing-on-error
convention. Function-first styling per the design memory; cosmetic polish folded into the
eventual Signal/M19 pass. `make ui` rebuilds the embedded `internal/dashboard/dist`.

## Wiring

Where the server constructs the deployer at startup, also:
1. `box := secretbox.Load(dataDir)` (or reuse the one credstore already opened).
2. `store := notify.Open(dataDir, box)`.
3. `disp := notify.NewDispatcher(store)`.
4. `det := notify.NewDetector(registry, disp)`; start its loop (stopped on shutdown).
5. Hand `store` + a test-send hook to the dashboard handler.

## Error handling

- Channel send failures: bounded retry, then log and drop (never crash, never block).
- Malformed channel config (e.g. unknown type, bad URL): rejected at `PutChannel`/`POST`
  time with a 400; the dispatcher skips a channel it cannot build and logs it.
- Seal/unseal failure: surfaced as an error to the caller; never logs secret material.
- Detector: a snapshot it cannot diff is seeded as the new baseline (no crash).

## Testing

TDD per unit:
- **secretbox**: seal→open round-trip; wrong key fails; credstore's existing tests stay green.
- **detector**: table-driven `diff(prev, next)` covering every transition + the
  seed-without-emit case + clean-stop-no-event.
- **dispatcher**: cooldown gate (suppresses within window, fires after), rule matching
  (type/agent/process/wildcard, dedup across rules), fan-out calls each channel once.
- **channels**: each `Send` against a fake transport — correct URL/body/headers, HMAC
  signature for webhook, SMTP envelope for email; error propagation.
- **store**: round-trip persist/load, secrets sealed on disk (no plaintext), redaction.
- **dashboard**: handler CRUD, secret never returned, test-send path.

## Live demo (end of milestone)

Real fleet (server + one agent) on the standard demo ports (`:9000`/`:9001`), scratch
data dir:
- Configure a **webhook** channel pointing at a local listener (e.g. a tiny `nc`/Go
  sink) and a rule routing crashes to it.
- Force a **process crash** and a **deploy failure**; confirm the webhook receives the
  expected JSON, and that a flapping process is **cooled down** (one alert, not a storm).
- Exercise the **Test** button from the dashboard.
- Telegram/Slack/email verified via their fake-transport unit tests; a manual check with
  real credentials documented but not required for the automated demo.
- Render the `#/notifications` UI in-browser (per the demo-viewable memory).
- Tear down by data dir + pid; confirm no orphans (`pgrep -fl marshal`); preserve the
  user's standing launchd daemon.

## Implementation phasing (one branch, each phase TDD'd + reviewed)

1. `internal/secretbox` extraction (credstore refactor, behavior-identical) + `notify`
   domain model + store.
2. detector + dispatcher (cooldown + rules engine), proven against a log-only channel.
3. the four channels behind the `Channel` interface.
4. dashboard handlers + test-send.
5. React `#/notifications` UI.
6. wiring + live demo + handoff.
