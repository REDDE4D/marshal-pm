# Changelog

All notable changes to Marshal are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
While the project is pre-1.0, the minor version bumps on new features and the patch
version on fixes; breaking changes may occur on any minor bump until 1.0.0.

Releases are cut on the `main` branch; day-to-day development happens on `dev` and is
promoted to `main` when a release is finished. See `CLAUDE.md` for the workflow.

## [Unreleased]

## [0.15.0] - 2026-07-01

### Added
- `stop`, `restart`, `delete`, and `reset` now accept multiple targets and
  comma-separated lists (e.g. `marshal restart 2 3`, `marshal delete 2,3`). An
  unknown target in a multi-target call warns and the rest still run.
- `marshal restart <marshal.yaml> --update-env` reloads an app's environment
  (inline `env:` and `env_file:`) from the config file and restarts it in place,
  preserving its ID and restart history. Other spec fields still require
  `delete` + `start`.

### Changed
- App IDs are now stable: the daemon persists each app's ID in `dump.json` and
  reuses it across restarts and `resurrect`. Existing installs migrate
  automatically — the first daemon restart after upgrade renumbers apps to a
  contiguous `1..N` and they stay fixed thereafter. This makes `restart <id>` /
  `delete <id>` reliable.

## [0.14.0] - 2026-06-26

### Added
- **"Ack all" control on the Exceptions page.** The dashboard errors page now has an `ack all · N`
  button in the section header that acknowledges every unacknowledged exception in the current window
  at once; it shows the outstanding count and disables itself when nothing is left to ack.

### Fixed
- **Exceptions page text overlap.** Long error messages, sources, and process labels are now truncated
  with an ellipsis instead of wrapping and overflowing the fixed-height ledger rows, which had caused
  text to overlap neighbouring rows and the ack buttons.

## [0.13.0] - 2026-06-26

### Added
- **Terminal "update available" hint.** The local daemon now runs the same update check the server
  dashboard uses, and the CLI prints a one-line hint to stderr after a command when a newer release
  exists (e.g. `marshal: update available — v0.13.0 (current v0.12.0) → …/releases/latest`). It is
  best-effort: shown only on an interactive terminal, never spawns a daemon, and is silenced by
  `MARSHAL_NO_UPDATE_CHECK`.

### Changed
- **Update checks now run every 6h instead of every 24h** (daemon and server), so new releases surface
  sooner.

## [0.12.0] - 2026-06-26

### Added
- **`marshal reset <name|id|all>`** zeroes an app's restart counters — the lifetime total, the
  crash-loop counter, and the trailing 24h restart-event history — so `marshal list`/`describe` and
  the dashboard's `restarts_24h` all return to zero. It does not restart the process or reset uptime
  (mirrors `pm2 reset`). Also available as `marshal fleet reset <agent> <sel>` and a dashboard control.
- **`marshal flush [name|id|all]`** clears an app's captured logs — the active log files, their rotated
  backups, and the in-memory ring — so `marshal logs` starts fresh. The selector is optional and
  defaults to all (mirrors `pm2 flush`). Also `marshal fleet flush <agent> <sel>` and a dashboard control.
- **`max_memory_restart` per-app config** (e.g. `max_memory_restart: 300M`) auto-restarts an app when
  its RSS exceeds the limit for 3 consecutive metric samples (~10–15s at the default 5s tick), so a
  momentary spike won't trigger it. Accepts `K`/`M`/`G` (1024-based) or a plain byte count. Settable
  from the dashboard's add-app form too. Note: because restarts are issued per app, a multi-instance
  app restarts all of its instances when any one exceeds the limit (per-instance restart is a future
  refinement).
- **Color-coded per-app prefix in `marshal logs … -f`**: when output is a terminal, each line's
  `name#idx` prefix is colorized (stable per app) so an interleaved multi-app tail is easy to scan.
  Piped/redirected output stays plain.

## [0.11.0] - 2026-06-25

### Added
- **`marshal stop`/`restart`/`delete` accept a `marshal.yaml`**, like `marshal start` does. Passing a
  config file targets every app it defines (an app that isn't running is warned about, not fatal),
  so `marshal stop marshal.yaml` works instead of looking for an app literally named `marshal.yaml`.
- **`marshal enroll <server> --token --fingerprint` / `marshal unenroll`** join or leave a central
  server from a host's local daemon. Once enrolled, every app on the host (everything `marshal start`
  manages) appears in that server's dashboard automatically — no per-app step. The daemon now watches
  its server config and connects/reconnects live, so enrolling no longer requires a daemon restart.

