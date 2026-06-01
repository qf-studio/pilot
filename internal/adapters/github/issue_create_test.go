package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

type fakeIssueAllowlist struct{ allowed bool }

func (f fakeIssueAllowlist) RepoIsAllowed(string, string, string) bool { return f.allowed }
func (f fakeIssueAllowlist) ConfiguredRepos() []string                 { return []string{"owner/allowed"} }

// TestValidateIssueRepo_FailClosed_C7 verifies C7 (TASK-347): a nil allowlist fails
// closed (unless PILOT_ALLOW_UNMANAGED_REPO=1); the AllowAllIssueRepos sentinel and a
// matching allowlist pass; a non-matching allowlist errors — matching executor's default.
func TestValidateIssueRepo_FailClosed_C7(t *testing.T) {
	if err := validateIssueRepo(nil, "owner", "repo"); err == nil {
		t.Error("nil allowlist must fail closed, got nil error")
	}

	t.Setenv(envBypassIssueAllowlist, "1")
	if err := validateIssueRepo(nil, "owner", "repo"); err != nil {
		t.Errorf("nil allowlist + bypass env should pass, got %v", err)
	}
	t.Setenv(envBypassIssueAllowlist, "") // disable bypass for the rest

	if err := validateIssueRepo(AllowAllIssueRepos(), "owner", "repo"); err != nil {
		t.Errorf("AllowAllIssueRepos should pass, got %v", err)
	}
	if err := validateIssueRepo(fakeIssueAllowlist{allowed: true}, "owner", "repo"); err != nil {
		t.Errorf("allowed repo should pass, got %v", err)
	}
	if err := validateIssueRepo(fakeIssueAllowlist{allowed: false}, "owner", "repo"); err == nil {
		t.Error("disallowed repo must error")
	}
}

func TestConventionalCommitRE(t *testing.T) {
	accept := []string{
		"feat: add OAuth login",
		"fix(auth): handle nil token",
		"chore(deps): bump go to 1.22",
		"refactor(executor): extract signal parser",
		"test(gateway): add integration coverage",
		"docs(readme): update quick-start",
		"perf(db): add index on task_id",
		"build(ci): upgrade golangci-lint",
		"ci: run tests on pull_request",
		"style(tui): align column headers",
	}
	for _, title := range accept {
		if !conventionalCommitRE.MatchString(title) {
			t.Errorf("expected ACCEPT for %q", title)
		}
	}

	reject := []string{
		"",
		"Add OAuth login",
		"feat add OAuth login",
		"feat:",
		"feat: ",
		"unknown(scope): do something",
		"Fix CI failure from PR #123",
		"feat(: missing closing paren",
		"FEAT: uppercase type",
	}
	for _, title := range reject {
		if conventionalCommitRE.MatchString(title) {
			t.Errorf("expected REJECT for %q", title)
		}
	}
}

func TestCreatePilotIssue_TitleValidation(t *testing.T) {
	tests := []struct {
		name    string
		title   string
		wantErr bool
	}{
		{"valid feat", "feat(github): add issue helper", false},
		{"valid fix no scope", "fix: handle nil response", false},
		{"invalid no type", "add issue helper", true},
		{"invalid empty", "", true},
		{"invalid uppercase", "FEAT: something", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Server only needed for non-error cases; validation fires before API call.
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				issue := Issue{Number: 1, Title: tt.title}
				_, _ = w.Write(mustMarshal(issue))
			}))
			defer server.Close()

			c := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			_, err := CreatePilotIssue(context.Background(), c, AllowAllIssueRepos(), "owner", "repo", tt.title, "body", []string{"pilot"})
			if (err != nil) != tt.wantErr {
				t.Errorf("createPilotIssue(%q) error = %v, wantErr %v", tt.title, err, tt.wantErr)
			}
			if tt.wantErr && err != nil && !strings.Contains(err.Error(), "conventional-commits") {
				t.Errorf("expected conventional-commits mention in error, got: %v", err)
			}
		})
	}
}

func TestCreatePilotIssue_APIForwarding(t *testing.T) {
	const wantTitle = "feat(github): add createPilotIssue helper"
	const wantBody = "implements the validation primitive"

	var gotTitle, gotBody string
	var gotLabels []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/owner/repo/issues" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		var input IssueInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		gotTitle = input.Title
		gotBody = input.Body
		gotLabels = input.Labels

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		issue := Issue{Number: 42, Title: input.Title, Body: input.Body}
		_, _ = w.Write(mustMarshal(issue))
	}))
	defer server.Close()

	c := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	issue, err := CreatePilotIssue(context.Background(), c, AllowAllIssueRepos(), "owner", "repo", wantTitle, wantBody, []string{"pilot"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if issue.Number != 42 {
		t.Errorf("issue.Number = %d, want 42", issue.Number)
	}
	if gotTitle != wantTitle {
		t.Errorf("API received title %q, want %q", gotTitle, wantTitle)
	}
	if gotBody != wantBody {
		t.Errorf("API received body %q, want %q", gotBody, wantBody)
	}
	if len(gotLabels) != 1 || gotLabels[0] != "pilot" {
		t.Errorf("API received labels %v, want [pilot]", gotLabels)
	}
}

func mustMarshal(v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
