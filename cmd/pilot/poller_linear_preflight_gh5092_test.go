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

// TestPreflightLinearLabels_TriggerWorkspaceScoped verifies a
// workspace-scoped trigger label logs a WARN, not an ERROR: v0.36.0's
// GetLabelByName falls back to a workspace-scoped lookup
// (getWorkspaceLabelByName), so this classification no longer fails
// startup — it only loses team-scope precedence (GH-5118).
func TestPreflightLinearLabels_TriggerWorkspaceScoped(t *testing.T) {
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
	if strings.Contains(logged, "level=ERROR") {
		t.Errorf("workspace-scoped trigger label must not be logged at ERROR (resolves via workspace fallback), got: %s", logged)
	}
	if !strings.Contains(logged, "level=WARN") {
		t.Errorf("expected a WARN line for the workspace-scoped trigger label, got: %s", logged)
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

// TestPreflightLinearLabels_TriggerMisconfigured verifies a trigger label
// owned by another team or missing entirely still produces the classified
// startup ERROR line — those classifications still fail closed under
// v0.36.0, since GetLabelByName's workspace fallback only matches a label
// with a nil team (GH-5092, GH-5118).
func TestPreflightLinearLabels_TriggerMisconfigured(t *testing.T) {
	tests := []struct {
		name           string
		classification linearSDK.LabelClassification
		remedy         string
		wantSubstr     string
	}{
		{
			name:           "another_team",
			classification: linearSDK.LabelAnotherTeam,
			remedy:         `label "pilot" belongs to team Engineering — move or rename it, or create a separate label under ENG`,
			wantSubstr:     "belongs to team Engineering",
		},
		{
			name:           "missing",
			classification: linearSDK.LabelMissing,
			remedy:         `label "pilot" does not exist — create it under team ENG`,
			wantSubstr:     "does not exist",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			classifier := &stubLinearClassifier{
				results: map[string]*linearSDK.LabelClassificationResult{
					"pilot": {
						Classification: tt.classification,
						Remedy:         tt.remedy,
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
			if !strings.Contains(logged, string(tt.classification)) {
				t.Errorf("expected the log line to name the classification, got: %s", logged)
			}
			if !strings.Contains(logged, tt.wantSubstr) {
				t.Errorf("expected the log line to carry the SDK remedy, got: %s", logged)
			}
		})
	}
}

// TestPreflightLinearLabels_StatusLabelMisconfigured verifies status label
// classifications produce the right severity/message under v0.36.0's
// GetOrCreateLabel: a missing label is auto-created (INFO), while
// workspace-scoped or another-team labels still warrant a WARN — but with
// text describing what GetOrCreateLabel actually does, not the pre-v0.36.0
// "will fail mid-poll" claim (GH-5092, GH-5118).
func TestPreflightLinearLabels_StatusLabelMisconfigured(t *testing.T) {
	tests := []struct {
		name           string
		classification linearSDK.LabelClassification
		remedy         string
		wantLevel      string
		wantSubstr     string
	}{
		{
			name:           "another_team",
			classification: linearSDK.LabelAnotherTeam,
			remedy:         `label "pilot-failed" belongs to team Engineering — move or rename it, or create a separate label under ENG`,
			wantLevel:      "level=WARN",
			wantSubstr:     "belongs to team Engineering",
		},
		{
			name:           "workspace_scoped",
			classification: linearSDK.LabelWorkspaceScoped,
			remedy:         `label "pilot-failed" is workspace-scoped (scope is immutable in Linear) — delete & recreate team-scoped under ENG`,
			wantLevel:      "level=WARN",
			wantSubstr:     "delete & recreate team-scoped",
		},
		{
			name:           "missing",
			classification: linearSDK.LabelMissing,
			remedy:         `label "pilot-failed" does not exist — create it under team ENG`,
			wantLevel:      "level=INFO",
			wantSubstr:     "does not exist",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			classifier := &stubLinearClassifier{
				results: map[string]*linearSDK.LabelClassificationResult{
					"pilot-failed": {
						Classification: tt.classification,
						Remedy:         tt.remedy,
					},
				},
			}

			preflightLinearLabels(context.Background(), newTestLogger(&buf), classifier, "ENG", "pilot", []string{"pilot-in-progress", "pilot-done", "pilot-failed"})

			logged := buf.String()
			if strings.Contains(logged, "level=ERROR") {
				t.Errorf("status label misconfiguration must not be logged at ERROR (only the trigger label fails closed), got: %s", logged)
			}
			if !strings.Contains(logged, tt.wantLevel) {
				t.Errorf("expected a %s line for the %s status label, got: %s", tt.wantLevel, tt.name, logged)
			}
			if !strings.Contains(logged, "label=pilot-failed") {
				t.Errorf("expected the log line to name the label, got: %s", logged)
			}
			if !strings.Contains(logged, string(tt.classification)) {
				t.Errorf("expected the log line to name the classification, got: %s", logged)
			}
			if !strings.Contains(logged, tt.wantSubstr) {
				t.Errorf("expected the log line to carry the SDK remedy, got: %s", logged)
			}
		})
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
