package daemon

import (
	"testing"
	"time"

	"marshal/internal/manager"
	"marshal/internal/metricstore"
	"marshal/internal/supervisor"
)

func TestMetricsSinceConverts(t *testing.T) {
	st, err := metricstore.Open(t.TempDir() + "/m.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.Append(1000, []metricstore.Sample{{Label: "api#0", Cpu: 10, Mem: 100}})
	_ = st.Append(2000, []metricstore.Sample{{Label: "api#0", Cpu: 20, Mem: 200}})

	fn := metricsSince(st)
	got := fn(1000) // strictly newer than 1000
	if len(got) != 1 || got[0].GetTsMs() != 2000 || got[0].GetLabel() != "api#0" || got[0].GetMem() != 200 {
		t.Fatalf("metricsSince(1000) = %+v, want one row ts=2000 mem=200", got)
	}
}

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
