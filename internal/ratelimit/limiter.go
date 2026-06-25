// Package ratelimit provides a per-key consecutive-failure lockout with
// exponential backoff, shared by the dashboard login path and the gRPC auth
// interceptors. A success (Reset) wipes the streak, so a key that is only
// sometimes failing never trips — important when many clients share one source
// IP (NAT). It is safe for concurrent use.
package ratelimit

import (
	"sync"
	"time"
)

// Policy parameterizes the lockout schedule.
type Policy struct {
	Threshold int           // consecutive failures before a lock engages
	Base      time.Duration // first lock duration
	Cap       time.Duration // maximum lock duration (also the idle-eviction window)
}

type entry struct {
	fails      int
	lockUntil  time.Time
	lockedOnce int // locks engaged so far, for exponential backoff
	lastSeen   time.Time
}

// Limiter tracks consecutive failures per key.
type Limiter struct {
	pol Policy
	now func() time.Time
	mu  sync.Mutex
	m   map[string]*entry
}

// New builds a Limiter. A nil now uses time.Now.
func New(pol Policy, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{pol: pol, now: now, m: map[string]*entry{}}
}

// RetryAfter reports whether key is currently locked and, if so, the remaining
// wait.
func (l *Limiter) RetryAfter(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.m[key]
	if !ok {
		return false, 0
	}
	now := l.now()
	if now.Before(e.lockUntil) {
		return true, e.lockUntil.Sub(now)
	}
	return false, 0
}

// LockedAny reports whether any of keys is locked, returning the longest wait.
func (l *Limiter) LockedAny(keys ...string) (bool, time.Duration) {
	locked := false
	var wait time.Duration
	for _, k := range keys {
		if lk, w := l.RetryAfter(k); lk {
			locked = true
			if w > wait {
				wait = w
			}
		}
	}
	return locked, wait
}

// Fail records a failed attempt for key, engaging (or extending) a lock once the
// failure count reaches the policy threshold.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.pruneLocked(now)
	e, ok := l.m[key]
	if !ok {
		e = &entry{}
		l.m[key] = e
	}
	e.lastSeen = now
	e.fails++
	if e.fails >= l.pol.Threshold {
		dur := l.pol.Base << e.lockedOnce // base * 2^lockedOnce
		if dur > l.pol.Cap || dur <= 0 {  // <=0 guards against shift overflow
			dur = l.pol.Cap
		}
		e.lockUntil = now.Add(dur)
		e.lockedOnce++
		e.fails = 0 // next threshold engages the next backoff step
	}
}

// Reset clears all state for key (called after a success).
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.m, key)
	l.mu.Unlock()
}

// pruneLocked drops idle entries. Caller holds l.mu.
//   - never-locked entries: evicted once idle past the cap window.
//   - previously-locked entries: evicted only once the lock has expired AND has
//     been expired longer than the cap window, so an entry mid-backoff is never
//     reset early.
func (l *Limiter) pruneLocked(now time.Time) {
	for k, e := range l.m {
		if e.lockUntil.IsZero() {
			if now.Sub(e.lastSeen) > l.pol.Cap {
				delete(l.m, k)
			}
			continue
		}
		if now.After(e.lockUntil) && now.Sub(e.lockUntil) > l.pol.Cap {
			delete(l.m, k)
		}
	}
}
