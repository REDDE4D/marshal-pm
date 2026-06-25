package server

import (
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"marshal/internal/pb"
)

// TestRegistryListConcurrentWithUpdate is a concurrency regression guard for the
// registry, which in production is read by ListFleet/the notification detector
// while the Connect goroutine pushes fresh snapshots. It exercises List()+marshal
// against a concurrent Update writer. Run under -race. It must stay race-free:
// List/Update serialize on r.mu, each Update publishes a fresh slice the producer
// never mutates, and protobuf marshal is safe for concurrent reads.
func TestRegistryListConcurrentWithUpdate(t *testing.T) {
	reg := NewRegistry()
	reg.Open("h1")
	reg.Update("h1",
		[]*pb.ProcInfo{{Name: "api", Pid: 42, State: "online"}, {Name: "web", Pid: 43}},
		&pb.HostMetrics{CpuPercent: 12.5, MemTotal: 1000})

	// Writer: keeps pushing fresh snapshots (reassigns e.procs/e.host).
	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			reg.Update("h1",
				[]*pb.ProcInfo{{Name: "api", Pid: 1, State: "online"}},
				&pb.HostMetrics{CpuPercent: 1})
		}
	}()
	// Readers: list and marshal concurrently with the writer.
	var readers sync.WaitGroup
	for i := 0; i < 16; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for j := 0; j < 200; j++ {
				for _, st := range reg.List() {
					if _, err := proto.Marshal(st); err != nil {
						t.Errorf("marshal: %v", err)
						return
					}
					for _, p := range st.GetProcs() {
						_ = p.GetState()
					}
				}
			}
		}()
	}
	readers.Wait()
	close(stop)
	writer.Wait()
}

func TestRegistryConnectedFreshAndOffline(t *testing.T) {
	now := time.Unix(1000, 0)
	reg := NewRegistry(WithOfflineAfter(10*time.Second), WithClock(func() time.Time { return now }))

	reg.Open("web-1")
	reg.Update("web-1", []*pb.ProcInfo{{Name: "api", State: "online"}}, nil)

	got := reg.List()
	if len(got) != 1 || got[0].GetAgentName() != "web-1" || !got[0].GetConnected() {
		t.Fatalf("got %+v", got)
	}
	if len(got[0].GetProcs()) != 1 {
		t.Fatalf("procs = %+v", got[0].GetProcs())
	}

	// No fresh snapshot past the offline window -> offline, snapshot retained.
	now = now.Add(11 * time.Second)
	if reg.List()[0].GetConnected() {
		t.Fatal("expected offline after lapse")
	}
	if len(reg.List()[0].GetProcs()) != 1 {
		t.Fatal("expected last snapshot retained while offline")
	}
}

func TestRegistryCloseMarksOfflineImmediately(t *testing.T) {
	now := time.Unix(2000, 0)
	reg := NewRegistry(WithOfflineAfter(time.Hour), WithClock(func() time.Time { return now }))
	reg.Open("web-1")
	reg.Update("web-1", []*pb.ProcInfo{{Name: "api"}}, nil)
	reg.Close("web-1")
	if reg.List()[0].GetConnected() {
		t.Fatal("expected offline immediately after Close")
	}
}

func TestRegistrySetMetaSurfacedInList(t *testing.T) {
	reg := NewRegistry()
	reg.Open("web-1")
	reg.SetMeta("web-1", AgentMeta{Hostname: "web-01", IP: "203.0.113.7", OS: "linux", Arch: "amd64", MarshalVersion: "v0.1.0", HostBootUnix: 1700000000})
	got := reg.List()[0]
	if got.GetHostname() != "web-01" || got.GetIp() != "203.0.113.7" || got.GetOs() != "linux" ||
		got.GetArch() != "amd64" || got.GetMarshalVersion() != "v0.1.0" || got.GetHostBootUnix() != 1700000000 {
		t.Fatalf("metadata not surfaced: %+v", got)
	}
}

func TestRegistryEvictRemovesStaleDisconnectedAgents(t *testing.T) {
	now := time.Unix(100000, 0)
	reg := NewRegistry(WithClock(func() time.Time { return now }))

	// Three agents: connected-and-fresh, disconnected-but-recent, disconnected-and-stale.
	reg.Update("fresh", nil, nil) // streamOpen, lastSeen=now
	reg.Update("recent", nil, nil)
	reg.Close("recent")
	reg.Update("stale", nil, nil)
	reg.Close("stale")

	// Advance the clock; "stale" was last seen well before the cutoff.
	now = now.Add(8 * 24 * time.Hour)
	cutoff := now.Add(-7 * 24 * time.Hour)

	// "recent" was actually last-seen at the original time too, so to isolate the
	// stale one we re-touch "fresh" and "recent" after the jump.
	reg.Update("fresh", nil, nil)
	reg.Update("recent", nil, nil)
	reg.Close("recent")

	removed := reg.Evict(cutoff)
	if removed != 1 {
		t.Fatalf("evicted %d, want 1", removed)
	}
	names := map[string]bool{}
	for _, a := range reg.List() {
		names[a.GetAgentName()] = true
	}
	if names["stale"] {
		t.Fatal("stale agent should have been evicted")
	}
	if !names["fresh"] || !names["recent"] {
		t.Fatalf("non-stale agents must survive: %v", names)
	}
}

func TestRegistryEvictKeepsConnectedAgents(t *testing.T) {
	now := time.Unix(100000, 0)
	reg := NewRegistry(WithClock(func() time.Time { return now }))
	reg.Update("live", nil, nil) // streamOpen stays true

	now = now.Add(100 * 24 * time.Hour)
	// Even though lastSeen is ancient, an open stream must never be evicted.
	if removed := reg.Evict(now); removed != 0 {
		t.Fatalf("evicted %d connected agents, want 0", removed)
	}
}

func TestRegistryStoresHostMetrics(t *testing.T) {
	reg := NewRegistry()
	reg.Open("h1")
	reg.Update("h1", nil, &pb.HostMetrics{CpuPercent: 12.5, MemTotal: 1000})
	got := reg.List()
	if len(got) != 1 {
		t.Fatalf("agents = %d, want 1", len(got))
	}
	h := got[0].GetHost()
	if h == nil || h.GetCpuPercent() != 12.5 || h.GetMemTotal() != 1000 {
		t.Fatalf("host = %+v, want cpu=12.5 mem=1000", h)
	}
}
