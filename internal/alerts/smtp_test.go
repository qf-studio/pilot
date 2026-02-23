package alerts

import (
	"context"
	"net"
	"strings"
	"testing"
)

// =============================================================================
// SMTPSender Tests
// =============================================================================

func TestNewSMTPSender(t *testing.T) {
	s := NewSMTPSender("smtp.example.com", 587, "from@example.com", "user", "pass")

	if s == nil {
		t.Fatal("expected non-nil SMTPSender")
	}
	if s.host != "smtp.example.com" {
		t.Errorf("expected host 'smtp.example.com', got '%s'", s.host)
	}
	if s.port != 587 {
		t.Errorf("expected port 587, got %d", s.port)
	}
	if s.from != "from@example.com" {
		t.Errorf("expected from 'from@example.com', got '%s'", s.from)
	}
	if s.username != "user" {
		t.Errorf("expected username 'user', got '%s'", s.username)
	}
	if s.password != "pass" {
		t.Errorf("expected password 'pass', got '%s'", s.password)
	}
}

func TestNewSMTPSender_NoAuth(t *testing.T) {
	s := NewSMTPSender("localhost", 25, "noreply@example.com", "", "")

	if s.username != "" {
		t.Error("expected empty username")
	}
	if s.password != "" {
		t.Error("expected empty password")
	}
}

func TestSMTPSender_Send_ConnectionRefused(t *testing.T) {
	// Use a port that nothing is listening on
	s := NewSMTPSender("127.0.0.1", 19876, "from@example.com", "", "")

	err := s.Send(context.Background(), []string{"to@example.com"}, "Test", "<h1>Hello</h1>")
	if err == nil {
		t.Fatal("expected error when SMTP server is not reachable")
	}
}

func TestSMTPSender_Send_InvalidHost(t *testing.T) {
	s := NewSMTPSender("nonexistent.invalid.host.test", 587, "from@example.com", "", "")

	err := s.Send(context.Background(), []string{"to@example.com"}, "Test", "<h1>Hello</h1>")
	if err == nil {
		t.Fatal("expected error for invalid host")
	}
}

// fakeSMTPServer starts a minimal fake SMTP server that accepts connections
// and records the DATA section. It only implements enough of the SMTP protocol
// to test the client-side code.
func fakeSMTPServer(t *testing.T) (addr string, received *strings.Builder, cleanup func()) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}

	var buf strings.Builder
	done := make(chan struct{})

	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// SMTP greeting
		_, _ = conn.Write([]byte("220 localhost SMTP Test\r\n"))

		scanner := make([]byte, 4096)
		inData := false

		for {
			n, err := conn.Read(scanner)
			if err != nil {
				return
			}
			line := string(scanner[:n])

			if inData {
				buf.WriteString(line)
				if strings.Contains(line, "\r\n.\r\n") {
					_, _ = conn.Write([]byte("250 OK\r\n"))
					inData = false
				}
				continue
			}

			upper := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(upper, "EHLO") || strings.HasPrefix(upper, "HELO"):
				_, _ = conn.Write([]byte("250-localhost\r\n250 OK\r\n"))
			case strings.HasPrefix(upper, "MAIL FROM"):
				_, _ = conn.Write([]byte("250 OK\r\n"))
			case strings.HasPrefix(upper, "RCPT TO"):
				_, _ = conn.Write([]byte("250 OK\r\n"))
			case strings.HasPrefix(upper, "DATA"):
				_, _ = conn.Write([]byte("354 Start mail input\r\n"))
				inData = true
			case strings.HasPrefix(upper, "QUIT"):
				_, _ = conn.Write([]byte("221 Bye\r\n"))
				return
			default:
				_, _ = conn.Write([]byte("250 OK\r\n"))
			}
		}
	}()

	return listener.Addr().String(), &buf, func() {
		_ = listener.Close()
		<-done
	}
}

