# Marshal — M23: Read-only dashboard file browser — Handoff

**Date:** 2026-06-19
**Branch:** `m23-file-browser` (all work done, reviewed, live-demoed; ready to merge `--no-ff` to `main`).
**Read the M22 handoff `2026-06-19-m22-managed-git-credentials.md` first** for the deploy/credential architecture and the dashboard→server→agent command seam this builds on.

---

## TL;DR

A dashboard user can now **browse and view (read-only)** the files of a git-deployed app on its
agent host, confined to that app's clone root (`deployRoot/<app>`). The per-app detail view gained
a **Files card** (shown only when the app is a git deployment) with a folder tree and a
**CodeMirror 6 read-only** viewer with syntax highlighting. Binary and oversize (>1 MiB) files
don't render as text — they degrade to an **honest capped Download** (raw bytes, ≤1 MiB, served as
an `octet-stream` attachment). Path confinement is the security core: no input (app name OR path)
can escape the clone root — `..`, absolute paths, and symlink-escape are all rejected.

Reuses the existing fleet command seam — **no new RPC, no new connection**: two additive
`ControlOp` variants (`list_dir`, `read_file`) + two additive `ControlResult` payloads
(`dir`, `file`). An M21/M22 agent without these ops still works.

Built spec → plan → 6-task subagent-driven TDD (each task reviewed) → whole-branch review (opus,
"merge with fixes") → fixes → live demo. Spec:
`docs/superpowers/specs/2026-06-19-file-browser-design.md`. Plan:
`docs/superpowers/plans/2026-06-19-file-browser.md`.

## What changed this session

- **Proto** (`fleet.proto`): `ListDirRequest{app,path}`, `ReadFileRequest{app,path}`,
  `DirEntry{name,is_dir,size,mod_unix,mode}`, `DirListing{path,entries}`,
  `FileContent{path,content,size,truncated,binary}`. `ControlOp` += `list_dir=7`, `read_file=8`;
  `ControlResult` += `dir=4`, `file=5`. All additive. Regen via `go generate ./internal/pb`.
- **`internal/deploy/browse.go`** (new) — the security-critical core:
  - `confine(root, rel) (string, error)`: `filepath.Clean(filepath.Join(root, rel))` + lexical
    `root+sep` prefix check, then `EvalSymlinks` on both sides and re-check against the **resolved**
    root (defeats symlink-escape). Rejects absolute paths, `..`, and symlinks pointing out. Returns
    **generic** errors (`"not found"`, `"path escapes deploy root"`, `"deploy root unavailable"`) —
    never leaks absolute server paths to the client.
  - `ListDir(root, rel) (*pb.DirListing, error)`: dirs-first then files, each alphabetical.
  - `ReadFile(root, rel) (*pb.FileContent, error)`: rejects dirs; head-only up to
    `maxFileBytes = 1<<20`; binary sniff (NUL in first `sniffBytes = 8<<10`) sets `Binary`;
    `Truncated`/`Size` from true on-disk size. **Always returns the capped raw bytes** (text or
    binary) + the `Binary` flag — the dashboard decides per-consumer whether to ship them.
- **`internal/deploy/deployer.go`**: `Root(name) (string, bool)` — clone dir of a known deployment
  (dir exists under deployRoot); rejects names containing a path separator (no traversal via the
  app name); returns false for unknown/non-deployment apps.
- **`internal/daemon/command.go`**: two new cases after `Redeploy` — resolve `s.deployer.Root(app)`
  → `deploy.ListDir`/`deploy.ReadFile` → `ControlResult{Ok, Dir|File}`. nil deployer → "deploy not
  supported"; unknown app → "not a git deployment".
- **`internal/dashboard/files.go`** (new) — `GET /api/fleet/{agent}/apps/{app}/dir?path=` →
  `dirListingDTO`; `GET .../file?path=` → `fileContentDTO`; both behind `requireSession`. Own JSON
  DTOs (M21 DTO-drop guard). View handler sets DTO `Content=""` when `Binary` (binary bytes never
  shipped as a JSON string). **`?raw=1`** branch streams the raw bytes as an attachment:
  `Content-Type: application/octet-stream`, `Content-Disposition: attachment; filename="<base>"`
  (filename sanitized: `path.Base` + strip `"`, all `<0x20`, `0x7F`, fallback `download` — no header
  injection). Error mapping: transport → 503, op-rejected → 400, success → 200.
- **web** (`web/`): `api.ts` `DirEntry/DirListing/FileContent` types + `listDir/readFile`
  + `fileDownloadURL` (appends `&raw=1`). New **`FileBrowser.tsx`** (breadcrumb, folders-first
  listing, CodeMirror 6 read-only via `@uiw/react-codemirror` + lang packs js/json/python/go,
  binary/oversize → Download, honest "viewing only" banner). `ProcessDetail.tsx` renders the Files
  card between metrics and logs, gated on `p?.source === "git"`. New web deps: `@uiw/react-codemirror`
  + `@codemirror/lang-{javascript,json,python,go}` (bundle +~258 KB gzip). `make ui` rebuilt the
  embedded `internal/dashboard/dist`.

