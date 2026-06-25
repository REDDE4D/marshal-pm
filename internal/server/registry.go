// Package server implements the Marshal central server: the Fleet gRPC service
// and an in-memory registry of connected agents and their last-known state.
package server

import (
	"sync"
	"time"

	"marshal/internal/pb"
)

type agentEntry struct {
	procs      []*pb.ProcInfo
	streamOpen bool
	lastSeen   time.Time
	meta       AgentMeta
	host       *pb.HostMetrics
}

// AgentMeta is per-agent host metadata captured on Hello.
type AgentMeta struct {
	Hostname       string
	IP             string
	OS             string
	Arch           string
	MarshalVersion string
	HostBootUnix   int64
}

// Registry holds the live fleet state, keyed by agent name.
type Registry struct {
	mu           sync.Mutex
	agents       map[string]*agentEntry
	offlineAfter time.Duration
	now          func() time.Time
}

// RegOption configures a Registry.
type RegOption func(*Registry)

// WithOfflineAfter sets how long after the last snapshot an agent with an open
// stream is still considered connected.
func WithOfflineAfter(d time.Duration) RegOption { return func(r *Registry) { r.offlineAfter = d } }

// WithClock overrides time.Now (used by tests).
func WithClock(fn func() time.Time) RegOption { return func(r *Registry) { r.now = fn } }

// NewRegistry builds an empty registry (default offlineAfter 10s, clock time.Now).
func NewRegistry(opts ...RegOption) *Registry {
	r := &Registry{agents: map[string]*agentEntry{}, offlineAfter: 10 * time.Second, now: time.Now}
	for _, o := range opts {
		o(r)
	}
	return r
}

func (r *Registry) entry(name string) *agentEntry {
	e := r.agents[name]
	if e == nil {
		e = &agentEntry{}
		r.agents[name] = e
	}
	return e
}

// Open marks an agent's stream as open (called on Hello).
func (r *Registry) Open(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.entry(name)
	e.streamOpen = true
	e.lastSeen = r.now()
}

// Update records a fresh snapshot (procs + host gauges) and bumps last-seen.
func (r *Registry) Update(name string, procs []*pb.ProcInfo, host *pb.HostMetrics) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.entry(name)
	e.procs = procs
	e.host = host
	e.streamOpen = true
	e.lastSeen = r.now()
}

// Close marks an agent's stream as closed; its last snapshot is retained.
func (r *Registry) Close(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.agents[name]; e != nil {
		e.streamOpen = false
	}
}

// Evict drops registry entries for agents whose stream is closed and whose last
// snapshot predates cutoff, releasing their retained proc/host snapshots. Agents
// with an open stream are never evicted regardless of age. Returns the number
// removed. This bounds the registry under churning/ephemeral agent names.
func (r *Registry) Evict(cutoff time.Time) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := 0
	for name, e := range r.agents {
		if !e.streamOpen && e.lastSeen.Before(cutoff) {
			delete(r.agents, name)
			removed++
		}
	}
	return removed
}

// SetMeta records host metadata for an agent (called on Hello).
func (r *Registry) SetMeta(name string, m AgentMeta) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entry(name).meta = m
}

// List snapshots every known agent and computes its connected flag.
func (r *Registry) List() []*pb.AgentState {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	out := make([]*pb.AgentState, 0, len(r.agents))
	for name, e := range r.agents {
		connected := e.streamOpen && now.Sub(e.lastSeen) <= r.offlineAfter
		out = append(out, &pb.AgentState{
			AgentName:      name,
			Connected:      connected,
			LastSeenUnix:   e.lastSeen.Unix(),
			Procs:          e.procs,
			Hostname:       e.meta.Hostname,
			Ip:             e.meta.IP,
			Os:             e.meta.OS,
			Arch:           e.meta.Arch,
			MarshalVersion: e.meta.MarshalVersion,
			HostBootUnix:   e.meta.HostBootUnix,
			Host:           e.host,
		})
	}
	return out
}
