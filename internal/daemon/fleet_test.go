package daemon

import (
	"testing"

	"marshal/internal/logs"
	"marshal/internal/manager"
	"marshal/internal/metrics"
	"marshal/internal/metricstore"
)

func TestSnapshotToProcCredential(t *testing.T) {
	p := snapshotToProc(manager.InstanceSnapshot{
		Name:       "priv",
		Source:     "git",
		Credential: "gh-ci",
	}, metrics.Sample{})
	if p.GetCredential() != "gh-ci" {
		t.Fatalf("credential not stamped: %q", p.GetCredential())
	}
}

func TestSnapshotToProcExtendedMetrics(t *testing.T) {
	sn := manager.InstanceSnapshot{Name: "api"}
	sn.ExitCode = 2
	sn.ExitReason = "exit status 2"
	p := snapshotToProc(sn, metrics.Sample{Threads: 12, Fds: -1})
	if p.GetThreads() != 12 {
		t.Fatalf("threads = %d, want 12", p.GetThreads())
	}
	if p.GetOpenFds() != -1 {
		t.Fatalf("open_fds = %d, want -1", p.GetOpenFds())
	}
	if p.GetExitCode() != 2 || p.GetExitReason() != "exit status 2" {
		t.Fatalf("exit = (%d, %q), want (2, \"exit status 2\")", p.GetExitCode(), p.GetExitReason())
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
