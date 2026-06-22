# M28 — Notification Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bound the dispatcher's cooldown map and let each event type have its own optional cooldown rate, defaulting to the global cooldown.

**Architecture:** `Settings` gains an optional `CooldownOverrides map[EventType]int` (key-presence = override). The dispatcher's `allow()` looks up the per-type cooldown via a new `Settings.cooldownFor` helper, and lazily prunes expired entries from its `last` map inside the existing locked section (no goroutine). Store/HTTP need no code change — the map flows through existing JSON (de)serialization; tasks add tests that prove it. The settings UI gains six fixed override rows.

**Tech Stack:** Go 1.26 (stdlib only), React + TypeScript (`web/`), embedded bundle via `make ui`.

## Global Constraints

- TDD: failing test first, then minimal implementation. (CLAUDE.md)
- Go: `go test ./... -race -count=1` green; `go vet ./...` clean; `gofmt -l .` silent before finishing. (CLAUDE.md)
- Branch is `m28-notification-hardening` (already created off `dev`); never commit to `main`. (CLAUDE.md)
- Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`. (CLAUDE.md)
- Every change records a `CHANGELOG.md` `[Unreleased]` entry as part of the work. (CLAUDE.md)
- Back-compat: existing `notifications.json` (no new field) must load with global cooldown applied to all types. (spec §1)
- The cooldown key is unchanged: `agent + "\x00" + process + "\x00" + string(type)`. (spec §2)
- Map semantics: key **presence** is the signal — absent = inherit global; present (incl. `0`) = use that value. (spec §1)

---

### Task 1: `CooldownOverrides` field + `cooldownFor` helper

**Files:**
- Modify: `internal/notify/model.go` (the `Settings` struct, add a method)
- Test: `internal/notify/model_test.go`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: existing `EventType` constants (`EventCrash`, `EventRecovered`, …) and `Settings`.
- Produces: `Settings.CooldownOverrides map[EventType]int` (json `cooldown_overrides,omitempty`); method `func (s Settings) cooldownFor(t EventType) time.Duration`.

- [ ] **Step 1: Write the failing test**

Append to `internal/notify/model_test.go`:

```go
func TestCooldownForPrecedence(t *testing.T) {
	s := Settings{
		CooldownSeconds:   300,
		CooldownOverrides: map[EventType]int{EventRecovered: 600, EventCrash: 0},
	}
	if got := s.cooldownFor(EventDeployFail); got != 300*time.Second {
		t.Errorf("no override: want 300s, got %v", got)
	}
	if got := s.cooldownFor(EventRecovered); got != 600*time.Second {
		t.Errorf("override present: want 600s, got %v", got)
	}
	if got := s.cooldownFor(EventCrash); got != 0 {
		t.Errorf("explicit 0 override: want 0, got %v", got)
	}
	// nil map falls through to the global for every type.
	bare := Settings{CooldownSeconds: 120}
	if got := bare.cooldownFor(EventRecovered); got != 120*time.Second {
		t.Errorf("nil overrides: want 120s, got %v", got)
	}
}
```

The test file currently imports only `"testing"`. Update its import block to:

```go
import (
	"testing"
	"time"
)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/notify/ -run TestCooldownForPrecedence`
Expected: FAIL — `s.cooldownFor undefined` and `s.CooldownOverrides unknown field`.

- [ ] **Step 3: Write minimal implementation**

In `internal/notify/model.go`, replace the `Settings` struct (currently lines 50–56) with:

```go
// Settings holds dispatcher tunables.
type Settings struct {
	CooldownSeconds int `json:"cooldown_seconds"`
	// SuppressRecovery silences "recovered" notices when true. It is inverted
	// (suppress, not enable) so the zero value keeps recovery on by default,
	// including for config files written before this field existed.
	SuppressRecovery bool `json:"suppress_recovery"`
	// CooldownOverrides maps an event type to a per-type cooldown in seconds,
	// overriding CooldownSeconds for that type. A key's PRESENCE is the signal:
	// absent  = inherit the global CooldownSeconds;
	// present = use this value (including an explicit 0, which disables the
	//           cooldown for that type). The map sidesteps the
	//           int-zero-means-unset ambiguity that CooldownSeconds has.
	CooldownOverrides map[EventType]int `json:"cooldown_overrides,omitempty"`
}

