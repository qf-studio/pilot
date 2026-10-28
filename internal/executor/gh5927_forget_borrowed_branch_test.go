package executor

import "testing"

// TestForgetBorrowedBranch is the GH-5927 registry-hygiene test: a fix task
// that was closed, declined or cancelled must stop owning its borrowed branch.
func TestForgetBorrowedBranch(t *testing.T) {
	tests := []struct {
		name       string
		forget     string
		wantBranch bool // "pilot/GH-5917" still resolves after the forget
	}{
		{name: "forgetting the recorded task drops the entry", forget: "GH-5922", wantBranch: false},
		{name: "forgetting another task leaves the entry", forget: "GH-9999", wantBranch: true},
		{name: "forgetting an unknown task is a no-op", forget: "", wantBranch: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Runner{}
			r.RecordBorrowedBranch("GH-5922", "pilot/GH-5917", 5921)

			r.ForgetBorrowedBranch(tt.forget)

			_, _, peek := r.BorrowedBranch("GH-5922")
			fix, consumed := r.FixIssueForBranch("pilot/GH-5917")
			if peek != tt.wantBranch || consumed != tt.wantBranch {
				t.Fatalf("after forgetting %q: BorrowedBranch=%v FixIssueForBranch=(%d,%v), want present=%v", tt.forget, peek, fix, consumed, tt.wantBranch)
			}
		})
	}
}

// A cancelled or declined task's entry goes with it.
func TestCancelAndDeclineForgetBorrowedBranch(t *testing.T) {
	r := &Runner{}
	r.RecordBorrowedBranch("GH-5922", "pilot/GH-5917", 5921)

	_ = r.Cancel("GH-5922") // not running: returns an error, but the entry must still go

	if _, ok := r.FixIssueForBranch("pilot/GH-5917"); ok {
		t.Fatal("borrowed-branch entry survived Cancel")
	}
}
