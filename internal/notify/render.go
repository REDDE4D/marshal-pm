package notify

import "fmt"

var eventTitles = map[EventType]string{
	EventCrash:       "Process crashed",
	EventRestartLoop: "Process in restart loop",
	EventAgentDown:   "Agent disconnected",
	EventAgentUp:     "Agent reconnected",
	EventDeployFail:  "Deploy failed",
	EventRecovered:   "Process recovered",
}

// render builds a human-facing Message for an event.
func render(e Event) Message {
	title := eventTitles[e.Type]
	if title == "" {
		title = string(e.Type)
	}
	who := e.Agent
	if e.Process != "" {
		who = fmt.Sprintf("%s / %s", e.Agent, e.Process)
	}
	body := fmt.Sprintf("[%s] %s: %s", who, title, e.Detail)
	return Message{Title: fmt.Sprintf("Marshal: %s (%s)", title, who), Body: body, Event: e}
}
