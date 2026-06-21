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
)

// Event is a single detected condition. Process is "" for agent-level events.
type Event struct {
	Type    EventType
	Agent   string
	Process string
	Detail  string
	Time    time.Time
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
