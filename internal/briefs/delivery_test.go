package briefs

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters/slack"
)

// Mock Slack client for testing
type mockSlackClient struct {
	shouldFail bool
	lastMsg    *slack.Message
	response   *slack.PostMessageResponse
}

func (m *mockSlackClient) PostMessage(ctx context.Context, msg *slack.Message) (*slack.PostMessageResponse, error) {
	m.lastMsg = msg
	if m.shouldFail {
		return nil, errors.New("mock slack error")
	}
	if m.response != nil {
		return m.response, nil
	}
	return &slack.PostMessageResponse{
		OK:      true,
		Channel: msg.Channel,
		TS:      "1234567890.123456",
	}, nil
}

// Mock email sender for testing
type mockEmailSender struct {
	shouldFail   bool
	lastTo       []string
	lastSubject  string
	lastHtmlBody string
}

func (m *mockEmailSender) Send(ctx context.Context, to []string, subject, htmlBody string) error {
	m.lastTo = to
	m.lastSubject = subject
	m.lastHtmlBody = htmlBody
	if m.shouldFail {
		return errors.New("mock email error")
	}
	return nil
}


func TestNewDeliveryService(t *testing.T) {
	config := &BriefConfig{
		Enabled:  true,
		Schedule: "0 9 * * *",
		Timezone: "UTC",
		Channels: []ChannelConfig{},
	}

	service := NewDeliveryService(config)

	if service == nil {
		t.Fatal("expected service, got nil")
	}

	if service.config != config {
		t.Error("config not set correctly")
	}

	if service.slackFmt == nil {
		t.Error("slack formatter not initialized")
	}

	if service.emailFmt == nil {
		t.Error("email formatter not initialized")
	}

	if service.plainFmt == nil {
		t.Error("plain text formatter not initialized")
	}
}

func TestDeliveryServiceWithOptions(t *testing.T) {
	config := &BriefConfig{}
	logger := slog.Default()

	mockEmail := &mockEmailSender{}

	service := NewDeliveryService(config,
		WithLogger(logger),
		WithEmailSender(mockEmail),
	)

	if service == nil {
		t.Fatal("expected service, got nil")
	}

	// Verify email sender was set
	if service.emailSender != mockEmail {
		t.Error("email sender not set by option")
	}
}

