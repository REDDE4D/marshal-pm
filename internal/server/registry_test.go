package server

import (
	"testing"
	"time"

	"marshal/internal/pb"
)

func TestRegistryConnectedFreshAndOffline(t *testing.T) {
	now := time.Unix(1000, 0)
	reg := NewRegistry(WithOfflineAfter(10*time.Second), WithClock(func() time.Time { return now }))

	reg.Open("web-1")
	reg.Update("web-1", []*pb.ProcInfo{{Name: "api", State: "online"}})

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
	reg.Update("web-1", []*pb.ProcInfo{{Name: "api"}})
	reg.Close("web-1")
	if reg.List()[0].GetConnected() {
		t.Fatal("expected offline immediately after Close")
	}
}
