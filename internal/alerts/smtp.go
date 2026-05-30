package alerts

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
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

	// allowInsecure permits delivery to a server that does not advertise
	// STARTTLS. It defaults to false, so credentials and alert bodies are never
	// sent in cleartext unless an operator explicitly opts in for a trusted
	// internal relay. (Set via SetAllowInsecure.)
	allowInsecure bool
	// tlsConfig is used for the STARTTLS upgrade. Defaults to verifying the
	// server certificate against host. Overridable for tests via SetTLSConfig.
	tlsConfig *tls.Config
}

// NewSMTPSender creates a new SMTP email sender.
func NewSMTPSender(host string, port int, from, username, password string) *SMTPSender {
	return &SMTPSender{
		host:      host,
		port:      port,
		from:      from,
		username:  username,
		password:  password,
		tlsConfig: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12},
	}
}

// SetAllowInsecure permits sending without STARTTLS (trusted internal relays).
// Off by default; turning it on re-enables cleartext transmission.
func (s *SMTPSender) SetAllowInsecure(allow bool) { s.allowInsecure = allow }

// SetTLSConfig overrides the STARTTLS client config.
func (s *SMTPSender) SetTLSConfig(cfg *tls.Config) { s.tlsConfig = cfg }

// Send sends an HTML email via SMTP.
//
// TASK-331: it honors ctx (so the dispatcher's per-channel timeout is enforced
// and a wedged mail server cannot block the dispatch worker indefinitely) and
// prefers STARTTLS, refusing to transmit credentials/alert bodies in cleartext
// unless allowInsecure is set. The previous implementation used
// net/smtp.SendMail, which ignored the context (raw TCP, no deadline) and would
// silently fall back to plaintext on port 25.
func (s *SMTPSender) Send(ctx context.Context, to []string, subject, htmlBody string) error {
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

	// Context-aware dial: aborts if ctx is cancelled/expires before connect.
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp dial %s: %w", addr, err)
	}
	// Propagate the context deadline to the connection so the SMTP conversation
	// (greeting, EHLO, STARTTLS, AUTH, DATA) also fails fast instead of blocking
	// on OS-level TCP timeouts when the server stalls mid-exchange.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	client, err := smtp.NewClient(conn, s.host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp new client: %w", err)
	}
	defer func() { _ = client.Close() }()

	// Prefer STARTTLS; refuse cleartext unless explicitly allowed.
	if ok, _ := client.Extension("STARTTLS"); ok {
		cfg := s.tlsConfig
		if cfg == nil {
			cfg = &tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12}
		}
		if err := client.StartTLS(cfg); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	} else if !s.allowInsecure {
		return fmt.Errorf("smtp server %s does not advertise STARTTLS; refusing to send in cleartext (enable allowInsecure for trusted relays)", s.host)
	}

	if s.username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.username, s.password, s.host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := client.Mail(s.from); err != nil {
		return fmt.Errorf("smtp mail from %s: %w", s.from, err)
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp rcpt %s: %w", rcpt, err)
		}
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write([]byte(msg.String())); err != nil {
		_ = w.Close()
		return fmt.Errorf("smtp write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp data close: %w", err)
	}

	return client.Quit()
}