// cooldownFor returns the cooldown duration for an event type: the per-type
// override if present, otherwise the global CooldownSeconds.
func (s Settings) cooldownFor(t EventType) time.Duration {
	secs := s.CooldownSeconds
	if v, ok := s.CooldownOverrides[t]; ok {
		secs = v
	}
	return time.Duration(secs) * time.Second
}
```

`model.go` already imports `"time"` (used by `Event.Time`), so no import change.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/notify/ -run TestCooldownForPrecedence`
Expected: PASS.

- [ ] **Step 5: Add the CHANGELOG entry**

In `CHANGELOG.md`, under `## [Unreleased]` → `### Added`, add:

```markdown
- Per-event-type cooldown overrides: each notification event type can have its own cooldown, falling back to the global cooldown when unset (`settings.cooldown_overrides`).
```

(If no `### Added` subsection exists under `[Unreleased]`, create it above any other subsection.)

- [ ] **Step 6: Commit**

```bash
git add internal/notify/model.go internal/notify/model_test.go CHANGELOG.md
git commit -m "feat(notify): per-event-type cooldown overrides + cooldownFor helper

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Dispatcher — per-type cooldown lookup + map pruning

**Files:**
- Modify: `internal/notify/dispatcher.go` (the `Dispatcher.last` field, `NewDispatcher`, `allow`; add `pruneLocked`)
- Test: `internal/notify/dispatcher_test.go`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: `Settings.cooldownFor` (Task 1); `d.store.Settings()`.
- Produces: internal `cooldownEntry{at time.Time; typ EventType}`; `d.last` becomes `map[string]cooldownEntry`; method `func (d *Dispatcher) pruneLocked(s Settings, now time.Time)`. (No exported-API change.)

- [ ] **Step 1: Write the failing tests**

Append to `internal/notify/dispatcher_test.go`:

```go
func TestDispatcherPerTypeCooldownOverride(t *testing.T) {
	st := &fakeStore{
		channels: []Channel{{Name: "tg", Type: "telegram", Enabled: true}},
		rules:    []Rule{{Name: "all", Enabled: true, Channels: []string{"tg"}}},
		settings: Settings{CooldownSeconds: 300, CooldownOverrides: map[EventType]int{EventRecovered: 30}},
	}
	cur := time.Unix(1000, 0)
	d, senders := newTestDispatcher(t, st, func() time.Time { return cur })
	rec := Event{Type: EventRecovered, Agent: "dev-1", Process: "api"}
	d.Emit(rec)
	cur = cur.Add(60 * time.Second) // past the 30s override, within the 300s global
	d.Emit(rec)
	if n := len(senders["tg"].sent); n != 2 {
		t.Fatalf("recovered should re-fire after its 30s override, got %d", n)
	}
	// A different type still uses the 300s global: a repeat within 60s is suppressed.
	crash := Event{Type: EventCrash, Agent: "dev-1", Process: "api"}
	d.Emit(crash)
	cur = cur.Add(60 * time.Second)
	d.Emit(crash)
	if n := len(senders["tg"].sent); n != 3 {
		t.Fatalf("crash should obey the 300s global (one extra send), got %d total", n)
	}
}

func TestDispatcherZeroOverrideDisablesCooldown(t *testing.T) {
	st := &fakeStore{
		channels: []Channel{{Name: "tg", Type: "telegram", Enabled: true}},
		rules:    []Rule{{Name: "all", Enabled: true, Channels: []string{"tg"}}},
		settings: Settings{CooldownSeconds: 300, CooldownOverrides: map[EventType]int{EventCrash: 0}},
	}
	now := time.Unix(1000, 0)
	d, senders := newTestDispatcher(t, st, func() time.Time { return now })
	ev := Event{Type: EventCrash, Agent: "dev-1", Process: "api"}
	d.Emit(ev)
	d.Emit(ev) // same instant; zero cooldown ⇒ both allowed
	if n := len(senders["tg"].sent); n != 2 {
		t.Fatalf("zero override disables cooldown, want 2 sends, got %d", n)
	}
}

