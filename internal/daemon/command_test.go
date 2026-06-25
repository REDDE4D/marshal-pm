package daemon

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/REDDE4D/marshal-pm/internal/config"
	"github.com/REDDE4D/marshal-pm/internal/deploy"
	"github.com/REDDE4D/marshal-pm/internal/manager"
	"github.com/REDDE4D/marshal-pm/internal/pb"
	"github.com/REDDE4D/marshal-pm/internal/store"
)

type fakeDeployHost struct{ sources map[string]config.GitSource }

func (h *fakeDeployHost) Exists(string) bool { return false }
func (h *fakeDeployHost) Source(n string) (config.GitSource, bool) {
	s, ok := h.sources[n]
	return s, ok
}
func (h *fakeDeployHost) Launch(config.App) error               { return nil }
func (h *fakeDeployHost) Restart(string) error                  { return nil }
func (h *fakeDeployHost) Writers(string) (io.Writer, io.Writer) { return io.Discard, io.Discard }

// newCommandTestServer builds a minimal Server suitable for handleFleetCommand tests.
// It uses a real store (no metrics/logs — procList tolerates nil samplers).
func newCommandTestServer(t *testing.T) *Server {
	t.Helper()
	st := store.NewAt(t.TempDir())
	if err := st.EnsureDir(); err != nil {
		t.Fatal(err)
	}
	s := &Server{mgr: manager.New(context.Background()), store: st}
	s.deployer = deploy.New(deployHost{s}, deploy.ExecRunner{}, t.TempDir())
	return s
}

func sleepLongSpec(name string) *pb.AppSpec {
	return &pb.AppSpec{Name: name, Cmd: "sleep", Args: []string{"30"}, Instances: 1, Restart: "no"}
}

func TestHandleFleetCommandStart(t *testing.T) {
	s := newCommandTestServer(t)
	defer s.mgr.StopAll()

	cmd := &pb.Command{
		RequestId: 1,
		Op: &pb.ControlOp{Op: &pb.ControlOp_Start{
			Start: &pb.StartRequest{Apps: []*pb.AppSpec{sleepLongSpec("app1")}},
		}},
	}
	res := s.handleFleetCommand(cmd)
	if !res.GetOk() {
		t.Fatalf("expected Ok=true, got error: %s", res.GetError())
	}
	if len(res.GetProcs()) == 0 {
		t.Fatal("expected procs in result, got none")
	}

	// verify auto-save: store should be loadable with 1 app
	apps, err := s.store.Load()
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if len(apps) != 1 || apps[0].Name != "app1" {
		t.Fatalf("store after start = %+v, want [{Name:app1}]", apps)
	}
}

func TestHandleFleetCommandStop(t *testing.T) {
	s := newCommandTestServer(t)
	defer s.mgr.StopAll()

	// Start an app first via handleFleetCommand
	startRes := s.handleFleetCommand(&pb.Command{
		RequestId: 1,
		Op: &pb.ControlOp{Op: &pb.ControlOp_Start{
			Start: &pb.StartRequest{Apps: []*pb.AppSpec{sleepLongSpec("app2")}},
		}},
	})
	if !startRes.GetOk() {
		t.Fatalf("start failed: %s", startRes.GetError())
	}

	stopRes := s.handleFleetCommand(&pb.Command{
		RequestId: 2,
		Op:        &pb.ControlOp{Op: &pb.ControlOp_Stop{Stop: &pb.Selector{Target: "app2"}}},
	})
	if !stopRes.GetOk() {
		t.Fatalf("stop failed: %s", stopRes.GetError())
	}
}

func TestHandleFleetCommandRestart(t *testing.T) {
	s := newCommandTestServer(t)
	defer s.mgr.StopAll()

	_ = s.handleFleetCommand(&pb.Command{
		RequestId: 1,
		Op: &pb.ControlOp{Op: &pb.ControlOp_Start{
			Start: &pb.StartRequest{Apps: []*pb.AppSpec{sleepLongSpec("app3")}},
		}},
	})

	res := s.handleFleetCommand(&pb.Command{
		RequestId: 2,
		Op:        &pb.ControlOp{Op: &pb.ControlOp_Restart{Restart: &pb.Selector{Target: "app3"}}},
	})
	if !res.GetOk() {
		t.Fatalf("restart failed: %s", res.GetError())
	}
}

