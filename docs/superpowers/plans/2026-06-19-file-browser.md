# M23 — Dashboard File Browser Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a dashboard user browse and view (read-only) the files of a git-deployed app on its agent host, confined to the app's clone root.

**Architecture:** Two new additive `ControlOp` variants (`ListDirRequest`, `ReadFileRequest`) and two additive `ControlResult` payloads (`DirListing`, `FileContent`) ride the *existing* dashboard → server → agent fleet command seam (the same path M21/M22 used for deploy). The agent resolves an app name to its clone dir via a new `Deployer.Root` accessor, then a confined-path helper in `internal/deploy` guarantees every read stays inside that root. The dashboard exposes two `GET` endpoints; the web UI adds a Files card to the per-app detail view with a CodeMirror 6 read-only viewer.

**Tech Stack:** Go (stdlib only on the agent/server), protobuf (regen via `go generate ./internal/pb`), React + TypeScript + Vite (web), CodeMirror 6 via `@uiw/react-codemirror` (new web dep).

## Global Constraints

- **Language/topology:** agent & server code is Go, stdlib-only (no new Go deps). Reuse the existing fleet command seam — no new RPC, no new connection.
- **Scope:** read-only. No file *writes* anywhere in this milestone. Confined to git-deployed apps' clone roots (`deployRoot/<app>`); never browse non-deployed processes or arbitrary host paths.
- **Read limits:** read cap `maxFileBytes = 1 << 20` (1 MiB); binary sniff over first `sniffBytes = 8 << 10` (8 KiB) for a NUL byte.
- **Proto additions are additive only:** `ControlOp` gains `list_dir = 7`, `read_file = 8`; `ControlResult` gains `dir = 4`, `file = 5`. Do not renumber existing fields.
- **TDD:** failing test first. `go test ./... -race -count=1` must pass; `gofmt -l .` silent; `go vet ./...` clean before finishing.
- **Git:** work on branch `m23-file-browser` (not `main`). Commit messages use imperative subject + trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- **Web is build-gated, not unit-tested** (the repo's `web/` has no test harness): the gate is `cd web && npx tsc -b` (typecheck) + `make ui` (build into the embedded, tracked `internal/dashboard/dist`).
- **DTO-drop guard (M21 lesson):** the dashboard serializes its own JSON DTO structs, never raw `pb` messages — make sure every new field survives into the DTO.

---

## Task 0: Branch setup

- [ ] **Step 1: Create the feature branch**

```bash
cd "/Users/sebastiankuprat/process manager"
git checkout -b m23-file-browser
```

- [ ] **Step 2: Verify baseline is green**

Run: `go build ./... && go test ./... -count=1`
Expected: builds, all packages PASS (baseline before any change).

---

## Task 1: Proto — file-op messages, ControlOp/ControlResult fields, regen

**Files:**
- Modify: `proto/marshal/v1/fleet.proto`
- Regenerate: `internal/pb/fleet.pb.go` (via `go generate ./internal/pb`)

**Interfaces:**
- Produces (Go types after regen): `pb.ListDirRequest{App, Path string}`, `pb.ReadFileRequest{App, Path string}`, `pb.DirEntry{Name string; IsDir bool; Size, ModUnix int64; Mode uint32}`, `pb.DirListing{Path string; Entries []*DirEntry}`, `pb.FileContent{Path string; Content []byte; Size int64; Truncated, Binary bool}`. New oneof wrappers `pb.ControlOp_ListDir{ListDir *ListDirRequest}`, `pb.ControlOp_ReadFile{ReadFile *ReadFileRequest}`. New result fields accessible via `res.GetDir()`, `res.GetFile()`.

- [ ] **Step 1: Add the messages and fields to fleet.proto**

In `proto/marshal/v1/fleet.proto`, add these messages near `GitCredential` (before `ControlOp`):

```proto
// M23 — read-only file browser for deployed apps.
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
  int64  size      = 3; // true on-disk size (may exceed len(content) when truncated/binary)
  bool   truncated = 4; // content is a head, capped at maxFileBytes
  bool   binary    = 5; // NUL-byte sniff: not rendered as text
}
```

In the existing `ControlOp` oneof, add after `redeploy = 6;`:

```proto
    ListDirRequest  list_dir  = 7; // M23
    ReadFileRequest read_file = 8; // M23
```

In the existing `ControlResult` message, add after `repeated ProcInfo procs = 3;`:

```proto
  DirListing  dir  = 4; // M23, set on list_dir success
  FileContent file = 5; // M23, set on read_file success
```

- [ ] **Step 2: Regenerate Go bindings**

Run: `cd "/Users/sebastiankuprat/process manager" && go generate ./internal/pb`
Expected: no output, `internal/pb/fleet.pb.go` updated (git diff shows the new types).

- [ ] **Step 3: Verify it compiles**

Run: `go build ./...`
Expected: builds clean (the new types exist; nothing consumes them yet).

- [ ] **Step 4: Commit**

```bash
git add proto/marshal/v1/fleet.proto internal/pb/fleet.pb.go
git commit -m "feat(proto): add list_dir/read_file ops and dir/file results (M23)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Confined path resolver (`internal/deploy/browse.go`)

This is the security-critical heart. It must make path escape impossible.

**Files:**
- Create: `internal/deploy/browse.go`
- Test: `internal/deploy/browse_test.go`

**Interfaces:**
- Produces: `func confine(root, rel string) (string, error)` — returns the absolute, symlink-resolved, confirmed-inside-root path, or an error if `rel` is absolute / escapes via `..` / symlinks out / does not exist.

- [ ] **Step 1: Write the failing test**

Create `internal/deploy/browse_test.go`:

```go
package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfine(t *testing.T) {
	root := t.TempDir()
	// Build a small tree: root/sub/file.txt
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "file.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A secret outside root, and a symlink inside root pointing at it.
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	ok := []struct{ rel, wantSuffix string }{
		{"", ""},                         // root itself
		{".", ""},                        // root itself
		{"sub", "sub"},                   // a dir
		{"sub/file.txt", "sub/file.txt"}, // a file
	}
	for _, c := range ok {
		got, err := confine(root, c.rel)
		if err != nil {
			t.Errorf("confine(%q) unexpected error: %v", c.rel, err)
			continue
		}
		if c.wantSuffix != "" && !strings.HasSuffix(got, c.wantSuffix) {
			t.Errorf("confine(%q) = %q, want suffix %q", c.rel, got, c.wantSuffix)
		}
	}

	bad := []string{
		"..",
		"../../etc/passwd",
		"sub/../../escapeout",
		"/etc/passwd",       // absolute
		"escape",            // symlink pointing outside root
		"escape/anything",   // path through the escaping symlink
	}
	for _, rel := range bad {
		if got, err := confine(root, rel); err == nil {
			t.Errorf("confine(%q) = %q, want error (escape must be rejected)", rel, got)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/deploy/ -run TestConfine -v`
Expected: FAIL — `undefined: confine`.

- [ ] **Step 3: Implement `confine`**

Create `internal/deploy/browse.go`:

```go
package deploy

import (
	"fmt"
	"path/filepath"
	"strings"
)

// confine resolves a caller-supplied relative path against a trusted root and
// guarantees the result stays inside root. It rejects absolute paths and any
// path that escapes via "..", and resolves symlinks so a symlink inside the
// tree cannot point outside it. Returns the absolute, symlink-resolved path.
func confine(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute paths not allowed")
	}
	// Force-root then Clean: any ".." that would escape "/" is collapsed away,
	// so the result is always lexically under root after the Join.
	clean := filepath.Clean("/" + rel)
	full := filepath.Join(root, clean)

	// Defeat symlink escape: resolve symlinks on both sides and re-check
	// containment against the *real* root.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	realFull, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", err // includes "does not exist"
	}
	if realFull != realRoot && !strings.HasPrefix(realFull, realRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes deploy root")
	}
	return realFull, nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/deploy/ -run TestConfine -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/deploy/browse.go internal/deploy/browse_test.go
git commit -m "feat(deploy): confined path resolver for file browsing (M23)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: `ListDir` and `ReadFile` (cap + binary sniff)

**Files:**
- Modify: `internal/deploy/browse.go`
- Test: `internal/deploy/browse_test.go`

**Interfaces:**
- Consumes: `confine` (Task 2); `pb` (already imported by the deploy package).
- Produces: `func ListDir(root, rel string) (*pb.DirListing, error)` — dirs first then files, each alphabetical. `func ReadFile(root, rel string) (*pb.FileContent, error)` — refuses directories; head-only up to `maxFileBytes`; `Binary=true` with empty content when a NUL byte is in the first `sniffBytes`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/deploy/browse_test.go`:

```go
func TestListDirOrdersDirsFirst(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "zdir"))
	mustMkdir(t, filepath.Join(root, "adir"))
	mustWrite(t, filepath.Join(root, "b.txt"), "x")
	mustWrite(t, filepath.Join(root, "a.txt"), "x")

	l, err := ListDir(root, "")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range l.GetEntries() {
		names = append(names, e.GetName())
	}
	want := []string{"adir", "zdir", "a.txt", "b.txt"} // dirs first (alpha), then files (alpha)
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", names, want)
	}
	if !l.GetEntries()[0].GetIsDir() {
		t.Errorf("first entry should be a dir")
	}
}

