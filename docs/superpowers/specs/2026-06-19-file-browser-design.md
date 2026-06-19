# Marshal — M23: Dashboard file browser (read-only) — Design

**Date:** 2026-06-19
**Status:** Approved design; ready for implementation plan.
**Builds on:** M21 (git deploy), M22 (managed git credentials). Read those handoffs for the
deploy architecture and the dashboard → server → agent command seam this reuses.

---

## 1. Goal & scope

Let a dashboard user **browse and view** the files of a **git-deployed app** on its agent host,
read-only, from the web dashboard. This is the natural follow-on to git deploy: once apps are
deployed from git, being able to inspect their on-host files (source, config, build output, logs
next to them) from the dashboard closes the loop for debugging.

**In scope (this milestone):**
- Browse the directory tree of a deployed app, confined to its clone root (`deployRoot/<app>`).
- View a file's contents with syntax highlighting (read-only).
- Download a file (also the fallback for binary / oversize files).

**Explicitly out of scope (deferred):**
- **Editing / saving** files. Deferred to a future **"commit & push"** milestone where edits
  become real git commits pushed to the origin (so redeploy preserves them). The design keeps the
  agent-side seam write-ready so that milestone adds write ops alongside reads, not a rewrite.
- Browsing **non-deployed** processes' working directories or **arbitrary host paths**. A deployed
  app has exactly one well-defined, confinable root; arbitrary paths widen the blast radius and are
  not needed for the inspect-a-deployment use case.

### Key tension acknowledged
A deployed app's directory is a **git clone**, and redeploy does `git fetch` + reset (effectively
`reset --hard`), which **discards local edits**. That is *why* this milestone is read-only: there
is no safe "edit on host" story until edits can be committed back. The UI states this honestly.

## 2. Architecture & topology

Reuses the existing fleet command path with **no new RPC and no new connection**:

```
dashboard (HTTP, requireSession)
  → server  FleetControl(agent_name, ControlOp)        [unary RPC, already exists]
    → agent  Command{request_id, ControlOp}             [server→agent, over Connect stream]
    ← agent  CommandResult{request_id, ControlResult}   [agent→server, correlated by request_id]
  ← server  FleetControlResponse{ControlResult}
← dashboard JSON
```

This is the exact seam M21/M22 used for `DeployRequest`/`RedeployRequest`. File ops are two more
`ControlOp` variants.

## 3. Protocol (additive — M21/M22 agents unaffected)

`proto/marshal/v1/fleet.proto`:

```proto
message ListDirRequest  { string app = 1; string path = 2; } // path: relative to clone root; "" = root
message ReadFileRequest { string app = 1; string path = 2; }

message DirEntry {
  string name     = 1;
  bool   is_dir   = 2;
  int64  size     = 3;
  int64  mod_unix = 4;
  uint32 mode     = 5; // unix perm bits, for display
}
message DirListing  { string path = 1; repeated DirEntry entries = 2; }
message FileContent {
  string path      = 1;
  bytes  content   = 2;
  int64  size      = 3; // true on-disk size (may exceed len(content) when truncated)
  bool   truncated = 4; // content is a head, capped at the read limit
  bool   binary    = 5; // NUL-byte sniff: not safe to render as text
}

// ControlOp oneof gains:
//   ListDirRequest  list_dir  = 7;
//   ReadFileRequest read_file = 8;
// ControlResult gains (additive fields):
//   DirListing  dir  = 4;
//   FileContent file = 5;
```

On success the relevant result field is populated; `ok=false, error=...` on failure (not a
deployment, path escape, not found, etc.). Re-run `make` / protoc to regenerate `internal/pb`.

## 4. Agent side (security-critical)

### 4.1 Resolving app → root
New `Deployer` accessor:
```go
// Root returns the clone directory of a known deployment and true, or ("", false)
// if name is not a git deployment this agent manages.
func (d *Deployer) Root(name string) (string, bool)
```
File ops on an unknown / non-deployed app return `ok=false, error="not a git deployment"`.

### 4.2 Confined path resolution (the unit-tested heart)
A small helper (e.g. `internal/deploy/browse.go` or `internal/filebrowse`) resolves a
caller-supplied relative path against a trusted root and **guarantees the result stays inside the
root**:
1. Reject absolute paths outright.
2. `filepath.Clean` the relative path; reject if it escapes via `..` (resulting path must remain
   under root, e.g. `rel == ".." || strings.HasPrefix(rel, ".."+sep)` → reject).
3. Join under root, then `filepath.EvalSymlinks` the candidate and verify the real path is still
   within the real (symlink-resolved) root — defeats symlink-escape (a symlink pointing outside).
4. For a non-existent leaf (none today; relevant for the future write milestone) resolve the parent
   and re-check.

Returns the absolute, confirmed-inside path or an error. Every file op funnels through this.

