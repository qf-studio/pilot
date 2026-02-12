// Package mocks provides mock implementations for E2E testing.
package mocks

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters/github"
)

// GitHubMock provides a mock GitHub API server for E2E testing.
// It tracks state across requests to simulate real GitHub behavior.
type GitHubMock struct {
	server      *httptest.Server
	mu          sync.RWMutex
	issues      map[int]*github.Issue
	prs         map[int]*github.PullRequest
	checkRuns   map[string]*github.CheckRunsResponse // keyed by SHA
	nextIssue   int
	nextPR      int
	nextComment int

	// Callbacks for test assertions
	OnIssueLabelAdded   func(issueNum int, label string)
	OnIssueLabelRemoved func(issueNum int, label string)
	OnPRCreated         func(pr *github.PullRequest)
	OnPRMerged          func(prNum int)
	OnCommentCreated    func(issueNum int, body string)
}

// NewGitHubMock creates a new mock GitHub API server.
func NewGitHubMock() *GitHubMock {
	m := &GitHubMock{
		issues:      make(map[int]*github.Issue),
		prs:         make(map[int]*github.PullRequest),
		checkRuns:   make(map[string]*github.CheckRunsResponse),
		nextIssue:   1,
		nextPR:      1,
		nextComment: 1,
	}

	m.server = httptest.NewServer(http.HandlerFunc(m.handleRequest))
	return m
}

// URL returns the base URL of the mock server.
func (m *GitHubMock) URL() string {
	return m.server.URL
}

// Close shuts down the mock server.
func (m *GitHubMock) Close() {
	m.server.Close()
}

// CreateIssue adds an issue to the mock.
func (m *GitHubMock) CreateIssue(title, body string, labels []string) *github.Issue {
	m.mu.Lock()
	defer m.mu.Unlock()

	issueLabels := make([]github.Label, len(labels))
	for i, l := range labels {
		issueLabels[i] = github.Label{Name: l}
	}

	issue := &github.Issue{
		Number:    m.nextIssue,
		Title:     title,
		Body:      body,
		State:     "open",
		Labels:    issueLabels,
		CreatedAt: time.Now(),
	}
	m.issues[m.nextIssue] = issue
	m.nextIssue++
	return issue
}

// GetIssue retrieves an issue by number.
func (m *GitHubMock) GetIssue(num int) *github.Issue {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.issues[num]
}

// CreatePR creates a PR in the mock (for pre-populating state before tests).
func (m *GitHubMock) CreatePR(number int, title, branchName, sha string) *github.PullRequest {
	m.mu.Lock()
	defer m.mu.Unlock()

	mergeable := true
	pr := &github.PullRequest{
		Number:         number,
		Title:          title,
		State:          "open",
		Merged:         false,
		Mergeable:      &mergeable,
		MergeableState: "clean",
		Head: github.PRRef{
			Ref: branchName,
			SHA: sha,
		},
		Base: github.PRRef{
			Ref: "main",
		},
		HTMLURL: m.server.URL + "/owner/repo/pull/" + strconv.Itoa(number),
	}
	m.prs[number] = pr

	// Update nextPR if needed
	if number >= m.nextPR {
		m.nextPR = number + 1
	}

	return pr
}

// SetCIStatus sets the CI check status for a given SHA.
func (m *GitHubMock) SetCIStatus(sha string, checks []github.CheckRun) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkRuns[sha] = &github.CheckRunsResponse{
		TotalCount: len(checks),
		CheckRuns:  checks,
	}
}

// SetCIPassing sets all required checks as passing for a SHA.
func (m *GitHubMock) SetCIPassing(sha string, checkNames []string) {
	checks := make([]github.CheckRun, len(checkNames))
	for i, name := range checkNames {
		checks[i] = github.CheckRun{
			Name:       name,
			Status:     "completed",
			Conclusion: "success",
		}
	}
	m.SetCIStatus(sha, checks)
}

// SetCIFailing sets a check as failing for a SHA.
func (m *GitHubMock) SetCIFailing(sha string, failingCheck string, passingChecks []string) {
	checks := make([]github.CheckRun, 0, len(passingChecks)+1)
	checks = append(checks, github.CheckRun{
		Name:       failingCheck,
		Status:     "completed",
		Conclusion: "failure",
	})
	for _, name := range passingChecks {
		checks = append(checks, github.CheckRun{
			Name:       name,
			Status:     "completed",
			Conclusion: "success",
		})
	}
	m.SetCIStatus(sha, checks)
}

// GetPR retrieves a PR by number.
func (m *GitHubMock) GetPR(num int) *github.PullRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.prs[num]
}

// GetOpenPRs returns all open PRs.
func (m *GitHubMock) GetOpenPRs() []*github.PullRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var prs []*github.PullRequest
	for _, pr := range m.prs {
		if pr.State == "open" {
			prs = append(prs, pr)
		}
	}
	return prs
}

