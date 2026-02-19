package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alekspetrov/pilot/internal/adapters/telegram"
)

func newTestNotifier(t *testing.T, captureText *string) (*TelegramNotifier, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req telegram.SendMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		*captureText = req.Text

		resp := telegram.SendMessageResponse{OK: true}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))

	client := telegram.NewClientWithBaseURL("test-token", server.URL)
	notifier := NewTelegramNotifier(client, "123456")
	return notifier, server
}

func TestTelegramNotifier_NotifyMerged(t *testing.T) {
	var receivedText string
	notifier, server := newTestNotifier(t, &receivedText)
	defer server.Close()

	prState := &PRState{
		PRNumber:        42,
		Stage:           StageMerged,
		EnvironmentName: "staging",
		PRTitle:         "feat: add search",
		TargetBranch:    "main",
	}

	err := notifier.NotifyMerged(context.Background(), prState)
	if err != nil {
		t.Fatalf("NotifyMerged error: %v", err)
	}

	if !strings.Contains(receivedText, "[staging]") {
		t.Error("expected merged notification to contain [staging] prefix")
	}
	if !strings.Contains(receivedText, "✅") {
		t.Error("expected merged notification to contain ✅")
	}
	if !strings.Contains(receivedText, "PR #42") {
		t.Error("expected merged notification to contain PR number")
	}
	if !strings.Contains(receivedText, "merged") {
		t.Error("expected merged notification to contain 'merged'")
	}
	if !strings.Contains(receivedText, "feat: add search") {
		t.Error("expected merged notification to contain PR title")
	}
	if !strings.Contains(receivedText, "main") {
		t.Error("expected merged notification to contain target branch")
	}
}

func TestTelegramNotifier_NotifyMerged_NoEnv(t *testing.T) {
	var receivedText string
	notifier, server := newTestNotifier(t, &receivedText)
	defer server.Close()

	prState := &PRState{
		PRNumber: 42,
		Stage:    StageMerged,
	}

	err := notifier.NotifyMerged(context.Background(), prState)
	if err != nil {
		t.Fatalf("NotifyMerged error: %v", err)
	}

	if strings.Contains(receivedText, "[") {
		t.Error("expected no env prefix when EnvironmentName is empty")
	}
	if !strings.Contains(receivedText, "PR #42") {
		t.Error("expected notification to contain PR number")
	}
}

func TestTelegramNotifier_NotifyCIFailed(t *testing.T) {
	var receivedText string
	notifier, server := newTestNotifier(t, &receivedText)
	defer server.Close()

	prState := &PRState{
		PRNumber:        42,
		EnvironmentName: "production",
		PRTitle:         "fix: login bug",
	}
	failedChecks := []string{"build", "lint"}

	err := notifier.NotifyCIFailed(context.Background(), prState, failedChecks)
	if err != nil {
		t.Fatalf("NotifyCIFailed error: %v", err)
	}

	if !strings.Contains(receivedText, "[production]") {
		t.Error("expected CI failed notification to contain [production] prefix")
	}
	if !strings.Contains(receivedText, "❌") {
		t.Error("expected CI failed notification to contain ❌")
	}
	if !strings.Contains(receivedText, "PR #42") {
		t.Error("expected CI failed notification to contain PR number")
	}
	if !strings.Contains(receivedText, "build") {
		t.Error("expected CI failed notification to contain failed check 'build'")
	}
	if !strings.Contains(receivedText, "lint") {
		t.Error("expected CI failed notification to contain failed check 'lint'")
	}
	if !strings.Contains(receivedText, "fix: login bug") {
		t.Error("expected CI failed notification to contain PR title")
	}
}

func TestTelegramNotifier_NotifyCIFailed_NoChecks(t *testing.T) {
	var receivedText string
	notifier, server := newTestNotifier(t, &receivedText)
	defer server.Close()

	prState := &PRState{PRNumber: 42}

	err := notifier.NotifyCIFailed(context.Background(), prState, nil)
	if err != nil {
		t.Fatalf("NotifyCIFailed error: %v", err)
	}

	if !strings.Contains(receivedText, "unknown") {
		t.Error("expected CI failed notification with no checks to mention 'unknown'")
	}
}

func TestTelegramNotifier_NotifyApprovalRequired(t *testing.T) {
	var receivedText string
	notifier, server := newTestNotifier(t, &receivedText)
	defer server.Close()

	prState := &PRState{
		PRNumber:        42,
		EnvironmentName: "production",
	}

	err := notifier.NotifyApprovalRequired(context.Background(), prState)
	if err != nil {
		t.Fatalf("NotifyApprovalRequired error: %v", err)
	}

	if !strings.Contains(receivedText, "[production]") {
		t.Error("expected approval notification to contain [production] prefix")
	}
	if !strings.Contains(receivedText, "⏳") {
		t.Error("expected approval notification to contain ⏳")
	}
	if !strings.Contains(receivedText, "PR #42") {
		t.Error("expected approval notification to contain PR number")
	}
	if !strings.Contains(receivedText, "/approve 42") {
		t.Error("expected approval notification to contain approve command")
	}
	if !strings.Contains(receivedText, "/reject 42") {
		t.Error("expected approval notification to contain reject command")
	}
}