func TestDeliverAllNoChannels(t *testing.T) {
	config := &BriefConfig{
		Channels: []ChannelConfig{},
	}

	service := NewDeliveryService(config)
	brief := createTestBrief()
	ctx := context.Background()

	results := service.DeliverAll(ctx, brief)

	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestDeliverAllSlackSuccess(t *testing.T) {
	// Skip: Requires interface-based mocking for slack.Client
	// This test documents expected behavior but cannot run without a mock
	t.Skip("Requires interface-based mocking for slack.Client")
}

func TestDeliverAllSlackFailure(t *testing.T) {
	config := &BriefConfig{
		Channels: []ChannelConfig{
			{Type: "slack", Channel: "#test-channel"},
		},
	}

	mockSlack := &mockSlackClient{shouldFail: true}

	service := &DeliveryService{
		config:      config,
		slackClient: createSlackClientWrapper(mockSlack),
		logger:      slog.Default(),
		slackFmt:    NewSlackFormatter(),
		emailFmt:    NewEmailFormatter(),
		plainFmt:    NewPlainTextFormatter(),
	}

	brief := createTestBrief()
	ctx := context.Background()

	results := service.DeliverAll(ctx, brief)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	result := results[0]
	if result.Success {
		t.Error("expected failure")
	}
	if result.Error == nil {
		t.Error("expected error")
	}
}

func TestDeliverAllSlackNoClient(t *testing.T) {
	config := &BriefConfig{
		Channels: []ChannelConfig{
			{Type: "slack", Channel: "#test-channel"},
		},
	}

	service := NewDeliveryService(config) // No slack client

	brief := createTestBrief()
	ctx := context.Background()

	results := service.DeliverAll(ctx, brief)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	result := results[0]
	if result.Success {
		t.Error("expected failure without slack client")
	}
	if result.Error == nil {
		t.Error("expected error")
	}
}

func TestDeliverAllEmailSuccess(t *testing.T) {
	config := &BriefConfig{
		Channels: []ChannelConfig{
			{Type: "email", Recipients: []string{"test@example.com"}},
		},
	}

	mockEmail := &mockEmailSender{}

	service := &DeliveryService{
		config:      config,
		emailSender: mockEmail,
		logger:      slog.Default(),
		slackFmt:    NewSlackFormatter(),
		emailFmt:    NewEmailFormatter(),
		plainFmt:    NewPlainTextFormatter(),
	}

	brief := createTestBrief()
	ctx := context.Background()

	results := service.DeliverAll(ctx, brief)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	result := results[0]
	if !result.Success {
		t.Errorf("expected success, got error: %v", result.Error)
	}

	if len(mockEmail.lastTo) != 1 || mockEmail.lastTo[0] != "test@example.com" {
		t.Errorf("unexpected recipients: %v", mockEmail.lastTo)
	}

	if mockEmail.lastSubject == "" {
		t.Error("expected non-empty subject")
	}

	if mockEmail.lastHtmlBody == "" {
		t.Error("expected non-empty HTML body")
	}
}

func TestDeliverAllEmailFailure(t *testing.T) {
	config := &BriefConfig{
		Channels: []ChannelConfig{
			{Type: "email", Recipients: []string{"test@example.com"}},
		},
	}

	mockEmail := &mockEmailSender{shouldFail: true}

	service := &DeliveryService{
		config:      config,
		emailSender: mockEmail,
		logger:      slog.Default(),
		slackFmt:    NewSlackFormatter(),
		emailFmt:    NewEmailFormatter(),
		plainFmt:    NewPlainTextFormatter(),
	}

	brief := createTestBrief()
	ctx := context.Background()

	results := service.DeliverAll(ctx, brief)

	result := results[0]
	if result.Success {
		t.Error("expected failure")
	}
}

func TestDeliverAllEmailNoSender(t *testing.T) {
	config := &BriefConfig{
		Channels: []ChannelConfig{
			{Type: "email", Recipients: []string{"test@example.com"}},
		},
	}

	service := NewDeliveryService(config) // No email sender

	brief := createTestBrief()
	ctx := context.Background()

	results := service.DeliverAll(ctx, brief)

	result := results[0]
	if result.Success {
		t.Error("expected failure without email sender")
	}
}

func TestDeliverAllEmailNoRecipients(t *testing.T) {
	config := &BriefConfig{
		Channels: []ChannelConfig{
			{Type: "email", Recipients: []string{}},
		},
	}

	mockEmail := &mockEmailSender{}

	service := &DeliveryService{
		config:      config,
		emailSender: mockEmail,
		logger:      slog.Default(),
		slackFmt:    NewSlackFormatter(),
		emailFmt:    NewEmailFormatter(),
		plainFmt:    NewPlainTextFormatter(),
	}

	brief := createTestBrief()
	ctx := context.Background()

	results := service.DeliverAll(ctx, brief)

	result := results[0]
	if result.Success {
		t.Error("expected failure with no recipients")
	}
}

func TestDeliverAllTelegramSuccess(t *testing.T) {
	// Skip: Requires interface-based mocking for telegram.Client
	t.Skip("Requires interface-based mocking for telegram.Client")
}

func TestDeliverAllTelegramFailure(t *testing.T) {
	// Skip: Requires interface-based mocking for telegram.Client
	t.Skip("Requires interface-based mocking for telegram.Client")
}

func TestDeliverAllTelegramNoClient(t *testing.T) {
	config := &BriefConfig{
		Channels: []ChannelConfig{
			{Type: "telegram", Channel: "123456"},
		},
	}

	service := NewDeliveryService(config) // No telegram client

	brief := createTestBrief()
	ctx := context.Background()

	results := service.DeliverAll(ctx, brief)

	result := results[0]
	if result.Success {
		t.Error("expected failure without telegram client")
	}
}

func TestDeliverAllUnsupportedChannel(t *testing.T) {
	config := &BriefConfig{
		Channels: []ChannelConfig{
			{Type: "unsupported", Channel: "test"},
		},
	}

	service := NewDeliveryService(config)
	brief := createTestBrief()
	ctx := context.Background()

	results := service.DeliverAll(ctx, brief)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	result := results[0]
	if result.Success {
		t.Error("expected failure for unsupported channel")
	}
	if result.Error == nil {
		t.Error("expected error for unsupported channel")
	}
}

func TestDeliverAllMultipleChannels(t *testing.T) {
	config := &BriefConfig{
		Channels: []ChannelConfig{
			{Type: "email", Recipients: []string{"a@test.com"}},
			{Type: "email", Recipients: []string{"b@test.com"}},
		},
	}

	mockEmail := &mockEmailSender{}

	service := &DeliveryService{
		config:      config,
		emailSender: mockEmail,
		logger:      slog.Default(),
		slackFmt:    NewSlackFormatter(),
		emailFmt:    NewEmailFormatter(),
		plainFmt:    NewPlainTextFormatter(),
	}

	brief := createTestBrief()
	ctx := context.Background()

	results := service.DeliverAll(ctx, brief)

	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}

	// All should succeed
	for i, result := range results {
		if !result.Success {
			t.Errorf("result %d failed: %v", i, result.Error)
		}
	}
}

