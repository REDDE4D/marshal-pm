# Terminal Update-Available Hint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Surface an "update available" hint in the terminal (not just the dashboard) and shorten the
update-check interval from 24h to 6h.

**Architecture:** The local daemon runs the existing `updatecheck.Checker` (mirroring the server). A
new `UpdateStatus` gRPC RPC exposes the cached result. The CLI, in a root `PersistentPostRunE`, does a
best-effort non-spawning dial of the daemon and prints a one-line banner to stderr when a newer release
exists. The interval constant is lowered to 6h for both daemon and server.

**Tech Stack:** Go 1.26.4, gRPC over a Unix socket (`internal/pb` generated from `proto/marshal/v1`),
cobra CLI, the existing `internal/updatecheck` package.

## Global Constraints

- Module path is `github.com/REDDE4D/marshal-pm`; imports are `github.com/REDDE4D/marshal-pm/internal/...`.
- TDD: write the failing test first, run it red, implement minimally, run it green, commit.
- All work on a feature branch off **`dev`** (never `main`). Branch: `cli-update-hint`.
- Commit trailer on every commit: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Regenerate protobufs with `make proto` (runs `./scripts/gen-proto.sh`) — never hand-edit `internal/pb`.
- Update `CHANGELOG.md` under `## [Unreleased]` in the final task (Added section).
- Before declaring done: `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .` (must list nothing).
- The hint is **hint-only** — never download, install, or replace anything (out of scope).
- Target release: **v0.13.0** (not tagged in this plan).

---

## File Structure

**Modify:**
- `proto/marshal/v1/daemon.proto` — `UpdateStatus` RPC + `UpdateInfo` message.
- `internal/updatecheck/checker.go` — `DefaultInterval` 24h → 6h.
- `internal/updatecheck/checker_test.go` — assert the new interval.
- `internal/daemon/server.go` — `Server.updater` field, checker construction in `Run`, `UpdateStatus` handler.
- `internal/daemon/server_test.go` — `UpdateStatus` handler test.
- `internal/client/client.go` — `ConnectExisting` (non-spawning dial).
- `internal/client/client_test.go` — `ConnectExisting` error when no daemon (create if absent).
- `cmd/marshal/main.go` — root `PersistentPostRunE` wiring.
- `cmd/marshal/update.go` (new) — `updateBanner` formatter + `maybePrintUpdateBanner`.
- `cmd/marshal/update_test.go` (new) — `updateBanner` unit tests.
- `CHANGELOG.md` — `[Unreleased]` Added entry.

---

### Task 0: Branch setup

- [ ] **Step 1: Create the feature branch off `dev`**

```bash
git checkout dev
git checkout -b cli-update-hint
```

- [ ] **Step 2: Confirm a clean baseline**

Run: `go build ./... && go test ./... -count=1`
Expected: builds and all tests pass.

---

### Task 1: Protobuf — `UpdateStatus` RPC + `UpdateInfo`

**Files:**
- Modify: `proto/marshal/v1/daemon.proto`
- Regenerate: `internal/pb/*` via `make proto`

**Interfaces:**
- Produces (generated): `pb.DaemonClient.UpdateStatus(ctx, *pb.Empty) (*pb.UpdateInfo, error)`;
  `pb.UpdateInfo` with `GetCurrent()/GetLatest()/GetOutdated()/GetCheckedAtUnix()`; `pb.DaemonServer`
  gains `UpdateStatus`.

- [ ] **Step 1: Add the RPC to the `Daemon` service** (`proto/marshal/v1/daemon.proto`)

In `service Daemon { ... }`, after the `Flush` RPC line, add:

```protobuf
  rpc UpdateStatus(Empty) returns (UpdateInfo);
```

- [ ] **Step 2: Add the message** (`proto/marshal/v1/daemon.proto`)

After the `Ack` message (near the top-level messages), add:

```protobuf
message UpdateInfo {
  string current         = 1;
  string latest          = 2; // empty until the first successful check
  bool   outdated        = 3;
  int64  checked_at_unix = 4; // 0 until the first successful check
}
```

- [ ] **Step 3: Regenerate**

Run: `make proto`
Expected: `internal/pb/*.pb.go` regenerated, no errors.

- [ ] **Step 4: Verify it builds**

Run: `go build ./...`
Expected: builds (the missing server handler is provided by `UnimplementedDaemonServer`).

- [ ] **Step 5: Commit**

