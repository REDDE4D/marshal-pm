package dashboard

import (
	"sync"
	"time"
)

const (
	lockoutThreshold = 5                // consecutive failures before a lock
	lockoutBase      = time.Minute      // first lock duration
	lockoutCap       = 15 * time.Minute // maximum lock duration
)

// limiterEntry tracks consecutive failures for one (user, IP) key.
type limiterEntry struct {
	fails      int
	lockUntil  time.Time
	lockedOnce int // number of locks engaged, for exponential backoff
	lastSeen   time.Time
}

// loginLimiter applies a per-key consecutive-failure lockout with exponential
// backoff. Keys are typically "user|ip". It is safe for concurrent use.
type loginLimiter struct {
	now func() time.Time
	mu  sync.Mutex
	m   map[string]*limiterEntry
}

func newLoginLimiter(now func() time.Time) *loginLimiter {
	if now == nil {
		now = time.Now
	}
	return &loginLimiter{now: now, m: map[string]*limiterEntry{}}
}

// retryAfter reports whether key is currently locked and, if so, how long until
// it unlocks.
func (l *loginLimiter) retryAfter(key string) (bool, time.Duration) {
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

// fail records a failed attempt for key, engaging (or extending) a lock once the
// failure count reaches the threshold.
func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.pruneLocked(now)
	e, ok := l.m[key]
	if !ok {
		e = &limiterEntry{}
		l.m[key] = e
	}
	e.lastSeen = now
	e.fails++
	if e.fails >= lockoutThreshold {
		dur := lockoutBase << e.lockedOnce // base * 2^lockedOnce
		if dur > lockoutCap || dur <= 0 {  // <=0 guards against shift overflow
			dur = lockoutCap
		}
		e.lockUntil = now.Add(dur)
		e.lockedOnce++
		e.fails = 0 // reset the counter; the next threshold engages the next backoff step
	}
}

// reset clears all state for key (called after a successful login).
func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	delete(l.m, key)
	l.mu.Unlock()
}

// pruneLocked drops idle entries. Caller holds l.mu.
//   - never-locked entries (fails below threshold): evicted once idle past the cap window.
//   - previously-locked entries: evicted only once the lock has expired AND has been
//     expired longer than the cap window, so an entry mid-backoff is never reset early.
func (l *loginLimiter) pruneLocked(now time.Time) {
	for k, e := range l.m {
		if e.lockUntil.IsZero() {
			if now.Sub(e.lastSeen) > lockoutCap {
				delete(l.m, k)
			}
			continue
		}
		if now.After(e.lockUntil) && now.Sub(e.lockUntil) > lockoutCap {
			delete(l.m, k)
		}
	}
}
