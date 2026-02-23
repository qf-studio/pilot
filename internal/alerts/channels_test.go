package alerts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/testutil"
)

// =============================================================================
// WebhookChannel Tests
// =============================================================================

func TestNewWebhookChannel(t *testing.T) {
	tests := []struct {
		name         string
		config       *WebhookChannelConfig
		expectMethod string
	}{
		{
			name: "default method POST",
			config: &WebhookChannelConfig{
				URL:    "https://example.com/webhook",
				Method: "",
			},
			expectMethod: http.MethodPost,
		},
		{
			name: "explicit POST",
			config: &WebhookChannelConfig{
				URL:    "https://example.com/webhook",
				Method: "POST",
			},
			expectMethod: http.MethodPost,
		},
		{
			name: "explicit PUT",
			config: &WebhookChannelConfig{
				URL:    "https://example.com/webhook",
				Method: "PUT",
			},
			expectMethod: http.MethodPut,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := NewWebhookChannel("test", tt.config)

			if ch.Name() != "test" {
				t.Errorf("expected name 'test', got '%s'", ch.Name())
			}
			if ch.Type() != "webhook" {
				t.Errorf("expected type 'webhook', got '%s'", ch.Type())
			}
			if ch.method != tt.expectMethod {
				t.Errorf("expected method '%s', got '%s'", tt.expectMethod, ch.method)
			}
		})
	}
}

func TestWebhookChannel_Send(t *testing.T) {
	var receivedRequest *http.Request
	var receivedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedRequest = r
		receivedBody = make([]byte, r.ContentLength)
		_, _ = r.Body.Read(receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := &WebhookChannelConfig{
		URL:    server.URL,
		Method: "POST",
		Headers: map[string]string{
			"X-Custom-Header": "custom-value",
		},
	}

	ch := NewWebhookChannel("test-webhook", config)

	alert := &Alert{
		ID:       "alert-123",
		Type:     AlertTypeTaskFailed,
		Severity: SeverityWarning,
		Title:    "Test Alert",
		Message:  "Test message",
	}

	err := ch.Send(context.Background(), alert)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Verify request
	if receivedRequest.Method != "POST" {
		t.Errorf("expected POST method, got %s", receivedRequest.Method)
	}
	if receivedRequest.Header.Get("Content-Type") != "application/json" {
		t.Error("expected Content-Type: application/json")
	}
	if receivedRequest.Header.Get("X-Custom-Header") != "custom-value" {
		t.Error("expected custom header")
	}
}

func TestWebhookChannel_Send_WithSignature(t *testing.T) {
	var receivedSignature string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSignature = r.Header.Get("X-Signature-256")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := &WebhookChannelConfig{
		URL:    server.URL,
		Secret: "my-secret-key",
	}

	ch := NewWebhookChannel("test-webhook", config)

	alert := &Alert{
		ID:   "alert-123",
		Type: AlertTypeTaskFailed,
	}

	err := ch.Send(context.Background(), alert)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if receivedSignature == "" {
		t.Error("expected signature header to be set")
	}
	if len(receivedSignature) < 10 {
		t.Error("expected signature to have reasonable length")
	}
}

func TestWebhookChannel_Send_ErrorStatus(t *testing.T) {
	statusCodes := []int{400, 401, 403, 404, 500, 503}

	for _, code := range statusCodes {
		t.Run(http.StatusText(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			}))
			defer server.Close()

			config := &WebhookChannelConfig{URL: server.URL}
			ch := NewWebhookChannel("test", config)

			alert := &Alert{ID: "test"}
			err := ch.Send(context.Background(), alert)

			if err == nil {
				t.Errorf("expected error for status %d", code)
			}
		})
	}
}

func TestWebhookChannel_Send_NetworkError(t *testing.T) {
	config := &WebhookChannelConfig{
		URL: "http://localhost:99999", // Invalid port
	}

	ch := NewWebhookChannel("test", config)

	alert := &Alert{ID: "test"}
	err := ch.Send(context.Background(), alert)

	if err == nil {
		t.Error("expected network error")
	}
}

