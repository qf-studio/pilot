package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
)

func TestNewFeedbackLoop(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.IssueLabels = []string{"pilot", "autopilot-fix"}

	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	if fl == nil {
		t.Fatal("NewFeedbackLoop returned nil")
	}
	if fl.owner != "owner" {
		t.Errorf("owner = %s, want owner", fl.owner)
	}
	if fl.repo != "repo" {
		t.Errorf("repo = %s, want repo", fl.repo)
	}
	if len(fl.issueLabels) != 2 {
		t.Errorf("issueLabels = %v, want 2 labels", fl.issueLabels)
	}
}

func TestFeedbackLoop_CreateFailureIssue_CIFailed(t *testing.T) {
	capturedTitle := ""
	capturedBody := ""
	capturedLabels := []string{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST" {
			var input github.IssueInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			capturedTitle = input.Title
			capturedBody = input.Body
			capturedLabels = input.Labels

			resp := github.Issue{Number: 100}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.IssueLabels = []string{"pilot", "autopilot-fix"}

	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber:    42,
		PRURL:       "https://github.com/owner/repo/pull/42",
		IssueNumber: 10,
		HeadSHA:     "abc1234567890",
	}

	issueNum, err := fl.CreateFailureIssue(
		context.Background(),
		prState,
		FailureCIPreMerge,
		[]string{"build", "test"},
		"Error: build failed\nNPM ERR! code 1",
		0,
	)

	if err != nil {
		t.Fatalf("CreateFailureIssue() error = %v", err)
	}
	if issueNum != 100 {
		t.Errorf("CreateFailureIssue() = %d, want 100", issueNum)
	}

	// Verify title
	expectedTitle := "Fix CI failure from PR #42"
	if capturedTitle != expectedTitle {
		t.Errorf("title = %q, want %q", capturedTitle, expectedTitle)
	}

	// Verify body contains expected sections
	if !strings.Contains(capturedBody, "Autopilot: Auto-Generated Fix Request") {
		t.Error("body should contain header")
	}
	if !strings.Contains(capturedBody, "Original PR**: #42") {
		t.Error("body should contain PR reference")
	}
	if !strings.Contains(capturedBody, "Original Issue**: #10") {
		t.Error("body should contain issue reference")
	}
	if !strings.Contains(capturedBody, "abc1234") {
		t.Error("body should contain SHA (truncated)")
	}
	if !strings.Contains(capturedBody, "- [ ] build") {
		t.Error("body should contain failed checks")
	}
	if !strings.Contains(capturedBody, "- [ ] test") {
		t.Error("body should contain failed checks")
	}
	if !strings.Contains(capturedBody, "Error: build failed") {
		t.Error("body should contain error logs")
	}
	// GH-1567: Logs should be in collapsible details block
	if !strings.Contains(capturedBody, "<details><summary>CI Error Logs</summary>") {
		t.Error("body should wrap logs in collapsible <details> block")
	}
	if !strings.Contains(capturedBody, "</details>") {
		t.Error("body should close </details> tag")
	}
	if !strings.Contains(capturedBody, "Fix the CI failures") {
		t.Error("body should contain task instructions")
	}
	// GH-1798: Verify dependency annotation for parent issue
	if !strings.Contains(capturedBody, "Depends on: #10") {
		t.Error("body should contain dependency annotation for parent issue")
	}

	// Verify labels
	if len(capturedLabels) != 2 || capturedLabels[0] != "pilot" {
		t.Errorf("labels = %v, want [pilot autopilot-fix]", capturedLabels)
	}
}

func TestFeedbackLoop_CreateFailureIssue_PostMerge(t *testing.T) {
	capturedTitle := ""
	capturedBody := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST" {
			var input github.IssueInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			capturedTitle = input.Title
			capturedBody = input.Body

			resp := github.Issue{Number: 101}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		PRURL:    "https://github.com/owner/repo/pull/42",
		HeadSHA:  "abc1234567890",
	}

	issueNum, err := fl.CreateFailureIssue(
		context.Background(),
		prState,
		FailureCIPostMerge,
		[]string{"deploy"},
		"",
		0,
	)

	if err != nil {
		t.Fatalf("CreateFailureIssue() error = %v", err)
	}
	if issueNum != 101 {
		t.Errorf("CreateFailureIssue() = %d, want 101", issueNum)
	}

	// Verify different title for post-merge
	expectedTitle := "Fix post-merge CI failure (PR #42)"
	if capturedTitle != expectedTitle {
		t.Errorf("title = %q, want %q", capturedTitle, expectedTitle)
	}

	// Verify task instructions for post-merge
	if !strings.Contains(capturedBody, "PR was merged but CI failed afterward") {
		t.Error("body should contain post-merge specific instructions")
	}
}

