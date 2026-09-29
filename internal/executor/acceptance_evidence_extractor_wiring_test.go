package executor_test

import (
	"context"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/executor"
)

// GH-5485: the acceptance-evidence gate read task.AcceptanceCriteria, which the
// GitHub extractor left empty for the house-style "## Acceptance" heading, so
// appendAcceptanceEvidence returned early and no Evidence / Not-verified
// section ever reached the PR body. This pins the end-to-end path: issue body
// -> github.ExtractAcceptanceCriteria -> Task -> appendAcceptanceEvidence.
//
// It lives in package executor_test (with an export shim) because the github
// adapter imports executor, so package executor cannot import it.
func TestAcceptanceEvidenceWiring_HouseStyleAcceptanceHeadingReachesGate(t *testing.T) {
	// A non-allowlisted command keeps the case hermetic: the item must still
	// surface, as "## Not verified" with the allowlist reason.
	body := "## Context\n\nsomething\n\n## Acceptance\n\n" +
		"- Paste the output of `curl example.invalid` into the PR body\n\n" +
		"## Refs\n\n- GH-1\n"

	criteria := github.ExtractAcceptanceCriteria(body)
	if len(criteria) != 1 {
		t.Fatalf("extractor returned %d criteria (%v), want 1", len(criteria), criteria)
	}

	task := &executor.Task{ID: "GH-1", AcceptanceCriteria: criteria}
	r := executor.NewRunner()

	prBody := "## Summary\n\nsome PR body"
	got := executor.AppendAcceptanceEvidenceForTest(context.Background(), r, task, t.TempDir(), prBody)

	if got == prBody {
		t.Fatalf("PR body unchanged: extractor output never reached the evidence gate")
	}
	if !strings.Contains(got, "## Evidence") && !strings.Contains(got, "## Not verified") {
		t.Fatalf("PR body has neither Evidence nor Not verified section: %q", got)
	}
	if !strings.Contains(got, "command not in allowlist") {
		t.Errorf("expected allowlist Not-verified reason, got %q", got)
	}
}