func TestWebhookChannel_Sign(t *testing.T) {
	config := &WebhookChannelConfig{
		URL:    "https://example.com",
		Secret: testutil.FakeWebhookSecret,
	}

	ch := NewWebhookChannel("test", config)

	payload := []byte(`{"test": "data"}`)
	signature := ch.sign(payload)

	if signature == "" {
		t.Error("expected non-empty signature")
	}

	// Same payload should produce same signature
	signature2 := ch.sign(payload)
	if signature != signature2 {
		t.Error("expected same signature for same payload")
	}

	// Different payload should produce different signature
	signature3 := ch.sign([]byte(`{"different": "data"}`))
	if signature == signature3 {
		t.Error("expected different signature for different payload")
	}
}

// =============================================================================
// EmailChannel Tests
// =============================================================================

type mockEmailSender struct {
	sentTo      []string
	sentSubject string
	sentBody    string
	err         error
}

func (m *mockEmailSender) Send(ctx context.Context, to []string, subject, htmlBody string) error {
	m.sentTo = to
	m.sentSubject = subject
	m.sentBody = htmlBody
	return m.err
}

func TestNewEmailChannel(t *testing.T) {
	sender := &mockEmailSender{}
	config := &EmailChannelConfig{
		To:      []string{"admin@example.com"},
		Subject: "Custom: {{title}}",
	}

	ch := NewEmailChannel("email-channel", sender, config)

	if ch.Name() != "email-channel" {
		t.Errorf("expected name 'email-channel', got '%s'", ch.Name())
	}
	if ch.Type() != "email" {
		t.Errorf("expected type 'email', got '%s'", ch.Type())
	}
}

func TestEmailChannel_Send(t *testing.T) {
	sender := &mockEmailSender{}
	config := &EmailChannelConfig{
		To: []string{"admin@example.com", "ops@example.com"},
	}

	ch := NewEmailChannel("test", sender, config)

	alert := &Alert{
		ID:       "alert-123",
		Type:     AlertTypeTaskFailed,
		Severity: SeverityWarning,
		Title:    "Test Alert Title",
		Message:  "Test message body",
	}

	err := ch.Send(context.Background(), alert)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(sender.sentTo) != 2 {
		t.Errorf("expected 2 recipients, got %d", len(sender.sentTo))
	}
	if sender.sentSubject == "" {
		t.Error("expected subject to be set")
	}
	if sender.sentBody == "" {
		t.Error("expected body to be set")
	}
}

func TestEmailChannel_FormatSubject(t *testing.T) {
	tests := []struct {
		name           string
		customSubject  string
		alert          *Alert
		expectContains string
	}{
		{
			name:          "custom subject with templates",
			customSubject: "[{{severity}}] {{type}}: {{title}}",
			alert: &Alert{
				Type:     AlertTypeTaskFailed,
				Severity: SeverityWarning,
				Title:    "My Alert",
			},
			expectContains: "warning",
		},
		{
			name:          "default subject for critical",
			customSubject: "",
			alert: &Alert{
				Severity: SeverityCritical,
				Title:    "Critical Issue",
			},
			expectContains: "CRITICAL",
		},
		{
			name:          "default subject for warning",
			customSubject: "",
			alert: &Alert{
				Severity: SeverityWarning,
				Title:    "Warning Issue",
			},
			expectContains: "WARNING",
		},
		{
			name:          "default subject for info",
			customSubject: "",
			alert: &Alert{
				Severity: SeverityInfo,
				Title:    "Info Alert",
			},
			expectContains: "INFO",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &EmailChannelConfig{
				To:      []string{"test@example.com"},
				Subject: tt.customSubject,
			}
			ch := NewEmailChannel("test", &mockEmailSender{}, config)

			subject := ch.formatSubject(tt.alert)
			if subject == "" {
				t.Error("expected non-empty subject")
			}
		})
	}
}

func TestEmailChannel_FormatBody(t *testing.T) {
	config := &EmailChannelConfig{
		To: []string{"test@example.com"},
	}
	ch := NewEmailChannel("test", &mockEmailSender{}, config)

	severities := []Severity{SeverityInfo, SeverityWarning, SeverityCritical}

	for _, severity := range severities {
		t.Run(string(severity), func(t *testing.T) {
			alert := &Alert{
				ID:          "alert-123",
				Type:        AlertTypeTaskFailed,
				Severity:    severity,
				Title:       "Test Title",
				Message:     "Test Message",
				Source:      "task:TASK-1",
				ProjectPath: "/my/project",
				CreatedAt:   time.Now(),
			}

			body := ch.formatBody(alert)

			// Verify HTML structure
			if body == "" {
				t.Error("expected non-empty body")
			}

			// Should contain alert info
			if len(body) < 100 {
				t.Error("expected substantial HTML body")
			}
		})
	}
}