```bash
git add proto/ internal/pb/
git commit -m "proto: UpdateStatus RPC and UpdateInfo message

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Shorten the check interval to 6h

**Files:**
- Modify: `internal/updatecheck/checker.go`
- Test: `internal/updatecheck/checker_test.go`

**Interfaces:**
- Produces: `updatecheck.DefaultInterval == 6 * time.Hour`.

- [ ] **Step 1: Write the failing test** (append to `internal/updatecheck/checker_test.go`)

```go
func TestDefaultIntervalIsSixHours(t *testing.T) {
	if DefaultInterval != 6*time.Hour {
		t.Fatalf("DefaultInterval = %v, want 6h", DefaultInterval)
	}
}
```

(Ensure `"time"` is imported in the test file.)

- [ ] **Step 2: Run it red**

Run: `go test ./internal/updatecheck/ -run TestDefaultIntervalIsSixHours -v`
Expected: FAIL (currently 24h).

- [ ] **Step 3: Change the constant** (`internal/updatecheck/checker.go`)

Replace:

```go
// DefaultInterval is how often the background poller re-checks for a release.
const DefaultInterval = 24 * time.Hour
```

with:

```go
// DefaultInterval is how often the background poller re-checks for a release.
const DefaultInterval = 6 * time.Hour
```

- [ ] **Step 4: Run it green + the package**

Run: `go test ./internal/updatecheck/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/updatecheck/checker.go internal/updatecheck/checker_test.go
git commit -m "feat(updatecheck): re-check every 6h instead of 24h

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Daemon runs the checker + `UpdateStatus` handler

**Files:**
- Modify: `internal/daemon/server.go`
- Test: `internal/daemon/server_test.go`

**Interfaces:**
- Consumes: `updatecheck.New/Run/Snapshot` (`internal/updatecheck`), generated `pb.UpdateInfo` (Task 1).
- Produces: `Server.updater *updatecheck.Checker`; `func (s *Server) UpdateStatus(context.Context, *pb.Empty) (*pb.UpdateInfo, error)`.

- [ ] **Step 1: Write the failing test** (append to `internal/daemon/server_test.go`)

```go
func TestUpdateStatusReportsSnapshot(t *testing.T) {
	// Stub GitHub's /releases/latest redirect to a newer version.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://github.com/x/y/releases/tag/v9.9.9")
		w.WriteHeader(http.StatusFound)
	}))
	defer stub.Close()

	chk := updatecheck.New("v0.1.0",
		updatecheck.WithReleasesURL(stub.URL),
		updatecheck.WithHTTPClient(stub.Client()))
	// One synchronous refresh via a brief Run; cancel right after.
	ctx, cancel := context.WithCancel(context.Background())
	chk.Run(ctx) // refreshes once immediately, then would tick; we cancel below
	cancel()

	srv := &Server{updater: chk}
	info, err := srv.UpdateStatus(context.Background(), &pb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if info.GetLatest() != "v9.9.9" || !info.GetOutdated() || info.GetCurrent() != "v0.1.0" {
		t.Fatalf("got %+v, want latest v9.9.9 outdated current v0.1.0", info)
	}
}

func TestUpdateStatusNilUpdater(t *testing.T) {
	srv := &Server{}
	info, err := srv.UpdateStatus(context.Background(), &pb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if info.GetOutdated() || info.GetLatest() != "" {
		t.Fatalf("nil updater should yield empty UpdateInfo, got %+v", info)
	}
}
```

> Note: `Checker.Run` refreshes once immediately and then blocks on a ticker until ctx is canceled.
> Calling `chk.Run(ctx)` then `cancel()` as above relies on Run returning after the immediate refresh
> only if ctx is already done. To guarantee a single synchronous refresh without blocking, instead
> cancel BEFORE the ticker matters: call `chk.Run` in a goroutine and poll `Snapshot()` until
> `Latest != ""`, or — simpler — give the Checker an unexported `refresh` and call it. Since `refresh`
> is unexported and this test is in package `daemon` (not `updatecheck`), use this polling form:

```go
	go chk.Run(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for chk.Snapshot().Latest == "" {
		if time.Now().After(deadline) {
			t.Fatal("checker never refreshed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
```

Use the goroutine+poll form (replace the `chk.Run(ctx); cancel()` two lines in the first test with
the block above). Add imports to `server_test.go`: `"net/http"`, `"net/http/httptest"`, `"time"`,
`"github.com/REDDE4D/marshal-pm/internal/updatecheck"`.

