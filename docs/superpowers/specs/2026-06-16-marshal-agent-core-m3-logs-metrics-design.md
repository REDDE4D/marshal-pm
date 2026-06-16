# Marshal Agent-Core — M3: Logs + Live Metrics — Design

Status: approved (2026-06-16). Milestone **M3** of the agent-core sub-project. Builds on
M1 (foreground supervisor) and M2 (daemon + control CLI), both merged on `main`.

Parent specs:
- `docs/superpowers/specs/2026-06-16-marshal-agent-core-design.md` (§7 Logs, §8 Metrics)
- `docs/superpowers/specs/2026-06-16-fleet-process-manager-architecture-design.md`

## 1. Goal

Capture each supervised instance's stdout/stderr to rotated files and an in-memory ring
buffer, stream them to `marshal logs <name|id> [-n N] [-f]`, and sample live CPU%/RSS per
instance (whole process group) every 5s, surfaced in `marshal list` / `describe`.

This implements the reserved `Logs` RPC and `ProcInfo.cpu`/`mem` fields the M2 gRPC
contract already defined — **no proto contract change is required**.

## 2. Scope

In:
- Per-instance stdout/stderr capture to rotated files (10MB, keep 5) via `lumberjack`.
- In-memory ring buffer (~1000 lines/instance) backing instant `logs -f`.
- `Logs` server-stream RPC: backfill `-n N` lines (from the ring buffer) then optionally
  follow live.
- `marshal logs <name|id> [-n N] [-f]` CLI command; merged, instance/stream-tagged output.
- Live metrics: per-instance CPU%/RSS summed over the **process group**, sampled every 5s
  via `gopsutil`, surfaced as new `CPU`/`MEM` columns in `list`/`describe`.
- Polish: strip the verbose gRPC error prefix in the CLI; add `tools.go` pinning the
  protoc plugins.

