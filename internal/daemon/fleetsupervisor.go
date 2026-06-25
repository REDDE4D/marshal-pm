package daemon

import (
	"context"
	"os"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/store"
)

// fleetTarget is the identity of the server connection the agent should hold. It
// is comparable and deliberately excludes the persisted per-agent token, which
// is derived state — including it would restart the client right after it
// enrolls and writes that token back to the store.
type fleetTarget struct {
	address     string
	name        string
	fingerprint string
	ca          string
	enrollToken string
}

// fleetRunner starts a fleet client for tgt (using fleetToken if already
// minted, else tgt.enrollToken) and blocks until ctx is cancelled. It returns
// early if the target is not connectable (e.g. bad TLS pin).
type fleetRunner func(ctx context.Context, tgt fleetTarget, fleetToken string)

// loadFleetTarget derives the desired connection from the store. enrolled is
// false when there's no server address or no usable credential.
func loadFleetTarget(st *store.Store) (tgt fleetTarget, fleetToken string, enrolled bool) {
	sc, err := st.LoadServer()
	if err != nil || sc == nil || sc.Address == "" {
		return fleetTarget{}, "", false
	}
	fleetToken, _ = st.LoadFleetToken() // a read error is treated as "no minted token" — we fall back to the enroll token
	if fleetToken == "" && sc.Token == "" {
		return fleetTarget{}, "", false
	}
	name := sc.Name
	if name == "" {
		if h, hErr := os.Hostname(); hErr == nil {
			name = h
		} else {
			name = "unknown"
		}
	}
	return fleetTarget{
		address:     sc.Address,
		name:        name,
		fingerprint: sc.Fingerprint,
		ca:          sc.CA,
		enrollToken: sc.Token,
	}, fleetToken, true
}

// superviseFleet keeps at most one live fleet runner matching the store's
// current config, reconnecting on change and stopping on unenroll. It applies
// the current config immediately, then re-checks every poll until ctx is done.
func superviseFleet(ctx context.Context, st *store.Store, poll time.Duration, run fleetRunner) {
	var (
		curr   fleetTarget
		hasCur bool
		cancel context.CancelFunc
	)
	stop := func() {
		if cancel != nil {
			cancel()
			cancel = nil
		}
		hasCur = false
		curr = fleetTarget{}
	}
	defer stop()

	tick := time.NewTicker(poll)
	defer tick.Stop()
	for {
		tgt, tok, enrolled := loadFleetTarget(st)
		switch {
		case !enrolled:
			if hasCur {
				stop()
			}
		case !hasCur || tgt != curr:
			stop()
			cctx, ccancel := context.WithCancel(ctx)
			cancel = ccancel
			curr, hasCur = tgt, true
			go run(cctx, tgt, tok)
		}

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