func TestHandleFleetCommandDelete(t *testing.T) {
	s := newCommandTestServer(t)
	defer s.mgr.StopAll()

	_ = s.handleFleetCommand(&pb.Command{
		RequestId: 1,
		Op: &pb.ControlOp{Op: &pb.ControlOp_Start{
			Start: &pb.StartRequest{Apps: []*pb.AppSpec{sleepLongSpec("app4")}},
		}},
	})

	res := s.handleFleetCommand(&pb.Command{
		RequestId: 2,
		Op:        &pb.ControlOp{Op: &pb.ControlOp_Delete{Delete: &pb.Selector{Target: "app4"}}},
	})
	if !res.GetOk() {
		t.Fatalf("delete failed: %s", res.GetError())
	}

	// verify auto-save after delete: store should have 0 apps
	apps, err := s.store.Load()
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("store after delete = %+v, want empty", apps)
	}
}

func TestHandleFleetCommandUnknownSelector(t *testing.T) {
	s := newCommandTestServer(t)

	res := s.handleFleetCommand(&pb.Command{
		RequestId: 3,
		Op:        &pb.ControlOp{Op: &pb.ControlOp_Stop{Stop: &pb.Selector{Target: "ghost"}}},
	})
	if res.GetOk() {
		t.Fatal("expected Ok=false for unknown selector")
	}
	if res.GetError() == "" {
		t.Fatal("expected non-empty error string")
	}
}

func TestHandleFleetCommandNilOp(t *testing.T) {
	s := newCommandTestServer(t)

	res := s.handleFleetCommand(&pb.Command{RequestId: 99, Op: nil})
	if res.GetOk() {
		t.Fatal("expected Ok=false for nil op")
	}
}

func TestHandleFleetCommandDeployAccepts(t *testing.T) {
	s := newCommandTestServer(t)
	res := s.handleFleetCommand(&pb.Command{Op: &pb.ControlOp{
		Op: &pb.ControlOp_Deploy{Deploy: &pb.DeployRequest{App: &pb.AppSpec{
			Name: "web", Cmd: "./server", Instances: 1,
			Source: &pb.GitSource{Repo: "https://example/r.git"},
		}}},
	}})
	if !res.GetOk() {
		t.Fatalf("deploy should be accepted, got error %q", res.GetError())
	}
}

func TestHandleFleetCommandDeployRejectsNoRepo(t *testing.T) {
	s := newCommandTestServer(t)
	res := s.handleFleetCommand(&pb.Command{Op: &pb.ControlOp{
		Op: &pb.ControlOp_Deploy{Deploy: &pb.DeployRequest{App: &pb.AppSpec{
			Name: "web", Cmd: "./server", Source: &pb.GitSource{},
		}}},
	}})
	if res.GetOk() {
		t.Fatal("deploy with empty repo should be rejected")
	}
}

