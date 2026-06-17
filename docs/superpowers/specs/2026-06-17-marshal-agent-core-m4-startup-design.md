# Marshal Agent-Core M4 — Boot Startup (design)

**Date:** 2026-06-17
**Milestone:** M4 (boot startup) of sub-project #1 (agent / supervisor core).
**Status:** design approved; ready for an implementation plan.

## 1. Goal

Let Marshal launch itself on boot. `marshal startup` generates and installs an init-system
service (systemd on Linux, launchd on macOS) that runs `marshal daemon` at boot/login as the
right user with the right environment. The booted daemon then auto-resurrects saved apps from
`dump.json` (already implemented — `internal/daemon/server.go` `Run` loads the dump on start).
`marshal unstartup` removes the service.

The init system supervises `marshald`; `marshald` supervises the apps. Crash-restart of the
daemon itself is delegated to the init system (`Restart=on-failure` / `KeepAlive`).

Scope is **pure startup only**. The two deferred log items (max-line cap in `logs.Sink`, the
follow backfill→subscribe gap) are explicitly out of scope; they belong to sub-project #2.

## 2. Privilege model

Two scopes, selected by a `--system` flag; default is user-level.

- **User-level (default, no root):**
  - systemd: `~/.config/systemd/user/marshal.service`, enabled with `systemctl --user`, plus
    `loginctl enable-linger <user>` so it starts at boot without an active login.
  - launchd: `~/Library/LaunchAgents/com.marshal.daemon.plist` (LaunchAgent, runs at login).
  - `startup` writes the file and runs the enable commands directly — it's the user's own home
    directory, so no privilege escalation is needed.
