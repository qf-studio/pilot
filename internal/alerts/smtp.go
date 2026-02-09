package alerts

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"
)

// SMTPSender sends emails via SMTP.
type SMTPSender struct {
	host     string
	port     int
	from     string
	username string
	password string
}

// NewSMTPSender creates a new SMTP email sender.
func NewSMTPSender(host string, port int, from, username, password string) *SMTPSender {
	return &SMTPSender{
		host:     host,
		port:     port,
		from:     from,
		username: username,
		password: password,
	}
}

// Send sends an HTML email via SMTP.
func (s *SMTPSender) Send(_ context.Context, to []string, subject, htmlBody string) error {
	addr := fmt.Sprintf("%s:%d", s.host, s.port)

	// Build MIME message
	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("From: %s\r\n", s.from))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", strings.Join(to, ",")))
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(htmlBody)

	var auth smtp.Auth
	if s.username != "" {
		auth = smtp.PlainAuth("", s.username, s.password, s.host)
	}

	return smtp.SendMail(addr, auth, s.from, to, []byte(msg.String()))
}
