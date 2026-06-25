package ratelimit

import (
	"testing"
	"time"
)

func testPolicy() Policy {
	return Policy{Threshold: 3, Base: time.Minute, Cap: 10 * time.Minute}
}

func TestLocksAfterThreshold(t *testing.T) {
	now := time.Unix(1000, 0)
	l := New(testPolicy(), func() time.Time { return now })

	for i := 0; i < 2; i++ {
		l.Fail("1.2.3.4")
		if locked, _ := l.RetryAfter("1.2.3.4"); locked {
			t.Fatalf("locked after %d failures, want >=3", i+1)
		}
	}
	l.Fail("1.2.3.4") // 3rd → lock
	locked, wait := l.RetryAfter("1.2.3.4")
	if !locked || wait != time.Minute {
		t.Fatalf("after threshold: locked=%v wait=%v, want true/1m", locked, wait)
	}
}

func TestResetClearsFailures(t *testing.T) {
	now := time.Unix(1000, 0)
	l := New(testPolicy(), func() time.Time { return now })
	l.Fail("ip")
	l.Fail("ip")
	l.Reset("ip") // a success wipes the streak
	l.Fail("ip")  // back to 1, not 3
	if locked, _ := l.RetryAfter("ip"); locked {
		t.Fatal("locked after reset+1 failure, want unlocked")
	}
}

func TestExponentialBackoffAndCap(t *testing.T) {
	now := time.Unix(1000, 0)
	l := New(testPolicy(), func() time.Time { return now })
	trip := func() { l.Fail("ip"); l.Fail("ip"); l.Fail("ip") }

	trip()
	if _, w := l.RetryAfter("ip"); w != time.Minute {
		t.Fatalf("1st lock = %v, want 1m", w)
	}
	now = now.Add(time.Minute) // let it expire, then trip again
	trip()
	if _, w := l.RetryAfter("ip"); w != 2*time.Minute {
		t.Fatalf("2nd lock = %v, want 2m", w)
	}
	for i := 0; i < 5; i++ { // keep tripping; must cap at 10m
		now = now.Add(l.pol.Cap)
		trip()
	}
	if _, w := l.RetryAfter("ip"); w != 10*time.Minute {
		t.Fatalf("capped lock = %v, want 10m", w)
	}
}

func TestLockedAny(t *testing.T) {
	now := time.Unix(1000, 0)
	l := New(testPolicy(), func() time.Time { return now })
	for i := 0; i < 3; i++ {
		l.Fail("user|ip")
	}
	// "ip" alone is clean, but the combined key is locked.
	if locked, _ := l.LockedAny("ip", "user|ip"); !locked {
		t.Fatal("LockedAny should report locked when any key is locked")
	}
	if locked, _ := l.LockedAny("ip", "other"); locked {
		t.Fatal("LockedAny should be false when no key is locked")
	}
}
