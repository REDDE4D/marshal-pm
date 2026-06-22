# Marshal — CI (GitHub Actions) — Handoff

**Date:** 2026-06-22
**Branch:** built on `ci-github-actions` (off `dev`), merged into `dev`.

---

## TL;DR

Added **GitHub Actions CI** to match the new `dev`/`main` release workflow. Two workflows:
- **`.github/workflows/ci.yml`** — on every push/PR to `dev` and `main`: a **Go** job
  (gofmt-must-be-clean, `go vet`, `go test ./... -race`, `make build`) and a **Web** job
  (`npm ci` + `npm run build` in `web/`, catching TypeScript breakage).
- **`.github/workflows/release.yml`** — on a pushed `v*` tag: cross-builds **version-stamped**
  binaries (`darwin/amd64`, `darwin/arm64`, `linux/amd64`, `linux/arm64`; `CGO_ENABLED=0`,
  `-ldflags -X marshal/internal/version.Version=<tag>`), tars them, and creates a **GitHub
  Release** with `gh release create --verify-tag`.

Validated locally: both YAML files parse; the cross-build loop produces correct Mach-O/ELF
binaries; gofmt is clean. The workflows themselves run for the first time when this lands on
`dev` (CI) and when the next `vX.Y.Z` tag is pushed (release).

## Notes / decisions

- Go version comes from `go.mod` via `setup-go` `go-version-file`; web uses Node 20 + the
  committed `web/package-lock.json` (`npm ci`).
- The embedded `internal/dashboard/dist` is committed, so Go build/test/cross-build need no
  Node — the Web job only guards that `web/src` still compiles.
- Release uses the built-in `GITHUB_TOKEN` (`permissions: contents: write`), no extra secrets.

## Known issues / deferred

- **`go test -race` may flake in CI** on `cmd/marshal/TestRunSupervisesAndStops` — a tight 5s
  SIGINT-exit deadline that's sensitive to loaded runners (documented since M25). If CI goes
  red on it, re-run; the real fix is bumping that deadline (fold into the test-coverage work
  below).
- No caching beyond setup-go/npm defaults; no lint beyond gofmt/vet (could add `golangci-lint`
  later).

## Concrete next step — **add unit-test coverage** (next task)

Before the next feature milestone, do a **unit-testing pass** to raise coverage and de-flake:
- Flesh out the **M26 notification dashboard handler tests** (only `GET` + `POST channel` are
  covered; `testChannel`/`deleteChannel`/`putRule`/`deleteRule`/`putSettings` are not).
- Add the **detector test gaps** noted in M26: a non-empty `ProcInfo.Detail` flowing into
  `EventDeployFail.Detail`, and a mixed-seeding case (new process appearing alongside a
  transitioning one in the same tick).
- **Bump the flaky `cmd/marshal` SIGINT deadline** so CI `-race` runs are reliable.
- Then continue with the next milestone (e.g. M27 — recovery/"resolved" notices) on a branch
  off `dev`, and cut **v0.2.0** by merging `dev` → `main` + tagging when release-ready.
