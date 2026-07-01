# Stable IDs, Multi-Target Selectors, and `restart --update-env` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make app IDs stable across restarts, let `stop`/`restart`/`delete`/`reset` accept multiple targets, and add `marshal restart <marshal.yaml> --update-env` to reload env in place.

**Architecture:** Three independent chunks. (1) Persist a daemon-assigned `ID` on each `config.App` in `dump.json` so `mgr.Add` reuses it across restart/resurrect. (2) Expand CLI selector args (multiple args + comma lists + config-file paths) into a de-duplicated target loop over the existing per-target RPCs. (3) A new `UpdateEnv` daemon RPC + manager method that swaps a stored app's env map and restarts it, driven by a `--update-env` flag that re-reads the config file CLI-side.

**Tech Stack:** Go 1.26 (Homebrew), cobra CLI, gRPC/protobuf (`internal/pb`), `go test`.

## Global Constraints

- **TDD:** failing test first, then minimal implementation (copied verbatim from CLAUDE.md).
- **Branch:** all work on `feature/stable-ids-selectors-envreload` (already created off `dev`). Never commit to `main`.
- **Lint/format before finishing each task:** `go vet ./... && gofmt -l .` (gofmt must list nothing).
- **Race check before finishing each feature group:** `go test ./... -race -count=1`.
- **Changelog:** add an entry under `## [Unreleased]` in `CHANGELOG.md` as part of the feature (last task of each group).
- **Commit trailer:** `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- **Module path:** `marshal`; imports are `github.com/REDDE4D/marshal-pm/internal/...`.
- **Proto regen:** `make proto` (runs `./scripts/gen-proto.sh`) after editing `proto/marshal/v1/daemon.proto`.
- **Review checkpoints:** three groups (Stable IDs → Multi-target → Env reload). Each group is independently mergeable; open as separate PRs off `dev` if preferred, or merge the whole branch once.

---

## File Structure

- `internal/config/config.go` — add persisted `ID` field to `App` (Group 1).
- `internal/manager/manager.go` — `Add` honors/assigns ID; new `maxAppID` helper; new `UpdateEnv` method (Groups 1 & 3).
- `internal/manager/manager_test.go` — ID assignment + `UpdateEnv` unit tests.
- `internal/store/store_test.go` — dump.json ID round-trip test.
- `proto/marshal/v1/daemon.proto` + regenerated `internal/pb/*` — `UpdateEnv` RPC (Group 3).
- `internal/daemon/server.go` — `UpdateEnv` handler (Group 3).
- `internal/daemon/server_test.go` — `UpdateEnv` handler test.
- `cmd/marshal/control.go` — `expandSelectorArgs`, `runSelector`, revised `selectorCmd`, new `restartCmd`, `runRestartUpdateEnv` (Groups 2 & 3).
- `cmd/marshal/control_test.go` — selector expansion + flag validation tests.
- `cmd/marshal/main.go` — swap the `restart` `selectorCmd` for `restartCmd()` (Group 2).
- `CHANGELOG.md` — one entry per group.

---

# GROUP 1 — Stable, persistent IDs

### Task 1: Persist an `ID` field on `config.App`

**Files:**
- Modify: `internal/config/config.go` (the `App` struct, near line 152)
- Test: `internal/store/store_test.go`

**Interfaces:**
- Produces: `config.App.ID int` — daemon-assigned, `yaml:"-" json:"id,omitempty"`, persisted in `dump.json`, never read from user YAML.

- [ ] **Step 1: Write the failing test**

Add to `internal/store/store_test.go`:

```go
func TestSaveLoadRoundTripsAppID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	s, err := store.New()
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	in := []config.App{
		{Name: "a", Cmd: "true", ID: 1},
		{Name: "b", Cmd: "true", ID: 2},
	}
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(out) != 2 || out[0].ID != 1 || out[1].ID != 2 {
		t.Fatalf("IDs not round-tripped: %+v", out)
	}
}
```

(Check the top of `store_test.go` for the exact import path of `config`; it is `github.com/REDDE4D/marshal-pm/internal/config`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestSaveLoadRoundTripsAppID`
Expected: FAIL — `unknown field 'ID' in struct literal of type config.App`.

- [ ] **Step 3: Add the field**

In `internal/config/config.go`, inside `type App struct`, add as the first field (above `Name`):

```go
	// ID is the daemon-assigned stable identifier, persisted in dump.json and
	// reused across restarts/resurrect. It is never read from user YAML.
	ID int `yaml:"-" json:"id,omitempty"`
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestSaveLoadRoundTripsAppID`
Expected: PASS.

- [ ] **Step 5: Format + commit**

```bash
go vet ./... && gofmt -l .
git add internal/config/config.go internal/store/store_test.go
git commit -m "feat(config): persist daemon-assigned app ID in dump.json

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: `mgr.Add` honors a persisted ID, else assigns the next free one

**Files:**
- Modify: `internal/manager/manager.go` (`Add`, near line 141; add `maxAppID` helper)
- Test: `internal/manager/manager_test.go`

**Interfaces:**
- Consumes: `config.App.ID` (Task 1).
- Produces: `Add` sets `ma.id` = `app.ID` when `> 0` and not already taken, else `maxAppID()+1`; writes it back to `ma.spec.ID`; advances `m.nextID` to at least `ma.id`. New unexported `func (m *Manager) maxAppID() int` (caller holds `m.mu`).

- [ ] **Step 1: Write the failing tests**

Add to `internal/manager/manager_test.go` (reuse the package's existing manager constructor/helpers — check how other tests build a `*Manager`, typically `manager.New(ctx)` with a `context.Background()` and a trivial app whose `Cmd` is `"true"` and `Instances` defaults to 1 after validation; if tests here add apps with `Instances: 1` explicitly, match that):

```go
func TestAddAssignsSequentialIDsWhenUnset(t *testing.T) {
	m := manager.New(context.Background())
	for i, name := range []string{"a", "b", "c"} {
		snaps, err := m.Add(config.App{Name: name, Cmd: "true", Instances: 1})
		if err != nil {
			t.Fatalf("Add %s: %v", name, err)
		}
		if snaps[0].ID != i+1 {
			t.Fatalf("app %s got ID %d, want %d", name, snaps[0].ID, i+1)
		}
	}
}

func TestAddReusesPersistedIDAndAdvancesCounter(t *testing.T) {
	m := manager.New(context.Background())
	snaps, err := m.Add(config.App{Name: "a", Cmd: "true", Instances: 1, ID: 5})
	if err != nil {
		t.Fatalf("Add a: %v", err)
	}
	if snaps[0].ID != 5 {
		t.Fatalf("got ID %d, want 5", snaps[0].ID)
	}
	// A subsequent zero-ID add must not collide with 5.
	snaps, err = m.Add(config.App{Name: "b", Cmd: "true", Instances: 1})
	if err != nil {
		t.Fatalf("Add b: %v", err)
	}
	if snaps[0].ID != 6 {
		t.Fatalf("got ID %d, want 6", snaps[0].ID)
	}
}

func TestAddDuplicateIncomingIDFallsBackToMaxPlusOne(t *testing.T) {
	m := manager.New(context.Background())
	if _, err := m.Add(config.App{Name: "a", Cmd: "true", Instances: 1, ID: 3}); err != nil {
		t.Fatalf("Add a: %v", err)
	}
	snaps, err := m.Add(config.App{Name: "b", Cmd: "true", Instances: 1, ID: 3}) // collides
	if err != nil {
		t.Fatalf("Add b: %v", err)
	}
	if snaps[0].ID != 4 {
		t.Fatalf("collision not resolved: got ID %d, want 4", snaps[0].ID)
	}
}
```

Stop the spawned `true` processes at test end is unnecessary — they exit immediately; the manager's supervisor handles it. If the existing tests call a cleanup (e.g. `m.StopAll()` via `t.Cleanup`), mirror that.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/manager/ -run 'TestAdd(AssignsSequentialIDsWhenUnset|ReusesPersistedID|DuplicateIncomingID)'`
Expected: FAIL (IDs come from the old `m.nextID++`, so Task 2's reuse/fallback assertions fail).

- [ ] **Step 3: Implement**

In `internal/manager/manager.go`, replace the ID-assignment lines in `Add` (currently `m.nextID++` and `ma := &managedApp{id: m.nextID, name: app.Name, spec: app}`) with:

```go
	id := app.ID
	if id <= 0 || m.idTaken(id) {
		id = m.maxAppID() + 1
	}
	if id > m.nextID {
		m.nextID = id
	}
	app.ID = id
	ma := &managedApp{id: id, name: app.Name, spec: app}
```

Add these helpers near `resolve` (both assume the caller holds `m.mu`):

```go
// maxAppID returns the largest id currently in use, or 0 if none.
func (m *Manager) maxAppID() int {
	max := 0
	for _, a := range m.apps {
		if a.id > max {
			max = a.id
		}
	}
	return max
}

// idTaken reports whether any managed app already uses id.
func (m *Manager) idTaken(id int) bool {
	for _, a := range m.apps {
		if a.id == id {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/manager/ -run 'TestAdd'`
Expected: PASS (including any pre-existing `TestAdd*`).

- [ ] **Step 5: Format + commit**

```bash
go vet ./... && gofmt -l .
git add internal/manager/manager.go internal/manager/manager_test.go
git commit -m "feat(manager): reuse persisted app IDs, assign next-free otherwise

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Migration integration test + changelog (Group 1 close-out)

**Files:**
- Test: `internal/manager/manager_test.go`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: `Add` (Task 2), `Specs()` (existing).

- [ ] **Step 1: Write the failing test**

This proves the automatic migration: apps loaded with no ID get contiguous 1..N, and `Specs()` then carries those IDs (so the next `store.Save` persists them).

```go
func TestLoadWithoutIDsAssignsContiguousThenSpecsCarriesThem(t *testing.T) {
	m := manager.New(context.Background())
	// Simulate a pre-upgrade dump.json: apps with ID == 0, added in order.
	for _, name := range []string{"x", "y", "z"} {
		if _, err := m.Add(config.App{Name: name, Cmd: "true", Instances: 1}); err != nil {
			t.Fatalf("Add %s: %v", name, err)
		}
	}
	specs := m.Specs()
	want := map[string]int{"x": 1, "y": 2, "z": 3}
	for _, s := range specs {
		if s.ID != want[s.Name] {
			t.Fatalf("Specs()[%s].ID = %d, want %d", s.Name, s.ID, want[s.Name])
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails, then passes**

Run: `go test ./internal/manager/ -run TestLoadWithoutIDsAssignsContiguousThenSpecsCarriesThem`
Expected: With Task 2 already implemented, this should PASS immediately **only if** `Add` writes the id back into `ma.spec.ID` (it does, via `app.ID = id` before `spec: app`). If it FAILS with `ID = 0`, verify `Specs()` returns `a.spec` and that `Add` assigned `app.ID` before storing `spec: app`.

- [ ] **Step 3: Add changelog entry**

Under `## [Unreleased]` in `CHANGELOG.md`, add (create an `### Fixed` or `### Changed` subhead if absent):

```markdown
### Changed
- App IDs are now stable: the daemon persists each app's ID in `dump.json` and
  reuses it across restarts and `resurrect`. Existing installs migrate
  automatically — the first daemon restart after upgrade renumbers apps to a
  contiguous `1..N` and they stay fixed thereafter. This makes `restart <id>` /
  `delete <id>` reliable.
```

- [ ] **Step 4: Race check + commit**

```bash
go test ./... -race -count=1
go vet ./... && gofmt -l .
git add internal/manager/manager_test.go CHANGELOG.md
git commit -m "test(manager): cover ID migration; changelog for stable IDs

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

**REVIEW CHECKPOINT 1** — Group 1 (Stable IDs) is complete and independently mergeable.

---

# GROUP 2 — Multi-target selectors

### Task 4: `expandSelectorArgs` helper

**Files:**
- Modify: `cmd/marshal/control.go` (add helper near `targetsFromArg`, ~line 200)
- Test: `cmd/marshal/control_test.go`

**Interfaces:**
- Consumes: existing `targetsFromArg(arg string) ([]string, bool, error)` and `isConfigFile`.
- Produces: `func expandSelectorArgs(args []string) (targets []string, multi bool, err error)` — splits each arg on commas, expands each element via `targetsFromArg`, flattens, de-dupes preserving order; `all` short-circuits to `["all"]` with `multi=false`; `multi` is true when any arg was a config file OR more than one target results.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/marshal/control_test.go`:

```go
func TestExpandSelectorArgs(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		want      []string
		wantMulti bool
	}{
		{"single", []string{"a"}, []string{"a"}, false},
		{"twoArgs", []string{"a", "b"}, []string{"a", "b"}, true},
		{"commaList", []string{"a,b"}, []string{"a", "b"}, true},
		{"mixedCommaAndArg", []string{"a,b", "c"}, []string{"a", "b", "c"}, true},
		{"dedup", []string{"a", "a"}, []string{"a"}, false},
		{"allShortCircuits", []string{"all", "b"}, []string{"all"}, false},
		{"numericIDs", []string{"1", "2"}, []string{"1", "2"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, multi, err := expandSelectorArgs(tc.args)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if multi != tc.wantMulti {
				t.Fatalf("multi = %v, want %v", multi, tc.wantMulti)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/marshal/ -run TestExpandSelectorArgs`
Expected: FAIL — `undefined: expandSelectorArgs`.

- [ ] **Step 3: Implement**

Add to `cmd/marshal/control.go`:

```go
// expandSelectorArgs turns CLI args — each possibly a comma-separated list, a
// name/id, or a marshal.yaml path — into a flat, de-duplicated target list.
// multi is true when more than one target results or any arg was a config file,
// which switches callers to warn-and-continue error handling. "all" anywhere
// short-circuits to a single "all" target.
func expandSelectorArgs(args []string) (targets []string, multi bool, err error) {
	seen := map[string]bool{}
	fromFile := false
	for _, raw := range args {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			ts, ff, e := targetsFromArg(part)
			if e != nil {
				return nil, false, e
			}
			fromFile = fromFile || ff
			for _, t := range ts {
				if !seen[t] {
					seen[t] = true
					targets = append(targets, t)
				}
			}
		}
	}
	if len(targets) == 0 {
		return nil, false, fmt.Errorf("no targets given")
	}
	for _, t := range targets {
		if t == "all" {
			return []string{"all"}, false, nil
		}
	}
	return targets, fromFile || len(targets) > 1, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/marshal/ -run TestExpandSelectorArgs`
Expected: PASS.

- [ ] **Step 5: Format + commit**

```bash
go vet ./... && gofmt -l .
git add cmd/marshal/control.go cmd/marshal/control_test.go
git commit -m "feat(cli): expandSelectorArgs for multi-target/comma selectors

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: `runSelector` runner + rewire `selectorCmd` (stop/delete/reset)

**Files:**
- Modify: `cmd/marshal/control.go` (`selectorCmd`, ~line 140)
- Test: `cmd/marshal/control_test.go`

**Interfaces:**
- Consumes: `expandSelectorArgs` (Task 4), `withClient`, `printProcs`.
- Produces: `func runSelector(cmd *cobra.Command, args []string, call func(context.Context, pb.DaemonClient, *pb.Selector) (*pb.ProcList, error)) error`. `selectorCmd` now uses `cobra.MinimumNArgs(1)` and delegates to `runSelector`.

- [ ] **Step 1: Write the failing test (arg arity)**

A full RPC-loop test needs a fake daemon; keep this task's automated check to the cobra arity wiring, which is what regressed the user. Add to `cmd/marshal/control_test.go`:

```go
func TestSelectorCmdAcceptsMultipleArgs(t *testing.T) {
	cmd := selectorCmd("stop <name|id|all>", "stop", func(context.Context, pb.DaemonClient, *pb.Selector) (*pb.ProcList, error) {
		return &pb.ProcList{}, nil
	})
	if cmd.Args == nil {
		t.Fatal("expected an Args validator")
	}
	// MinimumNArgs(1): zero args is an error, two args is fine.
	if err := cmd.Args(cmd, []string{}); err == nil {
		t.Fatal("expected error for zero args")
	}
	if err := cmd.Args(cmd, []string{"a", "b"}); err != nil {
		t.Fatalf("two args should be allowed, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/marshal/ -run TestSelectorCmdAcceptsMultipleArgs`
Expected: FAIL — two args rejected (`ExactArgs(1)`).

- [ ] **Step 3: Implement**

Replace the body of `selectorCmd` in `cmd/marshal/control.go` with:

```go
func selectorCmd(use, short string, call func(context.Context, pb.DaemonClient, *pb.Selector) (*pb.ProcList, error)) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSelector(cmd, args, call)
		},
	}
}

// runSelector expands args into targets and applies call to each. With multiple
// targets (or a config-file expansion) an errored target warns and the loop
// continues, returning a non-zero exit if any failed; a single explicit target
// fails hard.
func runSelector(cmd *cobra.Command, args []string, call func(context.Context, pb.DaemonClient, *pb.Selector) (*pb.ProcList, error)) error {
	targets, multi, err := expandSelectorArgs(args)
	if err != nil {
		return err
	}
	return withClient(func(ctx context.Context, c pb.DaemonClient) error {
		agg := &pb.ProcList{}
		failed := false
		for _, t := range targets {
			list, err := call(ctx, c, &pb.Selector{Target: t})
			if err != nil {
				if multi {
					fmt.Fprintf(cmd.ErrOrStderr(), "marshal: %s: %v\n", t, err)
					failed = true
					continue
				}
				return err
			}
			agg.Procs = append(agg.Procs, list.GetProcs()...)
		}
		printProcs(cmd, agg)
		if failed {
			return fmt.Errorf("one or more targets failed")
		}
		return nil
	})
}
```

Note: the old `selectorCmd` body (the per-arg `targetsFromArg` loop) is fully replaced — delete it. `stop`/`delete`/`reset` in `main.go` keep calling `selectorCmd(...)` unchanged and inherit multi-target for free.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/marshal/ -run 'TestSelectorCmdAcceptsMultipleArgs|TestExpandSelectorArgs'`
Expected: PASS.

- [ ] **Step 5: Format + commit**

```bash
go vet ./... && gofmt -l .
git add cmd/marshal/control.go cmd/marshal/control_test.go
git commit -m "feat(cli): multi-target stop/delete/reset via runSelector

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: `restartCmd()` (multi-target default path) + main.go wiring + changelog

**Files:**
- Modify: `cmd/marshal/control.go` (new `restartCmd`), `cmd/marshal/main.go` (~line 51), `CHANGELOG.md`
- Test: `cmd/marshal/control_test.go`

**Interfaces:**
- Consumes: `runSelector` (Task 5).
- Produces: `func restartCmd() *cobra.Command` with a `--update-env` bool flag (the flag's behavior is wired in Group 3; here it defaults false and the default path calls `runSelector` with `c.Restart`).

- [ ] **Step 1: Write the failing test**

```go
func TestRestartCmdHasUpdateEnvFlagAndMultiArg(t *testing.T) {
	cmd := restartCmd()
	if cmd.Flags().Lookup("update-env") == nil {
		t.Fatal("expected --update-env flag")
	}
	if err := cmd.Args(cmd, []string{"a", "b"}); err != nil {
		t.Fatalf("restart should accept multiple args, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/marshal/ -run TestRestartCmdHasUpdateEnvFlagAndMultiArg`
Expected: FAIL — `undefined: restartCmd`.

- [ ] **Step 3: Implement `restartCmd`**

Add to `cmd/marshal/control.go`:

```go
func restartCmd() *cobra.Command {
	var updateEnv bool
	cmd := &cobra.Command{
		Use:   "restart <name|id|all|marshal.yaml>...",
		Short: "Restart app(s); with --update-env, reload env from a marshal.yaml first",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if updateEnv {
				return runRestartUpdateEnv(cmd, args) // implemented in Group 3, Task 9
			}
			return runSelector(cmd, args, func(ctx context.Context, c pb.DaemonClient, sel *pb.Selector) (*pb.ProcList, error) {
				return c.Restart(ctx, sel)
			})
		},
	}
	cmd.Flags().BoolVar(&updateEnv, "update-env", false,
		"re-read env/env_file from the given marshal.yaml and apply it on restart")
	return cmd
}
```

Because `runRestartUpdateEnv` does not exist until Task 9, add a temporary stub at the bottom of `control.go` so the package compiles now (Task 9 replaces its body):

```go
// runRestartUpdateEnv is implemented in Group 3 (Task 9).
func runRestartUpdateEnv(cmd *cobra.Command, args []string) error {
	return fmt.Errorf("--update-env not yet implemented")
}
```

- [ ] **Step 4: Wire into `main.go`**

In `cmd/marshal/main.go`, replace the `restart` `selectorCmd(...)` block (the one whose closure calls `c.Restart`) with a single line:

```go
		restartCmd(),
```

- [ ] **Step 5: Run tests + build to verify**

Run: `go test ./cmd/marshal/ -run TestRestartCmdHasUpdateEnvFlagAndMultiArg && go build ./...`
Expected: PASS and a clean build.

- [ ] **Step 6: Changelog + commit**

Under `## [Unreleased]` → `### Added` in `CHANGELOG.md`:

```markdown
### Added
- `stop`, `restart`, `delete`, and `reset` now accept multiple targets and
  comma-separated lists (e.g. `marshal restart 2 3`, `marshal delete 2,3`). An
  unknown target in a multi-target call warns and the rest still run.
```

```bash
go test ./... -race -count=1
go vet ./... && gofmt -l .
git add cmd/marshal/control.go cmd/marshal/main.go cmd/marshal/control_test.go CHANGELOG.md
git commit -m "feat(cli): restartCmd with multi-target and --update-env flag scaffold

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

**REVIEW CHECKPOINT 2** — Group 2 (multi-target selectors) is complete. `--update-env` returns a not-implemented error until Group 3.

---

# GROUP 3 — `restart --update-env` (env reload)

### Task 7: `UpdateEnv` proto RPC + `manager.UpdateEnv`

**Files:**
- Modify: `proto/marshal/v1/daemon.proto`; regenerate `internal/pb/*`
- Modify: `internal/manager/manager.go` (new `UpdateEnv` method)
- Test: `internal/manager/manager_test.go`

**Interfaces:**
- Produces (proto): `rpc UpdateEnv(UpdateEnvRequest) returns (ProcList)` and `message UpdateEnvRequest { repeated AppSpec apps = 1; }`.
- Produces (manager): `func (m *Manager) UpdateEnv(name string, env map[string]string) ([]InstanceSnapshot, error)` — sets the app's `spec.Env = env`, restarts its instances, preserves `id`; error if the app is absent.

- [ ] **Step 1: Add the proto RPC + message**

In `proto/marshal/v1/daemon.proto`, add to `service Daemon` (after the `Reset` rpc):

```proto
  rpc UpdateEnv(UpdateEnvRequest) returns (ProcList);
```

And add a new message near `StartRequest`:

```proto
// UpdateEnvRequest carries apps whose env should be refreshed in place. Only
// name and env are read; other AppSpec fields are ignored.
message UpdateEnvRequest { repeated AppSpec apps = 1; }
```

- [ ] **Step 2: Regenerate pb**

Run: `make proto`
Expected: `internal/pb/daemon.pb.go` and `internal/pb/daemon_grpc.pb.go` change; `git status` shows them modified. Then `go build ./...` compiles (the server won't satisfy `DaemonServer` yet — that's Task 8; if `make proto` alone leaves the build red on the *client*, it should still compile because only the server interface gains a method).

Verify the client method exists:
Run: `grep -n 'UpdateEnv' internal/pb/daemon_grpc.pb.go | head`
Expected: shows `UpdateEnv(ctx context.Context, in *UpdateEnvRequest, ...)` on the client interface.

- [ ] **Step 3: Write the failing manager test**

```go
func TestUpdateEnvSwapsEnvRestartsAndKeepsID(t *testing.T) {
	m := manager.New(context.Background())
	snaps, err := m.Add(config.App{Name: "a", Cmd: "true", Instances: 1, Env: map[string]string{"K": "old"}})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	origID := snaps[0].ID

	out, err := m.UpdateEnv("a", map[string]string{"K": "new"})
	if err != nil {
		t.Fatalf("UpdateEnv: %v", err)
	}
	if out[0].ID != origID {
		t.Fatalf("ID changed: got %d want %d", out[0].ID, origID)
	}
	specs := m.Specs()
	if specs[0].Env["K"] != "new" {
		t.Fatalf("env not updated: %v", specs[0].Env)
	}
}

func TestUpdateEnvUnknownAppErrors(t *testing.T) {
	m := manager.New(context.Background())
	if _, err := m.UpdateEnv("nope", nil); err == nil {
		t.Fatal("expected error for unknown app")
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test ./internal/manager/ -run TestUpdateEnv`
Expected: FAIL — `m.UpdateEnv undefined`.

- [ ] **Step 5: Implement `UpdateEnv`**

Add to `internal/manager/manager.go` (model it on `Restart` — same locking and instance-recreate shape):

```go
// UpdateEnv replaces the named app's environment and restarts its instances in
// place, preserving the app's id and listing position.
func (m *Manager) UpdateEnv(name string, env map[string]string) ([]InstanceSnapshot, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	apps, err := m.resolve(name)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	a := apps[0]
	a.spec.Env = env
	insts := collectInstances([]*managedApp{a})
	m.mu.Unlock()

	stopInstances(insts)

	m.mu.Lock()
	fresh := make([]*managedInstance, 0, a.spec.Instances)
	for idx := 0; idx < a.spec.Instances; idx++ {
		fresh = append(fresh, m.startInstance(a.spec, idx))
	}
	a.insts = fresh
	m.mu.Unlock()

	return m.Describe(name)
}
```

(Check that `Describe` exists with signature `Describe(sel string) ([]InstanceSnapshot, error)` — `Restart` calls it. If `Restart` returns via a different tail, mirror `Restart` exactly.)

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/manager/ -run TestUpdateEnv`
Expected: PASS.

- [ ] **Step 7: Format + commit**

```bash
go vet ./... && gofmt -l .
git add proto/marshal/v1/daemon.proto internal/pb/ internal/manager/manager.go internal/manager/manager_test.go
git commit -m "feat(manager): UpdateEnv RPC + in-place env swap with restart

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: `server.UpdateEnv` handler + persistence

**Files:**
- Modify: `internal/daemon/server.go` (new `UpdateEnv` method near `Restart`, ~line 152)
- Test: `internal/daemon/server_test.go`

**Interfaces:**
- Consumes: `mgr.UpdateEnv` (Task 7), `s.store.Save(s.mgr.Specs())` (existing persistence pattern), `s.procList`.
- Produces: `func (s *Server) UpdateEnv(ctx context.Context, req *pb.UpdateEnvRequest) (*pb.ProcList, error)` — updates each present app, skips absent ones, persists, returns the aggregated `ProcList`.

- [ ] **Step 1: Write the failing test**

Model on existing `server_test.go` setup (find how it builds a `*Server` with a temp store — often a helper like `newTestServer(t)`; reuse it). The test starts one app, updates its env, and asserts persistence + skip behavior:

```go
func TestServerUpdateEnvPersistsAndSkipsUnknown(t *testing.T) {
	s := newTestServer(t) // reuse the package's existing helper
	if _, err := s.Start(context.Background(), &pb.StartRequest{Apps: []*pb.AppSpec{
		{Name: "a", Cmd: "true", Instances: 1, Env: map[string]string{"K": "old"}},
	}}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	list, err := s.UpdateEnv(context.Background(), &pb.UpdateEnvRequest{Apps: []*pb.AppSpec{
		{Name: "a", Env: map[string]string{"K": "new"}},
		{Name: "ghost", Env: map[string]string{"X": "1"}}, // not running → skipped
	}})
	if err != nil {
		t.Fatalf("UpdateEnv: %v", err)
	}
	// Only "a" comes back.
	if len(list.GetProcs()) != 1 || list.GetProcs()[0].GetName() != "a" {
		t.Fatalf("unexpected procs: %+v", list.GetProcs())
	}
	// Persisted env reflects the change.
	apps, err := s.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if apps[0].Env["K"] != "new" {
		t.Fatalf("env not persisted: %v", apps[0].Env)
	}
}
```

If `newTestServer` / the `s.store` field name differ, adapt to the existing test helpers — check the top of `server_test.go`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestServerUpdateEnvPersistsAndSkipsUnknown`
Expected: FAIL — `s.UpdateEnv undefined`.

- [ ] **Step 3: Implement the handler**

Add to `internal/daemon/server.go`:

```go
// UpdateEnv refreshes the env of each named app in place and restarts it. Apps
// that aren't currently managed are skipped (not an error), so a config file
// listing not-yet-started apps degrades gracefully. Updated apps are persisted.
func (s *Server) UpdateEnv(_ context.Context, req *pb.UpdateEnvRequest) (*pb.ProcList, error) {
	var out []manager.InstanceSnapshot
	for _, spec := range req.GetApps() {
		snaps, err := s.mgr.UpdateEnv(spec.GetName(), spec.GetEnv())
		if err != nil {
			continue // skip unknown/absent apps
		}
		out = append(out, snaps...)
	}
	if err := s.store.Save(s.mgr.Specs()); err != nil {
		return nil, status.Errorf(codes.Internal, "persist: %v", err)
	}
	return s.procList(out), nil
}
```

(Confirm `status`/`codes` are already imported in `server.go` — they are, used by `Start`. `manager` is imported too.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run TestServerUpdateEnvPersistsAndSkipsUnknown`
Expected: PASS.

- [ ] **Step 5: Format + commit**

```bash
go vet ./... && gofmt -l .
git add internal/daemon/server.go internal/daemon/server_test.go
git commit -m "feat(daemon): UpdateEnv handler persists refreshed env, skips absent apps

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: CLI `--update-env` path (`runRestartUpdateEnv`) + validation + changelog

**Files:**
- Modify: `cmd/marshal/control.go` (replace the Task 6 stub), `CHANGELOG.md`
- Test: `cmd/marshal/control_test.go`

**Interfaces:**
- Consumes: `isConfigFile`, `config.Load`, `withClient`, `printProcs`, `pb.UpdateEnvRequest`, `c.UpdateEnv`.
- Produces: real `runRestartUpdateEnv(cmd *cobra.Command, args []string) error`.

- [ ] **Step 1: Write the failing validation test**

The full RPC path needs a live daemon (covered by the demo); the unit test locks in the **validation** rule that regresses easily — a bare selector with `--update-env` must error before any RPC:

```go
func TestRunRestartUpdateEnvRequiresConfigFile(t *testing.T) {
	cmd := restartCmd()
	err := runRestartUpdateEnv(cmd, []string{"UNOBot"}) // bare name, no .yaml
	if err == nil {
		t.Fatal("expected error requiring a marshal.yaml path")
	}
	if !strings.Contains(err.Error(), "marshal.yaml") {
		t.Fatalf("error should mention the config-file requirement, got: %v", err)
	}
}
```

Ensure `strings` is imported in the test file.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/marshal/ -run TestRunRestartUpdateEnvRequiresConfigFile`
Expected: FAIL — the Task 6 stub returns `"--update-env not yet implemented"`, which lacks `marshal.yaml`.

- [ ] **Step 3: Replace the stub with the real implementation**

In `cmd/marshal/control.go`, replace the `runRestartUpdateEnv` stub body:

```go
// runRestartUpdateEnv re-reads env/env_file from the given marshal.yaml file(s)
// and applies it to the matching running apps via the UpdateEnv RPC. It requires
// at least one config-file argument, since the daemon cannot re-read env without
// the file. Apps listed in the file but not currently running are warned, not failed.
func runRestartUpdateEnv(cmd *cobra.Command, args []string) error {
	var specs []*pb.AppSpec
	requested := map[string]bool{}
	for _, raw := range args {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if !isConfigFile(part) {
				return fmt.Errorf("marshal restart --update-env requires a marshal.yaml path "+
					"(the daemon cannot re-read env without the config file); %q is not one", part)
			}
			cfg, err := config.Load(part)
			if err != nil {
				return err
			}
			for _, a := range cfg.Apps {
				specs = append(specs, &pb.AppSpec{Name: a.Name, Env: a.Env})
				requested[a.Name] = true
			}
		}
	}
	if len(specs) == 0 {
		return fmt.Errorf("marshal restart --update-env: no apps found in the given config file(s)")
	}
	return withClient(func(ctx context.Context, c pb.DaemonClient) error {
		list, err := c.UpdateEnv(ctx, &pb.UpdateEnvRequest{Apps: specs})
		if err != nil {
			return err
		}
		printProcs(cmd, list)
		got := map[string]bool{}
		for _, p := range list.GetProcs() {
			got[p.GetName()] = true
		}
		for name := range requested {
			if !got[name] {
				fmt.Fprintf(cmd.ErrOrStderr(), "marshal: %s: not running; env not applied\n", name)
			}
		}
		return nil
	})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/marshal/ -run TestRunRestartUpdateEnvRequiresConfigFile`
Expected: PASS.

- [ ] **Step 5: Changelog + commit**

Under `## [Unreleased]` → `### Added` in `CHANGELOG.md`:

```markdown
- `marshal restart <marshal.yaml> --update-env` reloads an app's environment
  (inline `env:` and `env_file:`) from the config file and restarts it in place,
  preserving its ID and restart history. Other spec fields still require
  `delete` + `start`.
```

```bash
go test ./... -race -count=1
go vet ./... && gofmt -l .
git add cmd/marshal/control.go cmd/marshal/control_test.go CHANGELOG.md
git commit -m "feat(cli): implement restart --update-env env reload

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 10: Full build, live demo, and handoff (final close-out)

**Files:**
- Create: `docs/handoffs/2026-07-01-stable-ids-selectors-envreload.md`

- [ ] **Step 1: Full test + vet + build**

Run:
```bash
go test ./... -race -count=1
go vet ./... && gofmt -l .
make build
./marshal --version
```
Expected: all tests pass; `gofmt -l .` prints nothing; binary builds.

- [ ] **Step 2: Live demo (scratch dir, standard ports)**

Follow the demo convention — scratch `XDG_DATA_HOME`, `marshal start` (not `run`), tear down after. Exercise all three features:

```bash
export XDG_DATA_HOME=/tmp/marshal-demo/data
mkdir -p "$XDG_DATA_HOME"
# start a demo daemon (background) with the freshly built ./marshal, then:
# 1) start 3 demo apps from a demo marshal.yaml; note IDs 1,2,3
./marshal ls
# 2) stable IDs: stop+start the daemon, then:
./marshal ls          # expect IDs still 1,2,3
# 3) multi-target:
./marshal restart 1 3
./marshal restart 2 99         # 2 restarts; 99 warns; non-zero exit
./marshal delete 2,3
# 4) env reload: an app that echoes $GREETING on boot; edit its env_file, then:
./marshal restart demo.yaml --update-env
./marshal logs <app> -n 5      # expect the new GREETING value; same ID as before
```
Record observed output in the handoff. Then tear down (stop apps, stop daemon, `rm -rf /tmp/marshal-demo`) and confirm `pgrep -fl marshal` shows no demo orphans.

- [ ] **Step 3: Write the handoff**

Create `docs/handoffs/2026-07-01-stable-ids-selectors-envreload.md` per the CLAUDE.md handoff convention: current state (branch, what merged), what changed and why (the three features + auto-migration), build/run/test commands, deferred items (fleet/dashboard parity, full-spec reload, multi-target describe/logs), and the concrete next step (open PR(s) into `dev`; note the live box migrates IDs on its next `systemctl --user restart marshal`).

- [ ] **Step 4: Commit**

```bash
git add docs/handoffs/2026-07-01-stable-ids-selectors-envreload.md
git commit -m "docs: handoff for stable IDs, multi-target selectors, env reload

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

**REVIEW CHECKPOINT 3** — All three groups complete; branch ready to merge into `dev` (or split into three PRs).

---

## Self-Review (spec coverage)

- **Spec A (`restart --update-env`, env-only, in-place, requires config file):** Tasks 7–9. ✓
- **Spec B1 (multi-target + comma lists for stop/restart/delete/reset; forgiving batch; single fails hard):** Tasks 4–6. ✓
- **Spec B2 (persisted ID on config.App; Add honors/assigns; auto-migration to 1..N; gaps allowed; fleet inherits):** Tasks 1–3. ✓
- **Deferred items (fleet/dashboard, full-spec reload, describe/logs multi-target):** recorded in Task 10 handoff. ✓
- **Conventions (TDD, gofmt, race, changelog per group, co-author trailer, branch off dev):** Global Constraints + per-task steps. ✓