func TestTelegramNotifier_NotifyFixIssueCreated(t *testing.T) {
	var receivedText string
	notifier, server := newTestNotifier(t, &receivedText)
	defer server.Close()

	prState := &PRState{
		PRNumber:        42,
		EnvironmentName: "staging",
	}

	err := notifier.NotifyFixIssueCreated(context.Background(), prState, 100)
	if err != nil {
		t.Fatalf("NotifyFixIssueCreated error: %v", err)
	}

	if !strings.Contains(receivedText, "[staging]") {
		t.Error("expected fix issue notification to contain [staging] prefix")
	}
	if !strings.Contains(receivedText, "🔄") {
		t.Error("expected fix issue notification to contain 🔄")
	}
	if !strings.Contains(receivedText, "Issue #100") {
		t.Error("expected fix issue notification to contain issue number")
	}
	if !strings.Contains(receivedText, "PR #42") {
		t.Error("expected fix issue notification to contain PR number")
	}
}

func TestTelegramNotifier_NotifyPipelineComplete(t *testing.T) {
	var receivedText string
	notifier, server := newTestNotifier(t, &receivedText)
	defer server.Close()

	prState := &PRState{
		PRNumber:        123,
		IssueNumber:     100,
		EnvironmentName: "staging",
		PRTitle:         "feat: new feature",
		TargetBranch:    "main",
	}

	err := notifier.NotifyPipelineComplete(context.Background(), prState)
	if err != nil {
		t.Fatalf("NotifyPipelineComplete error: %v", err)
	}

	if !strings.Contains(receivedText, "[staging]") {
		t.Error("expected pipeline complete notification to contain [staging] prefix")
	}
	if !strings.Contains(receivedText, "Pipeline complete") {
		t.Error("expected pipeline complete notification to contain 'Pipeline complete'")
	}
	if !strings.Contains(receivedText, "GH-100") {
		t.Error("expected pipeline complete notification to contain issue number")
	}
	if !strings.Contains(receivedText, "PR #123") {
		t.Error("expected pipeline complete notification to contain PR number")
	}
	if !strings.Contains(receivedText, "merged") {
		t.Error("expected pipeline complete notification to contain 'merged'")
	}
}

func TestTelegramNotifier_NotifyReleased(t *testing.T) {
	tests := []struct {
		name         string
		bumpType     BumpType
		wantContains []string
	}{
		{
			name:     "minor release",
			bumpType: BumpMinor,
			wantContains: []string{
				"[staging]",
				"✨",
				"Published",
				"Version:",
				"minor release",
				"From PR:",
				"View Release",
				"https://github.com/owner/repo/releases/tag/v1.2.0",
			},
		},
		{
			name:     "major release",
			bumpType: BumpMajor,
			wantContains: []string{
				"major release",
			},
		},
		{
			name:     "patch release",
			bumpType: BumpPatch,
			wantContains: []string{
				"patch release",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receivedText string
			notifier, server := newTestNotifier(t, &receivedText)
			defer server.Close()

			prState := &PRState{
				PRNumber:        42,
				ReleaseVersion:  "v1.2.0",
				ReleaseBumpType: tt.bumpType,
				EnvironmentName: "staging",
			}

			err := notifier.NotifyReleased(context.Background(), prState, "https://github.com/owner/repo/releases/tag/v1.2.0")
			if err != nil {
				t.Fatalf("NotifyReleased error: %v", err)
			}

			for _, want := range tt.wantContains {
				if !strings.Contains(receivedText, want) {
					t.Errorf("expected notification to contain %q, got: %s", want, receivedText)
				}
			}
		})
	}
}

func TestNewTelegramNotifier(t *testing.T) {
	client := telegram.NewClient("test-token")
	notifier := NewTelegramNotifier(client, "123456")

	if notifier == nil {
		t.Fatal("NewTelegramNotifier returned nil")
	}
	if notifier.client != client {
		t.Error("client not set correctly")
	}
	if notifier.chatID != "123456" {
		t.Errorf("chatID = %s, want 123456", notifier.chatID)
	}
}

func TestEscapeMarkdown(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello", "hello"},
		{"hello_world", "hello\\_world"},
		{"*bold*", "\\*bold\\*"},
		{"[link](url)", "\\[link\\]\\(url\\)"},
		{"code`block`", "code\\`block\\`"},
		{"item #1", "item \\#1"},
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

func TestEnvPrefix(t *testing.T) {
	tests := []struct {
		name     string
		envName  string
		expected string
	}{
		{"with env", "staging", "[staging] "},
		{"empty env", "", ""},
		{"production env", "production", "[production] "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := &PRState{EnvironmentName: tt.envName}
			got := envPrefix(pr)
			if got != tt.expected {
				t.Errorf("envPrefix() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestEnvironmentName(t *testing.T) {
	tests := []struct {
		name     string
		config   Config
		expected string
	}{
		{"custom name", Config{Environment: EnvStage, Name: "staging"}, "staging"},
		{"fallback to env", Config{Environment: EnvProd}, "prod"},
		{"dev fallback", Config{Environment: EnvDev}, "dev"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.EnvironmentName()
			if got != tt.expected {
				t.Errorf("EnvironmentName() = %q, want %q", got, tt.expected)
			}
		})
	}
}