func TestReadFileText(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.txt"), "hello")
	fc, err := ReadFile(root, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(fc.GetContent()) != "hello" || fc.GetBinary() || fc.GetTruncated() {
		t.Errorf("got %+v, want content=hello binary=false truncated=false", fc)
	}
	if fc.GetSize() != 5 {
		t.Errorf("size = %d, want 5", fc.GetSize())
	}
}

func TestReadFileBinary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "b.bin"), []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}
	fc, err := ReadFile(root, "b.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !fc.GetBinary() || len(fc.GetContent()) != 0 {
		t.Errorf("got binary=%v len(content)=%d, want binary=true content empty", fc.GetBinary(), len(fc.GetContent()))
	}
}

func TestReadFileTruncates(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("a", maxFileBytes+100)
	mustWrite(t, filepath.Join(root, "big.txt"), big)
	fc, err := ReadFile(root, "big.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !fc.GetTruncated() || len(fc.GetContent()) != maxFileBytes {
		t.Errorf("got truncated=%v len=%d, want truncated=true len=%d", fc.GetTruncated(), len(fc.GetContent()), maxFileBytes)
	}
	if fc.GetSize() != int64(maxFileBytes+100) {
		t.Errorf("size = %d, want %d", fc.GetSize(), maxFileBytes+100)
	}
}