### Changed
- **`marshal start`'s help now spells out that it's local-only** — apps it starts show in `marshal
  list` but not in a central-server dashboard (the local daemon and the fleet are separate stores).
  It points to `marshal fleet start <agent> <marshal.yaml>` for running apps on an enrolled agent.
- **`marshal list` (and every command that prints a process table) now renders a bordered table**
  with the state column colorized on a terminal (green online / red errored or stopped / yellow
  otherwise). Output to a pipe or file stays plain — no borders-breaking color codes.
- **`marshal server startup --self-enroll` now enrolls the one local daemon** instead of running a
  separate in-process agent under `<serverData>/agent`. There is now a single agent per host, so
  `marshal list` and the dashboard agree. Ctrl-C stops the server/dashboard; supervised apps keep
  running under the persistent daemon (stop them with `marshal stop`, the daemon with `marshal kill`).
  Upgrade note: a host that used the old self-enroll agent should remove `<serverData>/agent` and
  re-add its apps under the unified daemon.

### Fixed
- **`marshal import pm2` now bakes an absolute `cwd` into every app.** PM2 resolves an app's script
  relative to the ecosystem file's directory and defaults `cwd` to it; Marshal copied the (often
  empty) `cwd` verbatim, so a relative `script` like `src/index.js` resolved against the *daemon's*
  working directory (`$HOME` or `/` under launchd/systemd) — e.g. `/home/tgbot/src/index.js`
  (`MODULE_NOT_FOUND`). The generated `marshal.yaml` is now self-contained: an absent `cwd` defaults
  to the ecosystem directory and a relative one (e.g. `./dashboard-next`) is joined onto it.
- **`marshal import pm2` now diagnoses ESM ecosystem files instead of reporting a bare "no apps
  found".** When a project's `package.json` sets `"type":"module"`, node treats `ecosystem.config.js`
  as an ES module and silently ignores its CommonJS `module.exports`, so the importer received an
  empty object. The importer now detects this (and the related `export default` case) and tells the
  user to rename the file to `.cjs`. It also surfaces node's stderr when an ecosystem file throws
  during evaluation (e.g. a missing referenced `.env`), instead of collapsing to `exit status 1`.

## [0.10.0] - 2026-06-25

### Changed
- **Dashboard UX-clarity pass.** A consistency pass across the whole dashboard so actions explain
  themselves and outcomes are always visible — driven by a real confusion where the Notifications
  "add channel" form looked saved but wasn't. Concretely: disabled buttons now say *why* they're
  disabled (hover tooltip via a new `disabledReason`) instead of greying out silently; form fields
  gained required markers and inline hints (e.g. the channel **name** is now clearly distinct from
  the bot token); empty lists show guidance instead of blank space; success/error feedback is
  consistent and self-clearing; the Notifications "add channel"/"add rule" buttons are relabelled
  and the page shows an empty state; destructive file actions (delete/rename/new file) use proper
  in-app dialogs instead of the browser's `confirm`/`prompt`; the process control buttons no longer
  silently cancel a pending confirmation after 3 seconds; and the login form re-enables and refocuses
  after a failed attempt.

### Added
- **Shared UI primitives** for consistent dashboard clarity: `EmptyState`, `StatusMessage`/`useStatus`,
  and `ConfirmDialog`/`PromptDialog` (on the existing accessible modal), plus `disabledReason` on
  `Button` and `required`/`hint`/`error` on `Field`. A `web/src/components/README.md` documents the
  conventions for future pages.

## [0.9.0] - 2026-06-25

### Fixed
- **Notification settings silently failed to save.** The dashboard's "save settings" always
  reported "saved" regardless of the server's response, and the notifications page rendered an
  empty config (instead of an error) when the backend was unavailable — so when notifications were
  disabled server-side (e.g. an invalid `MARSHAL_MASTER_KEY`) or a save was rejected, the change
  appeared to succeed but reverted on reload with no explanation. The save button now surfaces the
  real error (and only claims success on HTTP 200), and the page now shows "notifications
  unavailable" when the backend is disabled. (`putNotifSettings`/`getNotifications` no longer
  swallow non-OK responses.)

