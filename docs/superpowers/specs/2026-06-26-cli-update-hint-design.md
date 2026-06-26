# Design: terminal "update available" hint + faster checks

**Date:** 2026-06-26
**Status:** Approved (brainstorming → ready for implementation plan)
**Target release:** v0.13.0 (adds a user-facing feature)

## Summary

Marshal's update check currently lives only in the **server dashboard** (`internal/server` wires an
`updatecheck.Checker` into the dashboard banner). The **terminal/CLI has no update awareness at all**,
and the dashboard check only refreshes once at startup then every 24h, so a freshly published release
isn't surfaced for up to a day. This change:

1. Runs the update checker inside the **local daemon** (mirroring the server).
2. Adds an `UpdateStatus` daemon RPC so the CLI can read the cached result.
3. Prints a one-line "update available" banner after CLI commands (when outdated), with guardrails.
4. Shortens the check interval from 24h to 6h (daemon + server).

It never downloads or installs anything — it only surfaces a hint, like the existing dashboard banner.

## Motivation

After releasing v0.12.0, neither the terminal nor (immediately) the dashboard showed an update hint.
Root cause: the CLI has no update feature, and the dashboard checker caches for 24h and had last
checked before the release existed. Operators who live in the terminal never learn a new version is
out. This brings the CLI to parity with the dashboard and tightens the latency.

## Existing code (reference)

- `internal/updatecheck/checker.go` — `Checker` with `New(current, …opts)`, `Run(ctx)` (refresh once
  immediately, then every `DefaultInterval`), `Snapshot() Result`, `Enabled()`. `DefaultInterval = 24h`.
- `internal/updatecheck/updatecheck.go` — `Result{Current, Latest, Outdated, CheckedAt}`,
  `Outdated(current, latest) bool`, `fetchLatest` (hits GitHub `/releases/latest` redirect),
  `DefaultReleasesURL`.
- `internal/server/server.go:433` — the server's wiring: `upd := updatecheck.New(version.String(),
  updatecheck.WithEnabled(os.Getenv("MARSHAL_NO_UPDATE_CHECK")=="")); go upd.Run(ctx)`.
- `internal/daemon/server.go` — daemon `Server` + `Run`; the CLI talks to it over a Unix socket.
- `cmd/marshal/main.go` `rootCmd()` — registers all subcommands; `cmd/marshal/control.go` has
  `withClient` (auto-spawns the daemon) and `isTerminal`.

## Components

### 1. Daemon runs the checker

In `daemon.Run` (`internal/daemon/server.go`), after the manager/sampler are set up and the `srv`
is constructed, build a checker and run it:

```go
upd := updatecheck.New(version.String(),
    updatecheck.WithEnabled(os.Getenv("MARSHAL_NO_UPDATE_CHECK") == ""))
srv.updater = upd
go upd.Run(serveCtx)
```

- Store it on `Server` as `updater *updatecheck.Checker` (nil-safe — handler tolerates nil).
- Use `serveCtx` (the same context the other daemon goroutines use) so it stops on shutdown.
- `version` is already imported in the daemon package (used for the fleet client).

### 2. `UpdateStatus` RPC

`proto/marshal/v1/daemon.proto`:

```protobuf
service Daemon {
  ...
  rpc UpdateStatus(Empty) returns (UpdateInfo);
}

message UpdateInfo {
  string current         = 1;
  string latest          = 2; // empty until the first successful check
  bool   outdated        = 3;
  int64  checked_at_unix = 4; // 0 until the first successful check
}
```

Regenerate `internal/pb` via `make proto`.

Daemon handler (`internal/daemon/server.go`):

```go
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

Returns a zero `UpdateInfo` (outdated=false) until the first refresh or when disabled — always safe
to call.

### 3. CLI banner

A pure formatter plus a best-effort post-run hook.

`cmd/marshal/` — formatter (unit-testable, no I/O):