Out (deferred to sub-project #2 or later):
- Deep log history beyond the ring buffer (file-tail backfill across rotated segments).
- Log compression / retention-by-age; per-stream-only `logs` view.
- Metric history/storage/aggregation.

## 3. Architecture

Approach **A** (daemon-owned sink registry + sampler; manager/proc stay thin):

```
  marshal logs ──gRPC stream── marshald.Logs ──┐
                                                ├─ logs.Registry (label → *logs.Sink)
  proc child stdout/stderr ──▶ Sink.Writer ─────┘     tee: raw→lumberjack file
                                                              line→ring + fanout
  marshald metrics.Sampler ──5s──▶ process-group CPU%/RSS ──▶ merged into ProcInfo
```

- The **manager** stays lifecycle-only (no lumberjack/gopsutil imports). It gains one
  optional functional option to obtain per-instance log writers.
- The **daemon** owns all M3 wiring: the `logs.Registry`, the `metrics.Sampler`, and the
  `Logs` RPC handler.
- `proc` gains writer fields on `Spec`; nil preserves M1 behavior (inherit the terminal),
  so `marshal run` is unchanged.

## 4. Component: `internal/logs`

One `Sink` per supervised instance, created once and **reused across restarts** so a
`restart` keeps the same files and ring history.

```go
type Line struct {
    Ts     time.Time
    Stderr bool
    Text   string
}

type Sink struct {
    out, err *lumberjack.Logger    // <dir>/<label>.out.log / .err.log, MaxSize 10MB, MaxBackups 5
    ring     *ringBuffer           // ~1000 Lines, mutex-guarded
    subs     map[int]chan Line     // -f subscribers
    // mutex(es) as needed
}
```

- `Sink.Writer(stderr bool) io.Writer` — the process's stdout/stderr is wired here. Each
  `Write` **tees**: raw bytes → the matching lumberjack writer (exact output preserved),
  and a **line-splitter** that accumulates until `\n`, stamps a `Line`, appends to the
  ring, and **non-blockingly** fans out to subscribers (drop-on-full: a slow `-f` client
  must never stall the supervised process).
- `Sink.Backfill(n int) []Line` — last `n` lines from the ring, merged across out/err
  (already time-ordered by append order).
- `Sink.Subscribe() (<-chan Line, func())` — live tail; the returned func unsubscribes.
- `Sink.Close()` — flush + close both lumberjack writers, close/drop subscribers (called
  on `delete`).

```go
type Registry struct { /* mu; dir string; sinks map[string]*Sink */ }
func (r *Registry) For(label string) *Sink   // create-or-return; reused on recreation
func (r *Registry) Remove(label string)      // Close + drop (on delete)
func (r *Registry) Resolve(labels []string) []*Sink
```

Log dir: `<state>/logs/` — `store` exposes the path, created `0o700`; files written
`0o600` (lumberjack `FileMode`-equivalent), matching the M2 filesystem security model.

## 5. Capture seam (`proc` + `manager`)

- `proc.Spec` gains `Stdout, Stderr io.Writer`. In `proc.Start`:
  `cmd.Stdout = orStdout(spec.Stdout)` and `cmd.Stderr = orStderr(spec.Stderr)`, where a
  nil writer falls back to `os.Stdout`/`os.Stderr`. **M1 `marshal run` is unchanged.**
- `manager` gains a functional option, e.g.
  `manager.New(ctx, manager.WithLogs(func(label string) (io.Writer, io.Writer)))`.
  When set, `startInstance` resolves the per-label writers (from the daemon's
  `registry.For(label)`) and sets them on the `proc.Spec`. Foreground `run` constructs the
  manager without the option.

## 6. Component: `internal/metrics`

```go
type Sample struct { Cpu float64; Mem uint64 }  // percent; RSS bytes

type Sampler struct {
    mu    sync.Mutex
    last  map[string]Sample            // label → latest reading
    procs map[int]*process.Process     // gopsutil handles, kept across ticks for CPU% deltas
    // tick interval (default 5s; configurable for tests)
}

func (s *Sampler) Run(ctx context.Context, snapshot func() []manager.InstanceSnapshot)
func (s *Sampler) Get(label string) (Sample, bool)
```

- Ticks every **5s**. Each tick, for every **online** instance: enumerate the process
  group (main pid + descendants via gopsutil `Children()`), sum `RSS` and `CPUPercent`,
  store keyed by label. gopsutil computes CPU% as a delta between samples, so per-pid
  `*process.Process` handles are retained across ticks; dead pids are pruned.
- Owned by the **daemon**, started in `Run`. When the daemon maps an `InstanceSnapshot`
  to a `ProcInfo`, it fills `cpu`/`mem` from `Get(label)` (zero if no sample yet). The
  manager never imports gopsutil.

## 7. `Logs` RPC + CLI

- **Daemon `Logs(LogRequest, stream)`**: resolve `target` (name | id | `all`) → labels →
  `registry.Resolve`. Emit `Backfill(lines)` as ordered `LogLine`s (across instances by
  `Ts`). If `follow`, `Subscribe` to each sink and stream until the client disconnects
  (`stream.Context().Done()`); otherwise return after backfill.
- **`marshal logs <name|id> [-n N] [-f]`** (cobra; default `-n 15`): consume the stream,
  printing `label | text` — stdout lines to the client's stdout, stderr lines to its
  stderr, both instance/stream-tagged. `LogLine` already carries `name`/`instance_id`/
  `stderr`.

## 8. `list` / `describe`

Add **CPU** and **MEM** columns: CPU as `12.3%`, MEM humanized (`45.2MB`). Non-online
instances show `-` (consistent with the existing UPTIME treatment).

## 9. Proto / build polish

- `daemon.proto` is already M3-ready (`Logs` stream, `ProcInfo.cpu`/`mem`,
  `LogRequest`/`LogLine`). No contract change; regenerate only if edited.
- Add `internal/pb/tools.go` (build-tagged `//go:build tools`) importing
  `google.golang.org/protobuf/cmd/protoc-gen-go` and
  `google.golang.org/grpc/cmd/protoc-gen-go-grpc`, and add them to `go.mod`, so proto
  regeneration is reproducible.
- **CLI error prefix:** in `cmd/marshal/control.go` `withClient`, unwrap gRPC status via
  `status.FromError` so users see `no app matching "x"` instead of
  `rpc error: code = NotFound desc = ...`.

## 10. Testing (TDD — failing test first)

- **`logs` (unit):** tee writes raw bytes to file *and* split lines to the ring; lumberjack
  rotation (tiny MaxSize in test) keeps the cap; backfill ordering across out/err;
  subscriber fanout incl. drop-on-full; `Close` flushes and drops subscribers.
- **`metrics` (unit):** spawn a real short-lived child that forks a grandchild; assert
  process-group RSS exceeds single-pid RSS; assert dead-pid pruning. Tolerant thresholds
  (CPU/mem are environment-sensitive).
- **`daemon` e2e:** `start` an app printing known lines → `logs -n N` returns them in
  order; `logs -f` receives a line emitted after subscribe; `list` shows non-zero MEM
  after one sample interval (make the sampler tick configurable for the test).
- Gate before finishing: `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .`
  (lists nothing), `go mod tidy` no-op.

### Gotchas (carried from M2)
- macOS Unix-socket path limit (~104 bytes): any test that binds a socket uses a short
  `/tmp` base (`os.MkdirTemp("/tmp", ...)`), not `t.TempDir()`. Logs unit tests are
  file-only and may use `t.TempDir()`; daemon e2e must use the short base.

## 11. New dependencies

- `gopkg.in/natefinch/lumberjack.v2` — rotating file writer (MaxSize/MaxBackups model).
- `github.com/shirou/gopsutil/v3` (process) — CPU%/RSS and process-group enumeration.
- protoc plugins pinned via `tools.go` (build-tagged; not compiled into `marshal`).

## 12. Module boundaries after M3

- `proc` — spawn/signal/wait; now accepts stdout/stderr writers.
- `supervisor` — unchanged (per-instance state machine / backoff).
- `manager` — lifecycle + optional log-writer factory option; no logs/metrics imports.
- `logs` — Sink (tee → lumberjack + ring + fanout), Registry. **new**
- `metrics` — gopsutil process-group Sampler. **new**
- `store` — adds the `logs/` dir path.
- `daemon` — owns Registry + Sampler; implements `Logs`; merges cpu/mem into `ProcInfo`.
- `cli` — `logs` command; unwrapped error messages; CPU/MEM columns.

## 13. Next step

Write the M3 implementation plan (writing-plans skill) from this spec, then execute it
TDD on a feature branch cut from `main`.