- **System-level (`--system`, root):**
  - systemd: `/etc/systemd/system/marshal.service` (`User=<user>`, `WantedBy=multi-user.target`).
  - launchd: `/Library/LaunchDaemons/com.marshal.daemon.plist` (`UserName=<user>`; starts at
    real boot).
  - `startup` **never self-escalates.** It stages the rendered file under the state dir and
    **prints** the exact `sudo …` command block for the user to run. Transparent and auditable
    (PM2's model).

## 3. Architecture

New leaf package `internal/startup`. It depends only on a resolved environment (binary path +
HOME/XDG/user) — **not** on `daemon`/`manager`. The CLI performs the only side effects.

Rendering is pure; side effects are a thin, injectable executor. This mirrors the existing seam
in the repo (pure logic vs. the side-effecting daemon).

### 3.1 Data model

```go
package startup

// Config is the resolved environment the boot service must reproduce.
type Config struct {
    Binary  string // absolute path to the marshal binary (os.Executable)
    User    string // current username (used only for --system units)
    Home    string // $HOME to pin, so the daemon finds the same state dir
    XDGData string // $XDG_DATA_HOME to pin, or "" (omitted from the unit)
    System  bool   // --system: root-level unit vs per-user
}

// Plan is the fully-resolved set of side effects. Building it is pure.
type Plan struct {
    UnitPath    string   // where the unit/plist file belongs
    StagePath   string   // where we stage it for --system (under the state dir)
    Content     string   // rendered unit/plist text
    PostInstall []string // commands to run (user) or print (--system)
    PostRemove  []string // for unstartup
    NeedsRoot   bool     // == Config.System
    Label       string   // "marshal.service" / "com.marshal.daemon"
}

// Platform renders Plans for one init system.
type Platform interface {
    InstallPlan(Config) Plan
    RemovePlan(Config) Plan
}
```

Two implementations: `systemd` and `launchd`.

### 3.2 Detection

`Detect(goos string) (Platform, error)`:
- `darwin` → launchd.
- `linux` → systemd, **only if** systemd is present. The systemd probe is injectable (a
  `func(string) bool` checking `/run/systemd/system`) so both present/absent paths are testable.
  Absent → error: "systemd not detected (only systemd is supported on Linux)".
- any other GOOS → error: "boot startup is not supported on <goos>" (Windows stays deferred,
  consistent with M1–M3).

`goos` is a parameter (not read from `runtime` inside) so detection is unit-testable.

## 4. Generated artifacts

All four variants pin the absolute binary path and the environment (`HOME`, and `XDG_DATA_HOME`
only when set) so the booted daemon resolves the same state dir and auto-resurrects `dump.json`.

### systemd — user (`~/.config/systemd/user/marshal.service`)
```ini
[Unit]
Description=Marshal process manager
After=network.target

[Service]
Type=simple
ExecStart=/abs/marshal daemon
Restart=on-failure
Environment=HOME=/home/alice
Environment=XDG_DATA_HOME=/home/alice/.local/share   # only if set

[Install]
WantedBy=default.target
```
PostInstall: `systemctl --user daemon-reload`; `systemctl --user enable --now marshal.service`;
`loginctl enable-linger <user>`. No `User=` for user units.
PostRemove: `systemctl --user disable --now marshal.service`; remove the file.

### systemd — system (`/etc/systemd/system/marshal.service`)
As above plus `User=<user>` and `WantedBy=multi-user.target`. Staged to
`<stateDir>/marshal.service`. Printed: `sudo cp <stage> /etc/systemd/system/marshal.service &&
sudo systemctl daemon-reload && sudo systemctl enable --now marshal.service`.

### launchd — user / LaunchAgent (`~/Library/LaunchAgents/com.marshal.daemon.plist`)
```xml
<plist version="1.0"><dict>
  <key>Label</key><string>com.marshal.daemon</string>
  <key>ProgramArguments</key><array>
    <string>/abs/marshal</string><string>daemon</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>EnvironmentVariables</key><dict>
    <key>HOME</key><string>/Users/alice</string>
    <key>XDG_DATA_HOME</key><string>...</string>   <!-- only if set -->
  </dict>
</dict></plist>
```
PostInstall: `launchctl bootstrap gui/<uid> <path>` (modern; document `launchctl load -w` as the
fallback). LaunchAgent runs at login.
PostRemove: `launchctl bootout gui/<uid> <path>`; remove the file.

### launchd — system / LaunchDaemon (`/Library/LaunchDaemons/com.marshal.daemon.plist`)
Adds `<key>UserName</key><string><user></string>`. Staged + printed:
`sudo cp <stage> /Library/LaunchDaemons/com.marshal.daemon.plist && sudo launchctl bootstrap
system <path>`. Starts at real boot.

### Rendering notes
- Encode values properly (XML escaping for plist, no unescaped newlines/`%` in ini) rather than
  raw interpolation.
- `Restart=on-failure` / `KeepAlive` mean the OS restarts a crashed daemon, complementing
  in-daemon resurrection.

## 5. CLI surface

Two commands in `cmd/marshal`, registered in `main.go`.

### `marshal startup [--system]`
1. Resolve `Config`: `os.Executable()` → abs binary; `user.Current()`; `$HOME`; `$XDG_DATA_HOME`.
2. `Detect(runtime.GOOS)` → platform (clear error on unsupported).
3. Build `InstallPlan`.
4. **User-level:** create the unit dir, write `Content` (0644), run each `PostInstall` command
   (stream output), then print a confirmation and a verify one-liner
   (`systemctl --user status marshal` / `launchctl list | grep marshal`).
5. **`--system`:** write `Content` to `StagePath` under the state dir, print the exact `sudo …`
   block, and exit 0 without executing it.

### `marshal unstartup [--system]`
Build `RemovePlan`. User-level: run the disable/unload commands, remove the unit file (ignore
"not found"). `--system`: print the `sudo` removal commands. Idempotent — removing a
non-existent service succeeds with an informational note.

### Failure handling
A failing `PostInstall` command surfaces its stderr and a non-zero exit; the written unit file is
left in place for inspection/retry. No rollback of partial installs (matches the tool's low-magic
posture).

These complete the `startup` / `unstartup` row the agent-core spec already lists (§9).

## 6. Testing

TDD throughout; the pure/side-effecting split makes most of it unit-level.

**Unit — `internal/startup`:**
- Render/Plan golden tests, table-driven over all four variants. Assert `UnitPath`/`StagePath`,
  `NeedsRoot`, the pinned `ExecStart`/`ProgramArguments` (abs binary + `daemon` arg), pinned
  `HOME`, `User=`/`UserName` present iff `--system`, the `WantedBy` target, and the exact
  `PostInstall`/`PostRemove` command lists.
- `XDG_DATA_HOME` omission: empty → no `XDG_DATA_HOME` emitted; set → emitted.
- Encoding: a `Config` whose paths contain characters needing escaping produces valid
  escaped XML/ini.
- `Detect(goos)`: `darwin`→launchd; unknown GOOS→error; the systemd branch uses the injectable
  probe to cover present (→systemd) and absent (→error).

**Integration — user-level round-trip** (temp `HOME`, no real `systemctl`/`launchctl`):
make the `PostInstall` runner injectable (a `Runner` that records invocations). Assert `startup`
writes the unit file to the temp path with the right content and 0644 mode and emits the expected
command sequence; `unstartup` removes the file and emits the disable commands. CI never executes
init-system commands.

**Not automated (manual, documented in the handoff):** enabling under a live systemd/launchd and
rebooting — a real-host smoke test, like the M2/M3 socket smoke tests.

**Gate before finishing** (CLAUDE.md): `go build ./...`; `go test ./... -race -count=1`;
`go vet ./...`; `gofmt -l .` (lists nothing).

## 7. Module boundaries touched

- **new** `internal/startup` — `Config`, `Plan`, `Platform`, `systemd`, `launchd`, `Detect`.
- `cmd/marshal` — `startup.go` with the two commands; registered in `main.go`.
- `internal/store` — may expose a helper for the `--system` stage path (e.g. `StagePath(name)`),
  or the CLI reuses `Dir()`.

No proto changes, no daemon changes — `marshal daemon` is already the boot entry point and
already auto-resurrects.
