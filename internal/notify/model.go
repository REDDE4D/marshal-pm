// Package notify detects fleet trouble (crashes, restart loops, agent up/down,
// deploy failures) from server snapshots and dispatches alerts to channels.
package notify

import (
	"context"
	"time"
)

// EventType enumerates the alertable fleet conditions.
type EventType string

const (
	EventCrash       EventType = "crash"
	EventRestartLoop EventType = "restart_loop"
	EventAgentDown   EventType = "agent_down"
	EventAgentUp     EventType = "agent_up"
	EventDeployFail  EventType = "deploy_fail"
	EventRecovered   EventType = "recovered"
)

// Event is a single detected condition. Process is "" for agent-level events.
type Event struct {
	Type    EventType
	Agent   string
	Process string
	Detail  string
	Time    time.Time
	// ResolvedIn, when >0, marks a coalesced alert: the condition resolved
	// within this duration, so it renders as a single "…then recovered" notice
	// instead of a separate alert and recovery. Zero means a normal alert.
	ResolvedIn time.Duration
}

// Channel is the non-secret config of a delivery destination.
type Channel struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"` // webhook | telegram | slack | email
	Enabled bool              `json:"enabled"`
	Config  map[string]string `json:"config"`
}

// Rule routes matching events to channels.
type Rule struct {
	Name     string      `json:"name"`
	Enabled  bool        `json:"enabled"`
	Events   []EventType `json:"events"` // empty = any
	Agent    string      `json:"agent"`  // "" or "*" = any
	Process  string      `json:"process"`
	Channels []string    `json:"channels"`
}

// Settings holds dispatcher tunables.
type Settings struct {
	CooldownSeconds int `json:"cooldown_seconds"`
	// SuppressRecovery silences "recovered" notices when true. It is inverted
	// (suppress, not enable) so the zero value keeps recovery on by default,
	// including for config files written before this field existed.
	SuppressRecovery bool `json:"suppress_recovery"`
	// CooldownOverrides maps an event type to a per-type cooldown in seconds,
	// overriding CooldownSeconds for that type. A key's PRESENCE is the signal:
	// absent  = inherit the global CooldownSeconds;
	// present = use this value (including an explicit 0, which disables the
	//           cooldown for that type). The map sidesteps the
	//           int-zero-means-unset ambiguity that CooldownSeconds has.
	CooldownOverrides map[EventType]int `json:"cooldown_overrides,omitempty"`
}

// cooldownFor returns the cooldown duration for an event type: the per-type
// override if present, otherwise the global CooldownSeconds.
func (s Settings) cooldownFor(t EventType) time.Duration {
	secs := s.CooldownSeconds
	if v, ok := s.CooldownOverrides[t]; ok {
		secs = v
	}
	return time.Duration(secs) * time.Second
}

// Message is a rendered alert handed to a Sender.
type Message struct {
	Title string
	Body  string
	Event Event
}

// Sender delivers a Message over one transport.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// Matches reports whether the event should route through this rule.
func (r Rule) Matches(e Event) bool {
	if !r.Enabled {
		return false
	}
	if len(r.Events) > 0 {
		ok := false
		for _, t := range r.Events {
			if t == e.Type {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if r.Agent != "" && r.Agent != "*" && r.Agent != e.Agent {
		return false
	}
	if r.Process != "" && r.Process != "*" && r.Process != e.Process {
		return false
	}
	return true
}
