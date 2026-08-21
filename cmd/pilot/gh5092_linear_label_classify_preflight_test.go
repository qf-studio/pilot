package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	linearSDK "github.com/qf-studio/studio-sdk/sdk/integrations/linear"
)

// stubLinearLabelClassifier stubs linearLabelClassifier for GH-5092: the
// startup preflight must be testable without a real Linear GraphQL call.
type stubLinearLabelClassifier struct {
	results map[string]*linearSDK.LabelClassificationResult
	errs    map[string]error
	calls   []string
}

func (s *stubLinearLabelClassifier) ClassifyLabel(_ context.Context, _, labelName string) (*linearSDK.LabelClassificationResult, error) {
	s.calls = append(s.calls, labelName)
	if err, ok := s.errs[labelName]; ok {
		return nil, err
	}
	if res, ok := s.results[labelName]; ok {
		return res, nil
	}
	return &linearSDK.LabelClassificationResult{
		Classification: linearSDK.LabelTeamScoped,
		Remedy:         "already team-scoped -- no action needed",
	}, nil
}

func newBufferLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return logger, &buf
}

// TestClassifyWorkspaceLabels_MisconfiguredTriggerLabel covers (a): a
// trigger label that isn't cleanly team-scoped must produce the classified
// startup error -- both an Error-level log line naming the label,
// classification and remedy, and a non-nil returned error carrying the same
// diagnosis, preserving the existing fail-closed contract.
func TestClassifyWorkspaceLabels_MisconfiguredTriggerLabel(t *testing.T) {
	logger, buf := newBufferLogger()
	classifier := &stubLinearLabelClassifier{
		results: map[string]*linearSDK.LabelClassificationResult{
			"pilot": {
				Classification: linearSDK.LabelAnotherTeam,
				Remedy:         `label "pilot" belongs to team Design -- move or rename it, or create a separate label under ENG`,
			},
		},
	}

	err := classifyWorkspaceLabels(context.Background(), logger, classifier, "acme", "ENG", "pilot")

	if err == nil {
		t.Fatal("expected a non-nil error for a misconfigured trigger label")
	}
	for _, want := range []string{"pilot", "another_team", "move or rename"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "level=ERROR") {
		t.Errorf("expected an Error-level log line, got: %s", logOutput)
	}
	for _, want := range []string{"label=pilot", "classification=another_team", "move or rename"} {
		if !strings.Contains(logOutput, want) {
			t.Errorf("log output missing %q, got: %s", want, logOutput)
		}
	}
}

// TestClassifyWorkspaceLabels_MisconfiguredStatusLabel covers (b): a
// misconfigured pilot-* status label must produce a startup WARN line
// naming the remedy before the poll loop begins, without failing the
// workspace closed (only the trigger label does that).
func TestClassifyWorkspaceLabels_MisconfiguredStatusLabel(t *testing.T) {
	logger, buf := newBufferLogger()
	classifier := &stubLinearLabelClassifier{
		results: map[string]*linearSDK.LabelClassificationResult{
			"pilot-in-progress": {
				Classification: linearSDK.LabelWorkspaceScoped,
				Remedy:         `label "pilot-in-progress" is workspace-scoped (scope is immutable in Linear) -- delete & recreate team-scoped under ENG`,
			},
		},
	}

	err := classifyWorkspaceLabels(context.Background(), logger, classifier, "acme", "ENG", "pilot")

	if err != nil {
		t.Fatalf("status label misconfiguration must not fail the workspace closed, got error: %v", err)
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "level=WARN") {
		t.Errorf("expected a WARN-level log line, got: %s", logOutput)
	}
	for _, want := range []string{"label=pilot-in-progress", "classification=workspace_scoped", "delete & recreate"} {
		if !strings.Contains(logOutput, want) {
			t.Errorf("log output missing %q, got: %s", want, logOutput)
		}
	}
	if strings.Contains(logOutput, "level=ERROR") {
		t.Errorf("a status label issue must never log at Error, got: %s", logOutput)
	}
}

// TestClassifyWorkspaceLabels_ClassifierErrorContinues covers (c): if the
// classifier call itself errors (network/API failure), the preflight must
// log WARN and keep going -- it must never block startup on its own
// failure, and it must still classify every remaining label.
func TestClassifyWorkspaceLabels_ClassifierErrorContinues(t *testing.T) {
	logger, buf := newBufferLogger()
	networkErr := errors.New("linear graphql: connection reset by peer")
	classifier := &stubLinearLabelClassifier{
		errs: map[string]error{
			"pilot": networkErr,
		},
	}

	err := classifyWorkspaceLabels(context.Background(), logger, classifier, "acme", "ENG", "pilot")

	if err != nil {
		t.Fatalf("a classifier error must not itself fail the preflight, got: %v", err)
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "level=WARN") {
		t.Errorf("expected a WARN-level log line for the classifier failure, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "connection reset by peer") {
		t.Errorf("expected the underlying classifier error in the log, got: %s", logOutput)
	}
	if strings.Contains(logOutput, "level=ERROR") {
		t.Errorf("a classifier error must never escalate to Error, got: %s", logOutput)
	}

	wantCalls := []string{"pilot", "pilot-in-progress", "pilot-done", "pilot-failed"}
	if len(classifier.calls) != len(wantCalls) {
		t.Fatalf("calls = %v, want preflight to continue through all of %v", classifier.calls, wantCalls)
	}
	for i, want := range wantCalls {
		if classifier.calls[i] != want {
			t.Errorf("calls[%d] = %q, want %q", i, classifier.calls[i], want)
		}
	}
}