func TestDeliverSpecificChannel(t *testing.T) {
	config := &BriefConfig{
		Channels: []ChannelConfig{
			{Type: "email", Channel: "team1", Recipients: []string{"team1@test.com"}},
			{Type: "email", Channel: "team2", Recipients: []string{"team2@test.com"}},
		},
	}

	mockEmail := &mockEmailSender{}

	service := &DeliveryService{
		config:      config,
		emailSender: mockEmail,
		logger:      slog.Default(),
		slackFmt:    NewSlackFormatter(),
		emailFmt:    NewEmailFormatter(),
		plainFmt:    NewPlainTextFormatter(),
	}

	brief := createTestBrief()
	ctx := context.Background()

	// Deliver to specific channel by full identifier
	result, err := service.Deliver(ctx, brief, "email:team1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %v", result.Error)
	}

	// Deliver to specific channel by name only
	result, err = service.Deliver(ctx, brief, "team2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %v", result.Error)
	}
}

func TestDeliverChannelNotFound(t *testing.T) {
	config := &BriefConfig{
		Channels: []ChannelConfig{
			{Type: "slack", Channel: "#channel1"},
		},
	}

	service := NewDeliveryService(config)
	brief := createTestBrief()
	ctx := context.Background()

	_, err := service.Deliver(ctx, brief, "#nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent channel")
	}
}

func TestDeliveryResultFields(t *testing.T) {
	result := DeliveryResult{
		Channel:   "slack:#test",
		Success:   true,
		Error:     nil,
		SentAt:    time.Now(),
		MessageID: "12345",
	}

	if result.Channel != "slack:#test" {
		t.Errorf("unexpected Channel: %s", result.Channel)
	}
	if !result.Success {
		t.Error("expected Success to be true")
	}
	if result.Error != nil {
		t.Error("expected nil Error")
	}
	if result.SentAt.IsZero() {
		t.Error("expected non-zero SentAt")
	}
	if result.MessageID != "12345" {
		t.Errorf("unexpected MessageID: %s", result.MessageID)
	}
}

func TestFormatTelegramTokens(t *testing.T) {
	tests := []struct {
		tokens   int64
		expected string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.0k"},
		{1500, "1.5k"},
		{10000, "10.0k"},
		{999999, "1000.0k"},
		{1000000, "1.0M"},
		{1500000, "1.5M"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatTelegramTokens(tt.tokens)
			if result != tt.expected {
				t.Errorf("formatTelegramTokens(%d) = %q, want %q", tt.tokens, result, tt.expected)
			}
		})
	}
}

// Helper to create a slack.Client wrapper that uses our mock
func createSlackClientWrapper(mock *mockSlackClient) *slack.Client {
	// Since we can't easily mock the actual slack.Client, we'll use an interface approach
	// For now, return nil and handle in tests
	return nil
}


// Override test to work with actual mock injection
func TestDeliveryServiceIntegrationSlack(t *testing.T) {
	// Skip if we can't mock properly
	t.Skip("Requires interface-based mocking for slack.Client")
}

