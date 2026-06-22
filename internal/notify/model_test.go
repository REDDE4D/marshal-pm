package notify

import "testing"

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