### Added
- **Send a test notification to all enabled channels.** The Notifications → Settings section gains
  a "send test notification" button that fires a test message through every enabled channel at once
  and reports per-channel results, so you can verify your configuration end-to-end. New
  `POST /api/notifications/test` (complements the existing per-channel test).

## [0.8.0] - 2026-06-25

### Added
- **Acknowledge error signatures.** Errors can now be acknowledged from the dashboard so they
  stop nagging: the rail error badge counts only *unacknowledged* signatures, and the Errors
  page gains an "ack/acked" button per row (acked rows dim) plus an "Unacked" metric.
  Acknowledgement is persisted server-side (keyed by the stable signature id) and **re-surfaces
  automatically if the error recurs** after it was acked. New `POST /api/errors/ack`; the
  `/api/errors` response gains `acknowledged` per signature and `cluster.unacknowledged`.

## [0.7.1] - 2026-06-25

### Fixed
- **Dashboard CPU shown 100× too high.** The backend already reports CPU as a percentage
  (gopsutil per-core %, summed over the process group), but the dashboard multiplied it by 100
  again — so a process using ~2% appeared as "220%", and peaks could exceed the machine's core
  count (e.g. "559%" on a 4-core host). The Overview ledger, process-detail cluster, and the
  metric-chart axis now display the value as-is.
- **Metric charts now show values on hover.** The CPU/memory charts had no interactivity; they
  now render a crosshair and a readout (value · peak · time) for the hovered point.

### Added
- **Single-host quickstart: `marshal server --self-enroll <marshal.yaml>`.** One command boots
  the fleet server + dashboard (defaults to `:9001`), enrolls an in-process local agent against
  it, and supervises the apps in the file — so a single host gets a working dashboard without the
  multi-step token/fingerprint enrollment dance.
- **`marshal server startup`.** Installs a boot service (systemd/launchd) for the fleet server +
  dashboard — the server-side counterpart of `marshal startup` (which runs the agent). With
  `--self-enroll <marshal.yaml>` it installs the single-host quickstart as a service; `--remove`
  uninstalls; `--system` installs a root-level unit.
- **PM2 ecosystem import.** `marshal import pm2 <ecosystem.config.js|.json|.yaml>` converts a
  PM2 ecosystem file to a `marshal.yaml`. `.js`/`.cjs` files are evaluated with `node`, so
  dynamic config (env loaders, spreads, etc.) resolves exactly as it would under PM2; `.json`
  and `.yaml` are read directly. Maps `script`/`interpreter`/`node_args` → `cmd`/`args` (with
  interpreter inferred from the script extension), plus `cwd`, `env`, `env_file`, `instances`,
  `autorestart`, `max_restarts`, and `kill_timeout`. Fields with no equivalent (cluster mode,
  `watch`, `cron_restart`, `instances: "max"`) are reported as warnings. Output goes to stdout
  or, with `-o`, to a `0600` file (it may contain resolved secrets); `--split-env` instead
  writes each app's env to a `0600` `<name>.env` file referenced via `env_file:`, keeping
  resolved secrets out of the generated `marshal.yaml`.

