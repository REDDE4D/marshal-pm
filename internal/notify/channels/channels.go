// Package channels implements notification transports behind notify.Sender.
package channels

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"marshal/internal/notify"
)

// httpDoer is the HTTP seam; tests override httpClient.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// httpClient is the default transport for webhook/slack/telegram channels. It has
// an explicit timeout so a hostile or black-hole endpoint can't hang a sender, and
// refuses to follow redirects (an operator-configured URL that 30x-bounces to an
// internal address is an SSRF vector). Tests override this seam.
var httpClient httpDoer = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("notify: refusing to follow redirect")
	},
}

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