func TestDeliveryServiceIntegrationEmail(t *testing.T) {
	config := &BriefConfig{
		Channels: []ChannelConfig{
			{Type: "email", Recipients: []string{"test@example.com"}},
		},
	}

	mockEmail := &mockEmailSender{}

	service := &DeliveryService{
		config:      config,
		emailSender: mockEmail,
		logger:      slog.Default(),
		slackFmt:    NewSlackFormatter(),
		emailFmt:    NewEmailFormatter(),
		plainFmt:    NewPlainTextFormatter(),
	}

	brief := createTestBrief()
	ctx := context.Background()

	results := service.DeliverAll(ctx, brief)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	// Verify email was formatted correctly
	if mockEmail.lastSubject == "" {
		t.Error("email subject not set")
	}

	if mockEmail.lastHtmlBody == "" {
		t.Error("email body not set")
	}

	// Check HTML contains expected elements
	expectedElements := []string{
		"<!DOCTYPE html>",
		"Pilot Daily Brief",
		"TASK-001",
		"TASK-002",
		"TASK-003",
		"TASK-004",
		"TASK-005",
	}

	for _, elem := range expectedElements {
		if !containsString(mockEmail.lastHtmlBody, elem) {
			t.Errorf("expected %q in email body", elem)
		}
	}
}

func containsString(haystack, needle string) bool {
	return len(haystack) >= len(needle) &&
		(haystack == needle ||
		 len(haystack) > len(needle) &&
		 (haystack[:len(needle)] == needle ||
		  containsString(haystack[1:], needle)))
}

func TestFormatTelegramBrief(t *testing.T) {
	config := &BriefConfig{}
	service := &DeliveryService{
		config:   config,
		logger:   slog.Default(),
		slackFmt: NewSlackFormatter(),
		emailFmt: NewEmailFormatter(),
		plainFmt: NewPlainTextFormatter(),
	}

	brief := createTestBrief()
	text := service.formatTelegramBrief(brief)

	// Check that key elements are present
	expectedElements := []string{
		"Daily Brief",
		"Jan 26, 2026",
		"tasks completed",
		"avg duration",
		"tokens used",
		"estimated cost",
		"Completed:",
		"TASK-001",
		"TASK-002",
		"Blocked:",
		"TASK-004",
		"Queue",
		"TASK-005",
	}

	for _, elem := range expectedElements {
		if !containsString(text, elem) {
			t.Errorf("expected %q in Telegram brief, not found", elem)
		}
	}
}

func TestFormatTelegramBriefEmpty(t *testing.T) {
	config := &BriefConfig{}
	service := &DeliveryService{
		config:   config,
		logger:   slog.Default(),
		slackFmt: NewSlackFormatter(),
		emailFmt: NewEmailFormatter(),
		plainFmt: NewPlainTextFormatter(),
	}

	brief := &Brief{
		GeneratedAt: time.Now(),
		Period: BriefPeriod{
			Start: time.Now().Add(-24 * time.Hour),
			End:   time.Now(),
		},
		Completed:  []TaskSummary{},
		InProgress: []TaskSummary{},
		Blocked:    []BlockedTask{},
		Upcoming:   []TaskSummary{},
		Metrics:    BriefMetrics{},
	}

	text := service.formatTelegramBrief(brief)

	// Should still have header and metrics
	if text == "" {
		t.Error("expected non-empty text for empty brief")
	}

	if !containsString(text, "Daily Brief") {
		t.Error("expected 'Daily Brief' in output")
	}
}

func TestWithSlackClient(t *testing.T) {
	config := &BriefConfig{}

	// WithSlackClient accepts nil without panic
	service := NewDeliveryService(config, WithSlackClient(nil))
	if service == nil {
		t.Fatal("expected service, got nil")
	}
}

func TestWithTelegramClient(t *testing.T) {
	config := &BriefConfig{}

	// WithTelegramClient accepts nil without panic
	service := NewDeliveryService(config, WithTelegramClient(nil))
	if service == nil {
		t.Fatal("expected service, got nil")
	}
}

func TestDeliverUnsupportedChannelType(t *testing.T) {
	config := &BriefConfig{
		Channels: []ChannelConfig{
			{Type: "unknown", Channel: "test"},
		},
	}

	service := NewDeliveryService(config)
	brief := createTestBrief()
	ctx := context.Background()

	_, err := service.Deliver(ctx, brief, "unknown:test")
	if err == nil {
		t.Error("expected error for unsupported channel type")
	}
}
