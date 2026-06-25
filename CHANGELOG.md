# Changelog

All notable changes to Marshal are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
While the project is pre-1.0, the minor version bumps on new features and the patch
version on fixes; breaking changes may occur on any minor bump until 1.0.0.

Releases are cut on the `main` branch; day-to-day development happens on `dev` and is
promoted to `main` when a release is finished. See `CLAUDE.md` for the workflow.

## [Unreleased]

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

[Unreleased]: https://github.com/REDDE4D/marshal-pm/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/REDDE4D/marshal-pm/compare/v0.4.1...v0.5.0
[0.4.1]: https://github.com/REDDE4D/marshal-pm/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/REDDE4D/marshal-pm/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/REDDE4D/marshal-pm/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/REDDE4D/marshal-pm/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/REDDE4D/marshal-pm/releases/tag/v0.1.0
