# M27 — Recovery / "resolved" Notices Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Emit a single `recovered` notification when a process that was alerting (crash, restart-loop, or deploy-fail) returns to `online`.

**Architecture:** The notify `Detector` gains a small cross-tick `alerting` map keyed by `agent\x00process`. Each poll it runs the existing pure `diff` for alert transitions, then a new `recoveries` method that marks alerting processes, emits a recovery when a flagged process reaches `online` (clearing the flag), clears silently on a clean `stopped`, and prunes vanished processes. The dispatcher drops `recovered` events when a global `SuppressRecovery` setting is on; everything else flows through the existing cooldown → rule-match → render → channel path.

**Tech Stack:** Go 1.26 (stdlib only), React/TypeScript dashboard (`web/`).

## Global Constraints

- Module path is `marshal`; imports are `marshal/internal/...`.
- TDD: failing test first, then implementation. `go test ./... -race` must stay green.
- `gofmt -l .` must list nothing; `go vet ./...` must be clean.
- The recovery feature defaults **ON**, realised via an **inverted** `Settings.SuppressRecovery bool` (`json:"suppress_recovery"`) whose zero value `false` = recovery enabled — no loader special-casing, and legacy `notifications.json` files without the field get recovery ON automatically.
- Event type string is exactly `"recovered"`; notification title is exactly `"Process recovered"`.
- Recovery `Detail` strings (exact): `"recovered after crash"`, `"recovered after restart loop"`, `"deploy recovered"`, fallback `"recovered"`.
- Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Work on a branch off `dev` (e.g. `m27-recovery-notices`); never commit to `main`.

---

### Task 1: Model + render (event type, setting, title)

**Files:**
- Modify: `internal/notify/model.go` (add `EventRecovered`; add `SuppressRecovery` to `Settings`)
- Modify: `internal/notify/render.go` (add title)
- Create: `internal/notify/render_test.go`

**Interfaces:**
- Produces: `EventRecovered EventType = "recovered"`; `Settings.SuppressRecovery bool` (`json:"suppress_recovery"`); `render(Event)` yields title `"Process recovered"` for `EventRecovered`.

- [ ] **Step 1: Write the failing test**

Create `internal/notify/render_test.go`:

```go
package notify

import (
	"strings"
	"testing"
)

func TestRenderRecoveredTitle(t *testing.T) {
	m := render(Event{Type: EventRecovered, Agent: "dev-1", Process: "api", Detail: "recovered after crash"})
	if !strings.Contains(m.Title, "Process recovered") {
		t.Fatalf("want 'Process recovered' in title, got %q", m.Title)
	}
	if !strings.Contains(m.Body, "recovered after crash") {
		t.Fatalf("want detail in body, got %q", m.Body)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/notify/ -run TestRenderRecoveredTitle`
Expected: FAIL — `EventRecovered` undefined (compile error).

- [ ] **Step 3: Add the event type and setting in `model.go`**

In the `const (...)` block (after `EventDeployFail`):

```go
	EventDeployFail  EventType = "deploy_fail"
	EventRecovered   EventType = "recovered"
```

Replace the `Settings` struct:

```go
// Settings holds dispatcher tunables.
type Settings struct {
	CooldownSeconds int `json:"cooldown_seconds"`
	// SuppressRecovery silences "recovered" notices when true. It is inverted
	// (suppress, not enable) so the zero value keeps recovery on by default,
	// including for config files written before this field existed.
	SuppressRecovery bool `json:"suppress_recovery"`
}
```

- [ ] **Step 4: Add the title in `render.go`**

In the `eventTitles` map (after `EventDeployFail`):

```go
	EventDeployFail:  "Deploy failed",
	EventRecovered:   "Process recovered",
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/notify/ -run TestRenderRecoveredTitle`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/notify/model.go internal/notify/render.go internal/notify/render_test.go
git commit -m "feat(notify): add recovered event type, suppress-recovery setting, title

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Detector recovery tracking

**Files:**
- Modify: `internal/notify/detector.go` (add `alerting` field, `procKey`, `recoveryDetail`, `recoveries`; wire `Run`; init in `NewDetector`)
- Modify: `internal/notify/detector_test.go` (add recovery tests)