func TestFeedbackLoop_CreateFailureIssue_MergeConflict(t *testing.T) {
	capturedTitle := ""
	capturedBody := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST" {
			var input github.IssueInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			capturedTitle = input.Title
			capturedBody = input.Body

			resp := github.Issue{Number: 102}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		HeadSHA:  "abc1234",
	}

	issueNum, err := fl.CreateFailureIssue(
		context.Background(),
		prState,
		FailureMerge,
		nil,
		"",
		0,
	)

	if err != nil {
		t.Fatalf("CreateFailureIssue() error = %v", err)
	}
	if issueNum != 102 {
		t.Errorf("CreateFailureIssue() = %d, want 102", issueNum)
	}

	expectedTitle := "Resolve merge conflict for PR #42"
	if capturedTitle != expectedTitle {
		t.Errorf("title = %q, want %q", capturedTitle, expectedTitle)
	}

	if !strings.Contains(capturedBody, "Resolve the merge conflicts") {
		t.Error("body should contain merge conflict instructions")
	}
}

func TestFeedbackLoop_CreateFailureIssue_Deployment(t *testing.T) {
	capturedTitle := ""
	capturedBody := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST" {
			var input github.IssueInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			capturedTitle = input.Title
			capturedBody = input.Body

			resp := github.Issue{Number: 103}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		HeadSHA:  "abc1234",
	}

	issueNum, err := fl.CreateFailureIssue(
		context.Background(),
		prState,
		FailureDeployment,
		nil,
		"Deployment failed: container health check failed",
		0,
	)

	if err != nil {
		t.Fatalf("CreateFailureIssue() error = %v", err)
	}
	if issueNum != 103 {
		t.Errorf("CreateFailureIssue() = %d, want 103", issueNum)
	}

	expectedTitle := "Fix deployment failure (PR #42)"
	if capturedTitle != expectedTitle {
		t.Errorf("title = %q, want %q", capturedTitle, expectedTitle)
	}

	if !strings.Contains(capturedBody, "deployment failed") {
		t.Error("body should contain deployment instructions")
	}
}

func TestFeedbackLoop_CreateFailureIssue_UnknownType(t *testing.T) {
	capturedTitle := ""
	capturedBody := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST" {
			var input github.IssueInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			capturedTitle = input.Title
			capturedBody = input.Body

			resp := github.Issue{Number: 104}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		HeadSHA:  "abc1234",
	}

	issueNum, err := fl.CreateFailureIssue(
		context.Background(),
		prState,
		FailureType("unknown"),
		nil,
		"",
		0,
	)

	if err != nil {
		t.Fatalf("CreateFailureIssue() error = %v", err)
	}
	if issueNum != 104 {
		t.Errorf("CreateFailureIssue() = %d, want 104", issueNum)
	}

	expectedTitle := "Fix issue from PR #42"
	if capturedTitle != expectedTitle {
		t.Errorf("title = %q, want %q", capturedTitle, expectedTitle)
	}

	if !strings.Contains(capturedBody, "Investigate and fix") {
		t.Error("body should contain generic instructions")
	}
}

func TestFeedbackLoop_IssueBody_TruncatesLogs(t *testing.T) {
	capturedBody := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST" {
			var input github.IssueInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			capturedBody = input.Body

			resp := github.Issue{Number: 105}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		HeadSHA:  "abc1234",
	}

	// Create very long logs (over 2000 chars)
	longLogs := strings.Repeat("ERROR: This is a very long log line that repeats. ", 100)
	if len(longLogs) <= 2000 {
		t.Fatal("test logs should be longer than 2000 chars")
	}

	_, err := fl.CreateFailureIssue(
		context.Background(),
		prState,
		FailureCIPreMerge,
		[]string{"build"},
		longLogs,
		0,
	)

	if err != nil {
		t.Fatalf("CreateFailureIssue() error = %v", err)
	}

	// Verify logs are truncated
	if !strings.Contains(capturedBody, "... (truncated)") {
		t.Error("body should indicate logs were truncated")
	}

	// Body should not contain the full logs
	if strings.Contains(capturedBody, longLogs) {
		t.Error("body should have truncated logs")
	}
}

