# Unified Host Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make one local daemon per host the single agent — so `marshal list` shows every host app and `marshal start` apps appear in the dashboard whenever the host is enrolled, with no second self-enroll agent store.

**Architecture:** Add `marshal enroll`/`unenroll` to write/clear server config in the default store; replace the daemon's once-at-startup fleet wiring with a supervisor goroutine that watches the store and (re)connects the fleet client live; refactor `--self-enroll` to enroll the one default-store daemon instead of running a private agent. Clean cutover, no migration code.

**Tech Stack:** Go 1.26, cobra CLI, gRPC fleet stream (`internal/fleet`), Unix-socket daemon (`internal/daemon`), file-backed `internal/store`.

## Global Constraints

- Go toolchain `/opt/homebrew/bin/go` (1.26.x). Module path `marshal`; imports `github.com/REDDE4D/marshal-pm/internal/...`.
- TDD: failing test first, watch it fail, minimal code, watch it pass, commit. `go test ./... -race -count=1` must stay green; `go vet ./...` clean; `gofmt -l .` lists nothing.
- Branch: `unified-host-agent` (off `dev`). Commit messages: imperative subject + trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Every change adds a `CHANGELOG.md` `[Unreleased]` entry as part of the work.
- The fleet connection comparison key is `{address, name, fingerprint, ca, enrollToken}` — it MUST NOT include the persisted per-agent fleet token, or the supervisor will restart the client immediately after it enrolls and persists that token.
- Default fleet poll interval: 2s. Tests override it via `daemon.WithFleetPollInterval`.

---

## File Structure

- `internal/store/store.go` — add `ClearServer()` (remove `fleet.json` + `fleet-token`).
- `internal/daemon/fleetsupervisor.go` *(new)* — `superviseFleet`, `fleetTarget`, `loadFleetTarget`, `fleetRunner`. One responsibility: keep at most one live fleet client matching the store's config.
- `internal/daemon/fleetsupervisor_test.go` *(new)* — supervisor behavior with an injected runner.
- `internal/daemon/server.go` — replace the inline fleet block (lines ~327–353) with a `fleetRunner` + `go superviseFleet(...)`; add `fleetPoll` to `runOptions` and `WithFleetPollInterval`.
- `cmd/marshal/enroll.go` *(new)* — `enrollCmd`, `unenrollCmd`.
- `cmd/marshal/enroll_test.go` *(new)* — enroll writes config; unenroll clears it.
- `cmd/marshal/main.go` — register `enrollCmd()` and `unenrollCmd()`.
- `cmd/marshal/selfenroll.go` — Phase 2 refactor onto the default store + new lifecycle.
- `cmd/marshal/control.go` — Phase 3 (optional) enrollment header in `printProcs`/list.
- `CHANGELOG.md`, `docs/handoffs/…` — notes + handoff.

---

# Phase 1 — `enroll`/`unenroll` + live fleet supervisor

Delivers the core need: a standing daemon can join a server and report all its apps with no restart.

## Task 1: `Store.ClearServer`

**Files:**
- Modify: `internal/store/store.go` (after `SaveFleetToken`, ~line 157)
- Test: `internal/store/store_test.go`

**Interfaces:**
- Produces: `func (s *Store) ClearServer() error` — removes the server config file and the fleet-token file; missing files are not an error.

- [ ] **Step 1: Write the failing test**

```go
func TestClearServerRemovesConfigAndToken(t *testing.T) {
	st := NewAt(t.TempDir())
	if err := st.SaveServer(&config.ServerConfig{Address: "srv:9000", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveFleetToken("agent-tok"); err != nil {
		t.Fatal(err)
	}
	if err := st.ClearServer(); err != nil {
		t.Fatalf("ClearServer: %v", err)
	}
	if sc, _ := st.LoadServer(); sc != nil {
		t.Errorf("server config still present: %+v", sc)
	}
	if tok, _ := st.LoadFleetToken(); tok != "" {
		t.Errorf("fleet token still present: %q", tok)
	}
	// Idempotent: clearing again on an empty store is fine.
	if err := st.ClearServer(); err != nil {
		t.Errorf("second ClearServer: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestClearServerRemoves -v`
Expected: FAIL — `st.ClearServer undefined`.

- [ ] **Step 3: Write minimal implementation**

Add to `internal/store/store.go` (`errors` and `io/fs` are already imported):

