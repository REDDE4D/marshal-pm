// Package metrics samples live per-instance CPU% and RSS (summed over each
// process group) via gopsutil.
package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/process"
)

// Sample is one instance's latest reading.
type Sample struct {
	Cpu     float64 // percent, summed over the process group
	Mem     uint64  // RSS bytes, summed over the process group
	Threads int32   // thread count, summed over the process group
	Fds     int32   // open FD count, summed over the group; -1 if unavailable (e.g. darwin)
}

// Instance is the minimal view the sampler needs of a supervised instance.
type Instance struct {
	Label  string
	Pid    int
	Online bool
}

// Sampler periodically samples process-group CPU%/RSS keyed by instance label.
type Sampler struct {
	interval time.Duration

	mu     sync.Mutex
	last   map[string]Sample
	procs  map[int32]*process.Process // retained across ticks for CPU% deltas
	onTick func(map[string]Sample)    // optional; fired each tick with fresh results
}

// NewSampler builds a sampler with the given tick interval (default 5s).
func NewSampler(interval time.Duration) *Sampler {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Sampler{
		interval: interval,
		last:     map[string]Sample{},
		procs:    map[int32]*process.Process{},
	}
}

// SetOnTick registers a callback fired once per sample tick with the fresh
// per-label results. Call before Run; not safe to change concurrently with it.
func (s *Sampler) SetOnTick(fn func(map[string]Sample)) { s.onTick = fn }

// Get returns the latest sample for a label.
func (s *Sampler) Get(label string) (Sample, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.last[label]
	return v, ok
}

// Run samples every interval until ctx is canceled. snapshot returns the
// current instances each tick.
func (s *Sampler) Run(ctx context.Context, snapshot func() []Instance) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	s.sample(snapshot()) // prime CPU% handles immediately
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sample(snapshot())
		}
	}
}

// SampleOnce performs a single synchronous sample (used by the daemon's tests
// and any caller that wants an immediate reading without waiting for a tick).
func (s *Sampler) SampleOnce(insts []Instance) { s.sample(insts) }

func (s *Sampler) sample(insts []Instance) {
	live := map[int32]bool{}
	result := make(map[string]Sample, len(insts))
	for _, in := range insts {
		if !in.Online || in.Pid <= 0 {
			continue
		}
		var sum Sample
		fdsOK := false
		for _, pid := range groupPids(int32(in.Pid)) {
			live[pid] = true
			p := s.handle(pid)
			if p == nil {
				continue
			}
			if c, err := p.Percent(0); err == nil {
				sum.Cpu += c
			}
			if m, err := p.MemoryInfo(); err == nil && m != nil {
				sum.Mem += m.RSS
			}
			if t, err := p.NumThreads(); err == nil {
				sum.Threads += t
			}
			if fd, err := p.NumFDs(); err == nil {
				sum.Fds += fd
				fdsOK = true
			}
		}
		if !fdsOK {
			sum.Fds = -1 // unavailable on this platform (gopsutil NumFDs unsupported)
		}
		result[in.Label] = sum
	}
	s.mu.Lock()
	s.last = result
	for pid := range s.procs {
		if !live[pid] {
			delete(s.procs, pid)
		}
	}
	s.mu.Unlock()
	if s.onTick != nil {
		s.onTick(result)
	}
}

// handle returns a cached process handle (created on first use), so Percent(0)
// computes a delta against the previous tick.
func (s *Sampler) handle(pid int32) *process.Process {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.procs[pid]; ok {
		return p
	}
	p, err := process.NewProcess(pid)
	if err != nil {
		return nil
	}
	s.procs[pid] = p
	return p
}

// groupPids returns pid plus all descendant pids.
func groupPids(pid int32) []int32 {
	out := []int32{pid}
	p, err := process.NewProcess(pid)
	if err != nil {
		return out
	}
	kids, err := p.Children()
	if err != nil {
		return out
	}
	for _, k := range kids {
		out = append(out, groupPids(k.Pid)...)
	}
	return out
}