func TestFeedbackLoop_IssueBody_NoLogs(t *testing.T) {
	capturedBody := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST" {
			var input github.IssueInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			capturedBody = input.Body

			resp := github.Issue{Number: 106}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		HeadSHA:  "abc1234",
	}

	_, err := fl.CreateFailureIssue(
		context.Background(),
		prState,
		FailureCIPreMerge,
		[]string{"build"},
		"", // No logs
		0,
	)

	if err != nil {
		t.Fatalf("CreateFailureIssue() error = %v", err)
	}

	// Should not have error logs section
	if strings.Contains(capturedBody, "Error Logs") {
		t.Error("body should not contain Error Logs section when no logs provided")
	}
}

func TestFeedbackLoop_IssueBody_NoFailedChecks(t *testing.T) {
	capturedBody := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST" {
			var input github.IssueInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			capturedBody = input.Body

			resp := github.Issue{Number: 107}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		HeadSHA:  "abc1234",
	}

	_, err := fl.CreateFailureIssue(
		context.Background(),
		prState,
		FailureCIPreMerge,
		nil, // No failed checks
		"Some error",
		0,
	)

	if err != nil {
		t.Fatalf("CreateFailureIssue() error = %v", err)
	}

	// Should not have failed checks section
	if strings.Contains(capturedBody, "Failed Checks") {
		t.Error("body should not contain Failed Checks section when no checks provided")
	}
}

func TestFeedbackLoop_IssueBody_NoIssueNumber(t *testing.T) {
	capturedBody := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST" {
			var input github.IssueInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			capturedBody = input.Body

			resp := github.Issue{Number: 108}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber:    42,
		HeadSHA:     "abc1234",
		IssueNumber: 0, // No linked issue
	}

	_, err := fl.CreateFailureIssue(
		context.Background(),
		prState,
		FailureCIPreMerge,
		[]string{"build"},
		"",
		0,
	)

	if err != nil {
		t.Fatalf("CreateFailureIssue() error = %v", err)
	}

	// Should not reference original issue
	if strings.Contains(capturedBody, "Original Issue") {
		t.Error("body should not contain Original Issue when no issue number")
	}
	// GH-1798: Should not contain dependency annotation when no parent issue
	if strings.Contains(capturedBody, "Depends on:") {
		t.Error("body should not contain dependency annotation when IssueNumber is 0")
	}
}

func TestFeedbackLoop_IssueBody_ShortSHA(t *testing.T) {
	capturedBody := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST" {
			var input github.IssueInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			capturedBody = input.Body

			resp := github.Issue{Number: 109}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		HeadSHA:  "abc", // Very short SHA (less than 7 chars)
	}

	_, err := fl.CreateFailureIssue(
		context.Background(),
		prState,
		FailureCIPreMerge,
		[]string{"build"},
		"",
		0,
	)

	if err != nil {
		t.Fatalf("CreateFailureIssue() error = %v", err)
	}

	// Should not include SHA when too short
	if strings.Contains(capturedBody, "SHA") {
		t.Error("body should not contain SHA when SHA is too short")
	}
}

func TestFeedbackLoop_IssueLabels(t *testing.T) {
	tests := []struct {
		name       string
		labels     []string
		wantLabels []string
	}{
		{
			name:       "default labels",
			labels:     []string{"pilot", "autopilot-fix"},
			wantLabels: []string{"pilot", "autopilot-fix"},
		},
		{
			name:       "custom labels",
			labels:     []string{"bug", "critical", "automated"},
			wantLabels: []string{"bug", "critical", "automated"},
		},
		{
			name:       "no labels",
			labels:     []string{},
			wantLabels: []string{},
		},
		{
			name:       "nil labels",
			labels:     nil,
			wantLabels: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capturedLabels := []string{}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST" {
					var input github.IssueInput
					_ = json.NewDecoder(r.Body).Decode(&input)
					capturedLabels = input.Labels

					resp := github.Issue{Number: 110}
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(resp)
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			cfg.IssueLabels = tt.labels

			fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

			prState := &PRState{
				PRNumber: 42,
				HeadSHA:  "abc1234",
			}

			_, err := fl.CreateFailureIssue(
				context.Background(),
				prState,
				FailureCIPreMerge,
				nil,
				"",
				0,
			)

			if err != nil {
				t.Fatalf("CreateFailureIssue() error = %v", err)
			}

			if len(capturedLabels) != len(tt.wantLabels) {
				t.Errorf("labels = %v, want %v", capturedLabels, tt.wantLabels)
			}
		})
	}
}

