package briefs

import (
	"context"
	"fmt"
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

// TestFormatReceiptsDigest_TruncatesLongList is the optional Telegram
// 4096-char guard: a per-run list long enough to blow the digest past
// maxReceiptsDigestLen is truncated with a "… +N more runs" line, and the
// total line still reflects every row, not just the ones shown.
func TestFormatReceiptsDigest_TruncatesLongList(t *testing.T) {
	var rows []*memory.Execution
	for i := 0; i < 200; i++ {
		rows = append(rows, &memory.Execution{
			TaskID: fmt.Sprintf("GH-%d", 5000+i), TaskSourceAdapter: "github",
			Status: "completed", LinesAdded: 10, LinesRemoved: 2,
			DurationMs: 60000, EstimatedCostUSD: 1.00,
		})
	}
	day := time.Date(2026, 8, 29, 18, 0, 0, 0, time.UTC)

	got := formatReceiptsDigest(rows, day)

	if len(got) > 4096 {
		t.Errorf("formatReceiptsDigest() length = %d, must stay under Telegram's 4096 limit", len(got))
	}
	if !strings.Contains(got, "more runs") {
		t.Errorf("expected a truncation marker in a 200-row digest, got:\n%s", got)
	}
	if !strings.Contains(got, "*Total:* 200 runs · +2000 −400 · $200.00") {
		t.Errorf("expected total to reflect all 200 rows regardless of truncation, got:\n%s", got)
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
		CompletedAt:       &now,
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

// TestReceiptsSchedulerRunNow_InFlightRunAppearsInNextDigest is GH-5261
// (PR#5258 review): a run still "running" at one digest's send time must be
// receipted in the NEXT digest once it completes — never dropped, and never
// receipted twice. Windowing on completed_at since the last digest sent
// (rather than a fixed calendar day keyed on created_at) is what makes this
// possible.
func TestReceiptsSchedulerRunNow_InFlightRunAppearsInNextDigest(t *testing.T) {
	store, cleanup := setupSchedulerTestStore(t)
	defer cleanup()

	// exec-early: created and completed well before the first digest fires —
	// must appear in digest 1 and never again.
	early := time.Now().Add(-2 * time.Hour)
	if err := store.SaveExecution(&memory.Execution{
		ID:                "exec-early",
		TaskID:            "GH-5300",
		ProjectPath:       "/tmp/proj",
		Status:            "completed",
		CreatedAt:         early,
		CompletedAt:       &early,
		EstimatedCostUSD:  1.00,
		TaskSourceAdapter: "github",
	}); err != nil {
		t.Fatalf("SaveExecution (early): %v", err)
	}

	// exec-inflight: created before the first digest but still "running" at
	// that point — must be excluded from digest 1 and included in digest 2
	// once it completes.
	startedBeforeDigest1 := time.Now().Add(-90 * time.Minute)
	if err := store.SaveExecution(&memory.Execution{
		ID:                "exec-inflight",
		TaskID:            "GH-5301",
		ProjectPath:       "/tmp/proj",
		Status:            "running",
		CreatedAt:         startedBeforeDigest1,
		EstimatedCostUSD:  0,
		TaskSourceAdapter: "github",
	}); err != nil {
		t.Fatalf("SaveExecution (inflight): %v", err)
	}

	sender := &mockTelegramSender{}
	config := &ReceiptsConfig{
		Enabled:  true,
		Schedule: "0 18 * * *",
		Timezone: "UTC",
		Channels: []ChannelConfig{{Type: "telegram", Channel: "@test"}},
	}
	scheduler := NewReceiptsScheduler(store, sender, config, nil)

	// Digest 1: only exec-early is terminal.
	if err := scheduler.RunNow(context.Background()); err != nil {
		t.Fatalf("RunNow (digest 1) failed: %v", err)
	}
	if len(sender.calls) != 1 {
		t.Fatalf("expected 1 send after digest 1, got %d", len(sender.calls))
	}
	if !strings.Contains(sender.calls[0].text, "#5300") {
		t.Errorf("digest 1 should mention #5300, got: %s", sender.calls[0].text)
	}
	if strings.Contains(sender.calls[0].text, "#5301") {
		t.Errorf("digest 1 must NOT mention still-running #5301, got: %s", sender.calls[0].text)
	}

	// exec-inflight finishes after digest 1 fired. The sleep guarantees
	// completed_at (CURRENT_TIMESTAMP, second precision) lands in a distinct
	// second from digest 1's brief_history SentAt (sub-second Go precision),
	// avoiding a same-second boundary flake in the completed_at >= start
	// comparison.
	time.Sleep(1100 * time.Millisecond)
	if err := store.MarkExecutionCompleted("exec-inflight", "", "", 1000); err != nil {
		t.Fatalf("MarkExecutionCompleted: %v", err)
	}

	// Digest 2: exec-inflight must now appear, and exec-early must NOT
	// reappear (already receipted in digest 1).
	if err := scheduler.RunNow(context.Background()); err != nil {
		t.Fatalf("RunNow (digest 2) failed: %v", err)
	}
	if len(sender.calls) != 2 {
		t.Fatalf("expected 2 sends after digest 2, got %d", len(sender.calls))
	}
	if !strings.Contains(sender.calls[1].text, "#5301") {
		t.Errorf("digest 2 should mention now-completed #5301, got: %s", sender.calls[1].text)
	}
	if strings.Contains(sender.calls[1].text, "#5300") {
		t.Errorf("digest 2 must NOT re-mention already-receipted #5300, got: %s", sender.calls[1].text)
	}
}