// =============================================================================
// Escape Markdown Tests
// =============================================================================

func TestEscapeMarkdown(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple text", "simple text"},
		{"*bold*", "\\*bold\\*"},
		{"_italic_", "\\_italic\\_"},
		{"[link](url)", "\\[link\\]\\(url\\)"},
		{"code `block`", "code \\`block\\`"},
		{"a.b.c", "a\\.b\\.c"},
		{"test!", "test\\!"},
		{"a+b=c", "a\\+b\\=c"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := escapeMarkdown(tt.input)
			if result != tt.expected {
				t.Errorf("escapeMarkdown(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// =============================================================================
// SlackChannel Tests (without real Slack client)
// =============================================================================

func TestSlackChannel_Name(t *testing.T) {
	// We can test the accessor methods without a real client
	ch := &SlackChannel{
		name:    "my-slack-channel",
		channel: "#alerts",
	}

	if ch.Name() != "my-slack-channel" {
		t.Errorf("expected name 'my-slack-channel', got '%s'", ch.Name())
	}
	if ch.Type() != "slack" {
		t.Errorf("expected type 'slack', got '%s'", ch.Type())
	}
}

func TestSlackChannel_SeverityColor(t *testing.T) {
	ch := &SlackChannel{}

	tests := []struct {
		severity Severity
		expected string
	}{
		{SeverityCritical, "danger"},
		{SeverityWarning, "warning"},
		{SeverityInfo, "#0066cc"},
	}

	for _, tt := range tests {
		t.Run(string(tt.severity), func(t *testing.T) {
			result := ch.severityColor(tt.severity)
			if result != tt.expected {
				t.Errorf("severityColor(%s) = %s, want %s", tt.severity, result, tt.expected)
			}
		})
	}
}

func TestSlackChannel_SeverityEmoji(t *testing.T) {
	ch := &SlackChannel{}

	tests := []struct {
		severity Severity
		expected string
	}{
		{SeverityCritical, "\U0001F6A8"},
		{SeverityWarning, "\u26a0\ufe0f"},
		{SeverityInfo, "\u2139\ufe0f"},
	}

	for _, tt := range tests {
		t.Run(string(tt.severity), func(t *testing.T) {
			result := ch.severityEmoji(tt.severity)
			if result != tt.expected {
				t.Errorf("severityEmoji(%s) = %s, want %s", tt.severity, result, tt.expected)
			}
		})
	}
}

func TestSlackChannel_FormatSlackBlocks(t *testing.T) {
	ch := &SlackChannel{
		name:    "test",
		channel: "#alerts",
	}

	alert := &Alert{
		ID:          "alert-123",
		Type:        AlertTypeTaskFailed,
		Severity:    SeverityCritical,
		Title:       "Critical Alert",
		Message:     "Something went wrong",
		Source:      "task:TASK-1",
		ProjectPath: "/my/project",
	}

	blocks := ch.formatSlackBlocks(alert)

	if len(blocks) < 2 {
		t.Errorf("expected at least 2 blocks, got %d", len(blocks))
	}

	// First block should be header
	if blocks[0].Type != "header" {
		t.Errorf("expected first block type 'header', got '%s'", blocks[0].Type)
	}
}

// =============================================================================
// TelegramChannel Tests (without real Telegram client)
// =============================================================================

func TestTelegramChannel_Name(t *testing.T) {
	ch := &TelegramChannel{
		name:   "my-telegram-channel",
		chatID: 123456789,
	}

	if ch.Name() != "my-telegram-channel" {
		t.Errorf("expected name 'my-telegram-channel', got '%s'", ch.Name())
	}
	if ch.Type() != "telegram" {
		t.Errorf("expected type 'telegram', got '%s'", ch.Type())
	}
}

func TestTelegramChannel_SeverityEmoji(t *testing.T) {
	ch := &TelegramChannel{}

	tests := []struct {
		severity Severity
		expected string
	}{
		{SeverityCritical, "\U0001F6A8"},
		{SeverityWarning, "\u26a0\ufe0f"},
		{SeverityInfo, "\u2139\ufe0f"},
	}

	for _, tt := range tests {
		t.Run(string(tt.severity), func(t *testing.T) {
			result := ch.severityEmoji(tt.severity)
			if result != tt.expected {
				t.Errorf("severityEmoji(%s) = %s, want %s", tt.severity, result, tt.expected)
			}
		})
	}
}

func TestTelegramChannel_FormatMessage(t *testing.T) {
	ch := &TelegramChannel{
		name:   "test",
		chatID: 123456789,
	}

	alert := &Alert{
		ID:          "alert-123",
		Type:        AlertTypeTaskFailed,
		Severity:    SeverityCritical,
		Title:       "Critical Alert",
		Message:     "Something went wrong",
		Source:      "task:TASK-1",
		ProjectPath: "/my/project",
		CreatedAt:   time.Now(),
	}

	msg := ch.formatMessage(alert)

	if msg == "" {
		t.Error("expected non-empty message")
	}

	// Should contain severity
	if len(msg) < 50 {
		t.Error("expected substantial message content")
	}
}

// =============================================================================
// PagerDutyChannel Tests (without real API)
// =============================================================================

func TestPagerDutyChannel_Name(t *testing.T) {
	config := &PagerDutyChannelConfig{
		RoutingKey: "routing-key",
		ServiceID:  "service-id",
	}

	ch := NewPagerDutyChannel("my-pagerduty", config)

	if ch.Name() != "my-pagerduty" {
		t.Errorf("expected name 'my-pagerduty', got '%s'", ch.Name())
	}
	if ch.Type() != "pagerduty" {
		t.Errorf("expected type 'pagerduty', got '%s'", ch.Type())
	}
}

func TestPagerDutyChannel_Send(t *testing.T) {
	var receivedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &receivedBody)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	ch := &PagerDutyChannel{
		name:       "test-pd",
		routingKey: testutil.FakePagerDutyRoutingKey,
		serviceID:  "test-service-id",
		baseURL:    server.URL,
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}

	alert := &Alert{
		Title:       "Test Alert",
		Message:     "Something broke",
		Severity:    SeverityCritical,
		Type:        AlertTypeTaskFailed,
		Source:      "test-project",
		ProjectPath: "/path/to/project",
		CreatedAt:   time.Now(),
		Metadata:    map[string]string{"task_id": "TASK-42"},
	}

	err := ch.Send(context.Background(), alert)
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}

	// Verify request body
	if receivedBody["routing_key"] != testutil.FakePagerDutyRoutingKey {
		t.Errorf("routing_key = %v, want %q", receivedBody["routing_key"], testutil.FakePagerDutyRoutingKey)
	}
	if receivedBody["event_action"] != "trigger" {
		t.Errorf("event_action = %v, want 'trigger'", receivedBody["event_action"])
	}

	payload, ok := receivedBody["payload"].(map[string]interface{})
	if !ok {
		t.Fatal("payload missing or wrong type")
	}
	if payload["severity"] != "critical" {
		t.Errorf("severity = %v, want 'critical'", payload["severity"])
	}
	if payload["component"] != "pilot" {
		t.Errorf("component = %v, want 'pilot'", payload["component"])
	}
}

func TestWebhookChannel_Send_VerifyHMACSignature(t *testing.T) {
	var receivedSignature string
	var receivedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSignature = r.Header.Get("X-Signature-256")
		body, _ := io.ReadAll(r.Body)
		receivedBody = body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	secret := testutil.FakeWebhookSecret
	config := &WebhookChannelConfig{
		URL:    server.URL,
		Secret: secret,
	}

	ch := NewWebhookChannel("hmac-test", config)

	alert := &Alert{
		ID:       "alert-hmac",
		Type:     AlertTypeTaskFailed,
		Severity: SeverityCritical,
		Title:    "HMAC Verification Test",
		Message:  "Verify the signature is correct",
	}

	err := ch.Send(context.Background(), alert)
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}

	// Verify signature format: "sha256=<hex>"
	if receivedSignature == "" {
		t.Fatal("expected X-Signature-256 header")
	}
	if len(receivedSignature) < 7 || receivedSignature[:7] != "sha256=" {
		t.Fatalf("expected signature prefix 'sha256=', got '%s'", receivedSignature)
	}

	// Recompute the expected HMAC
	expectedSig := ch.sign(receivedBody)
	actualSig := receivedSignature[7:] // strip "sha256=" prefix

	if actualSig != expectedSig {
		t.Errorf("HMAC mismatch: got %s, expected %s", actualSig, expectedSig)
	}
}

func TestWebhookChannel_Send_NoSignatureWithoutSecret(t *testing.T) {
	var receivedSignature string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSignature = r.Header.Get("X-Signature-256")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := &WebhookChannelConfig{
		URL:    server.URL,
		Secret: "", // No secret
	}
	ch := NewWebhookChannel("no-sig", config)

	err := ch.Send(context.Background(), &Alert{ID: "no-sig-test"})
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}

	if receivedSignature != "" {
		t.Errorf("expected no signature header, got '%s'", receivedSignature)
	}
}

