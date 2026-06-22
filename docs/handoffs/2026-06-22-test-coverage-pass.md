# Marshal — Test-coverage pass — Handoff

**Date:** 2026-06-22
**Branch:** built on `test-coverage-pass` (off `dev`), merged into `dev` (`--no-ff`, commit `eab6ce7`).

---

## TL;DR

Completed the **unit-testing pass** queued by the CI handoff — coverage for previously
untested code paths plus a de-flake of the one flaky CI test. No production code changed;
this is tests + a CHANGELOG entry only.

1. **M26 notification dashboard handlers** (`internal/dashboard/notifications_test.go`) —
   added tests for the endpoints that were previously uncovered (only `GET` + `POST channel`
   had tests):
   - `testChannel`: success, unknown-channel 404, send-error (`ok:false`), and
     secrets-decrypt-error (`ok:false`) paths.
   - `deleteChannel` / `deleteRule`: 204 on hit, 404 on miss.
   - `putRule`: create + missing-name 400. `putChannel`: missing-fields 400.
   - `putSettings`: store + bad-JSON 400.
   - `notifsReady` 503 when the store is nil.
   - The `fakeNotifs` test double now does **real delete-by-name** (returns found/not-found)
     and supports an injectable `secretsErr`, so both branches of the delete/secret paths run.

2. **Detector gaps** (`internal/notify/detector_test.go`):
   - `EventDeployFail.Detail` carries `ProcInfo.Detail` through (and falls back to
     `"deploy failed"` when empty).
   - A brand-new process in a snapshot seeds silently even when another process in the
     **same agent, same tick** transitions (only the transition emits).

3. **De-flake** (`cmd/marshal/run_test.go`): bumped `TestRunSupervisesAndStops`'s tight 5s
   deadlines to **15s** (startup wait) and **30s** (post-SIGINT exit). The test returns as
   soon as its condition is met, so the higher ceiling costs nothing on a fast machine but
   stops the flake on loaded `-race` runners (the long-standing M25 issue).

## How to verify

```bash
go test ./... -race -count=1     # all green (server pkg ~23s, rest fast)
go vet ./... && gofmt -l .       # both silent
go test ./internal/dashboard/ -run 'Notif|Channel|Rule|Settings' -v
go test ./internal/notify/ -run TestDiff -v
```

Full `-race` sweep was run and passed across every package. gofmt/vet clean.

## Notes / decisions

- **No live demo** this time: the change is test-only — nothing user-facing was added, so the
  "live demo" convention doesn't apply. The verification *is* the `-race` sweep above.
- Handlers use Go 1.22+ `r.PathValue("name")`; tests set it via `req.SetPathValue(...)` and
  call handler methods directly (no router needed), matching the existing test style.

## Known issues / deferred

- The flaky test is mitigated (generous deadlines), not structurally redesigned — if it ever
  recurs, the real fix is decoupling the assertion from wall-clock timing entirely.
- Coverage is broader but not exhaustive; `notifBuild` wiring in the real server path
  (vs. the injected fake) is still only exercised end-to-end, not unit-tested.

## Concrete next step

Begin the next milestone on a branch off `dev`:
- **M27 — recovery / "resolved" notices**: emit an event when a previously-alerting process
  returns to a healthy state (e.g. `errored`/`restarting` → `online`), so operators get an
  "all clear" rather than just the alarm. This extends `procEvent`/`diff` in
  `internal/notify/detector.go` and a new `EventType`.
- When release-ready, cut **v0.2.0**: move `[Unreleased]` → `## [0.2.0] - <date>`, update
  compare links, merge `dev` → `main` (`--no-ff`), tag `v0.2.0`, push `main`/`dev`/tag.