```go
// ClearServer removes the saved server config and the per-agent fleet token, so
// the daemon's fleet supervisor drops the connection (unenroll). Missing files
// are not an error.
func (s *Store) ClearServer() error {
	for _, p := range []string{s.serverPath(), s.FleetTokenPath()} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestClearServerRemoves -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat(store): ClearServer removes fleet config + token

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

## Task 2: Fleet supervisor (watcher loop)

**Files:**
- Create: `internal/daemon/fleetsupervisor.go`
- Test: `internal/daemon/fleetsupervisor_test.go`

**Interfaces:**
- Produces:
  - `type fleetTarget struct { address, name, fingerprint, ca, enrollToken string }` (comparable; the connection identity, excluding the persisted token).
  - `type fleetRunner func(ctx context.Context, tgt fleetTarget, fleetToken string)` — starts a fleet client and blocks until `ctx` is cancelled; returns immediately if the target isn't connectable.
  - `func loadFleetTarget(st *store.Store) (tgt fleetTarget, fleetToken string, enrolled bool)`
  - `func superviseFleet(ctx context.Context, st *store.Store, poll time.Duration, run fleetRunner)` — maintains at most one live runner matching the store; cancels it on config change/clear; returns when `ctx` is done.

- [ ] **Step 1: Write the failing test**

```go
package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/config"
	"github.com/REDDE4D/marshal-pm/internal/store"
)