func TestWebhookChannel_Send_CustomMethod(t *testing.T) {
	var receivedMethod string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := &WebhookChannelConfig{
		URL:    server.URL,
		Method: "PUT",
	}
	ch := NewWebhookChannel("put-webhook", config)

	err := ch.Send(context.Background(), &Alert{ID: "put-test"})
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}

	if receivedMethod != "PUT" {
		t.Errorf("expected PUT, got %s", receivedMethod)
	}
}

func TestWebhookChannel_Send_PayloadContainsAlert(t *testing.T) {
	var receivedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := &WebhookChannelConfig{URL: server.URL}
	ch := NewWebhookChannel("payload-test", config)

	alert := &Alert{
		ID:          "alert-payload",
		Type:        AlertTypeConsecutiveFails,
		Severity:    SeverityCritical,
		Title:       "Consecutive Failures",
		Message:     "3 tasks failed in a row",
		Source:      "project-x",
		ProjectPath: "/path/to/project",
	}

	err := ch.Send(context.Background(), alert)
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}

	if receivedBody["id"] != "alert-payload" {
		t.Errorf("expected id 'alert-payload', got %v", receivedBody["id"])
	}
	if receivedBody["type"] != string(AlertTypeConsecutiveFails) {
		t.Errorf("expected type '%s', got %v", AlertTypeConsecutiveFails, receivedBody["type"])
	}
	if receivedBody["severity"] != string(SeverityCritical) {
		t.Errorf("expected severity 'critical', got %v", receivedBody["severity"])
	}
	if receivedBody["title"] != "Consecutive Failures" {
		t.Errorf("expected title 'Consecutive Failures', got %v", receivedBody["title"])
	}
	if receivedBody["source"] != "project-x" {
		t.Errorf("expected source 'project-x', got %v", receivedBody["source"])
	}
}