// handleRequest routes requests to appropriate handlers.
func (m *GitHubMock) handleRequest(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// Route based on path pattern
	switch {
	// GET /repos/{owner}/{repo}/issues
	case r.Method == "GET" && strings.HasSuffix(path, "/issues") && !strings.Contains(path, "/issues/"):
		m.handleListIssues(w, r)

	// GET /repos/{owner}/{repo}/issues/{number}
	case r.Method == "GET" && strings.Contains(path, "/issues/") && !strings.Contains(path, "/labels"):
		m.handleGetIssue(w, r)

	// POST /repos/{owner}/{repo}/issues
	case r.Method == "POST" && strings.HasSuffix(path, "/issues"):
		m.handleCreateIssue(w, r)

	// PATCH /repos/{owner}/{repo}/issues/{number}
	case r.Method == "PATCH" && strings.Contains(path, "/issues/"):
		m.handleUpdateIssue(w, r)

	// POST /repos/{owner}/{repo}/issues/{number}/labels
	case r.Method == "POST" && strings.Contains(path, "/labels"):
		m.handleAddLabel(w, r)

	// DELETE /repos/{owner}/{repo}/issues/{number}/labels/{label}
	case r.Method == "DELETE" && strings.Contains(path, "/labels/"):
		m.handleRemoveLabel(w, r)

	// POST /repos/{owner}/{repo}/issues/{number}/comments
	case r.Method == "POST" && strings.Contains(path, "/comments"):
		m.handleCreateComment(w, r)

	// GET /repos/{owner}/{repo}/pulls
	case r.Method == "GET" && strings.HasSuffix(path, "/pulls") && !strings.Contains(path, "/pulls/"):
		m.handleListPRs(w, r)

	// GET /repos/{owner}/{repo}/pulls/{number}
	case r.Method == "GET" && strings.Contains(path, "/pulls/") && !strings.Contains(path, "/merge"):
		m.handleGetPR(w, r)

	// POST /repos/{owner}/{repo}/pulls
	case r.Method == "POST" && strings.HasSuffix(path, "/pulls"):
		m.handleCreatePR(w, r)

	// PATCH /repos/{owner}/{repo}/pulls/{number}
	case r.Method == "PATCH" && strings.Contains(path, "/pulls/"):
		m.handleUpdatePR(w, r)

	// PUT /repos/{owner}/{repo}/pulls/{number}/merge
	case r.Method == "PUT" && strings.Contains(path, "/merge"):
		m.handleMergePR(w, r)

	// GET /repos/{owner}/{repo}/commits/{sha}/check-runs
	case r.Method == "GET" && strings.Contains(path, "/check-runs"):
		m.handleGetCheckRuns(w, r)

	// GET /repos/{owner}/{repo}/branches/{branch}
	case r.Method == "GET" && strings.Contains(path, "/branches/"):
		m.handleGetBranch(w, r)

	default:
		w.WriteHeader(http.StatusOK)
	}
}

func (m *GitHubMock) handleListIssues(w http.ResponseWriter, r *http.Request) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var issues []*github.Issue
	for _, issue := range m.issues {
		if issue.State == "open" {
			issues = append(issues, issue)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(issues)
}

func (m *GitHubMock) handleGetIssue(w http.ResponseWriter, r *http.Request) {
	num := m.extractIssueNumber(r.URL.Path)
	if num == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	m.mu.RLock()
	issue, ok := m.issues[num]
	m.mu.RUnlock()

	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(issue)
}

func (m *GitHubMock) handleCreateIssue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title  string   `json:"title"`
		Body   string   `json:"body"`
		Labels []string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	issue := m.CreateIssue(req.Title, req.Body, req.Labels)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(issue)
}

func (m *GitHubMock) handleUpdateIssue(w http.ResponseWriter, r *http.Request) {
	num := m.extractIssueNumber(r.URL.Path)
	if num == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	var req struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	m.mu.Lock()
	issue, ok := m.issues[num]
	if ok && req.State != "" {
		issue.State = req.State
	}
	m.mu.Unlock()

	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(issue)
}

