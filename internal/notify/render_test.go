package notify

import (
	"strings"
	"testing"
	"time"
)

func TestRenderRecoveredTitle(t *testing.T) {
	m := render(Event{Type: EventRecovered, Agent: "dev-1", Process: "api", Detail: "recovered after crash"})
	if !strings.Contains(m.Title, "Process recovered") {
		t.Fatalf("want 'Process recovered' in title, got %q", m.Title)
	}
	if !strings.Contains(m.Body, "recovered after crash") {
		t.Fatalf("want detail in body, got %q", m.Body)
	}
}

func TestRenderMergedRecovery(t *testing.T) {
	m := render(Event{Type: EventCrash, Agent: "dev-1", Process: "api", Detail: "crashed (restart #2)", ResolvedIn: 4 * time.Second})
	if !strings.Contains(m.Title, "then recovered") {
		t.Fatalf("merged title should note recovery, got %q", m.Title)
	}
	if !strings.Contains(m.Body, "recovered after 4s") {
		t.Fatalf("merged body should note recovery duration, got %q", m.Body)
	}
}

func TestRenderPlainAlertUnchanged(t *testing.T) {
	m := render(Event{Type: EventCrash, Agent: "dev-1", Process: "api", Detail: "crashed (restart #2)"})
	if strings.Contains(m.Title, "then recovered") {
		t.Fatalf("plain alert must not be marked merged, got %q", m.Title)
	}
}
