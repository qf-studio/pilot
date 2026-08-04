// GH-4671: Runner-side ingestion of gh-guard shim denials.
//
// Every gh-guard-guarded backend.Execute() call (there are several across
// runner.go — the main run plus each retry lane) can carry its own
// BackendResult.GhGuardDenials, populated by the backend from the
// per-execution guard journal (see ghguard_spawn.go). This file turns those
// entries into the same two artifacts the GH-4670 detective audit produces
// for its own findings — an execution_events row and an alert-engine
// warning — so operators have one consistent place to look regardless of
// which half of the GH-4649 containment pair caught the bad call.
package executor

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/qf-studio/pilot/internal/executor/ghguard"
	"github.com/qf-studio/pilot/internal/memory"
)

// ingestGhGuardDenials journals and alerts on every gh-guard denial carried
// by result (nil-safe: no-op when result is nil or has no denials, which
// covers every retry lane where the Execute call failed before returning a
// result at all).
func (r *Runner) ingestGhGuardDenials(task *Task, result *BackendResult) {
	if result == nil || len(result.GhGuardDenials) == 0 {
		return
	}

	for _, denial := range result.GhGuardDenials {
		detail, marshalErr := json.Marshal(struct {
			Args      []string `json:"args"`
			Reason    string   `json:"reason"`
			TaskIssue string   `json:"task_issue"`
			TaskRepo  string   `json:"task_repo"`
			DeniedAt  string   `json:"denied_at"`
		}{
			Args:      denial.Args,
			Reason:    denial.Reason,
			TaskIssue: denial.TaskIssue,
			TaskRepo:  denial.TaskRepo,
			DeniedAt:  denial.Time.UTC().Format(time.RFC3339),
		})
		if marshalErr == nil {
			r.recordExecutionEvent(task.LogExecutionID(), memory.StageGhGuardDenied, string(detail))
		}

		argsStr := ghguard.FormatArgsForLog(denial.Args)

		slog.Warn("gh_guard_denied",
			slog.String("component", "executor.ghguard_audit"),
			slog.String("task_id", task.ID),
			slog.String("repo", task.SourceRepo),
			slog.String("args", argsStr),
			slog.String("reason", denial.Reason),
		)

		r.emitAlertEvent(AlertEvent{
			Type:      AlertEventTypeGhGuardDenied,
			TaskID:    task.ID,
			TaskTitle: task.Title,
			Project:   task.ProjectPath,
			Error:     "gh-guard denied `gh " + argsStr + "`: " + denial.Reason,
			Metadata: map[string]string{
				"repo":       task.SourceRepo,
				"task_issue": task.SourceIssueID,
				"reason":     denial.Reason,
			},
			Timestamp: time.Now(),
		})
	}
}
