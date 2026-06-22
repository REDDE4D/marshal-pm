// Package channels implements notification transports behind notify.Sender.
package channels

import (
	"fmt"
	"net/http"

	"marshal/internal/notify"
)

// httpDoer is the HTTP seam; tests override httpClient.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

var httpClient httpDoer = http.DefaultClient

// New builds a Sender for the channel's type.
func New(c notify.Channel, secrets map[string]string) (notify.Sender, error) {
	switch c.Type {
	case "webhook":
		return newWebhook(c, secrets)
	case "telegram":
		return newTelegram(c, secrets)
	case "slack":
		return newSlack(c, secrets)
	case "email":
		return newEmail(c, secrets)
	default:
		return nil, fmt.Errorf("unknown channel type %q", c.Type)
	}
}

// temporary stubs — replaced in Tasks 9 and 10
func newTelegram(notify.Channel, map[string]string) (notify.Sender, error) {
	return nil, fmt.Errorf("telegram: not yet")
}
func newSlack(notify.Channel, map[string]string) (notify.Sender, error) {
	return nil, fmt.Errorf("slack: not yet")
}
func newEmail(notify.Channel, map[string]string) (notify.Sender, error) {
	return nil, fmt.Errorf("email: not yet")
}