func TestSuperviseFleetStartsOnEnrollStopsOnClear(t *testing.T) {
	st := store.NewAt(t.TempDir())

	var mu sync.Mutex
	type run struct {
		tgt  fleetTarget
		tok  string
		done chan struct{}
	}
	var runs []*run
	runner := func(ctx context.Context, tgt fleetTarget, tok string) {
		r := &run{tgt: tgt, tok: tok, done: make(chan struct{})}
		mu.Lock()
		runs = append(runs, r)
		mu.Unlock()
		<-ctx.Done()
		close(r.done)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go superviseFleet(ctx, st, 10*time.Millisecond, runner)

	// Not enrolled → no runner.
	time.Sleep(40 * time.Millisecond)
	mu.Lock()
	if len(runs) != 0 {
		mu.Unlock()
		t.Fatalf("runner started before enrollment: %d", len(runs))
	}
	mu.Unlock()

	// Enroll → exactly one runner with the right target.
	if err := st.SaveServer(&config.ServerConfig{Address: "srv:9000", Name: "h1", Token: "enr", Fingerprint: "fp"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(runs) == 1 })
	mu.Lock()
	if runs[0].tgt.address != "srv:9000" || runs[0].tgt.enrollToken != "enr" {
		mu.Unlock()
		t.Fatalf("bad target: %+v", runs[0].tgt)
	}
	first := runs[0]
	mu.Unlock()

	// Unenroll → the running client is cancelled.
	if err := st.ClearServer(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.done:
	case <-time.After(time.Second):
		t.Fatal("runner not cancelled after unenroll")
	}
}

// Persisting the per-agent fleet token (what the client does after enrolling)
// must NOT cause a restart.
func TestSuperviseFleetIgnoresPersistedToken(t *testing.T) {
	st := store.NewAt(t.TempDir())
	if err := st.SaveServer(&config.ServerConfig{Address: "srv:9000", Name: "h1", Token: "enr", Fingerprint: "fp"}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	count := 0
	runner := func(ctx context.Context, _ fleetTarget, _ string) {
		mu.Lock()
		count++
		mu.Unlock()
		<-ctx.Done()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go superviseFleet(ctx, st, 10*time.Millisecond, runner)
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return count == 1 })

	// Simulate the client persisting its per-agent token.
	if err := st.SaveFleetToken("agent-token"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if count != 1 {
		t.Fatalf("supervisor restarted on token persist: count=%d", count)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestSuperviseFleet -v`
Expected: FAIL — `superviseFleet`, `fleetTarget`, etc. undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/daemon/fleetsupervisor.go`:

```go
package daemon

import (
	"context"
	"os"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/store"
)

// fleetTarget is the identity of the server connection the agent should hold. It
// is comparable and deliberately excludes the persisted per-agent token, which
// is derived state — including it would restart the client right after it
// enrolls and writes that token back to the store.
type fleetTarget struct {
	address     string
	name        string
	fingerprint string
	ca          string
	enrollToken string
}

// fleetRunner starts a fleet client for tgt (using fleetToken if already
// minted, else tgt.enrollToken) and blocks until ctx is cancelled. It returns
// early if the target is not connectable (e.g. bad TLS pin).
type fleetRunner func(ctx context.Context, tgt fleetTarget, fleetToken string)

// loadFleetTarget derives the desired connection from the store. enrolled is
// false when there's no server address or no usable credential.
func loadFleetTarget(st *store.Store) (tgt fleetTarget, fleetToken string, enrolled bool) {
	sc, err := st.LoadServer()
	if err != nil || sc == nil || sc.Address == "" {
		return fleetTarget{}, "", false
	}
	fleetToken, _ = st.LoadFleetToken()
	if fleetToken == "" && sc.Token == "" {
		return fleetTarget{}, "", false
	}
	name := sc.Name
	if name == "" {
		if h, hErr := os.Hostname(); hErr == nil {
			name = h
		} else {
			name = "unknown"
		}
	}
	return fleetTarget{
		address:     sc.Address,
		name:        name,
		fingerprint: sc.Fingerprint,
		ca:          sc.CA,
		enrollToken: sc.Token,
	}, fleetToken, true
}

// superviseFleet keeps at most one live fleet runner matching the store's
// current config, reconnecting on change and stopping on unenroll. It applies
// the current config immediately, then re-checks every poll until ctx is done.
func superviseFleet(ctx context.Context, st *store.Store, poll time.Duration, run fleetRunner) {
	var (
		curr   fleetTarget
		hasCur bool
		cancel context.CancelFunc
	)
	stop := func() {
		if cancel != nil {
			cancel()
			cancel = nil
		}
		hasCur = false
		curr = fleetTarget{}
	}
	defer stop()

	tick := time.NewTicker(poll)
	defer tick.Stop()
	for {
		tgt, tok, enrolled := loadFleetTarget(st)
		switch {
		case !enrolled:
			if hasCur {
				stop()
			}
		case !hasCur || tgt != curr:
			stop()
			cctx, ccancel := context.WithCancel(ctx)
			cancel = ccancel
			curr, hasCur = tgt, true
			go run(cctx, tgt, tok)
		}

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run TestSuperviseFleet -race -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/fleetsupervisor.go internal/daemon/fleetsupervisor_test.go
git commit -m "feat(daemon): fleet supervisor reconnects on store config changes

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

## Task 3: Wire the supervisor into the daemon

**Files:**
- Modify: `internal/daemon/server.go` — `runOptions`/`Option` (~lines 219–241), the inline fleet block (~lines 327–353).

**Interfaces:**
- Consumes: `superviseFleet`, `fleetTarget`, `fleetRunner` (Task 2); `fleetauth.ClientTLS`, `fleet.New`, the existing adapters `srv.fleetSnapshot()`, `metricsSince(mdb)`, `logsSince(reg)`, `hostSampler.Sample`, `srv.handleFleetCommand`.
- Produces: `func WithFleetPollInterval(d time.Duration) Option`.

- [ ] **Step 1: Write the failing test**

Add to `internal/daemon/server_test.go` (or a new `fleet_wiring_test.go`):

```go
func TestWithFleetPollIntervalSetsOption(t *testing.T) {
	var o runOptions
	WithFleetPollInterval(250 * time.Millisecond)(&o)
	if o.fleetPoll != 250*time.Millisecond {
		t.Fatalf("fleetPoll = %v, want 250ms", o.fleetPoll)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestWithFleetPollIntervalSetsOption -v`
Expected: FAIL — `o.fleetPoll` undefined / `WithFleetPollInterval` undefined.

- [ ] **Step 3: Write minimal implementation**

In `internal/daemon/server.go`, add the field + default + option:

```go
type runOptions struct {
	sampleInterval time.Duration
	retention      time.Duration
	logRetention   logs.Policy
	fleetPoll      time.Duration
}
```

```go
// WithFleetPollInterval overrides how often the fleet supervisor re-reads the
// store's server config (default 2s; used by tests).
func WithFleetPollInterval(d time.Duration) Option {
	return func(o *runOptions) { o.fleetPoll = d }
}
```

Set the default in `Run` (the `cfg := runOptions{...}` literal at ~line 262):

```go
	cfg := runOptions{sampleInterval: 5 * time.Second, retention: 168 * time.Hour, logRetention: logs.DefaultPolicy, fleetPoll: 2 * time.Second}
```

Replace the entire inline fleet block (the `if sc, err := st.LoadServer(); err == nil && sc != nil { ... }` at ~lines 327–353) with a runner + supervisor:

```go
	run := func(cctx context.Context, tgt fleetTarget, fleetTok string) {
		tlsCfg, tErr := fleetauth.ClientTLS(tgt.fingerprint, tgt.ca)
		if tErr != nil {
			log.Printf("fleet: disabled, bad TLS config: %v", tErr)
			return
		}
		fc := fleet.New(tgt.address, tgt.name, version.String(),
			srv.fleetSnapshot(),
			fleet.WithTLS(tlsCfg),
			fleet.WithAuth(fleetTok, tgt.enrollToken, st.SaveFleetToken),
			fleet.WithMetrics(metricsSince(mdb)),
			fleet.WithLogs(logsSince(reg)),
			fleet.WithHost(func() *pb.HostMetrics { return hostSampler.Sample() }),
			fleet.WithCommands(srv.handleFleetCommand))
		fc.Run(cctx)
	}
	go superviseFleet(serveCtx, st, cfg.fleetPoll, run)
```

(`name`/`fleetTok` are now derived inside `loadFleetTarget`/the supervisor, so the old name-resolution and `LoadFleetToken` lines in this block are removed.)

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/daemon/ -race -count=1`
Expected: PASS (existing daemon tests + the option test). Confirm `go build ./...` succeeds.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/server.go internal/daemon/server_test.go
git commit -m "refactor(daemon): drive the fleet client from the supervisor

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

## Task 4: `marshal enroll` / `marshal unenroll`

**Files:**
- Create: `cmd/marshal/enroll.go`
- Create: `cmd/marshal/enroll_test.go`
- Modify: `cmd/marshal/main.go` (register the commands)

**Interfaces:**
- Consumes: `store.New`, `store.ClearServer` (Task 1), `config.ServerConfig`, `fleetauth.ClientTLS`.
- Produces: `func enrollCmd() *cobra.Command`, `func unenrollCmd() *cobra.Command`.

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"io"
	"testing"

	"github.com/REDDE4D/marshal-pm/internal/store"
)

func TestEnrollWritesServerConfig(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cmd := enrollCmd()
	cmd.SetArgs([]string{"srv:9000", "--token", "enr", "--fingerprint", "AA:BB", "--name", "h1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	st, err := store.New()
	if err != nil {
		t.Fatal(err)
	}
	sc, _ := st.LoadServer()
	if sc == nil || sc.Address != "srv:9000" || sc.Token != "enr" || sc.Fingerprint != "AA:BB" || sc.Name != "h1" {
		t.Fatalf("server config = %+v", sc)
	}
}

func TestEnrollRequiresTokenAndPin(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	noTok := enrollCmd()
	noTok.SetArgs([]string{"srv:9000", "--fingerprint", "AA"})
	noTok.SetOut(io.Discard)
	noTok.SetErr(io.Discard)
	if err := noTok.Execute(); err == nil {
		t.Error("expected error without --token")
	}
	noPin := enrollCmd()
	noPin.SetArgs([]string{"srv:9000", "--token", "enr"})
	noPin.SetOut(io.Discard)
	noPin.SetErr(io.Discard)
	if err := noPin.Execute(); err == nil {
		t.Error("expected error without --fingerprint/--ca")
	}
}

func TestUnenrollClearsServerConfig(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	st, _ := store.New()
	if err := st.SaveServer(&config.ServerConfig{Address: "srv:9000", Token: "enr"}); err != nil {
		t.Fatal(err)
	}
	cmd := unenrollCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unenroll: %v", err)
	}
	if sc, _ := st.LoadServer(); sc != nil {
		t.Fatalf("server config still present: %+v", sc)
	}
}
```

(Add `"github.com/REDDE4D/marshal-pm/internal/config"` to the test imports for `TestUnenrollClearsServerConfig`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/marshal/ -run 'TestEnroll|TestUnenroll' -v`
Expected: FAIL — `enrollCmd`/`unenrollCmd` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `cmd/marshal/enroll.go`:

```go
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/REDDE4D/marshal-pm/internal/config"
	"github.com/REDDE4D/marshal-pm/internal/fleetauth"
	"github.com/REDDE4D/marshal-pm/internal/store"
)

// enrollCmd points this host's daemon at a central server, so all of its apps
// appear in that server's dashboard. The running daemon picks the change up
// within the fleet poll interval; no restart needed.
func enrollCmd() *cobra.Command {
	var token, fingerprint, ca, name string
	c := &cobra.Command{
		Use:   "enroll <server-address>",
		Short: "Enroll this host's daemon with a central server (so its apps appear in the dashboard)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if token == "" {
				return fmt.Errorf("--token is required (mint one on the server: marshal server token --rotate enroll)")
			}
			if fingerprint == "" && ca == "" {
				return fmt.Errorf("one of --fingerprint or --ca is required to pin the server's TLS certificate")
			}
			if _, err := fleetauth.ClientTLS(fingerprint, ca); err != nil {
				return fmt.Errorf("invalid TLS pin: %w", err)
			}
			if name == "" {
				if h, err := os.Hostname(); err == nil {
					name = h
				}
			}
			st, err := store.New()
			if err != nil {
				return err
			}
			// A fresh enrollment must not reuse a stale per-agent token.
			if err := st.ClearServer(); err != nil {
				return err
			}
			if err := st.SaveServer(&config.ServerConfig{
				Address: args[0], Name: name, Token: token, Fingerprint: fingerprint, CA: ca,
			}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "marshal: enrolled with %s as %q — a running daemon will connect within a few seconds.\n", args[0], name)
			return nil
		},
	}
	c.Flags().StringVar(&token, "token", "", "enrollment token minted on the server")
	c.Flags().StringVar(&fingerprint, "fingerprint", "", "pinned server cert SHA-256 fingerprint")
	c.Flags().StringVar(&ca, "ca", "", "CA file to verify the server cert (alternative to --fingerprint)")
	c.Flags().StringVar(&name, "name", "", "agent name reported to the server (default: hostname)")
	return c
}

// unenrollCmd disconnects this host's daemon from its central server.
func unenrollCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unenroll",
		Short: "Disconnect this host's daemon from its central server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := store.New()
			if err != nil {
				return err
			}
			if err := st.ClearServer(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "marshal: unenrolled — a running daemon will drop the connection within a few seconds.")
			return nil
		},
	}
}
```

Register in `cmd/marshal/main.go` where other commands are added (find the `AddCommand` block; mirror the existing style):

```go
	root.AddCommand(enrollCmd())
	root.AddCommand(unenrollCmd())
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/marshal/ -run 'TestEnroll|TestUnenroll' -v`
Expected: PASS (all three).

- [ ] **Step 5: Commit**

```bash
git add cmd/marshal/enroll.go cmd/marshal/enroll_test.go cmd/marshal/main.go
git commit -m "feat(cli): marshal enroll / unenroll to join or leave a server

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

## Task 5: End-to-end — live enroll reaches a server

**Files:**
- Create: `cmd/marshal/enroll_e2e_test.go`

**Interfaces:**
- Consumes: `daemon.Run`, `daemon.WithFleetPollInterval`, `client.Connect`, `store.NewAt`, and the existing fleet fake-server pattern (`internal/fleet/client_test.go` `newFakeServer`). Build a minimal in-test TLS gRPC `pb.FleetServer` that records received `Hello`/`Snapshot` agent messages, OR reuse the helper pattern from `internal/fleet/client_test.go`.

> Note: this test needs a TLS fleet server with a known fingerprint. Model it on `newFakeServer(t)` in `internal/fleet/client_test.go` (copy the minimal handshake-recording server into this test file; keep it local). The agent must enroll, then its host/app snapshot must arrive at the server after `enroll`.
>
> **If the fake-server extraction proves heavy, this task may be deferred** without blocking the phase: the supervisor integration test (Task 2) plus the live demo (Verification §2) already exercise the full enroll → report → unenroll path. Do not skip it silently — note the deferral in the commit/handoff if you defer.

- [ ] **Step 1: Write the failing test**

```go
//go:build e2e_fleet

package main

// Sketch — adapt the fake fleet server from internal/fleet/client_test.go.
// 1. Start a fake TLS FleetServer; capture its addr + cert fingerprint.
// 2. st := store.NewAt(t.TempDir()); go daemon.Run(ctx, st, daemon.WithFleetPollInterval(50*time.Millisecond)).
// 3. client.Connect(st) → Start one app → it shows in List (host view).
// 4. Assert the fake server has NOT seen this agent yet.
// 5. st.SaveServer(&config.ServerConfig{Address: fakeAddr, Token: "enr", Fingerprint: fp, Name: "h1"}).
// 6. waitFor: the fake server received a Hello from "h1" and a snapshot containing the app.
// 7. st.ClearServer() → waitFor: the agent's stream closes / no more snapshots.
```

Implement the sketch concretely using the copied fake-server helper. Assertions:
- after step 3, `List` returns the started app;
- after step 6, the fake server's recorded snapshots include the app label;
- after step 7, the connection is observed to drop.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/marshal/ -tags e2e_fleet -run TestEnrollE2E -v`
Expected: FAIL initially (before the helper/asserts are filled in), then drive it to green.

- [ ] **Step 3: Make it pass**

Fill in the fake server + assertions until green. Keep timeouts generous (use the `waitFor` polling helper, 2–5s deadlines) to avoid flakes — see the existing flaky-SIGINT deadline note in CLAUDE.md/handoffs.

- [ ] **Step 4: Run the full suite**

Run: `go test ./... -race -count=1` and `go test ./cmd/marshal/ -tags e2e_fleet -race -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/marshal/enroll_e2e_test.go
git commit -m "test(cli): e2e — live enroll streams host apps to a server

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

## Task 6: Phase 1 changelog + verification

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add the entry** under `## [Unreleased]`:

```markdown
### Added
- **`marshal enroll <server> --token --fingerprint` / `marshal unenroll`** join or leave a central
  server from a host's local daemon. Once enrolled, every app on the host (everything `marshal start`
  manages) appears in that server's dashboard automatically — no per-app step. The daemon now watches
  its server config and connects/reconnects live, so enrolling no longer requires a daemon restart.
```

- [ ] **Step 2: Verify**

Run: `go test ./... -race -count=1 && go vet ./... && gofmt -l .`
Expected: all green, gofmt silent.

- [ ] **Step 3: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs(changelog): marshal enroll/unenroll + live fleet reconnect

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

# Phase 2 — `--self-enroll` onto the unified agent

Makes the single-box quick-start enroll the one default-store daemon instead of a private agent.

## Task 7: Refactor `runSelfEnroll`

**Files:**
- Modify: `cmd/marshal/selfenroll.go`
- Modify: `cmd/marshal/server_test.go` / any self-enroll test (update for the new lifecycle).

**Interfaces:**
- Consumes: `store.New` (default store), `server.ServeDir`, `appToSpec`, `client.Connect`, `pb.StartRequest` (the same path `startCmd` uses).
- Behavior change: `--self-enroll` no longer runs an in-process `daemon.Run` against `<serverData>/agent`. It (1) prepares cert/password/token while the server is down, (2) writes the localhost server-block + fresh enroll token into the **default** store, (3) serves the server in-process (foreground), and (4) starts the yaml's apps on the local daemon via `client.Connect` (auto-spawning it). Ctrl-C stops the server; the daemon and its apps persist.

- [ ] **Step 1: Update the test first**

Write/adjust a test asserting the new contract (no `<serverData>/agent` store is created; the default store gets the server block; apps are started on the daemon). Because `runSelfEnroll` is long-running, test the **extracted helper** rather than the whole serve loop:

Extract a pure helper and test it:

```go
// prepareSelfEnroll writes the localhost server block + apps into the DEFAULT
// store and returns the app specs to start. (Extracted for testability.)
func prepareSelfEnroll(st *store.Store, dataDir, listenPort, enrollToken, fingerprint, hostname string, apps []config.App) error
```

Test:

```go
func TestPrepareSelfEnrollUsesDefaultStore(t *testing.T) {
	st := store.NewAt(t.TempDir())
	apps := []config.App{{Name: "a", Cmd: "./a"}}
	if err := prepareSelfEnroll(st, t.TempDir(), "9000", "enr", "fp", "h1", apps); err != nil {
		t.Fatal(err)
	}
	sc, _ := st.LoadServer()
	if sc == nil || sc.Address != "localhost:9000" || sc.Token != "enr" || sc.Fingerprint != "fp" {
		t.Fatalf("server block = %+v", sc)
	}
}
```

- [ ] **Step 2: Run it red, then implement**

Extract `prepareSelfEnroll` from the current body (replacing the `agentDir`/`store.NewAt(agentDir)` block at `selfenroll.go:73-85`), pointing at the passed-in default store. Then rewrite `runSelfEnroll` to:
- use `st, _ := store.New()` (default store);
- call `prepareSelfEnroll`;
- `go server.ServeDir(...)` (unchanged);
- start apps via `client.Connect(st)` + `c.Start(ctx, &pb.StartRequest{Apps: specs})` instead of `daemon.Run`.

Update the foreground/Ctrl-C messaging to state that apps keep running after the server stops.

- [ ] **Step 3: Run tests**

Run: `go test ./cmd/marshal/ -race -count=1`
Expected: PASS. Manually dogfood per the live-demo convention (see Verification section below).

- [ ] **Step 4: Commit**

```bash
git add cmd/marshal/selfenroll.go cmd/marshal/server_test.go
git commit -m "refactor(cli): --self-enroll enrolls the one default-store daemon

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

## Task 8: Phase 2 changelog + clean-cutover note

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add** under `## [Unreleased]`:

```markdown
### Changed
- **`marshal server startup --self-enroll` now enrolls the one local daemon** instead of running a
  separate in-process agent under `<serverData>/agent`. There is now a single agent per host, so
  `marshal list` and the dashboard agree. Ctrl-C stops the server/dashboard; supervised apps keep
  running under the persistent daemon (stop them with `marshal stop`, the daemon with `marshal kill`).
  Upgrade note: a host that used the old self-enroll agent should remove `<serverData>/agent` and
  re-add its apps under the unified daemon.
```

- [ ] **Step 2: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs(changelog): unified self-enroll lifecycle + cutover note

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

# Phase 3 (optional) — enrollment visibility in `marshal list`

## Task 9: Enrollment header on `marshal list`

**Files:**
- Modify: `cmd/marshal/control.go` (`listCmd`/`printProcs`).

**Interfaces:**
- Consumes: `store.New`, `store.LoadServer`.

- [ ] **Step 1: Write the failing test** — a renderer that, given an optional `*config.ServerConfig`, prints `enrolled → <addr>` or `not enrolled` above the table.

```go
func TestEnrollmentHeader(t *testing.T) {
	if got := enrollmentHeader(&config.ServerConfig{Address: "srv:9000"}); !strings.Contains(got, "srv:9000") || !strings.Contains(got, "enrolled") {
		t.Errorf("header = %q", got)
	}
	if got := enrollmentHeader(nil); !strings.Contains(got, "not enrolled") {
		t.Errorf("header = %q", got)
	}
}
```

- [ ] **Step 2–4:** Implement `enrollmentHeader(*config.ServerConfig) string`; have `listCmd` load the server config and print the header before `printProcs`. Run tests green.

- [ ] **Step 5: Commit**

```bash
git add cmd/marshal/control.go
git commit -m "feat(cli): show enrollment status above marshal list

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

# Verification (before finishing the branch)

1. `go test ./... -race -count=1` green; `go vet ./...` clean; `gofmt -l .` silent.
2. **Live demo** (per CLAUDE.md live-demo + standard-ports + teardown conventions), scratch dir `XDG_DATA_HOME=/tmp/marshal-demo/...`, server on `:9000`/`:9001`:
   - Start the server (down) → set password, rotate enroll token, capture fingerprint.
   - Start the server with `--http-listen :9001`.
   - `marshal start demo.yaml` (a couple of `sleep` apps) → confirm `marshal list` shows them.
   - `marshal enroll localhost:9000 --token <tok> --fingerprint <fp>` → within ~2s the apps appear in the dashboard (`https://localhost:9001`) and in `marshal fleet ps`.
   - `marshal start another.yaml` → the new app appears in the dashboard automatically (no re-enroll).
   - `marshal unenroll` → the host drops from the dashboard; `marshal list` still shows the apps.
   - Teardown: `marshal kill`; stop the server; remove the scratch dir; `pgrep -fl marshal` shows no orphans.
3. Write the handoff to `docs/handoffs/2026-06-25-unified-host-agent.md`.
4. Finish the branch via `superpowers:finishing-a-development-branch` (merge `dev` `--no-ff`).