**Interfaces:**
- Consumes: `EventRecovered`, `EventCrash`, `EventRestartLoop`, `EventDeployFail` (Task 1 + existing); existing pure `diff(prev, next, now) []Event`.
- Produces: `func recoveryDetail(from EventType) string`; method `func (d *Detector) recoveries(alerts []Event, next []*pb.AgentState, now time.Time) []Event`; `Detector.alerting map[string]EventType`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/notify/detector_test.go`:

```go
// tick simulates one detector poll: compute alerts via diff, then recoveries.
func (d *Detector) tick(prev, next []*pb.AgentState, now time.Time) []Event {
	alerts := diff(prev, next, now)
	return d.recoveries(alerts, next, now)
}

func newDetectorForTest() *Detector {
	return &Detector{alerting: map[string]EventType{}}
}

func TestRecoveryDetail(t *testing.T) {
	cases := map[EventType]string{
		EventCrash:       "recovered after crash",
		EventRestartLoop: "recovered after restart loop",
		EventDeployFail:  "deploy recovered",
		EventType("weird"): "recovered",
	}
	for from, want := range cases {
		if got := recoveryDetail(from); got != want {
			t.Errorf("recoveryDetail(%q) = %q, want %q", from, got, want)
		}
	}
}

func TestRecoveryAfterCrash(t *testing.T) {
	d := newDetectorForTest()
	now := time.Now()
	online := []*pb.AgentState{agent("dev-1", true, proc("api", "online", 0))}
	restarting := []*pb.AgentState{agent("dev-1", true, proc("api", "restarting", 1))}
	// tick 1: crash -> no recovery yet
	if evs := d.tick(online, restarting, now); len(evs) != 0 {
		t.Fatalf("no recovery on crash tick, got %+v", evs)
	}
	// tick 2: back online -> recovered
	evs := d.tick(restarting, online, now)
	if len(evs) != 1 || evs[0].Type != EventRecovered || evs[0].Process != "api" {
		t.Fatalf("want recovered for api, got %+v", evs)
	}
	if evs[0].Detail != "recovered after crash" {
		t.Fatalf("want crash detail, got %q", evs[0].Detail)
	}
}

func TestRecoveryDeployPathThroughBuilding(t *testing.T) {
	d := newDetectorForTest()
	now := time.Now()
	building := []*pb.AgentState{agent("dev-1", true, proc("web", "building", 0))}
	failed := []*pb.AgentState{agent("dev-1", true, proc("web", "failed", 0))}
	online := []*pb.AgentState{agent("dev-1", true, proc("web", "online", 0))}
	if evs := d.tick(building, failed, now); len(evs) != 0 { // deploy_fail tick
		t.Fatalf("no recovery on fail tick, got %+v", evs)
	}
	if evs := d.tick(failed, building, now); len(evs) != 0 { // rebuild, still alerting
		t.Fatalf("no recovery while building, got %+v", evs)
	}
	evs := d.tick(building, online, now) // online -> recovered
	if len(evs) != 1 || evs[0].Type != EventRecovered || evs[0].Detail != "deploy recovered" {
		t.Fatalf("want deploy recovered, got %+v", evs)
	}
}

func TestCleanStopWhileAlertingClearsSilently(t *testing.T) {
	d := newDetectorForTest()
	now := time.Now()
	online := []*pb.AgentState{agent("dev-1", true, proc("api", "online", 0))}
	errored := []*pb.AgentState{agent("dev-1", true, proc("api", "errored", 5))}
	stopped := []*pb.AgentState{agent("dev-1", true, proc("api", "stopped", 5))}
	d.tick(online, errored, now)                 // restart_loop -> alerting
	if evs := d.tick(errored, stopped, now); len(evs) != 0 {
		t.Fatalf("clean stop must not emit recovery, got %+v", evs)
	}
	// coming back online after a clean stop is a normal start, not a recovery
	if evs := d.tick(stopped, online, now); len(evs) != 0 {
		t.Fatalf("start after clean stop must not recover, got %+v", evs)
	}
}