## Key decisions / non-obvious

- **Read-only this milestone**; the next milestone is **commit & push** (edits become real git
  commits pushed to origin so redeploy preserves them). The seam is write-ready: `confine` handles a
  non-existent leaf, and adding a `WriteFileRequest` op is additive.
- **Honest capped download** (chosen by the user over true-full-file or drop-download): serve raw
  bytes up to the *existing* 1 MiB cap as a real attachment — resolves the spec's internal tension
  between "1 MiB read cap" and "download". Binary → "Download" (real bytes); truncated → "Download
  first 1 MiB". No uncapped fleet transfer; no new proto.
- **Card, not tab** — the dashboard has no tab component (stacked-card layout), so the design's
  "Files tab" ships as a card consistent with metrics/logs. Functionally identical.
- **Path confinement is layered**: `Root` rejects app-name separators *before* stat; the ServeMux
  `{app}` segment can't span `/`; `confine` does lexical + symlink-resolved containment. The bug in
  the *plan's own* confine sketch (`Clean("/"+"..") == "/"` passed containment) was caught in Task 2
  and fixed; opus-reviewed all escape vectors as genuinely rejected.

## Live demo result (2026-06-19, scratch `/tmp/marshal-m23-demo`, server `:9000`/`:9001`)

Real fleet (server + agent `dev-1`); deployed `browseapp` from a local git repo (file:// remote)
seeded with a source file, JSON config, README, a binary PNG, and a ~1.43 MiB text file:
- **Listing**: dirs first (`.git`, `config`, `src`), then files alphabetically.
- **Text view**: `src/main.go` → `binary=false`, `truncated=false`, real content; rendered
  read-only in CodeMirror with Go highlighting (screenshot captured).
- **Path escape**: `../../../../etc/passwd`, `..`, `src/../../../etc/passwd` all → **400
  `{"error":"path escapes deploy root"}`** — generic, no server path leaked (the error-wrap fix).
- **Binary** (`logo.png`): JSON view `binary=true`, **content length 0** (bytes not shipped as JSON).
- **Oversize** (`big.txt`, 1.5 MB): `truncated=true`, content capped at exactly `1048576`, true
  `size` reported.
- **Raw download** (`logo.png?raw=1`): `200`, `Content-Disposition: attachment; filename="logo.png"`,
  `Content-Type: application/octet-stream`, body = real PNG bytes (`8950 4e47` = ‰PNG).
- **Gating**: git app `browseapp` exposes `source=git` → Files card shown; command app `demoapp` →
  `source=command` → no card.
- Teardown by data dir only (per convention); the user's standing launchd daemon (pid 79947) was
  preserved; no orphan scratch processes.
- (Aside: the demo app sat in an `errored`/restart loop — its `run.sh` launch behavior, **pre-existing
  M21 deploy/launch territory, not an M23 concern**; the browser works on the clone dir regardless.)

## Known issues / deferred

- **No edit/save** — the planned **commit & push** milestone (key-file lifecycle aside, this is the
  natural next step; the file browser is its read half).
- **404 vs 400**: spec §6 wanted "file not found → 404"; all op-rejections currently map to 400
  (the frontend only special-cases 401, so behavior is unaffected). Either add an `os.IsNotExist`
  branch agent-side or update the spec.
- **`.git/` is listable** (it's part of the clone) — possible later UX nicety to collapse/hide.
- **No directory/zip download, no multi-select, no live refresh** — listing is request-time.
- **CodeMirror grew the JS bundle ~258 KB gzip** — advisory; could code-split later.
- Minor: web `key={e.name}` is per-listing (correct since entries are unique within a dir);
  raw-download filename derives from reflected agent path (sanitized).

## How to build / run / test

```bash
go build -o marshal ./cmd/marshal
go test ./... -race -count=1     # all packages green
gofmt -l . ; go vet ./...        # silent / clean
go generate ./internal/pb        # regenerate proto bindings (protoc + plugins on PATH)
make ui                          # web/ → internal/dashboard/dist (tracked, embedded)
```

Endpoints (behind dashboard session): `GET /api/fleet/{agent}/apps/{app}/dir?path=<rel>`,
`GET /api/fleet/{agent}/apps/{app}/file?path=<rel>[&raw=1]`.

## Concrete next step

1. **Merge `m23-file-browser` to `main`** (`--no-ff`, no remote) via
   `superpowers:finishing-a-development-branch`.
2. Then the **commit & push** milestone (the write half: edit in the browser → stage/commit/push to
   origin with M22-managed credentials → redeploy preserves the edit), or SSH deploy keys (the other
   deferred M22 follow-on).
