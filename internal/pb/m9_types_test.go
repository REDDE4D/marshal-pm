package pb

import "testing"

// TestM9Types pins the generated command surface so a bad regeneration is caught.
func TestM9Types(t *testing.T) {
	cmd := &Command{RequestId: 7, Op: &ControlOp{Op: &ControlOp_Restart{Restart: &Selector{Target: "api"}}}}
	if cmd.GetOp().GetRestart().GetTarget() != "api" {
		t.Fatal("restart selector did not round-trip")
	}
	sm := &ServerMessage{Msg: &ServerMessage_Command{Command: cmd}}
	if sm.GetCommand().GetRequestId() != 7 {
		t.Fatal("ServerMessage_Command did not round-trip")
	}
	res := &CommandResult{RequestId: 7, Result: &ControlResult{Ok: true, Procs: []*ProcInfo{{Name: "api"}}}}
	am := &AgentMessage{Msg: &AgentMessage_Result{Result: res}}
	if !am.GetResult().GetResult().GetOk() || am.GetResult().GetResult().GetProcs()[0].GetName() != "api" {
		t.Fatal("AgentMessage_Result did not round-trip")
	}
	start := &ControlOp{Op: &ControlOp_Start{Start: &StartRequest{Apps: []*AppSpec{{Name: "web"}}}}}
	if start.GetStart().GetApps()[0].GetName() != "web" {
		t.Fatal("start op did not round-trip")
	}
}
