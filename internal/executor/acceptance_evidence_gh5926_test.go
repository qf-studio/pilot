package executor

import (
	"context"
	"strings"
	"testing"
)

// TestEvidenceGateOutputs_TestChangedDoesNotSatisfyMakeTestBullet pins GH-5926:
// a scoped `make test-changed` gate proves less than the full suite, so its
// output must never stand in for an evidence bullet that names `make test`.
func TestEvidenceGateOutputs_TestChangedDoesNotSatisfyMakeTestBullet(t *testing.T) {
	gates := &QualityGatesResult{Enabled: true, Gates: []QualityGateResult{
		{Name: "test", Passed: true, Command: "make test-changed", Output: "scoped output\n"},
	}}
	outputs := newEvidenceGateOutputs(gates)

	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{"make test is not satisfied", "make test", false},
		{"make test with flag is not satisfied", "make test -j2", false},
		{"make test-changed is its own command", "make test-changed", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := outputs.lookup(tt.command)
			if ok != tt.want {
				t.Errorf("lookup(%q) = %v, want %v", tt.command, ok, tt.want)
			}
		})
	}

	// End to end: the bullet runs for real instead of reusing the scoped output.
	r := newEvidenceTestRunner(DefaultBackendConfig())
	fake := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
		"make test": {output: "full suite ran for real"},
	}}
	r.acceptanceRunner = fake
	task := &Task{ID: "GH-5926", AcceptanceCriteria: []string{"paste the output of `make test` into the PR body"}}
	got := r.appendAcceptanceEvidence(context.Background(), task, t.TempDir(), "## Summary", gates)
	if !strings.Contains(got, "full suite ran for real") || strings.Contains(got, "scoped output") {
		t.Errorf("make test bullet must run, not reuse the make test-changed gate, got:\n%s", got)
	}
	if strings.Contains(got, evidenceFromQualityGateMarker) {
		t.Errorf("bullet marked as reused from a quality gate:\n%s", got)
	}
}