func (m *GitHubMock) handleAddLabel(w http.ResponseWriter, r *http.Request) {
	num := m.extractIssueNumber(r.URL.Path)
	if num == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	var req struct {
		Labels []string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	m.mu.Lock()
	issue, ok := m.issues[num]
	if ok {
		for _, label := range req.Labels {
			issue.Labels = append(issue.Labels, github.Label{Name: label})
			if m.OnIssueLabelAdded != nil {
				m.OnIssueLabelAdded(num, label)
			}
		}
	}
	m.mu.Unlock()

	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(issue.Labels)
}

func (m *GitHubMock) handleRemoveLabel(w http.ResponseWriter, r *http.Request) {
	num := m.extractIssueNumber(r.URL.Path)
	label := m.extractLabel(r.URL.Path)

	m.mu.Lock()
	issue, ok := m.issues[num]
	if ok {
		var newLabels []github.Label
		for _, l := range issue.Labels {
			if !strings.EqualFold(l.Name, label) {
				newLabels = append(newLabels, l)
			}
		}
		issue.Labels = newLabels
		if m.OnIssueLabelRemoved != nil {
			m.OnIssueLabelRemoved(num, label)
		}
	}
	m.mu.Unlock()

	w.WriteHeader(http.StatusOK)
}

func (m *GitHubMock) handleCreateComment(w http.ResponseWriter, r *http.Request) {
	num := m.extractIssueNumber(r.URL.Path)

	var req struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	m.mu.Lock()
	commentID := m.nextComment
	m.nextComment++
	if m.OnCommentCreated != nil {
		m.OnCommentCreated(num, req.Body)
	}
	m.mu.Unlock()

	resp := map[string]interface{}{"id": commentID, "body": req.Body}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

func (m *GitHubMock) handleListPRs(w http.ResponseWriter, r *http.Request) {
	m.mu.RLock()
	prs := m.GetOpenPRs()
	m.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(prs)
}

func (m *GitHubMock) handleGetPR(w http.ResponseWriter, r *http.Request) {
	num := m.extractPRNumber(r.URL.Path)
	if num == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	m.mu.RLock()
	pr, ok := m.prs[num]
	m.mu.RUnlock()

	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(pr)
}

func (m *GitHubMock) handleCreatePR(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Head  string `json:"head"`
		Base  string `json:"base"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	m.mu.Lock()
	mergeable := true
	pr := &github.PullRequest{
		Number:    m.nextPR,
		Title:     req.Title,
		Body:      req.Body,
		State:     "open",
		Merged:    false,
		Mergeable: &mergeable,
		Head: github.PRRef{
			Ref: req.Head,
			SHA: "abc" + strconv.Itoa(m.nextPR) + "123",
		},
		Base: github.PRRef{
			Ref: req.Base,
		},
		HTMLURL: m.server.URL + "/owner/repo/pull/" + strconv.Itoa(m.nextPR),
	}
	m.prs[m.nextPR] = pr
	m.nextPR++

	if m.OnPRCreated != nil {
		m.OnPRCreated(pr)
	}
	m.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(pr)
}

func (m *GitHubMock) handleUpdatePR(w http.ResponseWriter, r *http.Request) {
	num := m.extractPRNumber(r.URL.Path)
	if num == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	var req struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	m.mu.Lock()
	pr, ok := m.prs[num]
	if ok && req.State != "" {
		pr.State = req.State
	}
	m.mu.Unlock()

	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(pr)
}

func (m *GitHubMock) handleMergePR(w http.ResponseWriter, r *http.Request) {
	num := m.extractPRNumber(r.URL.Path)
	if num == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	m.mu.Lock()
	pr, ok := m.prs[num]
	if ok {
		pr.State = "closed"
		pr.Merged = true
		if m.OnPRMerged != nil {
			m.OnPRMerged(num)
		}
	}
	m.mu.Unlock()

	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	resp := map[string]interface{}{
		"sha":     pr.Head.SHA,
		"merged":  true,
		"message": "Pull Request successfully merged",
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (m *GitHubMock) handleGetCheckRuns(w http.ResponseWriter, r *http.Request) {
	sha := m.extractSHA(r.URL.Path)

	m.mu.RLock()
	checks, ok := m.checkRuns[sha]
	m.mu.RUnlock()

	if !ok {
		// Return empty checks
		checks = &github.CheckRunsResponse{TotalCount: 0, CheckRuns: []github.CheckRun{}}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(checks)
}

func (m *GitHubMock) handleGetBranch(w http.ResponseWriter, r *http.Request) {
	// Return a simple branch response
	resp := github.Branch{
		Name:   "main",
		Commit: github.BranchCommit{SHA: "mainsha123"},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// Helper methods to extract IDs from paths

func (m *GitHubMock) extractIssueNumber(path string) int {
	// /repos/owner/repo/issues/123 or /repos/owner/repo/issues/123/labels
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == "issues" && i+1 < len(parts) {
			numStr := strings.Split(parts[i+1], "/")[0]
			num, _ := strconv.Atoi(numStr)
			return num
		}
	}
	return 0
}

func (m *GitHubMock) extractPRNumber(path string) int {
	// /repos/owner/repo/pulls/123 or /repos/owner/repo/pulls/123/merge
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == "pulls" && i+1 < len(parts) {
			numStr := strings.Split(parts[i+1], "/")[0]
			num, _ := strconv.Atoi(numStr)
			return num
		}
	}
	return 0
}

func (m *GitHubMock) extractSHA(path string) string {
	// /repos/owner/repo/commits/abc123/check-runs
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == "commits" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

func (m *GitHubMock) extractLabel(path string) string {
	// /repos/owner/repo/issues/123/labels/pilot-in-progress
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == "labels" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}
