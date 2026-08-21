package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	linearSDK "github.com/qf-studio/studio-sdk/sdk/integrations/linear"
)

// stubLinearClassifier is a fake linearLabelClassifier that returns
// per-label results (or an error) from a caller-supplied map, so tests can
// drive preflightLinearLabels without a live Linear API.
type stubLinearClassifier struct {
	results map[string]*linearSDK.LabelClassificationResult
	errs    map[string]error
}

func (s *stubLinearClassifier) ClassifyLabel(_ context.Context, _, labelName string) (*linearSDK.LabelClassificationResult, error) {
	if err, ok := s.errs[labelName]; ok {
		return nil, err
	}
	if result, ok := s.results[labelName]; ok {
		return result, nil
	}
	return &linearSDK.LabelClassificationResult{Classification: linearSDK.LabelTeamScoped}, nil
}

func newTestLogger(buf *strings.Builder) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// TestPreflightLinearLabels_TriggerMisconfigured verifies a misconfigured
// trigger label produces the classified startup ERROR line — the trigger
// label still fails closed, but the remedy is now logged immediately above
// that failure (GH-5092).
func TestPreflightLinearLabels_TriggerMisconfigured(t *testing.T) {
	var buf strings.Builder
	classifier := &stubLinearClassifier{
		results: map[string]*linearSDK.LabelClassificationResult{
			"pilot": {
				Classification: linearSDK.LabelWorkspaceScoped,
				Remedy:         `label "pilot" is workspace-scoped (scope is immutable in Linear) — delete & recreate team-scoped under ENG`,
			},
		},
	}

	preflightLinearLabels(context.Background(), newTestLogger(&buf), classifier, "ENG", "pilot", nil)

	logged := buf.String()
	if !strings.Contains(logged, "level=ERROR") {
		t.Errorf("expected an ERROR line for the misconfigured trigger label, got: %s", logged)
	}
	if !strings.Contains(logged, "label=pilot") {
		t.Errorf("expected the log line to name the label, got: %s", logged)
	}
	if !strings.Contains(logged, "workspace_scoped") {
		t.Errorf("expected the log line to name the classification, got: %s", logged)
	}
	if !strings.Contains(logged, "delete & recreate team-scoped") {
		t.Errorf("expected the log line to carry the SDK remedy, got: %s", logged)
	}
}

// TestPreflightLinearLabels_StatusLabelMisconfigured verifies a
// misconfigured status label produces a startup WARN line naming the
// remedy, ahead of the mid-poll Warn-and-continue degradation it already
// has (GH-5092).
func TestPreflightLinearLabels_StatusLabelMisconfigured(t *testing.T) {
	var buf strings.Builder
	classifier := &stubLinearClassifier{
		results: map[string]*linearSDK.LabelClassificationResult{
			"pilot-failed": {
				Classification: linearSDK.LabelAnotherTeam,
				Remedy:         `label "pilot-failed" belongs to team Engineering — move or rename it, or create a separate label under ENG`,
			},
		},
	}

	preflightLinearLabels(context.Background(), newTestLogger(&buf), classifier, "ENG", "pilot", []string{"pilot-in-progress", "pilot-done", "pilot-failed"})

	logged := buf.String()
	if strings.Contains(logged, "level=ERROR") {
		t.Errorf("status label misconfiguration must not be logged at ERROR (only the trigger label fails closed), got: %s", logged)
	}
	if !strings.Contains(logged, "level=WARN") {
		t.Errorf("expected a WARN line for the misconfigured status label, got: %s", logged)
	}
	if !strings.Contains(logged, "label=pilot-failed") {
		t.Errorf("expected the log line to name the label, got: %s", logged)
	}
	if !strings.Contains(logged, "another_team") {
		t.Errorf("expected the log line to name the classification, got: %s", logged)
	}
	if !strings.Contains(logged, "belongs to team Engineering") {
		t.Errorf("expected the log line to carry the SDK remedy, got: %s", logged)
	}
}

// TestPreflightLinearLabels_ClassifierErrorLogsWarnAndContinues verifies
// that a classification-call failure (network/API error) is logged at WARN
// and does not block the remaining labels or panic — the preflight must
// never block startup on its own failure (GH-5092).
func TestPreflightLinearLabels_ClassifierErrorLogsWarnAndContinues(t *testing.T) {
	var buf strings.Builder
	classifier := &stubLinearClassifier{
		errs: map[string]error{
			"pilot": errors.New("linear API: 500 internal server error"),
		},
		results: map[string]*linearSDK.LabelClassificationResult{
			"pilot-done": {
				Classification: linearSDK.LabelMissing,
				Remedy:         `label "pilot-done" does not exist — create it under team ENG`,
			},
		},
	}

	preflightLinearLabels(context.Background(), newTestLogger(&buf), classifier, "ENG", "pilot", []string{"pilot-done"})

	logged := buf.String()
	if strings.Contains(logged, "level=ERROR") {
		t.Errorf("a classifier error must never escalate to ERROR/fail closed, got: %s", logged)
	}
	if !strings.Contains(logged, "level=WARN") {
		t.Errorf("expected a WARN line for the classifier error, got: %s", logged)
	}
	if !strings.Contains(logged, "label=pilot") {
		t.Errorf("expected the error WARN line to name the label, got: %s", logged)
	}
	// The remaining status label must still be classified — one label's
	// classifier error must not abort the rest of the preflight.
	if !strings.Contains(logged, "label=pilot-done") {
		t.Errorf("expected classification to continue for subsequent labels after an error, got: %s", logged)
	}
	if !strings.Contains(logged, "does not exist") {
		t.Errorf("expected the subsequent label's remedy to still be logged, got: %s", logged)
	}
}
