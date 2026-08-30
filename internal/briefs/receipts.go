package briefs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
	"github.com/robfig/cron/v3"
)

// ReceiptsConfig holds configuration for the daily receipts digest (GH-5257):
// one line per terminal execution (issue ref, diff size, duration, cost) plus
// a day total, delivered to Telegram on its own schedule (default 18:00).
type ReceiptsConfig struct {
	Enabled  bool
	Schedule string
	Timezone string
	Channels []ChannelConfig
}

// receiptsBriefType is the brief_type stamped on brief_history rows recorded
// by the receipts digest, distinct from Scheduler's "daily" — see
// GetLastBriefSent's brief_type filter (GH-5257).
const receiptsBriefType = "receipts"

// receiptsRepresentativeChannel is the channel name recorded/read for
// catch-up bookkeeping, mirroring Scheduler.maybeCatchUp's use of "telegram"
// as a representative channel since Telegram is the only delivery mechanism
// the receipts digest supports in v1.
const receiptsRepresentativeChannel = "telegram"

// ReceiptsScheduler generates and delivers the daily receipts digest on a
// cron schedule. It forks the cron + timezone + catch-up idiom from
// Scheduler rather than generalizing it — Scheduler hardcodes
// GenerateDaily()/BriefType "daily", and the digest's flat
// per-execution-plus-total shape doesn't fit Brief's
// Completed/InProgress/Blocked sections.
type ReceiptsScheduler struct {
	store   *memory.Store
	sender  TelegramSender
	config  *ReceiptsConfig
	cron    *cron.Cron
	mu      sync.Mutex
	running bool
	entryID cron.EntryID
	logger  *slog.Logger
}

// NewReceiptsScheduler creates a new receipts digest scheduler. store is
// required for catch-up and send-history bookkeeping; sender is required to
// actually deliver the digest (nil is tolerated for tests that only exercise
// scheduling/catch-up detection).
func NewReceiptsScheduler(store *memory.Store, sender TelegramSender, config *ReceiptsConfig, logger *slog.Logger) *ReceiptsScheduler {
	if logger == nil {
		logger = slog.Default()
	}

	loc, err := time.LoadLocation(config.Timezone)
	if err != nil {
		logger.Warn("invalid timezone, using UTC", "timezone", config.Timezone, "error", err)
		loc = time.UTC
	}

	return &ReceiptsScheduler{
		store:  store,
		sender: sender,
		config: config,
		cron:   cron.New(cron.WithLocation(loc)),
		logger: logger,
	}
}

// Start begins the scheduler.
func (s *ReceiptsScheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return nil
	}

	if !s.config.Enabled {
		s.logger.Info("receipts digest scheduler disabled")
		return nil
	}

	entryID, err := s.cron.AddFunc(s.config.Schedule, func() {
		// runDigest's error is already logged internally; the cron callback
		// has no caller to propagate it to.
		_ = s.runDigest(ctx)
	})
	if err != nil {
		return err
	}

	s.entryID = entryID
	s.cron.Start()
	s.running = true

	nextRun := s.cron.Entry(s.entryID).Next

	s.logger.Info("receipts digest scheduler started",
		"schedule", s.config.Schedule,
		"timezone", s.config.Timezone,
		"next_run", nextRun,
	)

	s.maybeCatchUp(ctx)

	return nil
}

// Stop stops the scheduler.
func (s *ReceiptsScheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}

	ctx := s.cron.Stop()
	<-ctx.Done()
	s.running = false
	s.logger.Info("receipts digest scheduler stopped")
}

// IsRunning returns whether the scheduler is active.
func (s *ReceiptsScheduler) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// RunNow triggers an immediate digest generation and delivery.
func (s *ReceiptsScheduler) RunNow(ctx context.Context) error {
	return s.runDigest(ctx)
}