func TestDispatcherPrunesExpiredEntries(t *testing.T) {
	st := &fakeStore{
		channels: []Channel{{Name: "tg", Type: "telegram", Enabled: true}},
		rules:    []Rule{{Name: "all", Enabled: true, Channels: []string{"tg"}}},
		settings: Settings{CooldownSeconds: 300},
	}
	cur := time.Unix(1000, 0)
	d, _ := newTestDispatcher(t, st, func() time.Time { return cur })
	d.Emit(Event{Type: EventCrash, Agent: "dev-1", Process: "api"})
	d.Emit(Event{Type: EventCrash, Agent: "dev-2", Process: "api"}) // within cooldown: both retained
	if n := len(d.last); n != 2 {
		t.Fatalf("two live keys within cooldown, want 2, got %d", n)
	}
	cur = cur.Add(301 * time.Second) // both now past the 300s cooldown
	d.Emit(Event{Type: EventCrash, Agent: "dev-3", Process: "api"})
	if n := len(d.last); n != 1 {
		t.Fatalf("expired entries should be pruned, want 1 (only dev-3), got %d", n)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/notify/ -run 'TestDispatcherPerTypeCooldownOverride|TestDispatcherZeroOverrideDisablesCooldown|TestDispatcherPrunesExpiredEntries'`
Expected: FAIL — `TestDispatcherPerTypeCooldownOverride` (current code ignores overrides, so the second `recovered` is suppressed → 1 send) and `TestDispatcherPrunesExpiredEntries` (`d.last` keeps growing → 3, not 1). `d.last` still compiles (it's `map[string]time.Time`).

- [ ] **Step 3: Change the map type and `NewDispatcher`**

In `internal/notify/dispatcher.go`, change the `last` field on the `Dispatcher` struct (currently line 28) and add the entry type. Replace:

```go
	mu       sync.Mutex
	last     map[string]time.Time
}
```

with:

```go
	mu       sync.Mutex
	last     map[string]cooldownEntry
}

// cooldownEntry records when a (agent,process,type) key last fired and its type,
// so the prune sweep can apply the type's own cooldown without re-parsing the key.
type cooldownEntry struct {
	at  time.Time
	typ EventType
}
```

In `NewDispatcher` (currently line 46), change the map literal:

```go
		last:  map[string]cooldownEntry{},
```

- [ ] **Step 4: Rewrite `allow` and add `pruneLocked`**

Replace the entire `allow` method (currently lines 77–88) with:

```go
// allow records and checks the per-(agent,process,type) cooldown, then prunes
// entries that have outlived their own cooldown (they can never gate again).
func (d *Dispatcher) allow(e Event) bool {
	key := e.Agent + "\x00" + e.Process + "\x00" + string(e.Type)
	s := d.store.Settings()
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if last, ok := d.last[key]; ok && now.Sub(last.at) < s.cooldownFor(e.Type) {
		return false
	}
	d.last[key] = cooldownEntry{at: now, typ: e.Type}
	d.pruneLocked(s, now)
	return true
}

// pruneLocked drops entries whose age has reached their type's cooldown. Caller
// holds d.mu. An entry past its cooldown always allows the next event of that
// key, so removing it changes no observable behavior; this bounds the map to
// distinct keys seen within their cooldown window.
func (d *Dispatcher) pruneLocked(s Settings, now time.Time) {
	for k, e := range d.last {
		if now.Sub(e.at) >= s.cooldownFor(e.typ) {
			delete(d.last, k)
		}
	}
}
```

- [ ] **Step 5: Run the new tests and the full notify suite**

Run: `go test ./internal/notify/`
Expected: PASS (new tests green; existing `TestDispatcherCooldownSuppressesRepeat` etc. still pass — the default-cooldown path is unchanged).

- [ ] **Step 6: Add the CHANGELOG entry**

In `CHANGELOG.md`, under `## [Unreleased]` → `### Fixed` (create the subsection if absent), add:

```markdown
- The notification cooldown map is now pruned of expired entries on each emit, so it stays bounded regardless of fleet size or uptime (previously grew unbounded).
```

- [ ] **Step 7: Commit**

```bash
git add internal/notify/dispatcher.go internal/notify/dispatcher_test.go CHANGELOG.md
git commit -m "feat(notify): per-type cooldown lookup + prune the cooldown map

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Store round-trip tests for `cooldown_overrides`

**Files:**
- Test: `internal/notify/store_test.go`
- (No production code change expected: `SetSettings` assigns the whole `Settings` struct and `flushLocked` marshals it; the map round-trips natively. If a test fails because the store drops the map, fix `store.go` to pass it through, then re-run.)

**Interfaces:**
- Consumes: `testStore(t)` helper (returns `*Store, dir`); `secretbox.FromKey`; `Open`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/notify/store_test.go`:

