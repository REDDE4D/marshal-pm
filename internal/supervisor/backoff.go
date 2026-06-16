package supervisor

import "time"

// Backoff returns base*2^attempt, capped at max. attempt is zero-based.
func Backoff(attempt int, base, max time.Duration) time.Duration {
	d := base
	for i := 0; i < attempt; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	if d > max {
		return max
	}
	return d
}