func TestReadFileRejectsDir(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "d"))
	if _, err := ReadFile(root, "d"); err == nil {
		t.Errorf("ReadFile on a directory should error")
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}
func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/deploy/ -run 'TestListDir|TestReadFile' -v`
Expected: FAIL — `undefined: ListDir` / `undefined: ReadFile` / `undefined: maxFileBytes`.

- [ ] **Step 3: Implement ListDir/ReadFile**

Append to `internal/deploy/browse.go` (add imports `bytes`, `io`, `os`, `sort` to the existing import block):

```go
const (
	maxFileBytes = 1 << 20 // 1 MiB read cap
	sniffBytes   = 8 << 10 // bytes inspected for binary detection
)

// ListDir returns the entries of rel under root, dirs first then files, each
// group alphabetical. rel="" lists the root.
func ListDir(root, rel string) (*pb.DirListing, error) {
	full, err := confine(root, rel)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(full)
	if err != nil {
		return nil, err
	}
	out := make([]*pb.DirEntry, 0, len(ents))
	for _, e := range ents {
		info, err := e.Info()
		if err != nil {
			continue // raced away between ReadDir and Info; skip
		}
		out = append(out, &pb.DirEntry{
			Name:    e.Name(),
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			ModUnix: info.ModTime().Unix(),
			Mode:    uint32(info.Mode().Perm()),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir // dirs first
		}
		return out[i].Name < out[j].Name
	})
	return &pb.DirListing{Path: rel, Entries: out}, nil
}