func TestHandleFleetCommand_ListDirAndReadFile(t *testing.T) {
	deployRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(deployRoot, "app1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deployRoot, "app1", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		mgr:      manager.New(context.Background()),
		deployer: deploy.New(nil, nil, deployRoot),
	}
	defer s.mgr.StopAll()

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

func TestHandleFleetCommand_Commit(t *testing.T) {
	// Reuse the deploy package's real-git test repo by shelling out here.
	deployRoot := t.TempDir()
	app := "app1"
	work := filepath.Join(deployRoot, app)

	run := func(dir string, args ...string) {
		c := exec.Command("git", append([]string{"-c", "user.email=t@e", "-c", "user.name=t", "-c", "init.defaultBranch=main"}, args...)...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	remote := filepath.Join(deployRoot, "remote.git")
	run(deployRoot, "init", "--bare", "--initial-branch=main", remote)
	run(deployRoot, "clone", remote, work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "add", "README.md")
	run(work, "commit", "-m", "seed")
	run(work, "push", "origin", "main")

	h := &fakeDeployHost{sources: map[string]config.GitSource{app: {Repo: "r"}}}
	s := &Server{mgr: manager.New(context.Background()), deployer: deploy.New(h, deploy.ExecRunner{}, deployRoot)}
	defer s.mgr.StopAll()

	op := &pb.ControlOp{Op: &pb.ControlOp_Commit{Commit: &pb.CommitRequest{
		App: app, Kind: pb.CommitKind_COMMIT_EDIT, Path: "README.md",
		Content: []byte("edited\n"), Message: "Update README.md",
	}}}
	res := s.handleFleetCommand(&pb.Command{Op: op})
	if !res.GetOk() || res.GetCommit().GetBranch() != "main" {
		t.Fatalf("commit: ok=%v branch=%q err=%q", res.GetOk(), res.GetCommit().GetBranch(), res.GetError())
	}

	// nil deployer → not supported
	s2 := &Server{mgr: manager.New(context.Background())}
	if res := s2.handleFleetCommand(&pb.Command{Op: op}); res.GetOk() {
		t.Fatalf("nil deployer commit must fail")
	}
}

func TestHandleFleetCommand_CommitCreate(t *testing.T) {
	// Set up a bare remote + work clone, seeded with one commit (same pattern as
	// TestHandleFleetCommand_Commit), then issue a COMMIT_CREATE for a brand-new
	// path that does NOT yet exist in the working tree.
	deployRoot := t.TempDir()
	app := "app2"
	work := filepath.Join(deployRoot, app)

	run := func(dir string, args ...string) {
		c := exec.Command("git", append([]string{"-c", "user.email=t@e", "-c", "user.name=t", "-c", "init.defaultBranch=main"}, args...)...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	remote := filepath.Join(deployRoot, "remote2.git")
	run(deployRoot, "init", "--bare", "--initial-branch=main", remote)
	run(deployRoot, "clone", remote, work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "add", "README.md")
	run(work, "commit", "-m", "seed")
	run(work, "push", "origin", "main")

	h := &fakeDeployHost{sources: map[string]config.GitSource{app: {Repo: "r"}}}
	s := &Server{mgr: manager.New(context.Background()), deployer: deploy.New(h, deploy.ExecRunner{}, deployRoot)}
	defer s.mgr.StopAll()

	// COMMIT_CREATE for a file that does not yet exist.
	op := &pb.ControlOp{Op: &pb.ControlOp_Commit{Commit: &pb.CommitRequest{
		App: app, Kind: pb.CommitKind_COMMIT_CREATE, Path: "newfile.txt",
		Content: []byte("brand new\n"), Message: "Create newfile.txt",
	}}}
	res := s.handleFleetCommand(&pb.Command{Op: op})
	if !res.GetOk() || res.GetCommit().GetBranch() != "main" {
		t.Fatalf("commit_create: ok=%v branch=%q err=%q", res.GetOk(), res.GetCommit().GetBranch(), res.GetError())
	}

	// Verify the file actually landed in the work tree.
	got, err := os.ReadFile(filepath.Join(work, "newfile.txt"))
	if err != nil {
		t.Fatalf("newfile.txt not on disk: %v", err)
	}
	if string(got) != "brand new\n" {
		t.Fatalf("newfile.txt content = %q, want \"brand new\\n\"", got)
	}
}

func TestHandleFleetCommandReload(t *testing.T) {
	s := newCommandTestServer(t)
	defer s.mgr.StopAll()

	// Start an app so there is something to reload.
	start := &pb.Command{RequestId: 1, Op: &pb.ControlOp{Op: &pb.ControlOp_Start{
		Start: &pb.StartRequest{Apps: []*pb.AppSpec{sleepLongSpec("app1")}},
	}}}
	if res := s.handleFleetCommand(start); !res.GetOk() {
		t.Fatalf("start: %s", res.GetError())
	}

	reload := &pb.Command{RequestId: 2, Op: &pb.ControlOp{Op: &pb.ControlOp_Reload{
		Reload: &pb.Selector{Target: "app1"},
	}}}
	res := s.handleFleetCommand(reload)
	if !res.GetOk() {
		t.Fatalf("reload: expected Ok=true, got error: %s", res.GetError())
	}
	if len(res.GetProcs()) == 0 {
		t.Fatal("reload: expected procs in result, got none")
	}
}

func TestCredFromProtoSSH(t *testing.T) {
	got := credFromProto(&pb.GitCredential{
		Kind:       pb.CredentialKind_CRED_SSH,
		PrivateKey: "PRIV",
		KnownHosts: "h ssh-ed25519 AAAA",
		Username:   "git",
	})
	if !got.SSH || got.PrivateKey != "PRIV" || got.KnownHosts == "" {
		t.Fatalf("ssh mapping wrong: %+v", got)
	}
}

func TestCredFromProtoHTTPS(t *testing.T) {
	got := credFromProto(&pb.GitCredential{Username: "octocat", Token: "ghp_x"})
	if got.SSH || got.Username != "octocat" || got.Token != "ghp_x" {
		t.Fatalf("https mapping wrong: %+v", got)
	}
	if (credFromProto(nil)) != (deploy.Credential{}) {
		t.Fatal("nil credential must map to zero value")
	}
}