- [ ] **Step 2: Run it red**

Run: `go test ./internal/daemon/ -run TestUpdateStatus -v`
Expected: FAIL — `Server.updater` / `srv.UpdateStatus` undefined.

- [ ] **Step 3: Add the field + handler** (`internal/daemon/server.go`)

Add the import (with the others): `"github.com/REDDE4D/marshal-pm/internal/updatecheck"`.

Add a field to the `Server` struct (after `guard`):

```go
	updater          *updatecheck.Checker // background update-availability check
```

Add the handler (after the `Flush` handler):

```go
// UpdateStatus reports the daemon's cached update-availability check.
func (s *Server) UpdateStatus(_ context.Context, _ *pb.Empty) (*pb.UpdateInfo, error) {
	if s.updater == nil {
		return &pb.UpdateInfo{}, nil
	}
	r := s.updater.Snapshot()
	var checked int64
	if !r.CheckedAt.IsZero() {
		checked = r.CheckedAt.Unix()
	}
	return &pb.UpdateInfo{
		Current:       r.Current,
		Latest:        r.Latest,
		Outdated:      r.Outdated,
		CheckedAtUnix: checked,
	}, nil
}
```

- [ ] **Step 4: Construct and run the checker in `Run`** (`internal/daemon/server.go`)

`serveCtx` is created at server.go:390. Immediately after the `serveCtx, cancel := context.WithCancel(ctx)`
line (and its `defer cancel()`), add:

```go
	srv.updater = updatecheck.New(version.String(),
		updatecheck.WithEnabled(os.Getenv("MARSHAL_NO_UPDATE_CHECK") == ""))
	go srv.updater.Run(serveCtx)
```

(`version` and `os` are already imported in `server.go`.)

- [ ] **Step 5: Run it green + full package**

Run: `go test ./internal/daemon/ -run TestUpdateStatus -v && go test ./internal/daemon/ -count=1`
Expected: PASS, no regressions.

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/server.go internal/daemon/server_test.go
git commit -m "feat(daemon): run update checker and expose UpdateStatus

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: `client.ConnectExisting` (non-spawning dial)

**Files:**
- Modify: `internal/client/client.go`
- Test: `internal/client/client_test.go` (create if absent)

**Interfaces:**
- Produces: `func ConnectExisting(st *store.Store) (pb.DaemonClient, *grpc.ClientConn, error)` — dials
  only if the socket is alive; returns an error (never spawns) when no daemon is listening.

- [ ] **Step 1: Write the failing test** (`internal/client/client_test.go`)

```go
package client

import (
	"testing"

	"github.com/REDDE4D/marshal-pm/internal/store"
)

func TestConnectExistingNoDaemon(t *testing.T) {
	st := store.NewAt(t.TempDir()) // fresh dir → no socket → no daemon
	_, _, err := ConnectExisting(st)
	if err == nil {
		t.Fatal("expected error when no daemon is running")
	}
}
```

(If `client_test.go` already exists, append just the function and reuse its imports.)

- [ ] **Step 2: Run it red**

Run: `go test ./internal/client/ -run TestConnectExistingNoDaemon -v`
Expected: FAIL — `ConnectExisting` undefined.

- [ ] **Step 3: Implement** (`internal/client/client.go`, after `Connect`)

```go
// ConnectExisting dials the daemon only if it is already running; it never
// spawns one. Returns an error when nothing is listening on the socket. The
// caller must Close the returned conn on success.
func ConnectExisting(st *store.Store) (pb.DaemonClient, *grpc.ClientConn, error) {
	if !alive(st.SocketPath()) {
		return nil, nil, fmt.Errorf("daemon not running")
	}
	conn, err := grpc.NewClient("unix:"+st.SocketPath(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, fmt.Errorf("dial daemon: %w", err)
	}
	return pb.NewDaemonClient(conn), conn, nil
}
```

- [ ] **Step 4: Run it green**