// ReadFile returns the head (up to maxFileBytes) of the file at rel under root.
// Directories are rejected. Binary files (NUL byte in the first sniffBytes) are
// flagged and their content is omitted.
func ReadFile(root, rel string) (*pb.FileContent, error) {
	full, err := confine(root, rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory")
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	buf := make([]byte, maxFileBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	buf = buf[:n]

	sniff := n
	if sniff > sniffBytes {
		sniff = sniffBytes
	}
	binary := bytes.IndexByte(buf[:sniff], 0) >= 0

	content := buf
	if binary {
		content = nil
	}
	return &pb.FileContent{
		Path:      rel,
		Content:   content,
		Size:      info.Size(),
		Truncated: info.Size() > int64(n),
		Binary:    binary,
	}, nil
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/deploy/ -run 'TestConfine|TestListDir|TestReadFile' -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/deploy/browse.go internal/deploy/browse_test.go
git commit -m "feat(deploy): ListDir/ReadFile with size cap and binary sniff (M23)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: `Deployer.Root` accessor + agent command wiring

**Files:**
- Modify: `internal/deploy/deployer.go` (add `Root`; add `strings`/`os` imports if missing — `os` already imported)
- Test: `internal/deploy/deployer_test.go` (append)
- Modify: `internal/daemon/command.go` (two new switch cases)

**Interfaces:**
- Consumes: `d.dir(name)` (existing private), `deploy.ListDir`/`deploy.ReadFile` (Task 3), `s.deployer` (existing field on `daemon.Server`).
- Produces: `func (d *Deployer) Root(name string) (string, bool)` — clone dir for a known deployment (dir exists), else `("", false)`; rejects names containing a path separator.

- [ ] **Step 1: Write the failing test for Root**

Append to `internal/deploy/deployer_test.go`:

```go
func TestDeployerRoot(t *testing.T) {
	deployRoot := t.TempDir()
	d := New(nil, nil, deployRoot)

	// Unknown app: no dir on disk.
	if _, ok := d.Root("ghost"); ok {
		t.Errorf("Root(ghost) ok=true, want false")
	}
	// Make a deployment dir.
	if err := os.MkdirAll(filepath.Join(deployRoot, "app1"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := d.Root("app1")
	if !ok || got != filepath.Join(deployRoot, "app1") {
		t.Errorf("Root(app1) = (%q,%v), want (%q,true)", got, ok, filepath.Join(deployRoot, "app1"))
	}
	// Name with a separator must be rejected (no traversal via app name).
	if _, ok := d.Root("../etc"); ok {
		t.Errorf("Root(../etc) ok=true, want false")
	}
}
```

(If `os`/`filepath` aren't already imported in `deployer_test.go`, add them.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/deploy/ -run TestDeployerRoot -v`
Expected: FAIL — `d.Root undefined`.

- [ ] **Step 3: Implement Root**

In `internal/deploy/deployer.go`, add (near `dir` / `Forget`); ensure `strings` is imported:

```go
// Root returns the clone directory of a known deployment and true. A deployment
// is "known" when its dir exists under deployRoot. Names containing a path
// separator are rejected outright. Returns ("", false) otherwise.
func (d *Deployer) Root(name string) (string, bool) {
	if name == "" || strings.ContainsRune(name, filepath.Separator) {
		return "", false
	}
	dir := d.dir(name)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", false
	}
	return dir, true
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/deploy/ -run TestDeployerRoot -v`
Expected: PASS.

- [ ] **Step 5: Wire the agent command cases**

In `internal/daemon/command.go`, add two cases to the `switch v := op.GetOp().(type)` block, after the `*pb.ControlOp_Redeploy` case:

```go
	case *pb.ControlOp_ListDir:
		if s.deployer == nil {
			return &pb.ControlResult{Ok: false, Error: "deploy not supported"}
		}
		root, ok := s.deployer.Root(v.ListDir.GetApp())
		if !ok {
			return &pb.ControlResult{Ok: false, Error: "not a git deployment"}
		}
		listing, lerr := deploy.ListDir(root, v.ListDir.GetPath())
		if lerr != nil {
			return &pb.ControlResult{Ok: false, Error: lerr.Error()}
		}
		return &pb.ControlResult{Ok: true, Dir: listing}

	case *pb.ControlOp_ReadFile:
		if s.deployer == nil {
			return &pb.ControlResult{Ok: false, Error: "deploy not supported"}
		}
		root, ok := s.deployer.Root(v.ReadFile.GetApp())
		if !ok {
			return &pb.ControlResult{Ok: false, Error: "not a git deployment"}
		}
		fc, ferr := deploy.ReadFile(root, v.ReadFile.GetPath())
		if ferr != nil {
			return &pb.ControlResult{Ok: false, Error: ferr.Error()}
		}
		return &pb.ControlResult{Ok: true, File: fc}
```

- [ ] **Step 6: Write a command-dispatch test**

Append to `internal/daemon/command_test.go` (use the existing test patterns in that file for constructing a `*Server` with a deployer; mirror how the Deploy/Redeploy cases are tested — if those tests build a `Server` with a real `deploy.New(...)` over a temp deployRoot, do the same here):

```go
func TestHandleFleetCommand_ListDirAndReadFile(t *testing.T) {
	deployRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(deployRoot, "app1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deployRoot, "app1", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{deployer: deploy.New(nil, nil, deployRoot)} // adapt to the actual Server zero-value/test ctor used by sibling tests

	// list_dir
	listOp := &pb.ControlOp{Op: &pb.ControlOp_ListDir{ListDir: &pb.ListDirRequest{App: "app1", Path: ""}}}
	res := s.handleFleetCommand(&pb.Command{Op: listOp})
	if !res.GetOk() || len(res.GetDir().GetEntries()) != 1 || res.GetDir().GetEntries()[0].GetName() != "main.go" {
		t.Fatalf("list_dir: ok=%v entries=%v", res.GetOk(), res.GetDir().GetEntries())
	}

	// read_file
	readOp := &pb.ControlOp{Op: &pb.ControlOp_ReadFile{ReadFile: &pb.ReadFileRequest{App: "app1", Path: "main.go"}}}
	res = s.handleFleetCommand(&pb.Command{Op: readOp})
	if !res.GetOk() || string(res.GetFile().GetContent()) != "package main" {
		t.Fatalf("read_file: ok=%v content=%q", res.GetOk(), res.GetFile().GetContent())
	}

	// unknown app
	badOp := &pb.ControlOp{Op: &pb.ControlOp_ListDir{ListDir: &pb.ListDirRequest{App: "ghost", Path: ""}}}
	if res := s.handleFleetCommand(&pb.Command{Op: badOp}); res.GetOk() {
		t.Fatalf("list_dir on unknown app should fail")
	}

	// path escape
	escOp := &pb.ControlOp{Op: &pb.ControlOp_ReadFile{ReadFile: &pb.ReadFileRequest{App: "app1", Path: "../../etc/passwd"}}}
	if res := s.handleFleetCommand(&pb.Command{Op: escOp}); res.GetOk() {
		t.Fatalf("read_file escape should fail")
	}
}
```

NOTE for the implementer: construct `*Server` exactly the way the existing `TestHandleFleetCommand*` tests do (the zero-value `&Server{deployer: ...}` above is a sketch). If those tests use a helper/ctor, reuse it and only set the deployer. Add `os`/`filepath` imports if missing.

- [ ] **Step 7: Run to verify pass**

Run: `go test ./internal/deploy/ ./internal/daemon/ -count=1`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/deploy/deployer.go internal/deploy/deployer_test.go internal/daemon/command.go internal/daemon/command_test.go
git commit -m "feat(daemon): handle list_dir/read_file via Deployer.Root (M23)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Dashboard HTTP endpoints (`internal/dashboard/files.go`)

**Files:**
- Create: `internal/dashboard/files.go`
- Create: `internal/dashboard/files_test.go`
- Modify: `internal/dashboard/handlers.go` (register two routes)

**Interfaces:**
- Consumes: `h.controller.Control(ctx, agent, op) (*pb.ControlResult, error)`, `writeJSON`, `controlTimeout`, `h.requireSession` (all existing).
- Produces: `GET /api/fleet/{agent}/apps/{app}/dir?path=<rel>` → `dirListingDTO` JSON; `GET /api/fleet/{agent}/apps/{app}/file?path=<rel>` → `fileContentDTO` JSON. Error mapping: bad input / op-rejected → 400; transport error → 503.

- [ ] **Step 1: Write the failing test**

Create `internal/dashboard/files_test.go`. Mirror how `apps_test.go` builds a `*handler` with a fake `FleetController`; reuse that fake if one exists, otherwise define a local one:

```go
package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"marshal/internal/pb"
)

type fakeFilesController struct {
	res *pb.ControlResult
	err error
	gotOp *pb.ControlOp
}

func (f *fakeFilesController) Control(_ context.Context, _ string, op *pb.ControlOp) (*pb.ControlResult, error) {
	f.gotOp = op
	return f.res, f.err
}

func newFilesTestHandler(c FleetController) *handler {
	// Adapt to the real newHandler signature; pass nils for unused deps as
	// sibling tests in apps_test.go do, and a permissive/auth stub so
	// requireSession passes (reuse the same approach apps_test.go uses).
	return newTestHandlerWithController(c) // <- if apps_test.go has such a helper, use it; else inline newHandler(...) with the same args apps_test.go passes
}

func TestListDirEndpoint(t *testing.T) {
	c := &fakeFilesController{res: &pb.ControlResult{
		Ok: true,
		Dir: &pb.DirListing{Path: "", Entries: []*pb.DirEntry{
			{Name: "main.go", IsDir: false, Size: 12, Mode: 0o644},
		}},
	}}
	h := newFilesTestHandler(c)

	req := httptest.NewRequest(http.MethodGet, "/api/fleet/dev-1/apps/app1/dir?path=", nil)
	// attach an authenticated session cookie the same way apps_test.go does
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, withAuthCookie(req, h)) // reuse the test's auth helper

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var got dirListingDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Name != "main.go" {
		t.Fatalf("got %+v", got)
	}
	// Verify the op carried the right app/path.
	ld := c.gotOp.GetListDir()
	if ld.GetApp() != "app1" || ld.GetPath() != "" {
		t.Fatalf("op app/path = %q/%q", ld.GetApp(), ld.GetPath())
	}
}

func TestReadFileEndpoint_OpRejected(t *testing.T) {
	c := &fakeFilesController{res: &pb.ControlResult{Ok: false, Error: "path escapes deploy root"}}
	h := newFilesTestHandler(c)
	req := httptest.NewRequest(http.MethodGet, "/api/fleet/dev-1/apps/app1/file?path=../../etc/passwd", nil)
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, withAuthCookie(req, h))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
```

IMPORTANT for the implementer: open `internal/dashboard/apps_test.go` first and copy its exact pattern for (a) constructing the handler via `newHandler(...)` and (b) producing an authenticated request (session cookie / auth stub). Replace the `newTestHandlerWithController` / `withAuthCookie` sketches above with whatever apps_test.go actually uses. Do not invent a new auth path.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/dashboard/ -run 'TestListDirEndpoint|TestReadFileEndpoint' -v`
Expected: FAIL — `dirListingDTO` / handlers undefined.

- [ ] **Step 3: Implement files.go**

Create `internal/dashboard/files.go`:

```go
package dashboard

import (
	"context"
	"net/http"

	"marshal/internal/pb"
)

// JSON DTOs — the dashboard never serializes raw pb messages (M21 lesson).
type dirEntryDTO struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModUnix int64  `json:"mod_unix"`
	Mode    uint32 `json:"mode"`
}
type dirListingDTO struct {
	Path    string        `json:"path"`
	Entries []dirEntryDTO `json:"entries"`
}
type fileContentDTO struct {
	Path      string `json:"path"`
	Content   string `json:"content"` // text; empty when binary
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
}

// listDirFiles serves GET /api/fleet/{agent}/apps/{app}/dir?path=<rel>.
func (h *handler) listDirFiles(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("agent")
	app := r.PathValue("app")
	if agent == "" || app == "" {
		http.Error(w, "agent and app required", http.StatusBadRequest)
		return
	}
	op := &pb.ControlOp{Op: &pb.ControlOp_ListDir{ListDir: &pb.ListDirRequest{
		App: app, Path: r.URL.Query().Get("path"),
	}}}
	res, ok := h.fileControl(w, r, agent, op)
	if !ok {
		return
	}
	d := res.GetDir()
	out := dirListingDTO{Path: d.GetPath()}
	for _, e := range d.GetEntries() {
		out.Entries = append(out.Entries, dirEntryDTO{
			Name: e.GetName(), IsDir: e.GetIsDir(), Size: e.GetSize(),
			ModUnix: e.GetModUnix(), Mode: e.GetMode(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// readFileFiles serves GET /api/fleet/{agent}/apps/{app}/file?path=<rel>.
func (h *handler) readFileFiles(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("agent")
	app := r.PathValue("app")
	if agent == "" || app == "" {
		http.Error(w, "agent and app required", http.StatusBadRequest)
		return
	}
	op := &pb.ControlOp{Op: &pb.ControlOp_ReadFile{ReadFile: &pb.ReadFileRequest{
		App: app, Path: r.URL.Query().Get("path"),
	}}}
	res, ok := h.fileControl(w, r, agent, op)
	if !ok {
		return
	}
	f := res.GetFile()
	writeJSON(w, http.StatusOK, fileContentDTO{
		Path: f.GetPath(), Content: string(f.GetContent()), Size: f.GetSize(),
		Truncated: f.GetTruncated(), Binary: f.GetBinary(),
	})
}

// fileControl dispatches op to the agent and handles the shared error mapping.
// Returns (result, true) only when the agent executed the op successfully;
// otherwise it has already written the response and returns (_, false).
func (h *handler) fileControl(w http.ResponseWriter, r *http.Request, agent string, op *pb.ControlOp) (*pb.ControlResult, bool) {
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()
	res, err := h.controller.Control(ctx, agent, op)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return nil, false
	}
	if !res.GetOk() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": res.GetError()})
		return nil, false
	}
	return res, true
}
```

- [ ] **Step 4: Register the routes**

In `internal/dashboard/handlers.go`, add after the `/api/credentials` routes:

```go
	mux.HandleFunc("GET /api/fleet/{agent}/apps/{app}/dir", h.requireSession(h.listDirFiles))
	mux.HandleFunc("GET /api/fleet/{agent}/apps/{app}/file", h.requireSession(h.readFileFiles))
```

- [ ] **Step 5: Run to verify pass**

Run: `go test ./internal/dashboard/ -count=1`
Expected: PASS (the new tests + existing suite).

- [ ] **Step 6: Commit**

```bash
git add internal/dashboard/files.go internal/dashboard/files_test.go internal/dashboard/handlers.go
git commit -m "feat(dashboard): file browser endpoints for deployed apps (M23)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: Web — api client, FileBrowser component, ProcessDetail wiring

No unit tests (web has no harness). Gate: `npx tsc -b` typecheck + `make ui` build.

**Files:**
- Modify: `web/package.json` (add CodeMirror deps)
- Modify: `web/src/api.ts` (types + `listDir`/`readFile`)
- Create: `web/src/FileBrowser.tsx`
- Modify: `web/src/ProcessDetail.tsx` (render Files card when `source === "git"`)

**Interfaces:**
- Consumes: `/api/fleet/{agent}/apps/{app}/dir` and `/file` (Task 5); `Proc.source` (already `"command" | "git"`).
- Produces: `listDir(agent, app, path): Promise<DirListing>`, `readFile(agent, app, path): Promise<FileContent>`; `<FileBrowser agent app />`.

- [ ] **Step 1: Add CodeMirror dependencies**

Run:
```bash
cd "/Users/sebastiankuprat/process manager/web"
npm install @uiw/react-codemirror @codemirror/lang-javascript @codemirror/lang-json @codemirror/lang-python @codemirror/lang-go
```
Expected: `package.json`/`package-lock.json` updated; `node_modules` populated.

- [ ] **Step 2: Add API types and functions**

Append to `web/src/api.ts`:

```ts
export type DirEntry = { name: string; is_dir: boolean; size: number; mod_unix: number; mode: number };
export type DirListing = { path: string; entries: DirEntry[] };
export type FileContent = { path: string; content: string; size: number; truncated: boolean; binary: boolean };

export async function listDir(agent: string, app: string, path: string): Promise<DirListing> {
  const q = new URLSearchParams({ path });
  const r = await fetch(`/api/fleet/${encodeURIComponent(agent)}/apps/${encodeURIComponent(app)}/dir?${q}`);
  if (r.status === 401) throw new Error("unauthorized");
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || `dir failed (${r.status})`);
  return r.json();
}

export async function readFile(agent: string, app: string, path: string): Promise<FileContent> {
  const q = new URLSearchParams({ path });
  const r = await fetch(`/api/fleet/${encodeURIComponent(agent)}/apps/${encodeURIComponent(app)}/file?${q}`);
  if (r.status === 401) throw new Error("unauthorized");
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || `file failed (${r.status})`);
  return r.json();
}

export function fileDownloadURL(agent: string, app: string, path: string): string {
  const q = new URLSearchParams({ path });
  return `/api/fleet/${encodeURIComponent(agent)}/apps/${encodeURIComponent(app)}/file?${q}`;
}
```

- [ ] **Step 3: Create FileBrowser.tsx**

Create `web/src/FileBrowser.tsx`:

```tsx
import { useEffect, useState } from "react";
import CodeMirror from "@uiw/react-codemirror";
import { javascript } from "@codemirror/lang-javascript";
import { json } from "@codemirror/lang-json";
import { python } from "@codemirror/lang-python";
import { go } from "@codemirror/lang-go";
import { listDir, readFile, fileDownloadURL, type DirEntry, type FileContent } from "./api";

function langFor(name: string) {
  const ext = name.split(".").pop()?.toLowerCase();
  switch (ext) {
    case "ts": case "tsx": case "js": case "jsx": return [javascript({ jsx: true, typescript: true })];
    case "json": return [json()];
    case "py": return [python()];
    case "go": return [go()];
    default: return [];
  }
}

function joinPath(dir: string, name: string) { return dir ? `${dir}/${name}` : name; }
function parentPath(p: string) { const i = p.lastIndexOf("/"); return i < 0 ? "" : p.slice(0, i); }

export function FileBrowser({ agent, app }: { agent: string; app: string }) {
  const [path, setPath] = useState("");
  const [entries, setEntries] = useState<DirEntry[]>([]);
  const [open, setOpen] = useState<FileContent | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let stop = false;
    setErr(null);
    listDir(agent, app, path)
      .then((l) => { if (!stop) setEntries(l.entries); })
      .catch((e) => { if (!stop) setErr(String(e.message || e)); });
    return () => { stop = true; };
  }, [agent, app, path]);

  async function onEntry(e: DirEntry) {
    if (e.is_dir) { setOpen(null); setPath(joinPath(path, e.name)); return; }
    setErr(null);
    try { setOpen(await readFile(agent, app, joinPath(path, e.name))); }
    catch (e2: any) { setErr(String(e2.message || e2)); }
  }

  const crumbs = path ? path.split("/") : [];
  return (
    <div className="filebrowser">
      <div className="fb-note">Viewing only — edits aren't supported yet; redeploy overwrites local changes.</div>
      <div className="crumb fb-crumb">
        <a href="#" onClick={(ev) => { ev.preventDefault(); setOpen(null); setPath(""); }}>{app}</a>
        {crumbs.map((c, i) => {
          const sub = crumbs.slice(0, i + 1).join("/");
          return <span key={sub}><span className="sep">/</span>
            <a href="#" onClick={(ev) => { ev.preventDefault(); setOpen(null); setPath(sub); }}>{c}</a></span>;
        })}
      </div>
      {err && <div className="fb-err">{err}</div>}
      <div className="fb-body">
        <ul className="fb-list">
          {path !== "" && (
            <li className="fb-row" onClick={() => { setOpen(null); setPath(parentPath(path)); }}>
              <span className="fb-name">../</span></li>
          )}
          {entries.map((e) => (
            <li key={e.name} className="fb-row" onClick={() => onEntry(e)}>
              <span className="fb-name">{e.is_dir ? "📁 " : "📄 "}{e.name}</span>
              <span className="fb-size">{e.is_dir ? "" : `${e.size} B`}</span>
            </li>
          ))}
        </ul>
        <div className="fb-view">
          {!open && <div className="fb-empty">Select a file to view.</div>}
          {open && open.binary && (
            <div className="fb-empty">
              Binary file ({open.size} B). <a href={fileDownloadURL(agent, app, open.path)} download>Download</a>
            </div>
          )}
          {open && !open.binary && (
            <>
              {open.truncated && <div className="fb-note">Showing first 1 MiB of {open.size} B. <a href={fileDownloadURL(agent, app, open.path)} download>Download full</a></div>}
              <CodeMirror value={open.content} editable={false} readOnly extensions={langFor(open.path)} theme="dark" />
            </>
          )}
        </div>
      </div>
    </div>
  );
}
```

- [ ] **Step 4: Wire into ProcessDetail.tsx**

In `web/src/ProcessDetail.tsx`, add the import near the top:

```tsx
import { FileBrowser } from "./FileBrowser";
```

Then add a Files card. Insert it after the metrics card and before the logs card (find the `<div className="card">` that contains `log-controls` and place this just above it):

```tsx
      {p?.source === "git" && (
        <div className="card">
          <div className="card-head"><span className="lbl">files</span></div>
          <FileBrowser agent={agent} app={proc} />
        </div>
      )}
```

(NOTE: the design called this a "tab"; the codebase uses a stacked-card layout with no tab component, so it ships as a card — consistent with metrics/logs.)

- [ ] **Step 5: Add minimal styles**

Append to `web/src/styles.css`:

```css
.filebrowser { display: flex; flex-direction: column; gap: 8px; }
.fb-note { font-size: 11px; color: var(--dim); }
.fb-err { color: var(--danger, #e66); font-size: 12px; }
.fb-crumb a { cursor: pointer; }
.fb-body { display: grid; grid-template-columns: 260px 1fr; gap: 12px; min-height: 240px; }
.fb-list { list-style: none; margin: 0; padding: 0; max-height: 420px; overflow: auto; border-right: 1px solid var(--line, #333); }
.fb-row { display: flex; justify-content: space-between; gap: 8px; padding: 4px 8px; cursor: pointer; font-size: 13px; }
.fb-row:hover { background: var(--hover, #222); }
.fb-size { color: var(--dim); font-variant-numeric: tabular-nums; }
.fb-view { overflow: auto; }
.fb-empty { color: var(--dim); font-size: 13px; padding: 12px; }
```

- [ ] **Step 6: Typecheck**

Run: `cd "/Users/sebastiankuprat/process manager/web" && npx tsc -b`
Expected: no type errors. (Fix any signature mismatches against api.ts.)

- [ ] **Step 7: Build the embedded dist**

Run: `cd "/Users/sebastiankuprat/process manager" && make ui`
Expected: `internal/dashboard/dist` regenerated (tracked files updated).

- [ ] **Step 8: Verify the Go binary still builds with the new dist**

Run: `go build ./...`
Expected: builds clean.

- [ ] **Step 9: Commit**

```bash
git add web/package.json web/package-lock.json web/src/api.ts web/src/FileBrowser.tsx web/src/ProcessDetail.tsx web/src/styles.css internal/dashboard/dist
git commit -m "feat(web): read-only file browser card for deployed apps (M23)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: Full verification, live demo, handoff

**Files:**
- Create: `docs/handoffs/2026-06-19-m23-file-browser.md`

- [ ] **Step 1: Full test + lint sweep**

Run: `cd "/Users/sebastiankuprat/process manager" && go test ./... -race -count=1 && gofmt -l . && go vet ./...`
Expected: all packages PASS; `gofmt -l .` prints nothing; `go vet` clean.

- [ ] **Step 2: Live demo (per project convention)**

Follow the CLAUDE.md live-demo + memory conventions: scratch `XDG_DATA_HOME=/tmp/marshal-m23-demo`, server on `:9000`/`:9001`, set password + rotate enroll token while the server is **down**, then start server, enroll an agent with `marshal start` (not `run`), deploy a real git app, and:
  1. Open the app's detail view → confirm the **files** card appears (and does NOT appear for a plain command process).
  2. Browse the tree; open a source file → confirm read-only CodeMirror render with highlighting.
  3. `curl` the `/file` endpoint with `path=../../etc/passwd` (and `..`) → confirm **400** and no leak.
  4. Open a binary (e.g. a compiled artifact) and a >1 MiB file → confirm degrade-to-Download / truncation banner.
  5. Tear everything down (stop app + agent daemon by data dir + server, remove scratch dir). Run `pgrep -fl marshal` → confirm only the user's launchd daemon remains.

Record observations for the handoff.

- [ ] **Step 3: Write the handoff**

Create `docs/handoffs/2026-06-19-m23-file-browser.md` covering: current state (branch `m23-file-browser`, all green, demoed), what changed this session and why (the proto additions, the confined-path resolver as the security core, the card-not-tab UI decision, CodeMirror dep), how to build/run/test, deferred items (no edit/save → the planned commit-&-push milestone; `.git/` not collapsed; no zip/multi-select; no live refresh), and the concrete next step (merge `--no-ff` to `main` via `finishing-a-development-branch`, then the commit-&-push milestone).

- [ ] **Step 4: Commit the handoff**

```bash
git add docs/handoffs/2026-06-19-m23-file-browser.md
git commit -m "docs: M23 handoff — read-only dashboard file browser

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 5: Finish the branch**

Invoke `superpowers:finishing-a-development-branch` to choose the integration path (expected: `git merge --no-ff m23-file-browser` into `main`, no remote).

---

## Self-Review

**Spec coverage:**
- §1 scope (read-only, deployed apps only) → Tasks 2–6 (confinement, Root gating to deployments, `source === "git"` UI gate). ✓
- §3 protocol (additive fields) → Task 1. ✓
- §4 agent (Root + confinement + handlers) → Tasks 2–4. ✓
- §5 security & limits (confinement, 1 MiB cap, binary sniff) → Tasks 2–3 + adversarial tests. ✓
- §6 dashboard API (two GET endpoints, error mapping, DTOs) → Task 5. ✓
- §7 web UI (Files card, CodeMirror read-only, binary/oversize → download, honest banner) → Task 6. ✓
- §8 testing & demo → Tasks 2–5 tests + Task 7 demo. ✓
- §9 forward-compat (write-ready seam) → confinement helper + `Root` are write-agnostic; noted. ✓

**Placeholder scan:** no TBD/TODO. The two web "NOTE" callouts and the dashboard test's "adapt to apps_test.go" are explicit instructions to match existing patterns, not missing content — the surrounding code is complete.

**Type consistency:** `confine`/`ListDir`/`ReadFile` signatures match across Tasks 2–4; `Deployer.Root` returns `(string, bool)` consistently; DTO field names (`is_dir`, `mod_unix`, `truncated`, `binary`) match between Go (`files.go`) and TS (`api.ts`); proto field numbers are additive and consistent with the spec.

**One acknowledged design adaptation:** the spec said "Files **tab**"; the codebase has no tab component (stacked cards), so it ships as a **card** gated on `source === "git"`. Functionally identical; noted in Task 6 Step 4.