// runDigest generates the receipts digest for every terminal execution
// completed since the last digest was sent, and delivers it to every
// configured Telegram channel. Windowing on the last-sent digest (rather than
// a fixed calendar day) is deliberate (GH-5261 / PR#5258 review): a run
// started before 18:00 but still in flight at digest time — or any run
// created after 18:00 — would otherwise sit outside every digest's
// created_at-scoped window and never get receipted. Keying the window on
// completed_at and resuming exactly where the last digest left off
// guarantees every terminal execution is receipted exactly once. An empty
// window (no terminal executions) skips the send entirely — no "0 runs" noise.
func (s *ReceiptsScheduler) runDigest(ctx context.Context) error {
	s.logger.Info("generating receipts digest")

	loc, err := time.LoadLocation(s.config.Timezone)
	if err != nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)

	start := now.Add(-24 * time.Hour)
	if s.store != nil {
		lastRecord, err := s.store.GetLastBriefSent(receiptsRepresentativeChannel, receiptsBriefType)
		if err != nil {
			s.logger.Warn("receipts digest: failed to get last digest sent, falling back to 24h window", "error", err)
		} else if lastRecord != nil {
			start = lastRecord.SentAt.In(loc)
		}
	}

	rows, err := s.store.GetExecutionsForReceipts(memory.BriefQuery{Start: start, End: now})
	if err != nil {
		s.logger.Error("failed to load executions for receipts digest", "error", err)
		return err
	}

	if len(rows) == 0 {
		s.logger.Info("receipts digest: no terminal executions since last digest, skipping send")
		return nil
	}

	text := formatReceiptsDigest(rows, now)

	delivered := false
	for _, channel := range s.config.Channels {
		if channel.Type != "telegram" {
			continue
		}
		if s.deliverTelegram(ctx, channel.Channel, text) {
			delivered = true
		}
	}

	if delivered && s.store != nil {
		record := &memory.BriefRecord{
			SentAt:    time.Now(),
			Channel:   receiptsRepresentativeChannel,
			BriefType: receiptsBriefType,
		}
		if err := s.store.RecordBriefSent(record); err != nil {
			s.logger.Warn("failed to record receipts digest sent", "error", err)
		}
	}

	return nil
}

// deliverTelegram sends text to chatID, retrying as plain text if Markdown
// entity parsing fails (mirrors DeliveryService.deliverTelegram). Returns
// whether delivery succeeded.
func (s *ReceiptsScheduler) deliverTelegram(ctx context.Context, chatID, text string) bool {
	if s.sender == nil {
		s.logger.Warn("receipts digest: telegram sender not configured")
		return false
	}

	_, err := s.sender.SendBriefMessage(ctx, chatID, text, "Markdown")
	if err != nil && isTelegramParseEntityError(err) {
		s.logger.Warn("receipts digest: markdown parse failed, retrying as plain text",
			"chat_id", chatID,
			"error", err,
		)
		_, err = s.sender.SendBriefMessage(ctx, chatID, text, "")
	}
	if err != nil {
		s.logger.Error("failed to deliver receipts digest", "chat_id", chatID, "error", err)
		return false
	}

	s.logger.Info("receipts digest delivered", "chat_id", chatID)
	return true
}

// maybeCatchUp checks if a scheduled digest was missed and fires one if
// needed — the same detection idiom as Scheduler.maybeCatchUp, but reading
// GetLastBriefSent's own "receipts" brief_type so it can never be fooled by
// the daily brief's send history on a shared channel (GH-5257).
func (s *ReceiptsScheduler) maybeCatchUp(ctx context.Context) {
	if s.store == nil {
		s.logger.Info("receipts catch-up skipped: no store configured")
		return
	}

	lastRecord, err := s.store.GetLastBriefSent(receiptsRepresentativeChannel, receiptsBriefType)
	if err != nil {
		s.logger.Warn("receipts catch-up: failed to get last digest sent", "error", err)
		return
	}

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(s.config.Schedule)
	if err != nil {
		s.logger.Warn("receipts catch-up: failed to parse schedule", "schedule", s.config.Schedule, "error", err)
		return
	}

	now := time.Now()
	loc, _ := time.LoadLocation(s.config.Timezone)
	if loc == nil {
		loc = time.UTC
	}
	nowInTz := now.In(loc)

	checkTime := nowInTz.Add(-48 * time.Hour)
	var prevScheduled time.Time
	for {
		nextRun := schedule.Next(checkTime)
		if nextRun.After(nowInTz) {
			break
		}
		prevScheduled = nextRun
		checkTime = nextRun
	}

	if prevScheduled.IsZero() {
		s.logger.Info("receipts catch-up: no previous scheduled time found")
		return
	}

	if lastRecord == nil || lastRecord.SentAt.Before(prevScheduled) {
		lastSentStr := "never"
		if lastRecord != nil {
			lastSentStr = lastRecord.SentAt.Format(time.RFC3339)
		}
		s.logger.Info("receipts catch-up: missed digest detected, firing now",
			"last_sent", lastSentStr,
			"prev_scheduled", prevScheduled.Format(time.RFC3339),
		)
		if err := s.runDigest(ctx); err != nil {
			s.logger.Warn("receipts catch-up: run failed", "error", err)
		}
	} else {
		s.logger.Info("receipts catch-up: no missed digest",
			"last_sent", lastRecord.SentAt.Format(time.RFC3339),
			"prev_scheduled", prevScheduled.Format(time.RFC3339),
		)
	}
}

