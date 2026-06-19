package pb

import "testing"

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
