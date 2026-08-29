package briefs

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

func TestReceiptIssueRef(t *testing.T) {
	tests := []struct {
		name string
		exec *memory.Execution
		want string
	}{
		{
			name: "github execution strips GH- prefix from TaskID",
			exec: &memory.Execution{TaskID: "GH-5214", TaskSourceAdapter: "github"},
			want: "#5214",
		},
		{
			name: "github execution prefers TaskSourceIssueID over TaskID",
			exec: &memory.Execution{TaskID: "GH-5214", TaskSourceAdapter: "github", TaskSourceIssueID: "5299"},
			want: "#5299",
		},
		{
			name: "non-github execution falls back to task title",
			exec: &memory.Execution{TaskID: "local-1", TaskSourceAdapter: "linear", TaskTitle: "Fix the thing"},
			want: "Fix the thing",
		},
		{
			name: "github adapter but empty issue number falls back to title",
			exec: &memory.Execution{TaskID: "GH-", TaskSourceAdapter: "github", TaskTitle: "Untitled run"},
			want: "Untitled run",
		},
		{
			name: "no adapter, no title falls back to raw TaskID",
			exec: &memory.Execution{TaskID: "adhoc-42"},
			want: "adhoc-42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := receiptIssueRef(tt.exec); got != tt.want {
				t.Errorf("receiptIssueRef() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatReceiptLine(t *testing.T) {
	tests := []struct {
		name       string
		exec       *memory.Execution
		wantSubstr []string
		notWant    string
	}{
		{
			name: "completed run has no failed marker",
			exec: &memory.Execution{
				TaskID: "GH-5214", TaskSourceAdapter: "github", Status: "completed",
				LinesAdded: 88, LinesRemoved: 15, DurationMs: 14 * 60 * 1000, EstimatedCostUSD: 2.75,
			},
			wantSubstr: []string{"#5214", "+88 −15", "14m", "$2.75"},
			notWant:    "failed",
		},
		{
			name: "failed run is marked",
			exec: &memory.Execution{
				TaskID: "GH-5215", TaskSourceAdapter: "github", Status: "failed",
				LinesAdded: 3, LinesRemoved: 1, DurationMs: 30 * 1000, EstimatedCostUSD: 0.42,
			},
			wantSubstr: []string{"#5215", "✗ failed", "+3 −1", "30s", "$0.42"},
		},
		{
			name: "dynamic title text is markdown-escaped",
			exec: &memory.Execution{
				TaskID: "local-1", TaskSourceAdapter: "linear", TaskTitle: "fix_the_bug [urgent]", Status: "completed",
				LinesAdded: 1, LinesRemoved: 1, DurationMs: 1000, EstimatedCostUSD: 0.01,
			},
			wantSubstr: []string{`fix\_the\_bug \[urgent]`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatReceiptLine(tt.exec)
			for _, want := range tt.wantSubstr {
				if !strings.Contains(got, want) {
					t.Errorf("formatReceiptLine() = %q, want substring %q", got, want)
				}
			}
			if tt.notWant != "" && strings.Contains(got, tt.notWant) {
				t.Errorf("formatReceiptLine() = %q, did not want substring %q", got, tt.notWant)
			}
		})
	}
}

func TestFormatReceiptsTotal(t *testing.T) {
	tests := []struct {
		name string
		rows []*memory.Execution
		want string
	}{
		{
			name: "no rows",
			rows: nil,
			want: "0 runs · +0 −0 · $0.00",
		},
		{
			name: "sums across completed and failed rows",
			rows: []*memory.Execution{
				{Status: "completed", LinesAdded: 88, LinesRemoved: 15, EstimatedCostUSD: 2.75},
				{Status: "failed", LinesAdded: 3, LinesRemoved: 1, EstimatedCostUSD: 0.42},
			},
			want: "2 runs · +91 −16 · $3.17",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatReceiptsTotal(tt.rows); got != tt.want {
				t.Errorf("formatReceiptsTotal() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatReceiptsDigest(t *testing.T) {
	rows := []*memory.Execution{
		{TaskID: "GH-5214", TaskSourceAdapter: "github", Status: "completed", LinesAdded: 88, LinesRemoved: 15, DurationMs: 14 * 60 * 1000, EstimatedCostUSD: 2.75},
		{TaskID: "GH-5215", TaskSourceAdapter: "github", Status: "failed", LinesAdded: 3, LinesRemoved: 1, DurationMs: 30 * 1000, EstimatedCostUSD: 0.42},
	}
	day := time.Date(2026, 8, 29, 18, 0, 0, 0, time.UTC)

	got := formatReceiptsDigest(rows, day)

	for _, want := range []string{"Aug 29, 2026", "#5214", "#5215", "✗ failed", "*Total:* 2 runs · +91 −16 · $3.17"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatReceiptsDigest() missing %q, got:\n%s", want, got)
		}
	}
}

func TestNewReceiptsScheduler(t *testing.T) {
	store, cleanup := setupSchedulerTestStore(t)
	defer cleanup()

	tests := []struct {
		name   string
		config *ReceiptsConfig
		logger *slog.Logger
		wantTz string
	}{
		{
			name:   "valid timezone",
			config: &ReceiptsConfig{Enabled: true, Schedule: "0 18 * * *", Timezone: "America/New_York"},
			wantTz: "America/New_York",
		},
		{
			name:   "invalid timezone falls back to UTC",
			config: &ReceiptsConfig{Enabled: true, Schedule: "0 18 * * *", Timezone: "Invalid/Timezone"},
			logger: slog.Default(),
			wantTz: "UTC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewReceiptsScheduler(store, nil, tt.config, tt.logger)
			if s == nil {
				t.Fatal("expected scheduler, got nil")
			}
			if s.cron == nil {
				t.Fatal("expected cron instance, got nil")
			}
		})
	}
}

func TestReceiptsSchedulerRunNow_EmptyDaySkipsSend(t *testing.T) {
	store, cleanup := setupSchedulerTestStore(t)
	defer cleanup()

	sender := &mockTelegramSender{}
	config := &ReceiptsConfig{
		Enabled:  true,
		Schedule: "0 18 * * *",
		Timezone: "UTC",
		Channels: []ChannelConfig{{Type: "telegram", Channel: "@test"}},
	}
	scheduler := NewReceiptsScheduler(store, sender, config, nil)

	if err := scheduler.RunNow(context.Background()); err != nil {
		t.Fatalf("RunNow failed: %v", err)
	}

	if len(sender.calls) != 0 {
		t.Errorf("expected no send on empty day, got %d calls", len(sender.calls))
	}

	record, err := store.GetLastBriefSent(receiptsRepresentativeChannel, receiptsBriefType)
	if err != nil {
		t.Fatalf("GetLastBriefSent: %v", err)
	}
	if record != nil {
		t.Errorf("expected no brief_history record on empty day, got %+v", record)
	}
}

func TestReceiptsSchedulerRunNow_DeliversAndRecordsOwnBriefType(t *testing.T) {
	store, cleanup := setupSchedulerTestStore(t)
	defer cleanup()

	now := time.Now().UTC()
	if err := store.SaveExecution(&memory.Execution{
		ID:                "exec-1",
		TaskID:            "GH-5214",
		ProjectPath:       "/tmp/proj",
		Status:            "completed",
		CreatedAt:         now,
		LinesAdded:        88,
		LinesRemoved:      15,
		EstimatedCostUSD:  2.75,
		TaskSourceAdapter: "github",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	// A daily-brief record on the same channel, timestamped after the
	// receipts send will happen, must not be read back by the receipts
	// scheduler's own catch-up/history lookup (GH-5257 cross-contamination
	// guard).
	if err := store.RecordBriefSent(&memory.BriefRecord{
		SentAt:    time.Now().Add(1 * time.Hour),
		Channel:   receiptsRepresentativeChannel,
		BriefType: "daily",
	}); err != nil {
		t.Fatalf("RecordBriefSent (daily): %v", err)
	}

	sender := &mockTelegramSender{}
	config := &ReceiptsConfig{
		Enabled:  true,
		Schedule: "0 18 * * *",
		Timezone: "UTC",
		Channels: []ChannelConfig{{Type: "telegram", Channel: "@test"}},
	}
	scheduler := NewReceiptsScheduler(store, sender, config, nil)

	if err := scheduler.RunNow(context.Background()); err != nil {
		t.Fatalf("RunNow failed: %v", err)
	}

	if len(sender.calls) != 1 {
		t.Fatalf("expected 1 send, got %d", len(sender.calls))
	}
	if !strings.Contains(sender.calls[0].text, "#5214") {
		t.Errorf("expected digest text to mention #5214, got: %s", sender.calls[0].text)
	}

	record, err := store.GetLastBriefSent(receiptsRepresentativeChannel, receiptsBriefType)
	if err != nil {
		t.Fatalf("GetLastBriefSent: %v", err)
	}
	if record == nil {
		t.Fatal("expected a receipts brief_history record after delivery, got nil")
	}
	if record.BriefType != receiptsBriefType {
		t.Errorf("BriefType = %q, want %q", record.BriefType, receiptsBriefType)
	}
}
