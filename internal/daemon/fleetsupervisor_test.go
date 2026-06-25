package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/config"
	"github.com/REDDE4D/marshal-pm/internal/store"
)

func TestSuperviseFleetStartsOnEnrollStopsOnClear(t *testing.T) {
	st := store.NewAt(t.TempDir())

	var mu sync.Mutex
	type run struct {
		tgt  fleetTarget
		tok  string
		done chan struct{}
	}
	var runs []*run
	runner := func(ctx context.Context, tgt fleetTarget, tok string) {
		r := &run{tgt: tgt, tok: tok, done: make(chan struct{})}
		mu.Lock()
		runs = append(runs, r)
		mu.Unlock()
		<-ctx.Done()
		close(r.done)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go superviseFleet(ctx, st, 10*time.Millisecond, runner)

	// Not enrolled → no runner. A fixed sleep is safe for a negative assertion:
	// we expect zero runs, so this can never false-pass by finishing too early.
	time.Sleep(40 * time.Millisecond)
	mu.Lock()
	if len(runs) != 0 {
		mu.Unlock()
		t.Fatalf("runner started before enrollment: %d", len(runs))
	}
	mu.Unlock()

	// Enroll → exactly one runner with the right target.
	if err := st.SaveServer(&config.ServerConfig{Address: "srv:9000", Name: "h1", Token: "enr", Fingerprint: "fp"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(runs) == 1 })
	mu.Lock()
	if runs[0].tgt.address != "srv:9000" || runs[0].tgt.enrollToken != "enr" {
		mu.Unlock()
		t.Fatalf("bad target: %+v", runs[0].tgt)
	}
	first := runs[0]
	mu.Unlock()

	// Unenroll → the running client is cancelled.
	if err := st.ClearServer(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.done:
	case <-time.After(time.Second):
		t.Fatal("runner not cancelled after unenroll")
	}
}

// Persisting the per-agent fleet token (what the client does after enrolling)
// must NOT cause a restart.
func TestSuperviseFleetIgnoresPersistedToken(t *testing.T) {
	st := store.NewAt(t.TempDir())
	if err := st.SaveServer(&config.ServerConfig{Address: "srv:9000", Name: "h1", Token: "enr", Fingerprint: "fp"}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	count := 0
	runner := func(ctx context.Context, _ fleetTarget, _ string) {
		mu.Lock()
		count++
		mu.Unlock()
		<-ctx.Done()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go superviseFleet(ctx, st, 10*time.Millisecond, runner)
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return count == 1 })

	// Simulate the client persisting its per-agent token.
	if err := st.SaveFleetToken("agent-token"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if count != 1 {
		t.Fatalf("supervisor restarted on token persist: count=%d", count)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}