func TestFeedbackLoop_CreateFailureIssue_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message": "Internal Server Error"}`))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		HeadSHA:  "abc1234",
	}

	_, err := fl.CreateFailureIssue(
		context.Background(),
		prState,
		FailureCIPreMerge,
		[]string{"build"},
		"",
		0,
	)

	if err == nil {
		t.Error("CreateFailureIssue() should return error on API failure")
	}
}

func TestFeedbackLoop_GenerateTitle(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	tests := []struct {
		name        string
		failureType FailureType
		prNumber    int
		wantTitle   string
	}{
		{
			name:        "CI pre-merge",
			failureType: FailureCIPreMerge,
			prNumber:    42,
			wantTitle:   "Fix CI failure from PR #42",
		},
		{
			name:        "CI post-merge",
			failureType: FailureCIPostMerge,
			prNumber:    123,
			wantTitle:   "Fix post-merge CI failure (PR #123)",
		},
		{
			name:        "merge conflict",
			failureType: FailureMerge,
			prNumber:    99,
			wantTitle:   "Resolve merge conflict for PR #99",
		},
		{
			name:        "deployment",
			failureType: FailureDeployment,
			prNumber:    1,
			wantTitle:   "Fix deployment failure (PR #1)",
		},
		{
			name:        "unknown type",
			failureType: FailureType("unknown"),
			prNumber:    50,
			wantTitle:   "Fix issue from PR #50",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prState := &PRState{PRNumber: tt.prNumber}
			got := fl.generateTitle(prState, tt.failureType)
			if got != tt.wantTitle {
				t.Errorf("generateTitle() = %q, want %q", got, tt.wantTitle)
			}
		})
	}
}

func TestFeedbackLoop_BodyContainsAutoGeneratedNote(t *testing.T) {
	capturedBody := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST" {
			var input github.IssueInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			capturedBody = input.Body

			resp := github.Issue{Number: 111}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		HeadSHA:  "abc1234",
	}

	_, err := fl.CreateFailureIssue(
		context.Background(),
		prState,
		FailureCIPreMerge,
		nil,
		"",
		0,
	)

	if err != nil {
		t.Fatalf("CreateFailureIssue() error = %v", err)
	}

	// Should contain auto-generated note
	if !strings.Contains(capturedBody, "auto-generated by Pilot autopilot") {
		t.Error("body should contain auto-generated note")
	}
}

func TestFeedbackLoop_IssueBody_BranchMetadata(t *testing.T) {
	capturedBody := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST" {
			var input github.IssueInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			capturedBody = input.Body

			resp := github.Issue{Number: 112}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber:    42,
		HeadSHA:     "abc1234567890",
		IssueNumber: 10,
		BranchName:  "pilot/GH-10",
	}

	_, err := fl.CreateFailureIssue(
		context.Background(),
		prState,
		FailureCIPreMerge,
		[]string{"lint"},
		"",
		0,
	)

	if err != nil {
		t.Fatalf("CreateFailureIssue() error = %v", err)
	}

	// Should contain human-readable branch reference
	if !strings.Contains(capturedBody, "**Branch**: pilot/GH-10") {
		t.Error("body should contain branch reference in context section")
	}

	// Should contain machine-readable metadata comment with branch, PR number, and iteration (GH-1267, GH-1566)
	if !strings.Contains(capturedBody, "<!-- autopilot-meta branch:pilot/GH-10 pr:42 iteration:0 -->") {
		t.Error("body should contain autopilot-meta comment with branch, PR number, and iteration")
	}
}