Run: `go test ./internal/client/ -run TestConnectExistingNoDaemon -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/client/client.go internal/client/client_test.go
git commit -m "feat(client): ConnectExisting dials the daemon without spawning

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: CLI update banner

**Files:**
- Create: `cmd/marshal/update.go`, `cmd/marshal/update_test.go`
- Modify: `cmd/marshal/main.go`

**Interfaces:**
- Consumes: `pb.UpdateInfo`/`pb.DaemonClient.UpdateStatus` (Task 1), `client.ConnectExisting` (Task 4),
  `isTerminal` (cmd/marshal/control.go:472), `updatecheck.DefaultReleasesURL`.
- Produces: `func updateBanner(info *pb.UpdateInfo) string`; `func maybePrintUpdateBanner(cmd *cobra.Command)`;
  root `PersistentPostRunE` calls it.

- [ ] **Step 1: Write the failing test** (`cmd/marshal/update_test.go`)

```go
package main

import (
	"testing"

	"github.com/REDDE4D/marshal-pm/internal/pb"
)

func TestUpdateBanner(t *testing.T) {
	// Outdated → formatted line containing both versions and the releases URL.
	got := updateBanner(&pb.UpdateInfo{Current: "v0.11.0", Latest: "v0.12.0", Outdated: true})
	for _, want := range []string{"v0.12.0", "v0.11.0", "update available", "releases"} {
		if !contains(got, want) {
			t.Fatalf("banner %q missing %q", got, want)
		}
	}
	// Not outdated → empty.
	if s := updateBanner(&pb.UpdateInfo{Current: "v0.12.0", Latest: "v0.12.0", Outdated: false}); s != "" {
		t.Fatalf("up-to-date should yield empty banner, got %q", s)
	}
	// Outdated but empty latest → empty (no data yet).
	if s := updateBanner(&pb.UpdateInfo{Outdated: true, Latest: ""}); s != "" {
		t.Fatalf("empty latest should yield empty banner, got %q", s)
	}
	// nil → empty.
	if s := updateBanner(nil); s != "" {
		t.Fatalf("nil should yield empty banner, got %q", s)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

> (The `contains`/`indexOf` helpers avoid adding a `strings` import collision if the file already
> imports it; if `strings` is available in the test file, use `strings.Contains` instead and drop these.)

- [ ] **Step 2: Run it red**

Run: `go test ./cmd/marshal/ -run TestUpdateBanner -v`
Expected: FAIL — `updateBanner` undefined.

- [ ] **Step 3: Implement the formatter + hook** (`cmd/marshal/update.go`)

```go
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/REDDE4D/marshal-pm/internal/client"
	"github.com/REDDE4D/marshal-pm/internal/pb"
	"github.com/REDDE4D/marshal-pm/internal/store"
	"github.com/REDDE4D/marshal-pm/internal/updatecheck"
)

// updateBanner returns the one-line "update available" hint, or "" when the
// daemon reports up-to-date / has no data yet / info is nil.
func updateBanner(info *pb.UpdateInfo) string {
	if info == nil || !info.GetOutdated() || info.GetLatest() == "" {
		return ""
	}
	return fmt.Sprintf("marshal: update available — %s (current %s) → %s",
		info.GetLatest(), info.GetCurrent(), updatecheck.DefaultReleasesURL)
}

// maybePrintUpdateBanner prints the update hint to stderr after a command, but
// only when: the opt-out env var is unset, stderr is a terminal, and a daemon is
// already running (it never spawns one). Any error is swallowed — the hint is
// strictly best-effort and must never affect the command's outcome.
func maybePrintUpdateBanner(cmd *cobra.Command) {
	if os.Getenv("MARSHAL_NO_UPDATE_CHECK") != "" {
		return
	}
	if !isTerminal(cmd.ErrOrStderr()) {
		return
	}
	st, err := store.New()
	if err != nil {
		return
	}
	c, conn, err := client.ConnectExisting(st)
	if err != nil {
		return
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	info, err := c.UpdateStatus(ctx, &pb.Empty{})
	if err != nil {
		return
	}
	if b := updateBanner(info); b != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), b)
	}
}
```

- [ ] **Step 4: Wire the root post-run** (`cmd/marshal/main.go`, in `rootCmd()` where `root` is built)

After `root := &cobra.Command{...}` and before `root.AddCommand(...)`, add:

```go
	root.PersistentPostRunE = func(cmd *cobra.Command, _ []string) error {
		maybePrintUpdateBanner(cmd)
		return nil
	}
```

- [ ] **Step 5: Run it green + build**

Run: `go test ./cmd/marshal/ -run TestUpdateBanner -v && go build ./...`
Expected: PASS and a clean build.

- [ ] **Step 6: Commit**

```bash
git add cmd/marshal/update.go cmd/marshal/update_test.go cmd/marshal/main.go
git commit -m "feat(cli): print update-available banner after commands

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Changelog, verification, handoff

**Files:**
- Modify: `CHANGELOG.md`
- Create: `docs/handoffs/2026-06-26-cli-update-hint.md`

- [ ] **Step 1: Add changelog entries under `## [Unreleased]` → `### Added`**

```markdown
### Added
- **Terminal "update available" hint.** The local daemon now runs the same update check the server
  dashboard uses, and the CLI prints a one-line hint to stderr after a command when a newer release
  exists (e.g. `marshal: update available — v0.13.0 (current v0.12.0) → …/releases/latest`). It is
  best-effort: shown only on an interactive terminal, never spawns a daemon, and is silenced by
  `MARSHAL_NO_UPDATE_CHECK`.

### Changed
- **Update checks now run every 6h instead of every 24h** (daemon and server), so new releases surface
  sooner.
```

- [ ] **Step 2: Full verification**

Run: `go test ./... -race -count=1 && go vet ./... && gofmt -l .`
Expected: all pass; vet clean; `gofmt -l .` prints nothing.

- [ ] **Step 3: Write the handoff** (`docs/handoffs/2026-06-26-cli-update-hint.md`)

Cover (per the project convention): state (branch `cli-update-hint`, all tasks done, tests green);
what changed (daemon runs `updatecheck.Checker`; `UpdateStatus` RPC; CLI `PersistentPostRunE` banner
via non-spawning `client.ConnectExisting`, stderr + TTY-gated + `MARSHAL_NO_UPDATE_CHECK` opt-out;
interval 24h→6h); how to build/run/test (`make build`, `go test ./... -race`); deferred (auto-update,
`marshal update` command, configurable interval — all out of scope); next step (live demo, then merge
`dev`, cut v0.13.0).

- [ ] **Step 4: Commit**

```bash
git add CHANGELOG.md docs/handoffs/2026-06-26-cli-update-hint.md
git commit -m "docs: changelog + handoff for terminal update hint

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 5: Live demo (per project convention)**

On an isolated scratch store (`XDG_DATA_HOME=/tmp/marshal-demo/...`), build the binary, then prove the
banner with a **forced-old version**: build a binary stamped to an old version
(`go build -ldflags "-X github.com/REDDE4D/marshal-pm/internal/version.Version=v0.0.1" -o /tmp/marshal-demo/marshal ./cmd/marshal`),
start a daemon + a demo app with it, wait for the daemon's startup update check, then run
`/tmp/marshal-demo/marshal list` in a terminal and confirm the stderr banner appears citing the real
latest release. Also confirm it is silent when piped (`marshal list 2>/dev/null` / non-TTY) and with
`MARSHAL_NO_UPDATE_CHECK=1`. Tear down by data dir (no broad pkill — a standing launchd daemon runs);
verify no orphans (`pgrep -fl marshal`); remove the scratch dir.

---

## Self-Review

**Spec coverage:**
- Daemon runs the checker → Task 3 (field + construction in Run). ✓
- `UpdateStatus` RPC + `UpdateInfo` → Task 1 (proto) + Task 3 (handler). ✓
- CLI banner on every (daemon-touching) command, stderr, TTY-gated, non-spawning, env opt-out →
  Task 4 (non-spawning dial) + Task 5 (formatter + PostRun). ✓
- Interval → 6h (daemon + server, via the shared constant) → Task 2. ✓
- Tests: formatter (Task 5), `UpdateStatus` handler + nil-updater (Task 3), interval constant (Task 2),
  non-spawning dial (Task 4). ✓
- Changelog / handoff / live demo → Task 6. ✓

**Placeholder scan:** No "TBD"/"implement later"; every code step has real code. Task 3's test note
explains the Run/refresh timing and gives the exact goroutine+poll form to use — a correctness
instruction, not a placeholder.

**Type consistency:** `Server.updater *updatecheck.Checker`, `UpdateStatus(context.Context, *pb.Empty)
(*pb.UpdateInfo, error)`, `ConnectExisting(*store.Store) (pb.DaemonClient, *grpc.ClientConn, error)`,
`updateBanner(*pb.UpdateInfo) string`, `maybePrintUpdateBanner(*cobra.Command)` are used identically
across producing and consuming tasks. `UpdateInfo` field accessors (`GetCurrent/GetLatest/GetOutdated/
GetCheckedAtUnix`) match the proto field names in Task 1.