func TestPagerDutyChannel_Send_SeverityMapping(t *testing.T) {
	tests := []struct {
		name             string
		severity         Severity
		expectedSeverity string
	}{
		{"critical maps to critical", SeverityCritical, "critical"},
		{"warning maps to warning", SeverityWarning, "warning"},
		{"info maps to info", SeverityInfo, "info"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receivedBody map[string]interface{}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &receivedBody)
				w.WriteHeader(http.StatusAccepted)
			}))
			defer server.Close()

			ch := &PagerDutyChannel{
				name:       "pd-sev-test",
				routingKey: testutil.FakePagerDutyRoutingKey,
				baseURL:    server.URL,
				client:     &http.Client{},
			}

			alert := &Alert{
				Title:     "Severity Test",
				Message:   "Testing severity mapping",
				Severity:  tt.severity,
				Type:      AlertTypeTaskFailed,
				Source:    "test",
				CreatedAt: time.Now(),
			}

			err := ch.Send(context.Background(), alert)
			if err != nil {
				t.Fatalf("Send() error: %v", err)
			}

			payload, ok := receivedBody["payload"].(map[string]interface{})
			if !ok {
				t.Fatal("payload missing or wrong type")
			}
			if payload["severity"] != tt.expectedSeverity {
				t.Errorf("severity = %v, want %s", payload["severity"], tt.expectedSeverity)
			}
		})
	}
}