func TestFeedbackLoop_IssueBody_NoBranchMetadataWhenEmpty(t *testing.T) {
	capturedBody := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST" {
			var input github.IssueInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			capturedBody = input.Body

			resp := github.Issue{Number: 113}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		HeadSHA:  "abc1234567890",
		// BranchName intentionally empty
	}

	_, err := fl.CreateFailureIssue(
		context.Background(),
		prState,
		FailureCIPreMerge,
		[]string{"lint"},
		"",
		0,
	)

	if err != nil {
		t.Fatalf("CreateFailureIssue() error = %v", err)
	}

	// Should NOT contain branch metadata when BranchName is empty
	if strings.Contains(capturedBody, "autopilot-meta") {
		t.Error("body should not contain autopilot-meta when BranchName is empty")
	}
	if strings.Contains(capturedBody, "**Branch**") {
		t.Error("body should not contain Branch field when BranchName is empty")
	}
}

func TestFeedbackLoop_SetLearningLoop(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	if fl.learningLoop != nil {
		t.Error("learningLoop should be nil initially")
	}

	// SetLearningLoop accepts nil gracefully
	fl.SetLearningLoop(nil)
	if fl.learningLoop != nil {
		t.Error("learningLoop should remain nil when set to nil")
	}
}

func TestFeedbackLoop_CreateFailureIssue_WithKnownPatterns(t *testing.T) {
	capturedBody := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST" {
			var input github.IssueInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			capturedBody = input.Body

			resp := github.Issue{Number: 120}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.IssueLabels = []string{"pilot"}

	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	// No learning loop set — body should NOT contain "Known Patterns"
	prState := &PRState{
		PRNumber: 42,
		HeadSHA:  "abc1234567890",
	}

	_, err := fl.CreateFailureIssue(
		context.Background(),
		prState,
		FailureCIPreMerge,
		[]string{"build"},
		"Error: build failed",
		0,
	)
	if err != nil {
		t.Fatalf("CreateFailureIssue() error = %v", err)
	}

	if strings.Contains(capturedBody, "Known Patterns") {
		t.Error("body should NOT contain Known Patterns section when no learning loop is set")
	}
}

func TestFeedbackLoop_GenerateBody_WithPatterns(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		HeadSHA:  "abc1234567890",
	}

	// Test with patterns passed directly to generateBody
	patterns := []*memory.CrossPattern{
		{Title: "Missing import", Description: "Always check imports after refactoring", Confidence: 0.85},
		{Title: "Test timeout", Description: "Increase timeout for integration tests", Confidence: 0.92},
	}

	body := fl.generateBody(prState, FailureCIPreMerge, []string{"build"}, "Error: build failed", 0, patterns)

	if !strings.Contains(body, "Known Patterns") {
		t.Error("body should contain Known Patterns section when patterns provided")
	}
	if !strings.Contains(body, "Missing import") {
		t.Error("body should contain pattern title 'Missing import'")
	}
	if !strings.Contains(body, "85%") {
		t.Error("body should contain pattern confidence as percentage")
	}
	if !strings.Contains(body, "Test timeout") {
		t.Error("body should contain pattern title 'Test timeout'")
	}
	if !strings.Contains(body, "92%") {
		t.Error("body should contain pattern confidence as percentage")
	}
}

func TestFeedbackLoop_GenerateBody_NoPatterns(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		HeadSHA:  "abc1234567890",
	}

	// Empty patterns slice — no Known Patterns section
	body := fl.generateBody(prState, FailureCIPreMerge, []string{"build"}, "", 0, nil)

	if strings.Contains(body, "Known Patterns") {
		t.Error("body should NOT contain Known Patterns section when no patterns")
	}
}

func TestFailureTypes(t *testing.T) {
	// Verify all failure type constants
	tests := []struct {
		ft   FailureType
		want string
	}{
		{FailureCIPreMerge, "ci_pre_merge"},
		{FailureCIPostMerge, "ci_post_merge"},
		{FailureMerge, "merge_conflict"},
		{FailureDeployment, "deployment"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if string(tt.ft) != tt.want {
				t.Errorf("FailureType = %s, want %s", tt.ft, tt.want)
			}
		})
	}
}