func TestSMTPSender_Send_WithFakeServer(t *testing.T) {
	addr, received, cleanup := fakeSMTPServer(t)
	defer cleanup()

	host, portStr, _ := net.SplitHostPort(addr)
	var port int
	for _, c := range portStr {
		port = port*10 + int(c-'0')
	}

	s := NewSMTPSender(host, port, "from@example.com", "", "")

	err := s.Send(
		context.Background(),
		[]string{"admin@example.com"},
		"Alert: Task Failed",
		"<h1>Task Failed</h1><p>Details here.</p>",
	)
	if err != nil {
		t.Fatalf("Send() unexpected error: %v", err)
	}

	body := received.String()
	if !strings.Contains(body, "From: from@example.com") {
		t.Error("expected From header in message")
	}
	if !strings.Contains(body, "To: admin@example.com") {
		t.Error("expected To header in message")
	}
	if !strings.Contains(body, "Subject: Alert: Task Failed") {
		t.Error("expected Subject header in message")
	}
	if !strings.Contains(body, "Content-Type: text/html") {
		t.Error("expected HTML content type")
	}
	if !strings.Contains(body, "<h1>Task Failed</h1>") {
		t.Error("expected HTML body content")
	}
}

func TestSMTPSender_Send_MultipleRecipients(t *testing.T) {
	addr, received, cleanup := fakeSMTPServer(t)
	defer cleanup()

	host, portStr, _ := net.SplitHostPort(addr)
	var port int
	for _, c := range portStr {
		port = port*10 + int(c-'0')
	}

	s := NewSMTPSender(host, port, "from@example.com", "", "")

	err := s.Send(
		context.Background(),
		[]string{"admin@example.com", "ops@example.com"},
		"Multi-recipient Test",
		"<p>Hello all</p>",
	)
	if err != nil {
		t.Fatalf("Send() unexpected error: %v", err)
	}

	body := received.String()
	if !strings.Contains(body, "admin@example.com,ops@example.com") {
		t.Error("expected both recipients in To header")
	}
}

func TestSMTPSender_Send_MIMEHeaders(t *testing.T) {
	addr, received, cleanup := fakeSMTPServer(t)
	defer cleanup()

	host, portStr, _ := net.SplitHostPort(addr)
	var port int
	for _, c := range portStr {
		port = port*10 + int(c-'0')
	}

	s := NewSMTPSender(host, port, "alerts@pilot.dev", "", "")

	err := s.Send(
		context.Background(),
		[]string{"test@example.com"},
		"MIME Test",
		"<html><body>Test</body></html>",
	)
	if err != nil {
		t.Fatalf("Send() unexpected error: %v", err)
	}

	body := received.String()
	if !strings.Contains(body, "MIME-Version: 1.0") {
		t.Error("expected MIME-Version header")
	}
	if !strings.Contains(body, "charset=UTF-8") {
		t.Error("expected UTF-8 charset in content type")
	}
}

func TestSMTPSender_ImplementsEmailSender(t *testing.T) {
	// Compile-time check that SMTPSender implements EmailSender
	var _ EmailSender = (*SMTPSender)(nil)
}

// =============================================================================
// Integration with EmailChannel
// =============================================================================

func TestSMTPSender_WithEmailChannel(t *testing.T) {
	addr, _, cleanup := fakeSMTPServer(t)
	defer cleanup()

	host, portStr, _ := net.SplitHostPort(addr)
	var port int
	for _, c := range portStr {
		port = port*10 + int(c-'0')
	}

	sender := NewSMTPSender(host, port, "alerts@pilot.dev", "", "")

	config := &EmailChannelConfig{
		To:      []string{"admin@example.com"},
		Subject: "[{{severity}}] {{title}}",
	}
	ch := NewEmailChannel("smtp-email", sender, config)

	alert := &Alert{
		ID:       "smtp-alert-1",
		Type:     AlertTypeTaskFailed,
		Severity: SeverityCritical,
		Title:    "Build Failed",
		Message:  "CI build failed on main branch",
	}

	err := ch.Send(context.Background(), alert)
	if err != nil {
		t.Fatalf("EmailChannel.Send() with SMTP sender: %v", err)
	}
}