```go
func TestCooldownOverridesPersist(t *testing.T) {
	s, dir := testStore(t)
	if err := s.SetSettings(Settings{
		CooldownSeconds:   120,
		CooldownOverrides: map[EventType]int{EventRecovered: 600},
	}); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	key[0] = 7
	s2, err := Open(dir, secretbox.FromKey(key))
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Settings().CooldownOverrides[EventRecovered]; got != 600 {
		t.Fatalf("override not persisted: %+v", s2.Settings())
	}
}

func TestNoCooldownOverridesStaysNil(t *testing.T) {
	s, dir := testStore(t)
	if err := s.SetSettings(Settings{CooldownSeconds: 120}); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	key[0] = 7
	s2, err := Open(dir, secretbox.FromKey(key))
	if err != nil {
		t.Fatal(err)
	}
	if s2.Settings().CooldownOverrides != nil {
		t.Fatalf("expected nil overrides, got %+v", s2.Settings().CooldownOverrides)
	}
}

func TestLegacyConfigNoOverrides(t *testing.T) {
	dir := t.TempDir()
	// A notifications.json predating the field: no cooldown_overrides key.
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
	if s.Settings().CooldownOverrides != nil {
		t.Fatal("legacy config must load with nil overrides")
	}
	if s.Settings().CooldownSeconds != 300 {
		t.Fatalf("legacy global cooldown lost: %d", s.Settings().CooldownSeconds)
	}
}
```

`store_test.go` already imports `os`, `path/filepath`, `testing`, and `marshal/internal/secretbox` (used by existing legacy/suppress tests), so no import change.

- [ ] **Step 2: Run tests to verify they pass (or fail meaningfully)**

Run: `go test ./internal/notify/ -run 'TestCooldownOverridesPersist|TestNoCooldownOverridesStaysNil|TestLegacyConfigNoOverrides'`
Expected: PASS immediately (the map round-trips through existing JSON). If `TestCooldownOverridesPersist` fails with the map dropped, that's a real defect — fix `store.go` so `SetSettings`/`flushLocked` preserve `CooldownOverrides`, then re-run to green. These tests are still valuable as regression guards.

- [ ] **Step 3: Commit**