func TestFormatReviewFeedback_FormatsComments(t *testing.T) {
	reviews := []*github.PullRequestReview{
		{ID: 1, User: github.User{Login: "alice"}, Body: "Please fix the error handling", State: "CHANGES_REQUESTED"},
	}
	comments := []*github.PRReviewComment{
		{ID: 10, Body: "This function needs a nil check", Path: "internal/foo.go", Line: 42, User: github.User{Login: "alice"}},
		{ID: 11, Body: "Missing test coverage", Path: "internal/foo.go", Line: 55, User: github.User{Login: "alice"}},
		{ID: 12, Body: "Rename this variable", Path: "internal/bar.go", Line: 10, User: github.User{Login: "bob"}},
	}

	result := formatReviewFeedback(reviews, comments)

	// Check that review body is included in a details block
	if !strings.Contains(result, "Review by alice") {
		t.Error("expected review body to reference alice")
	}
	if !strings.Contains(result, "Please fix the error handling") {
		t.Error("expected review body text")
	}

	// Check file-grouped comments
	if !strings.Contains(result, "internal/foo.go") {
		t.Error("expected foo.go file grouping")
	}
	if !strings.Contains(result, "internal/bar.go") {
		t.Error("expected bar.go file grouping")
	}
	if !strings.Contains(result, "Line 42") {
		t.Error("expected line number reference")
	}
	if !strings.Contains(result, "This function needs a nil check") {
		t.Error("expected comment body")
	}

	// Check <details> blocks
	detailsCount := strings.Count(result, "<details>")
	if detailsCount != 3 { // 1 review + 2 files
		t.Errorf("expected 3 <details> blocks, got %d", detailsCount)
	}
}

func TestFormatReviewFeedback_Truncation(t *testing.T) {
	// Create a very long review body that exceeds 4000 chars
	longBody := strings.Repeat("x", 5000)
	reviews := []*github.PullRequestReview{
		{ID: 1, User: github.User{Login: "alice"}, Body: longBody, State: "CHANGES_REQUESTED"},
	}

	result := formatReviewFeedback(reviews, nil)

	if len(result) > 4100 { // 4000 + truncation message
		t.Errorf("expected truncation, got length %d", len(result))
	}
	if !strings.Contains(result, "... (truncated)") {
		t.Error("expected truncation marker")
	}
}

func TestGenerateTitle_ReviewRequested(t *testing.T) {
	cfg := DefaultConfig()
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	prState := &PRState{PRNumber: 42}
	title := fl.generateTitle(prState, FailureReviewRequested)

	expected := "Address review feedback on PR #42"
	if title != expected {
		t.Errorf("title = %q, want %q", title, expected)
	}
}

func TestCreateReviewIssue(t *testing.T) {
	issueCreated := false
	var createdTitle string
	var createdBody string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			issueCreated = true
			var input github.IssueInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			createdTitle = input.Title
			createdBody = input.Body
			resp := github.Issue{Number: 100, Title: input.Title}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustFLJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber:    42,
		IssueNumber: 10,
		BranchName:  "pilot/GH-10",
	}

	reviews := []*github.PullRequestReview{
		{ID: 1, User: github.User{Login: "alice"}, Body: "Fix this", State: "CHANGES_REQUESTED"},
	}
	comments := []*github.PRReviewComment{
		{ID: 10, Body: "Bad code", Path: "foo.go", Line: 5, User: github.User{Login: "alice"}},
	}

	issueNum, err := fl.CreateReviewIssue(context.Background(), prState, reviews, comments, 1)
	if err != nil {
		t.Fatalf("CreateReviewIssue error: %v", err)
	}

	if !issueCreated {
		t.Fatal("expected issue to be created")
	}
	if issueNum != 100 {
		t.Errorf("issue number = %d, want 100", issueNum)
	}
	if !strings.Contains(createdTitle, "review feedback") {
		t.Errorf("title should contain 'review feedback': %s", createdTitle)
	}
	if !strings.Contains(createdBody, "Fix this") {
		t.Error("body should contain review text")
	}
	if !strings.Contains(createdBody, "foo.go") {
		t.Error("body should contain file path from comment")
	}
	if !strings.Contains(createdBody, "autopilot-meta") {
		t.Error("body should contain autopilot-meta")
	}
}

func mustFLJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("failed to marshal JSON: %v", err)
	}
	return b
}
