package daemon

import (
	"testing"
	"time"

	"marshal/internal/manager"
	"marshal/internal/supervisor"
)

func TestProcInfosMapsSnapshot(t *testing.T) {
	snaps := []manager.InstanceSnapshot{{
		ID: 1, Name: "api", InstanceID: 0, Label: "api#0",
		Snapshot: supervisor.Snapshot{
			State: supervisor.StateOnline, Pid: 4242, Restarts: 2,
			StartedAt: time.Now().Add(-3 * time.Second),
		},
	}}
	out := procInfos(snaps)
	if len(out) != 1 {
		t.Fatalf("len = %d", len(out))
	}
	p := out[0]
	if p.GetName() != "api" || p.GetPid() != 4242 || p.GetState() != "online" || p.GetRestarts() != 2 {
		t.Fatalf("proc = %+v", p)
	}
	if p.GetUptimeMs() <= 0 {
		t.Fatal("expected positive uptime for an online proc")
	}
}
