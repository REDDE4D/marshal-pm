# Handoff — PM2 import + single-host quickstart, v0.7.0 (2026-06-25)

## Current state

- Released **v0.7.0** (cut on `dev`, merged to `main`, tagged, pushed). `main` = release branch.
- `go test ./... -race` green; gofmt/vet clean; 39 web tests green.
- Fifth+ same-day release line: v0.4.0 (security) → v0.6.1 (dashboard fix) → v0.7.0 (this).

## What shipped this milestone

### PM2 ecosystem import — `marshal import pm2 <file>`
- `internal/pm2import`: `Convert(Ecosystem) (Config, []warnings)` (pure) + `Load(path)` (JSON/YAML
  direct; `.js`/`.cjs` evaluated with `node` so loadEnv()/spreads resolve as under PM2) +
  `Config.YAML()` + `Config.SplitEnvFiles(dir)`.
- Mapping: `script`/`interpreter`/`node_args` → `cmd`/`args` (interpreter inferred from the script
  extension; `none`/no-ext runs the script directly), plus `cwd`, `env` (values stringified),
  `env_file`, `instances`, `autorestart`→`restart`, `max_restarts`, `kill_timeout`(ms)→duration.
  Unsupported (cluster, watch, cron_restart, instances:"max") → warnings on stderr.
- CLI `cmd/marshal/importcmd.go`: stdout by default, `-o` writes 0600, `--split-env` writes each
  app's env to a 0600 `<name>.env` and references it via `env_file:` (keeps secrets out of the YAML).

### Single-host quickstart — `marshal server --self-enroll <marshal.yaml>`
- `cmd/marshal/selfenroll.go`: boots the server (`server.ServeDir`) + an in-process agent
  (`daemon.Run`) in one process. Mints an enroll token + reads the fingerprint while the server is
  down, writes a localhost `server:` block + the apps to a dedicated agent store under
  `<dataDir>/agent`, then runs both concurrently (the agent's fleet client retries until the server
  is up). Prompts for a dashboard password if none is set. Dashboard defaults to `:9001`.

### `marshal server startup`
- `cmd/marshal/server_startup.go` + parameterized `internal/startup`: the `startup.Config` gained
  `Args []string` (default `["daemon"]`) and `Label string` (default → the original `marshal` /
  `com.marshal.daemon` names, so existing `marshal startup` is byte-for-byte unchanged — covered by
  `TestDefaultsPreserveDaemonService`). The server unit is `marshal-server` / `com.marshal-server`
  and runs `server --http-listen … [--self-enroll <abs yaml>]`. Flags: `--self-enroll`, `--system`,
  `--remove`, `--http-listen`.

## Key decisions / non-obvious bits

- **node-eval for `.js` ecosystems** is the whole trick — static JS parsing can't handle
  `loadEnv()`/spreads, but `node -e "JSON.stringify(require(file))"` resolves exactly what PM2 sees.
- **self-enroll runs two servers in one process** (server + agent) rather than spawning
  subprocesses; relies on the fleet client's reconnect to paper over startup order.
- **startup label/args defaults preserve the daemon unit** — the launchd label stays
  `com.marshal.daemon` when `Label==""`, so macOS `marshal unstartup` still finds old installs.
- A boot service can't prompt, so `server startup` warns if no dashboard password is set, and
  self-enroll-as-a-service needs `marshal server passwd` run first.

## Build / run / test

```bash
make build
go test ./... -race -count=1
cd web && npm run build           # only if touching the dashboard
goreleaser release --snapshot --clean   # local dry-run (skip docker if no daemon)
```

Release flow unchanged: promote `[Unreleased]`→`[X.Y.Z]` + compare links on `dev`, commit, merge
`dev`→`main` (`--no-ff`), tag, push `main`+`dev`+tag → the Release workflow runs GoReleaser.

## Deferred / known issues

- **In-process ed25519 keygen** (credstore ssh-keygen temp file) — the last open item from the
  original security review; deferred (adds an `x/crypto` dep for a Low finding).
- `import pm2` splits a string `args` field on whitespace (best-effort; complex quoting may need
  manual fixup) and inlines `env` for non-`env_file` configs unless `--split-env` is used.
- `dockers`/`docker_manifests`/`brews` are GoReleaser-deprecated (pinned `~> v2`); revisit at v3.

## Concrete next step

Verify the v0.7.0 Release workflow is green (binaries + deb/rpm + GHCR image + brew → 0.7.0). The
distribution + PM2-migration + single-host-dashboard story is now complete; the remaining hardening
item is the in-process ed25519 keygen.
