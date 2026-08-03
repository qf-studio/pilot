// Package executor — GH-4671 gh-guard journal ingestion.
//
// The preventive counterpart to GH-4670's post-run audit
// (sideeffect_audit.go). Where GH-4670 detects a sibling-issue mutation
// after the fact, gh-guard (internal/executor/ghguard) stops it from
// happening: every `gh` call the Claude Code subprocess makes is
// intercepted at the Bash boundary and classified against the task's own
// issue/PR/branch. A denied call is journaled to disk by the shim
// subprocess (which runs and exits per gh invocation, so it can't hold
// anything in memory across calls); this file picks that journal up once
// the run completes and reuses the exact GH-4670 event type and alert
// channel, per GH-4671's own instruction not to invent a new one.
package executor

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/executor/ghguard"
	"github.com/qf-studio/pilot/internal/memory"
)

// ingestGHGuardJournal reads any gh-guard deny entries recorded for task
// during this run (ghGuardJournalPath, written by the shim subprocess via
// setupGHGuardShim/ghGuardEnv), journals each as an
// executor.github_sideeffect execution event, fires the same
// AlertEventTypeGithubSideEffect alert GH-4670 uses, and removes the
// journal file so a retried task_id doesn't re-report stale entries.
// Fails open on read errors: one WARN log, no event, no alert — a missing
// or malformed journal must never affect the run's reported outcome.
func (r *Runner) ingestGHGuardJournal(task *Task) {
	path := ghGuardJournalPath(task.ID)
	entries, err := ghguard.ReadJournal(path)
	if err != nil {
		slog.Warn("gh_guard_journal_read_failed",
			slog.String("component", "executor.ghguard_journal_ingest"),
			slog.String("task_id", task.ID),
			slog.Any("error", err),
		)
		return
	}
	if len(entries) == 0 {
		return
	}

	for _, entry := range entries {
		detail, marshalErr := json.Marshal(struct {
			Args      []string  `json:"args"`
			Reason    string    `json:"reason"`
			Issue     string    `json:"issue,omitempty"`
			Repo      string    `json:"repo,omitempty"`
			Branch    string    `json:"branch,omitempty"`
			DeniedAt  time.Time `json:"denied_at"`
			TaskIssue string    `json:"task_issue"`
		}{
			Args:      entry.Args,
			Reason:    entry.Reason,
			Issue:     entry.Issue,
			Repo:      entry.Repo,
			Branch:    entry.Branch,
			DeniedAt:  entry.Time,
			TaskIssue: task.SourceIssueID,
		})
		if marshalErr == nil {
			r.recordExecutionEvent(task.LogExecutionID(), memory.StageGithubSideEffect, string(detail))
		}

		slog.Warn("gh_guard_denied",
			slog.String("component", "executor.ghguard_journal_ingest"),
			slog.String("task_id", task.ID),
			slog.Any("args", entry.Args),
			slog.String("reason", entry.Reason),
		)

		r.emitAlertEvent(AlertEvent{
			Type:      AlertEventTypeGithubSideEffect,
			TaskID:    task.ID,
			TaskTitle: task.Title,
			Project:   task.ProjectPath,
			Error: fmt.Sprintf("session dispatched for %s#%s attempted `gh %s`, blocked by gh-guard: %s",
				task.SourceRepo, task.SourceIssueID, strings.Join(entry.Args, " "), entry.Reason),
			Metadata: map[string]string{
				"repo":       task.SourceRepo,
				"task_issue": task.SourceIssueID,
				"gh_args":    strings.Join(entry.Args, " "),
				"reason":     entry.Reason,
			},
			Timestamp: time.Now(),
		})
	}

	if err := ghguard.RemoveJournal(path); err != nil {
		slog.Warn("gh_guard_journal_remove_failed",
			slog.String("component", "executor.ghguard_journal_ingest"),
			slog.String("task_id", task.ID),
			slog.Any("error", err),
		)
	}
}
