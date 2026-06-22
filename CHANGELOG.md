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
- **CI** — GitHub Actions: `ci.yml` runs gofmt/vet/`go test -race`/build and a web
  (TypeScript) build on every push and PR to `dev`/`main`; `release.yml` cross-builds
  version-stamped binaries (darwin/linux × amd64/arm64) and attaches them to a GitHub
  Release when a `v*` tag is pushed.

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

[Unreleased]: https://github.com/REDDE4D/marshal-pm/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/REDDE4D/marshal-pm/releases/tag/v0.1.0
