package daemon

import (
	"testing"

	"marshal/internal/logs"
	"marshal/internal/manager"
	"marshal/internal/metricstore"
)

func TestSnapshotToProcCredential(t *testing.T) {
	p := snapshotToProc(manager.InstanceSnapshot{
		Name:       "priv",
		Source:     "git",
		Credential: "gh-ci",
	}, 0, 0)
	if p.GetCredential() != "gh-ci" {
		t.Fatalf("credential not stamped: %q", p.GetCredential())
	}
}

func TestLogsSinceShipsNewRingLines(t *testing.T) {
	reg := logs.NewRegistry(t.TempDir())
	s := reg.For("api#0")
	_, _ = s.Writer(false).Write([]byte("hello\nworld\n"))

	fn := logsSince(reg)
	got := fn(0)
	if len(got) != 2 || got[0].GetText() != "hello" || got[1].GetText() != "world" {
		t.Fatalf("logsSince(0) = %+v, want hello,world", got)
	}
	if got[0].GetLabel() != "api#0" {
		t.Fatalf("label = %q, want api#0", got[0].GetLabel())
	}
	// strictly-newer filter: everything already shipped -> nothing new
	wm := got[1].GetTsMs()
	if rest := fn(wm); len(rest) != 0 {
		t.Fatalf("logsSince(maxTs) = %+v, want none", rest)
	}
}

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