// receiptIssueRef returns the display ref for a receipt line — the GitHub
// issue number (with the "GH-" TaskID prefix stripped, or TaskSourceIssueID
// when set) when the execution came from GitHub, otherwise the task title,
// falling back to the raw TaskID. Mirrors the established idiom in
// executor/lifecycle.go's stripInProgressLabelOnTerminalFailure.
func receiptIssueRef(exec *memory.Execution) string {
	if exec.TaskSourceAdapter == "github" {
		issueNum := strings.TrimPrefix(exec.TaskID, "GH-")
		if exec.TaskSourceIssueID != "" {
			issueNum = exec.TaskSourceIssueID
		}
		if issueNum != "" {
			return "#" + issueNum
		}
	}
	if exec.TaskTitle != "" {
		return exec.TaskTitle
	}
	return exec.TaskID
}

// formatReceiptLine formats a single execution's receipt line, e.g.
// "#5214 · +88 −15 · 14m · $2.75" for a completed run, or
// "#5214 ✗ failed · +88 −15 · 14m · $2.75" for a failed one.
func formatReceiptLine(exec *memory.Execution) string {
	ref := escapeTelegramMarkdown(receiptIssueRef(exec))

	status := ""
	if exec.Status == "failed" {
		status = " ✗ failed"
	}

	return fmt.Sprintf("%s%s · +%d −%d · %s · $%.2f",
		ref, status, exec.LinesAdded, exec.LinesRemoved, formatDuration(exec.DurationMs), exec.EstimatedCostUSD)
}

// receiptsTotals sums the diff size and cost across every row for the day
// total line.
func receiptsTotals(rows []*memory.Execution) (linesAdded, linesRemoved int, costUSD float64) {
	for _, exec := range rows {
		linesAdded += exec.LinesAdded
		linesRemoved += exec.LinesRemoved
		costUSD += exec.EstimatedCostUSD
	}
	return linesAdded, linesRemoved, costUSD
}

// formatReceiptsTotal formats the day total line, e.g.
// "3 runs · +140 −42 · $6.30".
func formatReceiptsTotal(rows []*memory.Execution) string {
	linesAdded, linesRemoved, costUSD := receiptsTotals(rows)
	return fmt.Sprintf("%d runs · +%d −%d · $%.2f", len(rows), linesAdded, linesRemoved, costUSD)
}

// maxReceiptsDigestLen caps the per-run list so the digest stays comfortably
// under Telegram's 4096-char message limit; the total line always reflects
// every row regardless of how much of the list got truncated.
const maxReceiptsDigestLen = 3900

// formatReceiptsDigest formats the full Telegram digest message: a header,
// one line per terminal execution, and a day total line. If the per-run list
// would push the message past maxReceiptsDigestLen, it's truncated with a
// final "… +N more runs" line — the total line (computed over every row, not
// just the shown ones) is always included in full.
func formatReceiptsDigest(rows []*memory.Execution, day time.Time) string {
	header := fmt.Sprintf("🧾 *Receipts — %s*\n━━━━━━━━━━━━━━━━━━━━━\n", day.Format("Jan 2, 2006"))
	totalLine := fmt.Sprintf("\n*Total:* %s", formatReceiptsTotal(rows))

	var lines strings.Builder
	for i, exec := range rows {
		line := formatReceiptLine(exec) + "\n"
		budgetExceeded := len(header)+lines.Len()+len(line)+len(totalLine) > maxReceiptsDigestLen
		if budgetExceeded && i > 0 {
			fmt.Fprintf(&lines, "… +%d more runs\n", len(rows)-i)
			break
		}
		lines.WriteString(line)
	}

	return header + lines.String() + totalLine
}
