package supervisor

import (
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	base := 100 * time.Millisecond
	max := 15 * time.Second
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 100 * time.Millisecond},
		{1, 200 * time.Millisecond},
		{2, 400 * time.Millisecond},
		{3, 800 * time.Millisecond},
		{20, 15 * time.Second}, // capped
	}
	for _, c := range cases {
		if got := Backoff(c.attempt, base, max); got != c.want {
			t.Errorf("Backoff(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}
}
