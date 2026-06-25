# Handoff — gRPC auth throttle + per-app env_file, v0.6.0 (2026-06-25)

## Current state

- Released **v0.6.0** (cut on `dev`, merged to `main`, tagged, pushed). `main` = release branch,
  `dev` = integration.
- `go test ./... -race` green; gofmt/vet clean; 39 web tests green.
- Fourth same-day release: v0.4.0 (security), v0.4.1 (GoReleaser), v0.5.0 (install methods +
  update notifier), v0.6.0 (this).

## What shipped this milestone

### Per-IP gRPC auth throttle (security; closes the last deferred review item)
- Repeated failed admin/agent/enroll token attempts from one source IP trip a lockout
  (10 consecutive failures → 5s, doubling to 5min). Further attempts get
  `codes.ResourceExhausted` + an audited `rate_limited` event **before** any token comparison
  (reject-fast, no tarpit → can't be turned into a resource-exhaustion vector).
- **NAT-safe**: a successful auth `Reset`s the IP's counter, so several agents behind one NAT
  can't be locked out by one misconfigured/hostile peer — only an IP that is *purely* failing
  trips. This is the property that made a per-IP lockout safe to ship (the reason it was
  originally deferred).
- The dashboard login limiter was generic, so it was extracted to **`internal/ratelimit`**
  (`Policy{Threshold, Base, Cap}`, consecutive-fail exponential backoff, `Reset`, prune) and is
  now used by both the dashboard and the fleet interceptors. The throttle lives on `AuthStore`
  (`throttle *ratelimit.Limiter`, default policy installed in `LoadOrInitAuth`, override via
  `SetThrottle`). Interceptor helpers are nil-safe.

### Per-app env_file (feature; PM2 ecosystem-config parity)
- `config.App.EnvFile` (`yaml:"env_file" json:"-"`). At `config.Load`, each app's env_file is
  read (relative to the marshal.yaml dir, or absolute), parsed, and merged into `Env` with
  **inline `env:` taking precedence**. `parseDotEnv` handles `#` comments, a leading `export `,
  trimming, matched surrounding quotes, and first-`=` splitting.
- Load-time only: `Load` does unmarshal → `loadEnvFiles(dir)` → `Prepare`. `Parse([]byte)` (used
  by the dashboard/in-memory path) does NOT resolve env_file — it's a YAML-file feature. The
  merged `Env` is what's persisted to dump.json (env_file is `json:"-"`), so resurrect uses the
  merged result; the .env is re-read only when you re-run `marshal start`/`run` (matches PM2).

## Key decisions / non-obvious bits

- **Throttle keys on peer IP only** (no identity for a failed token). Reject-fast + reset-on-
  success is the design that avoids a NAT-shared-fleet DoS. Default threshold 10 (higher than the
  dashboard's 5) since agents reconnect.
- **env_file precedence = inline wins.** PM2's JS lets either win by spread order; Marshal picks
  the intuitive "explicit YAML beats the file" and documents it. Collisions are rare.
- **No `script:`/interpreter auto-detect** — use `cmd: node, args: [src/index.js]`. **cwd**
  defaults to the daemon's working dir, not the config-file dir — set it explicitly.

## Build / run / test

```bash
make build
go test ./... -race -count=1
cd web && npm run build           # only if touching the dashboard
goreleaser release --snapshot --clean   # local dry-run (skip docker if no daemon)
```

Release flow (used for v0.6.0): promote `[Unreleased]`→`[X.Y.Z]` + compare links on `dev`,
commit, merge `dev`→`main` (`--no-ff`), `git tag vX.Y.Z`, push `main` + `dev` + tag. The Release
workflow runs GoReleaser (binaries + checksums + deb/rpm + Docker + brew formula).

## Deferred / known issues

- **In-process ed25519 keygen** (replace the `ssh-keygen` temp-file in credstore) — the only
  remaining item from the original security review; deferred because it adds an `x/crypto` dep
  for a Low-severity finding.
- **GHCR image visibility**: the `ghcr.io/redde4d/marshal` package may still be **private**
  (GHCR default). Make it public in the package settings for anonymous `docker pull` — one-time.
- `dockers`/`docker_manifests` and `brews` are GoReleaser-deprecated (works under the `~> v2`
  pin); revisit at the v3 boundary.
- env_file is a YAML-config feature; the dashboard "add app" flow doesn't resolve it (set env
  inline there).

## Concrete next step

Verify the v0.6.0 Release workflow is green (binaries + deb/rpm + image + brew formula bumped to
0.6.0). If continuing, the in-process ed25519 keygen is the last open hardening item; otherwise
the distribution + PM2-parity story is complete.