func TestRecoveryPrunesVanishedProcess(t *testing.T) {
	d := newDetectorForTest()
	now := time.Now()
	online := []*pb.AgentState{agent("dev-1", true, proc("api", "online", 0))}
	restarting := []*pb.AgentState{agent("dev-1", true, proc("api", "restarting", 1))}
	gone := []*pb.AgentState{agent("dev-1", true)} // api removed
	d.tick(online, restarting, now)                // alerting api
	d.tick(restarting, gone, now)                  // api vanished -> pruned, no recovery
	if len(d.alerting) != 0 {
		t.Fatalf("alerting map should be pruned, got %v", d.alerting)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/notify/ -run 'Recovery|CleanStop'`
Expected: FAIL — `recoveries`, `recoveryDetail`, and `Detector.alerting` undefined (compile error).

- [ ] **Step 3: Implement in `detector.go`**

Add `alerting` to the `Detector` struct (after `prev`):

```go
type Detector struct {
	lister   Lister
	emit     Emitter
	interval time.Duration
	now      func() time.Time
	prev     []*pb.AgentState
	alerting map[string]EventType // key: agent\x00process -> last alert type
}
```

Initialise it in `NewDetector`:

```go
func NewDetector(l Lister, e Emitter, interval time.Duration) *Detector {
	return &Detector{lister: l, emit: e, interval: interval, now: time.Now, alerting: map[string]EventType{}}
}
```

Add the helpers and method (e.g. after `procEvent`):

```go
func procKey(agent, process string) string { return agent + "\x00" + process }

// recoveryDetail describes what a process recovered from.
func recoveryDetail(from EventType) string {
	switch from {
	case EventCrash:
		return "recovered after crash"
	case EventRestartLoop:
		return "recovered after restart loop"
	case EventDeployFail:
		return "deploy recovered"
	default:
		return "recovered"
	}
}

// recoveries records this tick's alerts, then emits a recovery for any alerting
// process that has returned to "online". A clean "stopped" clears the flag
// silently; processes that vanish from the snapshot are pruned.
func (d *Detector) recoveries(alerts []Event, next []*pb.AgentState, now time.Time) []Event {
	for _, e := range alerts {
		if e.Process != "" {
			d.alerting[procKey(e.Agent, e.Process)] = e.Type
		}
	}
	present := map[string]bool{}
	var out []Event
	for _, a := range next {
		for _, p := range a.GetProcs() {
			key := procKey(a.GetAgentName(), p.GetName())
			present[key] = true
			from, ok := d.alerting[key]
			if !ok {
				continue
			}
			switch p.GetState() {
			case "online":
				out = append(out, Event{Type: EventRecovered, Agent: a.GetAgentName(), Process: p.GetName(), Detail: recoveryDetail(from), Time: now})
				delete(d.alerting, key)
			case "stopped":
				delete(d.alerting, key) // clean stop: alarm moot
			}
		}
	}
	for key := range d.alerting {
		if !present[key] {
			delete(d.alerting, key)
		}
	}
	return out
}
```

Wire `Run` to emit alerts then recoveries:

```go
		case <-t.C:
			next := d.lister.List()
			now := d.now()
			alerts := diff(d.prev, next, now)
			for _, e := range alerts {
				d.emit.Emit(e)
			}
			for _, e := range d.recoveries(alerts, next, now) {
				d.emit.Emit(e)
			}
			d.prev = next
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/notify/ -run 'Recovery|CleanStop|Diff|Detector'`
Expected: PASS (existing `diff`/detector tests stay green).

- [ ] **Step 5: Commit**

```bash
git add internal/notify/detector.go internal/notify/detector_test.go
git commit -m "feat(notify): detect process recovery and emit recovered events

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Dispatcher suppress gate

**Files:**
- Modify: `internal/notify/dispatcher.go` (gate at top of `Emit`)
- Modify: `internal/notify/dispatcher_test.go` (add suppress tests)

**Interfaces:**
- Consumes: `EventRecovered`, `Settings.SuppressRecovery` (Task 1); existing `Dispatcher.Emit`, `fakeStore`, `newTestDispatcher`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/notify/dispatcher_test.go`:

```go
func TestDispatcherSuppressesRecovery(t *testing.T) {
	st := &fakeStore{
		channels: []Channel{{Name: "tg", Type: "telegram", Enabled: true}},
		rules:    []Rule{{Name: "all", Enabled: true, Channels: []string{"tg"}}},
		settings: Settings{CooldownSeconds: 300, SuppressRecovery: true},
	}
	now := time.Unix(1000, 0)
	d, senders := newTestDispatcher(t, st, func() time.Time { return now })
	d.Emit(Event{Type: EventRecovered, Agent: "dev-1", Process: "api"})
	if s := senders["tg"]; s != nil && len(s.sent) != 0 {
		t.Fatalf("recovered must be suppressed, got %d", len(s.sent))
	}
	d.Emit(Event{Type: EventCrash, Agent: "dev-1", Process: "api"})
	if len(senders["tg"].sent) != 1 {
		t.Fatalf("crash should still deliver, got %d", len(senders["tg"].sent))
	}
}

func TestDispatcherDeliversRecoveryWhenEnabled(t *testing.T) {
	st := &fakeStore{
		channels: []Channel{{Name: "tg", Type: "telegram", Enabled: true}},
		rules:    []Rule{{Name: "all", Enabled: true, Channels: []string{"tg"}}},
		settings: Settings{CooldownSeconds: 300, SuppressRecovery: false},
	}
	now := time.Unix(1000, 0)
	d, senders := newTestDispatcher(t, st, func() time.Time { return now })
	d.Emit(Event{Type: EventRecovered, Agent: "dev-1", Process: "api"})
	if len(senders["tg"].sent) != 1 {
		t.Fatalf("recovered should deliver when enabled, got %d", len(senders["tg"].sent))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/notify/ -run 'DispatcherSuppress|DispatcherDeliversRecovery'`
Expected: FAIL — `TestDispatcherSuppressesRecovery` fails (recovered currently delivered).

- [ ] **Step 3: Implement the gate in `dispatcher.go`**

At the top of `Emit`, before the cooldown check:

```go
// Emit gates the event by cooldown, then fans out to matching channels.
func (d *Dispatcher) Emit(e Event) {
	if e.Type == EventRecovered && d.store.Settings().SuppressRecovery {
		return
	}
	if !d.allow(e) {
		return
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/notify/ -run Dispatcher`
Expected: PASS (all dispatcher tests).

- [ ] **Step 5: Commit**

```bash
git add internal/notify/dispatcher.go internal/notify/dispatcher_test.go
git commit -m "feat(notify): suppress recovered events when SuppressRecovery is set

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Store default + persistence coverage

The inverted flag means no store code change is needed — `SetSettings` already assigns the whole `Settings` struct and JSON round-trips the field. This task locks that behavior with tests.

**Files:**
- Modify: `internal/notify/store_test.go` (add default + round-trip tests)

**Interfaces:**
- Consumes: `Settings.SuppressRecovery` (Task 1); existing `testStore`, `Open`, `secretbox.FromKey`.

- [ ] **Step 1: Write the tests (expected to pass immediately — characterization)**

Append to `internal/notify/store_test.go`:

```go
func TestDefaultRecoveryEnabled(t *testing.T) {
	s, _ := testStore(t)
	if s.Settings().SuppressRecovery {
		t.Fatal("recovery should default ON (SuppressRecovery=false)")
	}
}

func TestSuppressRecoveryPersists(t *testing.T) {
	s, dir := testStore(t)
	if err := s.SetSettings(Settings{CooldownSeconds: 120, SuppressRecovery: true}); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	key[0] = 7
	s2, err := Open(dir, secretbox.FromKey(key))
	if err != nil {
		t.Fatal(err)
	}
	if !s2.Settings().SuppressRecovery {
		t.Fatalf("suppress_recovery not persisted: %+v", s2.Settings())
	}
}

func TestLegacyConfigRecoveryEnabled(t *testing.T) {
	dir := t.TempDir()
	// A notifications.json predating the field: no suppress_recovery key.
	legacy := `{"channels":{},"rules":{},"settings":{"cooldown_seconds":300}}`
	if err := os.WriteFile(filepath.Join(dir, "notifications.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	key[0] = 7
	s, err := Open(dir, secretbox.FromKey(key))
	if err != nil {
		t.Fatal(err)
	}
	if s.Settings().SuppressRecovery {
		t.Fatal("legacy config must default to recovery ON")
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test ./internal/notify/ -run 'DefaultRecoveryEnabled|SuppressRecoveryPersists|LegacyConfigRecoveryEnabled'`
Expected: PASS. (`os` and `path/filepath` are already imported in `store_test.go`.)

- [ ] **Step 3: Commit**

```bash
git add internal/notify/store_test.go
git commit -m "test(notify): cover recovery default-on and suppress_recovery persistence

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Dashboard — event checkbox + settings toggle

**Files:**
- Modify: `web/src/api.ts:373` (`NotifSettings` type + default in `getNotifications`)
- Modify: `web/src/Notifications.tsx:8` (`EVENT_TYPES`) and `:157-166` (`SettingsSection`)

**Interfaces:**
- Consumes: backend `suppress_recovery` JSON field (Task 1); existing `putNotifSettings`, `NotifConfig`.

- [ ] **Step 1: Extend the settings type in `api.ts`**

Replace line 373:

```ts
export type NotifSettings = { cooldown_seconds: number; suppress_recovery?: boolean };
```

In `getNotifications`'s fallback (line ~379), leave `settings: { cooldown_seconds: 300 }` as-is — `suppress_recovery` is optional and absent means recovery on.

- [ ] **Step 2: Add `recovered` to the rule event list in `Notifications.tsx`**

Replace line 8:

```ts
const EVENT_TYPES = ["crash", "restart_loop", "agent_down", "agent_up", "deploy_fail", "recovered"];
```

- [ ] **Step 3: Add the recovery toggle to `SettingsSection`**

Replace the `SettingsSection` function (lines 157-166):

```tsx
function SettingsSection({ cfg, onChange }: { cfg: NotifConfig; onChange: () => void }) {
  const [cooldown, setCooldown] = useState(cfg.settings.cooldown_seconds);
  const [recovery, setRecovery] = useState(!cfg.settings.suppress_recovery);
  return (
    <section>
      <h3>Settings</h3>
      <label>Cooldown (seconds): <input type="number" value={cooldown} onChange={(e) => setCooldown(Number(e.target.value))} /></label>
      <label><input type="checkbox" checked={recovery} onChange={(e) => setRecovery(e.target.checked)} /> Send recovery notices</label>
      <button onClick={async () => { await putNotifSettings({ cooldown_seconds: cooldown, suppress_recovery: !recovery }); onChange(); }}>Save</button>
    </section>
  );
}
```

- [ ] **Step 4: Verify the web build compiles**

Run: `cd web && npm run build`
Expected: build succeeds, no TypeScript errors. (This mirrors the CI Web job.)

- [ ] **Step 5: Commit**

```bash
git add web/src/api.ts web/src/Notifications.tsx
git commit -m "feat(web): recovered rule event + send-recovery-notices toggle

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Changelog + full verification

**Files:**
- Modify: `CHANGELOG.md` (`[Unreleased] → Added`)

- [ ] **Step 1: Add the changelog entry**

Under `## [Unreleased]` → `### Added` in `CHANGELOG.md`, append:

```markdown
- **Recovery notices** — the notification detector now emits a `recovered` event
  ("Process recovered") when a process that was crashing, restart-looping, or
  deploy-failing returns to `online` (including deploy recovery through an
  intermediate build). Controlled by a "Send recovery notices" setting that is on
  by default; routes through existing notification rules.
```

- [ ] **Step 2: Run the full verification sweep**

Run: `go test ./... -race -count=1 && go vet ./... && gofmt -l .`
Expected: all packages `ok`; vet silent; gofmt prints nothing.

- [ ] **Step 3: Build the binary**

Run: `make build && ./marshal --version`
Expected: builds; version reports a `git describe` value.

- [ ] **Step 4: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs(changelog): note M27 recovery notices

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 5: Live demo + handoff (per CLAUDE.md)**

Run a scratch demo on standard ports (`:9000`/`:9001`, `XDG_DATA_HOME=/tmp/marshal-demo/...`), drive a process through crash → online and confirm a "Process recovered" notice fires (webhook channel to a local listener is simplest), toggle the setting off and confirm suppression. Tear down (stop processes + daemon + server, remove scratch dir) and confirm `pgrep -fl marshal` is clean. Then write `docs/handoffs/2026-06-22-m27-recovery-notices.md` and, when release-ready, cut **v0.2.0**.

---

## Self-Review

**Spec coverage:**
- Model (`EventRecovered`, `SuppressRecovery` inverted) → Task 1. ✓
- Detector stateful `alerting`, `recoveries`, `recoveryDetail`, deploy path, clean-stop, pruning → Task 2. ✓
- Dispatcher suppress gate (cooldown bucket unchanged) → Task 3. ✓
- Store default-ON + persistence + legacy file → Task 4. ✓
- Render title → Task 1. ✓
- Frontend `EVENT_TYPES` + settings toggle → Task 5. ✓
- Release (CHANGELOG, v0.2.0), demo, handoff → Task 6. ✓
- Edge cases table (transient restart crash+recovered, new process silent) — covered by Task 2 logic; transient restart is the `TestRecoveryAfterCrash` shape, new-process-silent is guaranteed because new procs never enter `alerting`.

**Placeholder scan:** No TBD/TODO; every code step shows complete code. ✓

**Type consistency:** `recoveries(alerts []Event, next []*pb.AgentState, now time.Time) []Event`, `recoveryDetail(from EventType) string`, `procKey(agent, process string) string`, `Detector.alerting map[string]EventType`, `Settings.SuppressRecovery bool` used identically across Tasks 1–4. Frontend `suppress_recovery` matches the Go `json` tag. ✓
