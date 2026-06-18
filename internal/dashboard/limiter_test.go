package dashboard

import (
	"testing"
	"time"
)

func TestLimiterLocksAfterThreshold(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })

	for i := 0; i < lockoutThreshold-1; i++ {
		l.fail("admin|1.2.3.4")
	}
	if locked, _ := l.retryAfter("admin|1.2.3.4"); locked {
		t.Fatal("locked before reaching threshold")
	}
	l.fail("admin|1.2.3.4") // crosses the threshold
	locked, wait := l.retryAfter("admin|1.2.3.4")
	if !locked {
		t.Fatal("not locked at threshold")
	}
	if wait != lockoutBase {
		t.Fatalf("wait = %v; want %v", wait, lockoutBase)
	}
}

func TestLimiterResetClearsState(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })
	for i := 0; i < lockoutThreshold; i++ {
		l.fail("admin|ip")
	}
	l.reset("admin|ip")
	if locked, _ := l.retryAfter("admin|ip"); locked {
		t.Fatal("still locked after reset")
	}
}

func TestLimiterLockExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })
	for i := 0; i < lockoutThreshold; i++ {
		l.fail("admin|ip")
	}
	now = now.Add(lockoutBase + time.Second)
	if locked, _ := l.retryAfter("admin|ip"); locked {
		t.Fatal("still locked after the backoff elapsed")
	}
}

func TestLimiterBackoffDoublesAndCaps(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })

	// Drive several lockout cycles; each adds `lockoutThreshold` fresh failures
	// after the previous lock has expired.
	want := []time.Duration{lockoutBase, 2 * lockoutBase, 4 * lockoutBase, 8 * lockoutBase, lockoutCap, lockoutCap}
	for _, w := range want {
		for i := 0; i < lockoutThreshold; i++ {
			l.fail("admin|ip")
		}
		_, wait := l.retryAfter("admin|ip")
		if wait != w {
			t.Fatalf("backoff = %v; want %v", wait, w)
		}
		now = now.Add(wait + time.Second) // let the lock expire before the next cycle
	}
}

func TestLimiterKeysIndependent(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })
	for i := 0; i < lockoutThreshold; i++ {
		l.fail("admin|1.1.1.1")
	}
	if locked, _ := l.retryAfter("admin|2.2.2.2"); locked {
		t.Fatal("a different IP was locked")
	}
}
