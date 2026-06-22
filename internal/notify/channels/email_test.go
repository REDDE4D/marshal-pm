package channels

import (
	"context"
	"net/smtp"
	"strings"
	"testing"

	"marshal/internal/notify"
)

func TestEmailSend(t *testing.T) {
	var gotAddr, gotFrom string
	var gotTo []string
	var gotMsg string
	old := smtpSend
	smtpSend = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		gotAddr, gotFrom, gotTo, gotMsg = addr, from, to, string(msg)
		return nil
	}
	t.Cleanup(func() { smtpSend = old })

	s, err := New(notify.Channel{Name: "mail", Type: "email", Config: map[string]string{
		"host": "smtp.test", "port": "587", "from": "marshal@test", "to": "ops@test", "username": "marshal@test",
	}}, map[string]string{"password": "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), notify.Message{Title: "Subj", Body: "Body"}); err != nil {
		t.Fatal(err)
	}
	if gotAddr != "smtp.test:587" || gotFrom != "marshal@test" || len(gotTo) != 1 || gotTo[0] != "ops@test" {
		t.Fatalf("envelope wrong: %s %s %v", gotAddr, gotFrom, gotTo)
	}
	if !strings.Contains(gotMsg, "Subject: Subj") || !strings.Contains(gotMsg, "Body") {
		t.Fatalf("message wrong: %q", gotMsg)
	}
}

func TestEmailSubjectNoHeaderInjection(t *testing.T) {
	var gotMsg string
	old := smtpSend
	smtpSend = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		gotMsg = string(msg)
		return nil
	}
	t.Cleanup(func() { smtpSend = old })

	s, err := New(notify.Channel{Name: "mail", Type: "email", Config: map[string]string{
		"host": "smtp.test", "port": "587", "from": "marshal@test", "to": "ops@test",
	}}, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), notify.Message{
		Title: "Evil\r\nBcc: attacker@evil.test",
		Body:  "body",
	}); err != nil {
		t.Fatal(err)
	}
	// After CRLF stripping the injected text must appear on the Subject line,
	// not as a separate header. Check that no standalone "Bcc:" line exists.
	for _, line := range strings.Split(gotMsg, "\n") {
		if strings.HasPrefix(strings.TrimRight(line, "\r"), "Bcc:") {
			t.Fatalf("SMTP header injection not prevented; message contains injected Bcc header:\n%s", gotMsg)
		}
	}
	if !strings.Contains(gotMsg, "Subject:") {
		t.Fatalf("Subject header missing from message:\n%s", gotMsg)
	}
}