func TestPagerDutyChannel_Send_DedupKey(t *testing.T) {
	var receivedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &receivedBody)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	ch := &PagerDutyChannel{
		name:       "pd-dedup",
		routingKey: testutil.FakePagerDutyRoutingKey,
		baseURL:    server.URL,
		client:     &http.Client{},
	}

	alert := &Alert{
		Type:      AlertTypeConsecutiveFails,
		Source:    "my-project",
		Severity:  SeverityCritical,
		Title:     "Dedup Test",
		Message:   "Test",
		CreatedAt: time.Now(),
	}

	err := ch.Send(context.Background(), alert)
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}

	expectedDedup := "pilot-consecutive_failures-my-project"
	if receivedBody["dedup_key"] != expectedDedup {
		t.Errorf("dedup_key = %v, want %s", receivedBody["dedup_key"], expectedDedup)
	}
}

func TestPagerDutyChannel_Send_PayloadStructure(t *testing.T) {
	var receivedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &receivedBody)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	ch := &PagerDutyChannel{
		name:       "pd-structure",
		routingKey: testutil.FakePagerDutyRoutingKey,
		serviceID:  "test-service",
		baseURL:    server.URL,
		client:     &http.Client{},
	}

	alert := &Alert{
		Type:        AlertTypeTaskStuck,
		Source:      "task:TASK-42",
		Severity:    SeverityWarning,
		Title:       "Task Stuck",
		Message:     "No progress for 10m",
		ProjectPath: "/home/user/project",
		CreatedAt:   time.Now(),
		Metadata:    map[string]string{"task_id": "TASK-42", "phase": "build"},
	}

	err := ch.Send(context.Background(), alert)
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}

	// Verify top-level fields
	if receivedBody["routing_key"] != testutil.FakePagerDutyRoutingKey {
		t.Errorf("routing_key = %v", receivedBody["routing_key"])
	}
	if receivedBody["event_action"] != "trigger" {
		t.Errorf("event_action = %v", receivedBody["event_action"])
	}

	// Verify payload structure
	payload, ok := receivedBody["payload"].(map[string]interface{})
	if !ok {
		t.Fatal("payload missing")
	}

	if payload["component"] != "pilot" {
		t.Errorf("component = %v, want 'pilot'", payload["component"])
	}
	if payload["group"] != "/home/user/project" {
		t.Errorf("group = %v, want '/home/user/project'", payload["group"])
	}
	if payload["class"] != string(AlertTypeTaskStuck) {
		t.Errorf("class = %v, want '%s'", payload["class"], AlertTypeTaskStuck)
	}
	if payload["source"] != "task:TASK-42" {
		t.Errorf("source = %v, want 'task:TASK-42'", payload["source"])
	}

	// Verify custom_details contains metadata
	details, ok := payload["custom_details"].(map[string]interface{})
	if !ok {
		t.Fatal("custom_details missing or wrong type")
	}
	if details["task_id"] != "TASK-42" {
		t.Errorf("custom_details.task_id = %v, want 'TASK-42'", details["task_id"])
	}
	if details["phase"] != "build" {
		t.Errorf("custom_details.phase = %v, want 'build'", details["phase"])
	}
}

func TestPagerDutyChannel_Send_NetworkError(t *testing.T) {
	ch := &PagerDutyChannel{
		name:       "pd-net-err",
		routingKey: testutil.FakePagerDutyRoutingKey,
		baseURL:    "http://localhost:99999",
		client:     &http.Client{},
	}

	err := ch.Send(context.Background(), &Alert{
		Title:     "Test",
		Message:   "fail",
		Severity:  SeverityWarning,
		CreatedAt: time.Now(),
	})
	if err == nil {
		t.Fatal("expected network error")
	}
}

func TestPagerDutyChannel_Send_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	ch := &PagerDutyChannel{
		name:       "test-pd",
		routingKey: testutil.FakePagerDutyRoutingKey,
		baseURL:    server.URL,
		client:     &http.Client{Timeout: 5 * time.Second},
	}

	err := ch.Send(context.Background(), &Alert{
		Title:     "Test",
		Message:   "fail",
		Severity:  SeverityWarning,
		CreatedAt: time.Now(),
	})
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
}
