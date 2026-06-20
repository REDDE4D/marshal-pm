package pb

import "testing"

func TestGitCredentialWire(t *testing.T) {
	d := &DeployRequest{
		App:        &AppSpec{Name: "x", Source: &GitSource{Repo: "r", Credential: "gh-ci"}},
		Credential: &GitCredential{Username: "octocat", Token: "ghp_x"},
	}
	if d.GetApp().GetSource().GetCredential() != "gh-ci" {
		t.Fatalf("GitSource.Credential not wired")
	}
	if d.GetCredential().GetToken() != "ghp_x" {
		t.Fatalf("DeployRequest.Credential not wired")
	}
	rd := &RedeployRequest{Target: "x", Credential: &GitCredential{Token: "ghp_y"}}
	if rd.GetCredential().GetToken() != "ghp_y" {
		t.Fatalf("RedeployRequest.Credential not wired")
	}
	if (&ProcInfo{Credential: "gh-ci"}).GetCredential() != "gh-ci" {
		t.Fatalf("ProcInfo.Credential not wired")
	}
}

func TestGitSourceAndDeployOpsGenerated(t *testing.T) {
	spec := &AppSpec{Name: "x", Cmd: "c", Source: &GitSource{Repo: "r", Ref: "main", Build: "go build", Subdir: "sub"}}
	if spec.GetSource().GetRepo() != "r" || spec.GetSource().GetSubdir() != "sub" {
		t.Fatalf("AppSpec.Source round-trip failed: %+v", spec.GetSource())
	}
	pi := &ProcInfo{Source: "git", Detail: "build exited 1"}
	if pi.GetSource() != "git" || pi.GetDetail() != "build exited 1" {
		t.Fatalf("ProcInfo source/detail failed: %+v", pi)
	}
	op := &ControlOp{Op: &ControlOp_Deploy{Deploy: &DeployRequest{App: spec}}}
	if op.GetDeploy().GetApp().GetName() != "x" {
		t.Fatal("ControlOp_Deploy round-trip failed")
	}
	rop := &ControlOp{Op: &ControlOp_Redeploy{Redeploy: &RedeployRequest{Target: "x"}}}
	if rop.GetRedeploy().GetTarget() != "x" {
		t.Fatal("ControlOp_Redeploy round-trip failed")
	}
}

func TestCommitOpWire(t *testing.T) {
	op := &ControlOp{Op: &ControlOp_Commit{Commit: &CommitRequest{
		App:        "app1",
		Kind:       CommitKind_COMMIT_RENAME,
		Path:       "a.txt",
		NewPath:    "b.txt",
		Content:    []byte("hi"),
		Message:    "Rename a.txt → b.txt",
		Credential: &GitCredential{Username: "octocat", Token: "ghp_x"},
	}}}
	c := op.GetCommit()
	if c.GetApp() != "app1" || c.GetKind() != CommitKind_COMMIT_RENAME ||
		c.GetPath() != "a.txt" || c.GetNewPath() != "b.txt" ||
		string(c.GetContent()) != "hi" || c.GetCredential().GetToken() != "ghp_x" {
		t.Fatalf("CommitRequest not wired: %+v", c)
	}
	res := &ControlResult{Ok: true, Commit: &CommitResult{Sha: "abc123", Branch: "main"}}
	if res.GetCommit().GetSha() != "abc123" || res.GetCommit().GetBranch() != "main" {
		t.Fatalf("CommitResult not wired: %+v", res.GetCommit())
	}
}