### 4.3 Command handlers
`internal/daemon/command.go` gains two cases mirroring the deploy cases:
- `*pb.ControlOp_ListDir`: resolve root → confine path → `os.ReadDir` → map to `DirEntry`s
  (sorted: dirs first, then files, each alphabetical). Stat each entry for size/mode/modtime.
- `*pb.ControlOp_ReadFile`: resolve root → confine path → stat (reject dirs) → read up to the cap,
  sniff for binary, return `FileContent`.

## 5. Security & limits

- **Path confinement** (4.2) — the one thing that must be bulletproof. Adversarial test table:
  `..`, `../../etc/passwd`, absolute paths, a symlink inside the clone pointing outside, `.`-tricks,
  empty path (→ root). Each must be rejected or correctly contained.
- **Read cap** — `maxFileBytes` ≈ 1 MiB. Larger files return `truncated=true` with the head only;
  `size` carries the true size so the UI can show "showing first 1 MiB of N" + Download.
- **Binary detection** — sniff the first N bytes (e.g. 8 KiB) for NUL; if present, `binary=true`,
  content omitted/short, UI shows a placeholder + Download (does not render as text).
- **No writes** this milestone — there is no code path that opens a file for writing.
- `.git/` is listable (it is part of the clone); not special-cased. Noted as a possible later
  nicety (collapse/hide by default).

## 6. Server / dashboard HTTP API

Two endpoints, behind `requireSession`, consistent with existing fleet routes
(`internal/dashboard`):

- `GET /api/fleet/{agent}/apps/{app}/dir?path=<rel>` → `DirListing` as JSON.
- `GET /api/fleet/{agent}/apps/{app}/file?path=<rel>` → `FileContent` as JSON (content
  base64-or-text per JSON marshaling of `bytes`; UI decodes).

Each builds the corresponding `ControlOp` and issues a `FleetControl` to the named agent, then maps
the `ControlResult` to JSON. Error mapping (matching existing handlers):
- unknown app / path escape / not-a-deployment → **400**;
- file not found → **404**;
- agent offline / store disabled → **503**.

New file `internal/dashboard/files.go` (+ `files_test.go`); wired through `handlers.go`/`server.go`
like `credentials.go`. The M21 DTO-drop lesson applies — make sure new fields survive the
server-side view structs.

## 7. Web UI

`web/` (CodeMirror 6 chosen for read-only-now / editable-later, lightweight enough for the embedded
`internal/dashboard/dist` bundle):

- **"Files" tab** on the per-app `ProcessDetail` view, shown **only when the app has a git source**
  (i.e. is a deployment); hidden for plain processes.
- `FileBrowser.tsx`:
  - Breadcrumb of the current path (root = app name).
  - Listing: folders first then files; columns name / size / modified. Click a folder to descend;
    click `..`/breadcrumb to ascend (client-side path math, server re-confines every request).
  - Click a file → right-hand **CodeMirror 6 read-only** pane, language inferred from extension.
  - Binary / oversize → placeholder ("binary file" / "showing first 1 MiB of N") + **Download**.
- A small honest banner: *"Viewing only — edits aren't supported yet; redeploy overwrites local
  changes."*
- `api.ts`: `listDir(agent, app, path)`, `readFile(agent, app, path)`, types `DirEntry`,
  `DirListing`, `FileContent`.

## 8. Testing & demo

- **TDD** per task; `go test ./... -race -count=1` clean; `gofmt`/`go vet` clean.
- Heaviest coverage on **path confinement** (adversarial table) and **binary/oversize** handling.
- Agent handler tests (list/read against a temp clone-like dir), dashboard handler tests (mock
  fleet), proto round-trip.
- **Live demo** per the project convention (scratch dir, server `:9000`/`:9001`):
  1. Deploy a real git app; open its **Files** tab; navigate the tree.
  2. Open a source file → confirm highlighted, read-only.
  3. Attempt a `..` escape via crafted `path` → confirm **rejected** (400, nothing leaked).
  4. Open a binary and an oversize file → confirm degrade-to-Download / truncation banner.
  5. Tear down; confirm no orphan `marshal` processes (only the user's launchd daemon remains).

## 9. Forward-compatibility (the planned commit & push milestone)

This design is deliberately write-ready so option 3 lands cleanly later:
- The confined-path resolver already handles a non-existent leaf (parent-resolution branch).
- Adding write is a new `WriteFileRequest` `ControlOp` + a server endpoint + an editable CodeMirror
  mode — the browse/read surface is untouched.
- The commit step (stage → commit → push with M22-managed credentials) is additive on the agent.

## 10. Deferred / known issues

- No edit/save (the commit & push milestone).
- `.git/` not collapsed/hidden — possible later UX nicety.
- No directory download / zip; no multi-file selection.
- No file watching / live refresh — listing is request-time.
