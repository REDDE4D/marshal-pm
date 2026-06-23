package notify

import (
	"testing"
	"time"
)

func TestRuleMatches(t *testing.T) {
	crash := Event{Type: EventCrash, Agent: "dev-1", Process: "api"}
	cases := []struct {
		name string
		rule Rule
		want bool
	}{
		{"wildcard all", Rule{Enabled: true}, true},
		{"event match", Rule{Enabled: true, Events: []EventType{EventCrash}}, true},
		{"event miss", Rule{Enabled: true, Events: []EventType{EventDeployFail}}, false},
		{"agent match", Rule{Enabled: true, Agent: "dev-1"}, true},
		{"agent miss", Rule{Enabled: true, Agent: "dev-2"}, false},
		{"agent star", Rule{Enabled: true, Agent: "*"}, true},
		{"process match", Rule{Enabled: true, Process: "api"}, true},
		{"process miss", Rule{Enabled: true, Process: "web"}, false},
		{"disabled", Rule{Enabled: false}, false},
	}
	for _, c := range cases {
		if got := c.rule.Matches(crash); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestCooldownForPrecedence(t *testing.T) {
	s := Settings{
		CooldownSeconds:   300,
		CooldownOverrides: map[EventType]int{EventRecovered: 600, EventCrash: 0},
	}
	if got := s.cooldownFor(EventDeployFail); got != 300*time.Second {
		t.Errorf("no override: want 300s, got %v", got)
	}
	if got := s.cooldownFor(EventRecovered); got != 600*time.Second {
		t.Errorf("override present: want 600s, got %v", got)
	}
	if got := s.cooldownFor(EventCrash); got != 0 {
		t.Errorf("explicit 0 override: want 0, got %v", got)
	}
	// nil map falls through to the global for every type.
	bare := Settings{CooldownSeconds: 120}
	if got := bare.cooldownFor(EventRecovered); got != 120*time.Second {
		t.Errorf("nil overrides: want 120s, got %v", got)
	}
}

func TestCoalesceWindowDefaultsWhenNil(t *testing.T) {
	if got := (Settings{}).coalesceWindow(); got != 10*time.Second {
		t.Fatalf("nil window should default to 10s, got %s", got)
	}
}

func TestCoalesceWindowExplicitZeroDisables(t *testing.T) {
	z := 0
	if got := (Settings{CoalesceWindowSeconds: &z}).coalesceWindow(); got != 0 {
		t.Fatalf("explicit 0 should be 0s (disabled), got %s", got)
	}
}

func TestCoalesceWindowExplicitValue(t *testing.T) {
	w := 25
	if got := (Settings{CoalesceWindowSeconds: &w}).coalesceWindow(); got != 25*time.Second {
		t.Fatalf("want 25s, got %s", got)
	}
}