```go
// updateBanner returns the one-line hint, or "" when not outdated / missing data.
func updateBanner(info *pb.UpdateInfo) string {
    if info == nil || !info.GetOutdated() || info.GetLatest() == "" {
        return ""
    }
    return fmt.Sprintf("marshal: update available — %s (current %s) → %s",
        info.GetLatest(), info.GetCurrent(), updatecheck.DefaultReleasesURL)
}
```

Root `PersistentPostRunE` (`cmd/marshal/main.go`): best-effort, never affects the command's own exit.

```go
root.PersistentPostRunE = func(cmd *cobra.Command, _ []string) error {
    maybePrintUpdateBanner(cmd)
    return nil
}
```

`maybePrintUpdateBanner` rules (all must hold to print):
- `os.Getenv("MARSHAL_NO_UPDATE_CHECK") == ""` (opt-out honored).
- stderr is a terminal (`isTerminal(cmd.ErrOrStderr())`) — silent in scripts/pipes/CI.
- a daemon is already reachable via a **non-spawning** dial (see below); on any dial/RPC error,
  silent. It must NOT spawn a daemon just to check.
- the returned `UpdateInfo` yields a non-empty `updateBanner(...)`.

When all hold, write the banner line to `cmd.ErrOrStderr()`.

**Non-spawning dial:** `withClient`/`client.Connect` auto-spawns the daemon. The banner must not do
that. Add a non-spawning connect helper (dial the existing socket only; return an error if nothing is
listening) — either a new `client.ConnectExisting(st)` or a local dial in the CLI that opens the unix
socket at `st.SocketPath()` and returns a `pb.DaemonClient`. Use a short timeout (e.g. 1s) so a hung
socket can't delay the prompt.

Because cobra runs `PersistentPostRunE` only when the command's `RunE` succeeds, an errored command
never prints a banner. Purely-local commands (`run`, `import`, `version`, `--help`) typically have no
daemon reachable, so the non-spawning dial fails and they stay silent — which is the intended
behavior ("every command that talks to the daemon").

### 4. Interval → 6h

`internal/updatecheck/checker.go`: `const DefaultInterval = 6 * time.Hour` (was 24h). Update the
doc comment. Applies to both the daemon (new) and the server (existing).

## Data flow

```
daemon startup ─▶ updatecheck.Checker.Run (refresh now, then every 6h) ─▶ cached Result
CLI command ─▶ RunE (primary work) ─▶ PersistentPostRunE
                                         └▶ non-spawning dial ─▶ UpdateStatus RPC ─▶ Snapshot
                                              └▶ updateBanner() ─▶ stderr (if outdated & TTY & not opted out)
```

## Error handling

- Checker refresh errors are already swallowed by `updatecheck` (best-effort); the daemon inherits this.
- `UpdateStatus` with a nil updater returns an empty `UpdateInfo` (no error).
- The CLI banner is best-effort: any dial/RPC error, non-TTY stderr, or opt-out → print nothing, and
  never change the command's exit code.

## Testing (TDD)

- **cmd/marshal**: `updateBanner` returns the formatted line when `outdated && latest != ""`, and `""`
  otherwise (nil info, not outdated, empty latest) — pure unit test, like `labelColor`.
- **internal/daemon**: `UpdateStatus` returns the checker's snapshot. Construct a `Server` whose
  `updater` is an `updatecheck.New(current, WithReleasesURL(stubURL), WithHTTPClient(...))` pointed at
  an `httptest` server that 302-redirects to `/releases/tag/vX.Y.Z`; call `refresh` (or `Run` briefly)
  then assert `UpdateStatus` reports the expected `Outdated`/`Latest`. Also assert a nil-updater
  `Server` returns an empty `UpdateInfo` without error.
- **internal/updatecheck**: assert `DefaultInterval == 6*time.Hour`; update any existing test/comment
  that referenced 24h.

## Out of scope

- Auto-update / self-replacement (the checker is hint-only by design).
- A dedicated `marshal update` command.
- Configurable interval / configurable releases URL via flags (the env opt-out and the constant suffice).
- Showing the banner on stdout or in non-interactive output.
