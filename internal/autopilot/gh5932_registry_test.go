package autopilot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// These GH-5932 tests run the controller and feedback loop against the real
// executor.Runner borrowed-branch registry, not a fake of it.

// newRunnerRegistry returns a real Runner holding fix issue 5922's borrowed
// pilot/GH-5917 branch.
func newRunnerRegistry() *executor.Runner {
	r := executor.NewRunner()
	r.RecordBorrowedBranch("GH-5922", "pilot/GH-5917", 5921)
	return r
}

// An issue absent from the fake GitHub's map answers 404, which is a lookup
// failure: the unverifiable cases below rely on that.
func TestResolveAdoptedIssueNumber_UnverifiableTargets(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		issues        map[int]adoptFakeIssue
		wantIssue     int
		wantSource    string
		wantEntryKept bool // the registry still resolves pilot/GH-5917 to 5922
	}{
		{
			name:          "closing reference whose lookup fails is not bound",
			body:          "Closes #4001",
			issues:        map[int]adoptFakeIssue{},
			wantIssue:     5917,
			wantSource:    adoptSourceBranch,
			wantEntryKept: true, // the fix issue is unverifiable too
		},
		{
			name:          "registry fix issue whose lookup fails is not bound and its entry is kept",
			body:          "no reference",
			issues:        map[int]adoptFakeIssue{},
			wantIssue:     5917,
			wantSource:    adoptSourceBranch,
			wantEntryKept: true,
		},
		{
			name:          "live fix issue that lost to a closing reference keeps its entry",
			body:          "Closes #5917",
			issues:        map[int]adoptFakeIssue{5917: {state: "open"}, 5922: {state: "open", labels: fixLabels}},
			wantIssue:     5917,
			wantSource:    adoptSourceClosingReference,
			wantEntryKept: true,
		},
		{
			name:          "unverifiable fix issue that lost to a closing reference keeps its entry",
			body:          "Closes #5917",
			issues:        map[int]adoptFakeIssue{5917: {state: "open"}},
			wantIssue:     5917,
			wantSource:    adoptSourceClosingReference,
			wantEntryKept: true,
		},
		{
			name:       "dead fix issue that lost to a closing reference loses its entry",
			body:       "Closes #5917",
			issues:     map[int]adoptFakeIssue{5917: {state: "open"}, 5922: {state: "closed", labels: fixLabels}},
			wantIssue:  5917,
			wantSource: adoptSourceClosingReference,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := newRunnerRegistry()
			c, _, _ := newAdoptController(t, tt.issues, nil)
			c.SetBorrowedBranchLookup(registry)

			got := c.resolveAdoptedIssueNumber(context.Background(), adoptPR("pilot/GH-5917", tt.body))

			if got.Number != tt.wantIssue || got.Source != tt.wantSource {
				t.Fatalf("resolve = {issue:%d source:%q}, want {%d %q}", got.Number, got.Source, tt.wantIssue, tt.wantSource)
			}
			fix, kept := registry.FixIssueForBranch("pilot/GH-5917")
			if kept != tt.wantEntryKept || (kept && fix != 5922) {
				t.Errorf("registry after adoption: FixIssueForBranch = (%d, %v), want kept=%v", fix, kept, tt.wantEntryKept)
			}
		})
	}
}

// The registry hygiene hook in the feedback loop: a fix issue found dead on
// the dedup path stops owning its branch; a live one keeps it.
func TestFeedbackLoop_DeadFixIssueDropsBorrowedBranch(t *testing.T) {
	tests := []struct {
		name      string
		existing  *github.Issue
		wantKept  bool
		wantIssue int
	}{
		{"closed without shipping drops the entry", &github.Issue{Number: 5922, State: github.StateClosed}, false, 555},
		{"open and live keeps the entry", &github.Issue{Number: 5922, State: github.StateOpen, Labels: []github.Label{{Name: "autopilot-fix"}}}, true, 5922},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/repos/owner/repo/issues/5922" && r.Method == http.MethodGet:
					_ = json.NewEncoder(w).Encode(tt.existing)
				case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodGet:
					_ = json.NewEncoder(w).Encode([]*github.Issue{})
				case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(github.Issue{Number: 555})
				default:
					_, _ = w.Write([]byte("{}"))
				}
			}))
			defer server.Close()

			registry := newRunnerRegistry()
			c := NewController(DefaultConfig(), github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL), nil, "owner", "repo")
			store := newTestStateStore(t)
			c.SetStateStore(store)
			c.SetBorrowedBranchLookup(registry)

			checks := []string{"Test"}
			key := spawnedFixDedupKey(5921, FailureCIPostMerge, checks)
			if _, err := store.ClaimSpawnedFix("owner/repo", key); err != nil {
				t.Fatalf("seed claim: %v", err)
			}
			if err := store.RecordSpawnedFixIssue("owner/repo", key, 5922); err != nil {
				t.Fatalf("seed fix issue: %v", err)
			}

			got, err := c.feedbackLoop.CreateFailureIssue(context.Background(), &PRState{PRNumber: 5921, HeadSHA: "abc1234"}, FailureCIPostMerge, checks, "", 1)
			if err != nil {
				t.Fatalf("CreateFailureIssue: %v", err)
			}
			if got != tt.wantIssue {
				t.Errorf("issue = %d, want %d", got, tt.wantIssue)
			}
			if _, kept := registry.FixIssueForBranch("pilot/GH-5917"); kept != tt.wantKept {
				t.Errorf("registry entry kept = %v, want %v", kept, tt.wantKept)
			}
		})
	}
}

// A failed closing-issue lookup must not silently put the review on the
// tracked (possibly CI-fix) issue.
func TestGatherPostMergeReviewInput_PreferClosingLookupFailure(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantIssue int
		wantErr   string
	}{
		{name: "no closing reference in the body refuses the tracked issue", body: "nothing", wantErr: "closing-issue lookup failed"},
		{name: "closing reference in the body still wins", body: "Closes #5917", wantIssue: 5917},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newGatherFake("pilot/GH-5917")
			fake.pr.Body = tt.body
			fake.closingErr = errors.New("graphql down")
			fake.issues[5917] = &github.Issue{Number: 5917, Title: "the real issue"}
			fake.issues[5922] = &github.Issue{Number: 5922, Title: "the fix issue"}

			got, err := gatherPostMergeReviewInput(context.Background(), fake, "o", "r", "", 5902, 5922, true, false, nil)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("gather: %v", err)
			}
			if got.IssueNumber != tt.wantIssue {
				t.Errorf("issue = %d, want %d", got.IssueNumber, tt.wantIssue)
			}
		})
	}
}
