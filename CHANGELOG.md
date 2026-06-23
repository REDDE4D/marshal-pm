# Changelog

All notable changes to Marshal are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
While the project is pre-1.0, the minor version bumps on new features and the patch
version on fixes; breaking changes may occur on any minor bump until 1.0.0.

Releases are cut on the `main` branch; day-to-day development happens on `dev` and is
promoted to `main` when a release is finished. See `CLAUDE.md` for the workflow.

## [Unreleased]

### Added
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

[Unreleased]: https://github.com/REDDE4D/marshal-pm/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/REDDE4D/marshal-pm/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/REDDE4D/marshal-pm/releases/tag/v0.1.0
