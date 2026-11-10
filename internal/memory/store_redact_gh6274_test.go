package memory

import (
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

const gh6274Secret = "zq-opaque-config-value-6274"

func newGH6274Store(t *testing.T) *Store {
	t.Helper()
	secretpatterns.ResetSecretValuesForTest()
	t.Cleanup(secretpatterns.ResetSecretValuesForTest)
	secretpatterns.RegisterSecretValues(gh6274Secret)
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// mutation pin: ReclassifyCompletionAsFailed/AsSuperseded and TerminateNonTerminalExecution/AsSuperseded bind reason to executions.error unredacted -> this test fails
func TestStore_SiblingReasonWritersRedactError(t *testing.T) {
	store := newGH6274Store(t)
	tests := []struct {
		name, startStatus string
		fn                func(taskID, project, reason string) error
	}{
		{"ReclassifyCompletionAsFailed", "completed", store.ReclassifyCompletionAsFailed},
		{"ReclassifyCompletionAsSuperseded", "completed", store.ReclassifyCompletionAsSuperseded},
		{"TerminateNonTerminalExecution", "running", store.TerminateNonTerminalExecution},
		{"TerminateNonTerminalExecutionAsSuperseded", "running", store.TerminateNonTerminalExecutionAsSuperseded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, taskID := "sib-"+tt.name, "GH-sib-"+tt.name
			exec := &Execution{ID: id, TaskID: taskID, ProjectPath: "/p", Status: tt.startStatus, CommitSHA: "abc123"}
			if err := store.SaveExecution(exec); err != nil {
				t.Fatalf("SaveExecution: %v", err)
			}
			if err := tt.fn(taskID, "/p", "closed unmerged "+gh6274Secret); err != nil {
				t.Fatal(err)
			}
			got, err := store.GetExecution(id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status == tt.startStatus {
				t.Fatalf("row not reclassified: status=%q", got.Status)
			}
			if strings.Contains(got.Error, gh6274Secret) || !strings.Contains(got.Error, "<redacted>") {
				t.Errorf("error = %q", got.Error)
			}
		})
	}
}

// mutation pin: ResolveOrphanedRunningExecution writes the raw prURL to executions.pr_url while the event detail is redacted -> this test fails
func TestResolveOrphanedRunningExecution_RedactsPRURLColumn(t *testing.T) {
	store := newGH6274Store(t)
	if err := store.SaveExecution(&Execution{ID: "orph-pr", TaskID: "GH-orph-pr", ProjectPath: "/p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ResolveOrphanedRunningExecution("orph-pr", "https://example.test/pr?x="+gh6274Secret); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetExecution("orph-pr")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.PRUrl, gh6274Secret) || !strings.Contains(got.PRUrl, "<redacted>") {
		t.Errorf("pr_url = %q", got.PRUrl)
	}
}

// mutation pin: ResolveOrphanedRunningExecution redacts the package constant orphanedRunningNoEvidenceError (dead code) -> this test fails
func TestResolveOrphanedRunningExecution_NoEvidenceErrorIsStoredVerbatim(t *testing.T) {
	store := newGH6274Store(t)
	// A registered value that is a substring of the constant makes any Redact
	// call on it observable; the constant is not user data and must not pass
	// through Redact.
	secretpatterns.RegisterSecretValues("no live process")
	if err := store.SaveExecution(&Execution{ID: "orph-fail", TaskID: "GH-orph-fail", ProjectPath: "/p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ResolveOrphanedRunningExecution("orph-fail", ""); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetExecution("orph-fail")
	if err != nil {
		t.Fatal(err)
	}
	if got.Error != orphanedRunningNoEvidenceError {
		t.Errorf("error = %q, want the constant verbatim", got.Error)
	}
}