### Fixed
- **install.sh PATH guidance.** When the binary lands in `~/.local/bin` (the fallback when
  `/usr/local/bin` isn't writable) and that directory isn't on `PATH`, the installer now prints
  the exact `export PATH=…` commands to fix it, instead of a one-line note that's easy to miss.

## [0.6.1] - 2026-06-25

### Fixed
- **Dashboard chrome rendering.** Added a global `<button>` reset so utility buttons
  (sign out, + add app, + connect agent) no longer show the browser's default light button
  background on the dark theme; defined the previously-unstyled `restart all` control; and
  forced the icon-rail glyphs (`⚠`, `⚿`) to text presentation so they render in the muted nav
  colour instead of as bright color-emoji.

## [0.6.0] - 2026-06-25

### Added
- **Per-app `env_file`.** An app may name a dotenv file (`env_file: .env.aegis`) whose
  `KEY=VALUE` lines are loaded and merged into its environment, with inline `env:` taking
  precedence. Resolved relative to the `marshal.yaml` directory; supports `#` comments, a
  leading `export `, and quoted values. Lets several apps share one script with per-app env
  files (the common PM2 ecosystem pattern) without inlining secrets into the YAML.

### Security
- **Per-IP gRPC auth throttle.** Repeated failed admin/agent/enroll token attempts from one
  source IP now trip a lockout (10 consecutive failures → 5s, doubling to 5min), rejecting
  further attempts with `ResourceExhausted` and an audited `rate_limited` event before any token
  comparison. A successful auth resets the IP's counter, so several agents behind one NAT can't
  be locked out by a single misconfigured (or hostile) peer — only an IP that is *purely*
  failing is throttled. The login limiter was extracted to a shared `internal/ratelimit`
  package used by both the dashboard and the fleet interceptors.

## [0.5.0] - 2026-06-25

### Added
- **More install methods.** Releases now also ship **`.deb`/`.rpm` packages** (binary +
  shell completions + a disabled-by-default `marshal.service`) and a multi-arch **Docker
  image of the fleet server + dashboard** at `ghcr.io/redde4d/marshal`. The Go module path
  was renamed to `github.com/REDDE4D/marshal-pm`, so `go install
  github.com/REDDE4D/marshal-pm/cmd/marshal@latest` now works.
- **Update-available notifier.** The server checks once a day (anonymously, via GitHub's
  `/releases/latest` redirect — no API token, no identifiers) whether a newer Marshal
  release exists and surfaces a dismissible banner in the dashboard, which also flags any
  connected agents running an older version. It never downloads or replaces anything —
  update via your install method. Opt out with `MARSHAL_NO_UPDATE_CHECK=1`.

### Changed
- **Module path renamed** from `marshal` to `github.com/REDDE4D/marshal-pm` (enables
  `go install`; no behavior change).

## [0.4.1] - 2026-06-25

### Build / tooling
- **Prebuilt binary distribution.** Releases now ship cross-compiled archives
  (linux/darwin × amd64/arm64) with checksums via GoReleaser, driven by the
  existing `v*` tag push. Adds a **Homebrew formula** published to
  `REDDE4D/homebrew-tap` (`brew install REDDE4D/tap/marshal`, Linux + macOS) and a
  `curl … | sh` [install script](install.sh). README gains an Install section.
  Requires a `HOMEBREW_TAP_GITHUB_TOKEN` Actions secret + a `REDDE4D/homebrew-tap`
  repo for the formula push (the binary release works without them).

## [0.4.0] - 2026-06-25

### Added
- **gRPC auth-failure auditing:** the fleet gRPC interceptors now record failed
  admin/agent/enroll token attempts (source class + peer IP + outcome) to the same
  `login-audit.log` surfaced by `marshal server audit`. Previously gRPC auth failures
  left no record at all. The audit log is now created server-side and shared with the
  dashboard login path (single writer), so it works even when the dashboard is disabled.

### Security
- **Git deploy argument-injection hardening:** `source.repo`, `source.ref`, and `source.subdir`
  are now validated. Repo/ref values that git would interpret as command-line options (a leading
  `-`, e.g. `--upload-pack=…`) and subdir values that escape the clone directory (absolute paths
  or `..` traversal) are rejected. Validation runs both at config-parse time and again at the
  deploy sink, so resurrected on-disk state (`dump.json`) is re-checked rather than trusted.
- **Per-IP dashboard login lockout:** the login limiter now caps failures per source IP in
  addition to per-(user, IP), so an attacker can no longer dodge the lockout by rotating the
  username field. A successful login clears only the per-user bucket, never the per-IP counter.
- **Constant-time agent-token lookup:** `authAgent` now hashes the presented token once and
  compares it against every enrolled agent with a constant-time compare and no early return,
  removing the timing oracle from the previous short-circuiting, map-order-dependent loop.
- **Notification HTTP hardening:** the webhook/Slack/Telegram client now has a 30s timeout (a
  black-hole endpoint can no longer hang a sender) and refuses to follow redirects (an
  operator-configured URL that 30x-bounces to an internal address was an SSRF vector).
- **Master-key env warning:** when the AES master key is sourced from `MARSHAL_MASTER_KEY`, the
  server logs a one-time notice that env vars are readable via `/proc/<pid>/environ` and inherited
  by child processes; the `0600` `master.key` file remains the recommended source.

### Fixed
- **`dump.json` written `0600` instead of `0644`:** the state dump serializes app `Env`, which
  commonly holds secrets (DB passwords, API keys); it is no longer world/group readable.
- **Registry memory bound:** disconnected agents whose last snapshot is older than the 7-day
  retention window are now evicted from the in-memory fleet registry during the periodic prune,
  so churning/ephemeral agent names can no longer grow the map without bound.

## [0.3.0] - 2026-06-24

### Changed
- **Dashboard "Marshal Instrument" redesign (M-A):** the entire web dashboard has been
  rebuilt in a new instrument/ledger design language — a left **icon rail** + top context
  bar shell replaces the per-page topbar; **metric clusters** (semantic, colour-per-metric)
  and dense **hairline ledgers** (numbered rows, hover quick-actions ▤ Log · ▸ Restart ·
  ⟲ Reload · ■ Stop) replace the old cards. Every page is restyled: Fleet overview,
  Process detail (Overview/Files sub-tabs), Errors, Notifications (rewritten with toggles
  and event chips), Credentials, Login, and the Add-app / Connect-agent modals. Adds a
  **live-log modal** (stream/level/text-regex filtering, pause) and a dedicated **Logs page**
  (`#/logs`). Every cell is backed by the real data shipped in M-B…M-G/M-F (host metrics,
  extended per-process metrics, restart history, error signatures); no mocked values.
  Inter is now bundled alongside JetBrains Mono. Includes a hardening pass (loading/empty/
  error states, focus rings, keyboard-accessible controls, icon-button aria-labels, a React
  error boundary around page content, and narrow-viewport responsiveness).

### Added
- **Errors/exceptions subsystem (M-F):** server-side error-signature grouping — stderr is
  normalized and deduplicated into signatures with occurrence counts, first/last-seen, affected
  processes, best-effort source location, and a 24-bucket occurrence trend. New `GET /api/errors`
  endpoint (range `24h`/`7d`/`all`, optional `agent` filter) and a transitional Errors page
  (`#/errors`).
- **Control additions (M-G):** graceful **reload** (rolling per-instance restart, distinct from
  restart), a per-agent **restart all** action, and a **log download** endpoint
  (`GET /api/logs/download`, plain-text full history honoring the stream/search filters).
- **Restart history (M-E):** each process now shows how many times it restarted
  in the last 24h and when it last restarted, recorded from real supervisor
  restart events in a local SQLite event store (7-day retention) and surfaced on
  the process card and in `/api/fleet`.
- **Host system metrics (M-C):** each agent now reports current-value host
  gauges — CPU%, load average (1/5/15), memory (used/total/percent), and
  network I/O rate (bytes/sec) — shipped with the periodic state push and shown
  on the agent band and in `/api/fleet`.
- **Extended per-process metrics (M-D):** thread count and open file-descriptor
  count (group-summed; FDs shown as `—` where the platform does not report them,
  e.g. macOS), plus the last exit code and reason for each process, surfaced on
  the process card and in `/api/fleet`.
- Agent host metadata (hostname, IP, OS/arch, marshal version, host uptime) on the fleet view (M-B).
- Dashboard "Connect an agent": generates a ready-to-run command (with a freshly minted enroll token, the server fingerprint, and address) to enroll a new agent host.
- Per-event-type cooldown overrides: each notification event type can have its own cooldown, falling back to the global cooldown when unset (`settings.cooldown_overrides`).
- Alert/recovery coalescing: a transient crash-then-recover blip (within a
  configurable window, default 10s; set to 0 to disable) is now delivered as a
  single merged notice instead of a separate alert and recovery.

### Fixed
- The notification cooldown map is now pruned of expired entries on each emit, so it stays bounded regardless of fleet size or uptime (previously grew unbounded).
- A `marshal.yaml` with a `server:` block and no `apps:` is now valid; `marshal start` previously rejected it with "config has no apps", preventing fleet agents (which start with zero apps and receive them later via fleet deploy) from enrolling.
- "Connect an agent" modal: the generated command no longer overflows the modal — the command block now wraps/contains long token and fingerprint lines, and the rotation warning is styled.

## [0.2.0] - 2026-06-22

### Added
- **CI** — GitHub Actions: `ci.yml` runs gofmt/vet/`go test -race`/build and a web
  (TypeScript) build on every push and PR to `dev`/`main`; `release.yml` cross-builds
  version-stamped binaries (darwin/linux × amd64/arm64) and attaches them to a GitHub
  Release when a `v*` tag is pushed.
- **Test coverage** — notification dashboard handlers (`testChannel`,
  `deleteChannel`, `putRule`, `deleteRule`, `putSettings`, plus not-found/error and
  service-unavailable paths) and detector edge cases (deploy-fail detail pass-through
  with fallback, and a new process seeding silently alongside a transitioning one).
- **Recovery notices** — the notification detector now emits a `recovered` event
  ("Process recovered") when a process that was crashing, restart-looping, or
  deploy-failing returns to `online` (including deploy recovery through an
  intermediate build). Controlled by a "Send recovery notices" setting that is on
  by default; routes through existing notification rules.

### Fixed
- **Flaky CI test** — bumped the tight 5s deadlines in
  `cmd/marshal/TestRunSupervisesAndStops` (15s startup, 30s post-SIGINT exit) so
  `go test -race` runs reliably on loaded runners.

## [0.1.0] - 2026-06-22

First versioned release. Marshal is a free, self-hosted process manager and bottom-up
fleet manager (agents per host → central server → web dashboard), written in Go and
published under GPL-3.0. This release baselines the work through milestone **M26** and
introduces semantic versioning + this changelog.

### Added
- **Process supervision** — run/supervise apps from `marshal.yaml`, restart policies,
  max-restarts, multi-instance, CPU/memory metrics, captured stdout/stderr logs, a local
  daemon with a control CLI, dump/resurrect, and a boot-time startup service.
- **Fleet** — a central server aggregating connected agents over a mutual-TLS gRPC link
  with token enrollment and a pinned server fingerprint; per-agent command channel
  (restart/stop/start/deploy across the fleet); fleet-wide `ps`, logs, and metrics.
- **Web dashboard** — session-authenticated SPA (embedded in the binary) showing fleet
  and per-process state, logs, metrics, with login rate-limiting and an audit log.
- **Git deploys (M21–M24)** — deploy/redeploy apps from a git repo, build step, an
  in-dashboard read-only file browser, and edit→commit→push.
- **Managed git credentials (M22, M25)** — an encrypted-at-rest credential store
  (AES-256-GCM under a server master key) for HTTPS tokens and generated **SSH deploy
  keys** (server-minted ed25519, TOFU-pinned host key, strict agent-side verification).
- **Notification service (M26)** — server-side fleet alerting: a snapshot-diff detector
  raises `crash` / `restart_loop` / `agent_down` / `agent_up` / `deploy_fail` events; a
  dispatcher applies a per-`(agent,process,type)` cooldown and a rules engine
  (event-type + agent + process → channels); delivery via **webhook (HMAC-signed),
  Telegram, Slack, and email (SMTP)**. Per-channel secrets are sealed via the shared
  `internal/secretbox` (the credential store reuses the same master key). Managed from a
  `#/notifications` dashboard page with a Test button; secrets are write-only over the API.

### Build / tooling
- `make build` now stamps the version from `git describe --tags` via `-ldflags`
  (`marshal --version` reports it); `make version` prints the resolved version.

[Unreleased]: https://github.com/REDDE4D/marshal-pm/compare/v0.15.0...HEAD
[0.15.0]: https://github.com/REDDE4D/marshal-pm/compare/v0.14.0...v0.15.0
[0.14.0]: https://github.com/REDDE4D/marshal-pm/compare/v0.13.0...v0.14.0
[0.13.0]: https://github.com/REDDE4D/marshal-pm/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/REDDE4D/marshal-pm/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/REDDE4D/marshal-pm/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/REDDE4D/marshal-pm/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/REDDE4D/marshal-pm/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/REDDE4D/marshal-pm/compare/v0.7.1...v0.8.0
[0.7.1]: https://github.com/REDDE4D/marshal-pm/compare/v0.7.0...v0.7.1
[0.7.0]: https://github.com/REDDE4D/marshal-pm/compare/v0.6.1...v0.7.0
[0.6.1]: https://github.com/REDDE4D/marshal-pm/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/REDDE4D/marshal-pm/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/REDDE4D/marshal-pm/compare/v0.4.1...v0.5.0
[0.4.1]: https://github.com/REDDE4D/marshal-pm/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/REDDE4D/marshal-pm/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/REDDE4D/marshal-pm/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/REDDE4D/marshal-pm/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/REDDE4D/marshal-pm/releases/tag/v0.1.0
