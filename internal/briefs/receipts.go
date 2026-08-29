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

// runDigest generates the day's receipts digest and delivers it to every
// configured Telegram channel. An empty day (no terminal executions) skips
// the send entirely — no "0 runs" noise.
func (s *ReceiptsScheduler) runDigest(ctx context.Context) error {
	s.logger.Info("generating receipts digest")

	loc, err := time.LoadLocation(s.config.Timezone)
	if err != nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	rows, err := s.store.GetExecutionsForReceipts(memory.BriefQuery{Start: start, End: now})
	if err != nil {
		s.logger.Error("failed to load executions for receipts digest", "error", err)
		return err
	}

	if len(rows) == 0 {
		s.logger.Info("receipts digest: no terminal executions today, skipping send")
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

// formatReceiptsDigest formats the full Telegram digest message: a header,
// one line per terminal execution, and a day total line.
func formatReceiptsDigest(rows []*memory.Execution, day time.Time) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("🧾 *Receipts — %s*\n", day.Format("Jan 2, 2006")))
	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━\n")

	for _, exec := range rows {
		sb.WriteString(formatReceiptLine(exec))
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("*Total:* %s", formatReceiptsTotal(rows)))

	return sb.String()
}