```bash
git add internal/notify/store_test.go
git commit -m "test(notify): cooldown_overrides round-trips + legacy/nil cases

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: HTTP round-trip test for `cooldown_overrides`

**Files:**
- Test: `internal/dashboard/notifications_test.go`
- (No handler change: `putSettings` decodes the whole `notify.Settings`, so the map flows through.)

**Interfaces:**
- Consumes: `fakeNotifs` (has a `settings notify.Settings` field set by `SetSettings`); `testHandlerWithNotifs(t, n)`; `h.putSettings`.

- [ ] **Step 1: Write the failing test**

Append to `internal/dashboard/notifications_test.go` (mirrors `TestPutSettingsRoundTripsSuppressRecovery`):

```go
func TestPutSettingsRoundTripsCooldownOverrides(t *testing.T) {
	n := &fakeNotifs{}
	h := testHandlerWithNotifs(t, n)
	body, _ := json.Marshal(notify.Settings{
		CooldownSeconds:   60,
		CooldownOverrides: map[notify.EventType]int{notify.EventRecovered: 600},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/notifications/settings", bytes.NewReader(body))
	h.putSettings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body %s", rec.Code, rec.Body)
	}
	if got := n.settings.CooldownOverrides[notify.EventRecovered]; got != 600 {
		t.Fatalf("cooldown_overrides not stored: %+v", n.settings)
	}
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./internal/dashboard/ -run TestPutSettingsRoundTripsCooldownOverrides`
Expected: PASS (handler already round-trips the struct). If it fails, the `fakeNotifs.SetSettings` may not store the field — make it assign the whole struct.

- [ ] **Step 3: Commit**

```bash
git add internal/dashboard/notifications_test.go
git commit -m "test(dashboard): putSettings round-trips cooldown_overrides

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Settings UI — per-event cooldown override rows

**Files:**
- Modify: `web/src/api.ts:373` (the `NotifSettings` type)
- Modify: `web/src/Notifications.tsx:157-168` (the `SettingsSection` component)
- Regenerate: `internal/dashboard/dist` via `make ui` (committed)

**Interfaces:**
- Consumes: `EVENT_TYPES` (already defined at `Notifications.tsx:8`); `putNotifSettings`; `NotifConfig`/`NotifSettings`.
- Produces: `NotifSettings.cooldown_overrides?: Record<string, number>`; the rendered override inputs.

- [ ] **Step 1: Extend the API type**

In `web/src/api.ts`, change line 373:

```ts
export type NotifSettings = { cooldown_seconds: number; suppress_recovery?: boolean; cooldown_overrides?: Record<string, number> };
```

- [ ] **Step 2: Add override rows to `SettingsSection`**

In `web/src/Notifications.tsx`, replace the whole `SettingsSection` function (lines 157–168) with:

```tsx
function SettingsSection({ cfg, onChange }: { cfg: NotifConfig; onChange: () => void }) {
  const [cooldown, setCooldown] = useState(cfg.settings.cooldown_seconds);
  const [recovery, setRecovery] = useState(!cfg.settings.suppress_recovery);
  const [overrides, setOverrides] = useState<Record<string, string>>(() => {
    const init: Record<string, string> = {};
    for (const ev of EVENT_TYPES) {
      const v = cfg.settings.cooldown_overrides?.[ev];
      init[ev] = v === undefined ? "" : String(v);
    }
    return init;
  });
  return (
    <section>
      <h3>Settings</h3>
      <label>Cooldown (seconds): <input type="number" value={cooldown} onChange={(e) => setCooldown(Number(e.target.value))} /></label>
      <label><input type="checkbox" checked={recovery} onChange={(e) => setRecovery(e.target.checked)} /> Send recovery notices</label>
      <div>
        <h4>Per-event cooldown (seconds)</h4>
        {EVENT_TYPES.map((ev) => (
          <label key={ev}>{ev}: <input type="number" placeholder={`${cooldown} (global)`} value={overrides[ev]} onChange={(e) => setOverrides({ ...overrides, [ev]: e.target.value })} /></label>
        ))}
      </div>
      <button onClick={async () => {
        const co: Record<string, number> = {};
        for (const ev of EVENT_TYPES) {
          if (overrides[ev] !== "") co[ev] = Number(overrides[ev]);
        }
        await putNotifSettings({ cooldown_seconds: cooldown, suppress_recovery: !recovery, cooldown_overrides: co });
        onChange();
      }}>Save</button>
    </section>
  );
}
```

- [ ] **Step 3: Type-check / build the frontend**

Run: `cd web && npm run build` (or `make ui` from repo root, which builds `web/` into `internal/dashboard/dist`).
Expected: build succeeds, no TypeScript errors.

- [ ] **Step 4: Regenerate the embedded bundle**

Run (from repo root): `make ui`
Expected: `internal/dashboard/dist` updated. Confirm the new strings ship:

Run: `grep -rl "Per-event cooldown" internal/dashboard/dist`
Expected: at least one `assets/index-*.js` matches.

- [ ] **Step 5: Commit**

```bash
git add web/src/api.ts web/src/Notifications.tsx internal/dashboard/dist
git commit -m "feat(dashboard): per-event cooldown override rows in settings

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Final verification (run before declaring the branch done)

- [ ] `go test ./... -race -count=1` — all packages green.
- [ ] `go vet ./...` — clean.
- [ ] `gofmt -l .` — prints nothing.
- [ ] `make build` — version-stamped binary builds.
- [ ] Spec coverage re-check against `docs/superpowers/specs/2026-06-22-m28-notification-hardening-design.md`.

Then: requesting-code-review (whole branch), address findings, live demo (spec "Verification / live demo" section — two event types at different cooldown rates against a webhook sink on `:9000`/`:9001`, UI persistence across reload, teardown by data-dir + PID, `pgrep -fl marshal` clean), write the handoff to `docs/handoffs/`, and finish the branch (merge `--no-ff` into `dev`).

---

## Self-Review

**Spec coverage:**
- §1 data model (`CooldownOverrides` + `cooldownFor`) → Task 1. ✓
- §2a per-type lookup → Task 2 (steps 4). ✓
- §2b prune → Task 2 (steps 3–4, `pruneLocked`). ✓
- §3 persistence (nil / populated / legacy) → Task 3. ✓
- §4 HTTP round-trip → Task 4; UI rows + `make ui` → Task 5. ✓
- §5 testing — `cooldownFor` (T1), per-type + 0-disable + prune (T2), store (T3), HTTP (T4). ✓
- CHANGELOG Added (T1) + Fixed (T2). ✓
- Live demo / final verification → Final sections. ✓

**Placeholder scan:** No TBD/TODO/"add error handling"/"similar to Task N"; every code step shows full code. ✓

**Type consistency:** `cooldownFor(t EventType) time.Duration`, `cooldownEntry{at time.Time; typ EventType}`, `d.last map[string]cooldownEntry`, `CooldownOverrides map[EventType]int` / TS `cooldown_overrides?: Record<string, number>` — names and signatures match across Tasks 1→2→3→4→5. The cooldown key string is identical to the existing code. ✓
