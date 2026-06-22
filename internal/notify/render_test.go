package notify

import (
	"strings"
	"testing"
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
