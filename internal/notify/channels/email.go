package channels

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"

	"marshal/internal/notify"
)

// smtpSend is the SMTP seam; tests override it.
var smtpSend = smtp.SendMail

type email struct {
	host, port, from, to, username, password string
}

func newEmail(c notify.Channel, secrets map[string]string) (notify.Sender, error) {
	e := &email{
		host:     c.Config["host"],
		port:     c.Config["port"],
		from:     c.Config["from"],
		to:       c.Config["to"],
		username: c.Config["username"],
		password: secrets["password"],
	}
	if e.host == "" || e.port == "" || e.from == "" || e.to == "" {
		return nil, fmt.Errorf("email: host, port, from, to required")
	}
	return e, nil
}

// stripCRLF removes CR/LF so a value can't inject extra headers.
func stripCRLF(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

func (e *email) Send(_ context.Context, m notify.Message) error {
	msg := []byte(fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s\r\n",
		e.from, e.to, stripCRLF(m.Title), m.Body))
	var auth smtp.Auth
	if e.username != "" {
		auth = smtp.PlainAuth("", e.username, e.password, e.host)
	}
	return smtpSend(e.host+":"+e.port, auth, e.from, []string{e.to}, msg)
}
