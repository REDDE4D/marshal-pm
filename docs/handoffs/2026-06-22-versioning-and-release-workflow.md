# Marshal — Versioning, Changelog & Release Workflow — Handoff

**Date:** 2026-06-22
**Branch:** committed on `main` (the v0.1.0 release-baseline commit); `dev` created off it
for all future work.

---

## TL;DR

Introduced **proper versioning + a changelog + a two-branch release workflow**. From now on:
`main` is the **release** branch (moves only when a release is cut), and **`dev`** is the
integration branch where day-to-day work lands. The current state (everything through **M26**)
is baselined and tagged **`v0.1.0`**.

## What changed this session

- **`CHANGELOG.md`** (new) — [Keep a Changelog](https://keepachangelog.com/) + SemVer format,
  with an `## [Unreleased]` section and a `## [0.1.0] - 2026-06-22` section summarizing the
  project through M26 (supervision, fleet, dashboard, git deploys, managed credentials/SSH
  deploy keys, the M26 notification service). Compare links target `REDDE4D/marshal-pm`.
- **`Makefile`** — `make build` now stamps the version from `git describe --tags` via
  `-ldflags "-X marshal/internal/version.Version=$(VERSION)"`; added a `make version` helper
  that prints the resolved version. A plain `go build` still works and reports the in-source
  default `0.0.0-dev`. (`internal/version.Version` was already ldflags-overridable.)
- **`CLAUDE.md`** — new **"Versioning & release workflow (IMPORTANT)"** section (SemVer,
  changelog-as-you-go, the `dev`→`main`-on-release model, the release-cut + tag steps); the
  Conventions branching bullet now says work branches off `dev`, never directly on `main`;
  the Build section documents `make build` / `make version`.
- **Tag `v0.1.0`** on `main` at the release-baseline commit.
- **`dev` branch** created off `main` for future work.

## The workflow (how to operate from here)

- **Feature/milestone work:** branch off `dev` → TDD → review → merge back into `dev`
  (`--no-ff`). Update `CHANGELOG.md`'s `[Unreleased]` as part of the work, not at the end.
- **Cutting a release:** on `dev`, move `[Unreleased]` items into a new
  `## [X.Y.Z] - YYYY-MM-DD` section, update the bottom compare links, merge `dev` → `main`
  (`--no-ff`), `git tag vX.Y.Z` on `main`, then push `main`, `dev`, and the tag.
- **Versioning:** pre-1.0 — minor bumps on features, patch on fixes; breaking changes may
  ride a minor bump until 1.0.0.
- **Version in the binary:** always build releases with `make build` so `marshal --version`
  carries the real tag (e.g. `v0.1.0`, or `v0.1.0-3-gabc123` mid-cycle, `-dirty` when the
  tree is uncommitted).

## How to build / run / test

```bash
make build                 # stamps version from git tags
make version               # print the resolved version
go test ./... -race -count=1
gofmt -l . ; go vet ./...
```

## Deferred / known issues

- No CI yet — tagging/release is manual. A GitHub Actions release workflow (build + attach
  binaries on tag push, lint/test on PR to `dev`) is a natural follow-up.
- `CHANGELOG.md` `[0.1.0]` is a high-level summary, not a per-milestone reconstruction; the
  detailed history lives in `docs/handoffs/`.
- The M26 deferred items still stand (see `docs/handoffs/2026-06-22-m26-notification-service.md`):
  SMTP send ctx/deadline, cooldown-map pruning, thinner notification handler tests.

## Concrete next step

Start the next milestone (e.g. **M27 — recovery/"resolved" notices**, or the M26 hardening
batch) **on a branch off `dev`**, updating `CHANGELOG.md` `[Unreleased]` as you go. Cut
**v0.2.0** by merging `dev` → `main` and tagging when that milestone is release-ready.
