package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestNewWebhookHandler(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)
	handler := NewWebhookHandler(client, "secret123", "pilot")

	if handler == nil {
		t.Fatal("NewWebhookHandler returned nil")
	}
	if handler.client != client {
		t.Error("handler.client not set correctly")
	}
	if handler.webhookSecret != "secret123" {
		t.Errorf("handler.webhookSecret = %s, want 'secret123'", handler.webhookSecret)
	}
	if handler.pilotLabel != "pilot" {
		t.Errorf("handler.pilotLabel = %s, want 'pilot'", handler.pilotLabel)
	}
}

func TestOnIssue(t *testing.T) {
	handler := NewWebhookHandler(nil, "", "pilot")

	handler.OnIssue(func(ctx context.Context, issue *Issue, repo *Repository) error {
		return nil
	})

	if handler.onIssue == nil {
		t.Error("OnIssue did not set callback")
	}
}

func TestVerifySignature(t *testing.T) {
	tests := []struct {
		name      string
		secret    string
		payload   string
		signature string
		want      bool
	}{
		{
			name:      "valid signature",
			secret:    "mysecret",
			payload:   `{"action":"opened"}`,
			signature: computeHMAC("mysecret", `{"action":"opened"}`),
			want:      true,
		},
		{
			name:      "invalid signature",
			secret:    "mysecret",
			payload:   `{"action":"opened"}`,
			signature: "sha256=invalid123456789",
			want:      false,
		},
		{
			name:      "empty secret - skip verification",
			secret:    "",
			payload:   `{"action":"opened"}`,
			signature: "anything",
			want:      true,
		},
		{
			name:      "missing sha256 prefix",
			secret:    "mysecret",
			payload:   `{"action":"opened"}`,
			signature: "abc123",
			want:      false,
		},
		{
			name:      "wrong payload",
			secret:    "mysecret",
			payload:   `{"action":"closed"}`,
			signature: computeHMAC("mysecret", `{"action":"opened"}`),
			want:      false,
		},
		{
			name:      "wrong secret",
			secret:    "wrongsecret",
			payload:   `{"action":"opened"}`,
			signature: computeHMAC("mysecret", `{"action":"opened"}`),
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewWebhookHandler(nil, tt.secret, "pilot")
			got := h.VerifySignature([]byte(tt.payload), tt.signature)
			if got != tt.want {
				t.Errorf("VerifySignature() = %v, want %v", got, tt.want)
			}
		})
	}
}

// computeHMAC computes the HMAC-SHA256 signature for testing
func computeHMAC(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestHandle(t *testing.T) {
	tests := []struct {
		name        string
		eventType   string
		payload     map[string]interface{}
		wantProcess bool
		wantErr     bool
	}{
		{
			name:        "non-issue event",
			eventType:   "push",
			payload:     map[string]interface{}{"action": "push"},
			wantProcess: false,
			wantErr:     false,
		},
		{
			name:        "pull_request event - ignored",
			eventType:   "pull_request",
			payload:     map[string]interface{}{"action": "opened"},
			wantProcess: false,
			wantErr:     false,
		},
		{
			name:      "issues event - edited action - ignored",
			eventType: "issues",
			payload: map[string]interface{}{
				"action": "edited",
				"issue": map[string]interface{}{
					"number": float64(42),
				},
			},
			wantProcess: false,
			wantErr:     false,
		},
		{
			name:      "issues event - closed action - ignored",
			eventType: "issues",
			payload: map[string]interface{}{
				"action": "closed",
				"issue": map[string]interface{}{
					"number": float64(42),
				},
			},
			wantProcess: false,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewWebhookHandler(nil, "", "pilot")

			processed := false
			handler.OnIssue(func(ctx context.Context, issue *Issue, repo *Repository) error {
				processed = true
				return nil
			})

			err := handler.Handle(context.Background(), tt.eventType, tt.payload)

			if (err != nil) != tt.wantErr {
				t.Errorf("Handle() error = %v, wantErr %v", err, tt.wantErr)
			}
			if processed != tt.wantProcess {
				t.Errorf("processed = %v, want %v", processed, tt.wantProcess)
			}
		})
	}
}

func TestHandleIssueOpened(t *testing.T) {
	tests := []struct {
		name        string
		payload     map[string]interface{}
		hasPilot    bool
		wantProcess bool
		wantErr     bool
	}{
		{
			name: "issue with pilot label",
			payload: map[string]interface{}{
				"action": "opened",
				"issue": map[string]interface{}{
					"number":   float64(42),
					"title":    "Test Issue",
					"body":     "Issue body",
					"state":    "open",
					"html_url": "https://github.com/org/repo/issues/42",
					"labels": []interface{}{
						map[string]interface{}{
							"id":   float64(123),
							"name": "pilot",
						},
					},
				},
				"repository": map[string]interface{}{
					"name":      "repo",
					"full_name": "org/repo",
					"html_url":  "https://github.com/org/repo",
					"owner": map[string]interface{}{
						"login": "org",
					},
				},
			},
			hasPilot:    true,
			wantProcess: true,
			wantErr:     false,
		},
		{
			name: "issue without pilot label",
			payload: map[string]interface{}{
				"action": "opened",
				"issue": map[string]interface{}{
					"number":   float64(42),
					"title":    "Test Issue",
					"body":     "Issue body",
					"state":    "open",
					"html_url": "https://github.com/org/repo/issues/42",
					"labels": []interface{}{
						map[string]interface{}{
							"id":   float64(123),
							"name": "bug",
						},
					},
				},
				"repository": map[string]interface{}{
					"name":      "repo",
					"full_name": "org/repo",
					"html_url":  "https://github.com/org/repo",
					"owner": map[string]interface{}{
						"login": "org",
					},
				},
			},
			hasPilot:    false,
			wantProcess: false,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock server to return issue details
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				issue := Issue{
					Number:  42,
					Title:   "Test Issue",
					Body:    "Issue body",
					State:   "open",
					HTMLURL: "https://github.com/org/repo/issues/42",
				}
				if tt.hasPilot {
					issue.Labels = []Label{{Name: "pilot"}}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(issue)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			handler := NewWebhookHandler(client, "", "pilot")

			processed := false
			handler.OnIssue(func(ctx context.Context, issue *Issue, repo *Repository) error {
				processed = true
				return nil
			})

			err := handler.Handle(context.Background(), "issues", tt.payload)

			if (err != nil) != tt.wantErr {
				t.Errorf("Handle() error = %v, wantErr %v", err, tt.wantErr)
			}
			if processed != tt.wantProcess {
				t.Errorf("processed = %v, want %v", processed, tt.wantProcess)
			}
		})
	}
}

func TestHandleIssueLabeled(t *testing.T) {
	tests := []struct {
		name        string
		payload     map[string]interface{}
		wantProcess bool
		wantErr     bool
	}{
		{
			name: "labeled with pilot label",
			payload: map[string]interface{}{
				"action": "labeled",
				"label": map[string]interface{}{
					"id":   float64(456),
					"name": "pilot",
				},
				"issue": map[string]interface{}{
					"number":   float64(42),
					"title":    "Test Issue",
					"body":     "Issue body",
					"state":    "open",
					"html_url": "https://github.com/org/repo/issues/42",
					"labels": []interface{}{
						map[string]interface{}{
							"id":   float64(456),
							"name": "pilot",
						},
					},
				},
				"repository": map[string]interface{}{
					"name":      "repo",
					"full_name": "org/repo",
					"html_url":  "https://github.com/org/repo",
					"owner": map[string]interface{}{
						"login": "org",
					},
				},
			},
			wantProcess: true,
			wantErr:     false,
		},
		{
			name: "labeled with non-pilot label",
			payload: map[string]interface{}{
				"action": "labeled",
				"label": map[string]interface{}{
					"id":   float64(789),
					"name": "bug",
				},
				"issue": map[string]interface{}{
					"number":   float64(42),
					"title":    "Test Issue",
					"body":     "Issue body",
					"state":    "open",
					"html_url": "https://github.com/org/repo/issues/42",
					"labels": []interface{}{
						map[string]interface{}{
							"id":   float64(789),
							"name": "bug",
						},
					},
				},
				"repository": map[string]interface{}{
					"name":      "repo",
					"full_name": "org/repo",
					"html_url":  "https://github.com/org/repo",
					"owner": map[string]interface{}{
						"login": "org",
					},
				},
			},
			wantProcess: false,
			wantErr:     false,
		},
		{
			name: "labeled event missing label data",
			payload: map[string]interface{}{
				"action": "labeled",
				"issue": map[string]interface{}{
					"number":   float64(42),
					"title":    "Test Issue",
					"state":    "open",
					"html_url": "https://github.com/org/repo/issues/42",
					"labels":   []interface{}{},
				},
				"repository": map[string]interface{}{
					"name":      "repo",
					"full_name": "org/repo",
					"html_url":  "https://github.com/org/repo",
					"owner": map[string]interface{}{
						"login": "org",
					},
				},
			},
			wantProcess: false,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock server to return issue details
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				issue := Issue{
					Number:  42,
					Title:   "Test Issue",
					Body:    "Issue body",
					State:   "open",
					HTMLURL: "https://github.com/org/repo/issues/42",
					Labels:  []Label{{Name: "pilot"}},
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(issue)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			handler := NewWebhookHandler(client, "", "pilot")

			processed := false
			handler.OnIssue(func(ctx context.Context, issue *Issue, repo *Repository) error {
				processed = true
				return nil
			})

			err := handler.Handle(context.Background(), "issues", tt.payload)

			if (err != nil) != tt.wantErr {
				t.Errorf("Handle() error = %v, wantErr %v", err, tt.wantErr)
			}
			if processed != tt.wantProcess {
				t.Errorf("processed = %v, want %v", processed, tt.wantProcess)
			}
		})
	}
}

func TestHandleIssueLabeled_CustomLabel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issue := Issue{
			Number:  42,
			Title:   "Test Issue",
			Body:    "Issue body",
			State:   "open",
			HTMLURL: "https://github.com/org/repo/issues/42",
			Labels:  []Label{{Name: "ai-assist"}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issue)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	handler := NewWebhookHandler(client, "", "ai-assist") // Custom pilot label

	processed := false
	handler.OnIssue(func(ctx context.Context, issue *Issue, repo *Repository) error {
		processed = true
		return nil
	})

	payload := map[string]interface{}{
		"action": "labeled",
		"label": map[string]interface{}{
			"id":   float64(456),
			"name": "ai-assist",
		},
		"issue": map[string]interface{}{
			"number":   float64(42),
			"title":    "Test Issue",
			"body":     "Issue body",
			"state":    "open",
			"html_url": "https://github.com/org/repo/issues/42",
			"labels": []interface{}{
				map[string]interface{}{
					"id":   float64(456),
					"name": "ai-assist",
				},
			},
		},
		"repository": map[string]interface{}{
			"name":      "repo",
			"full_name": "org/repo",
			"html_url":  "https://github.com/org/repo",
			"owner": map[string]interface{}{
				"login": "org",
			},
		},
	}

	err := handler.Handle(context.Background(), "issues", payload)

	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if !processed {
		t.Error("expected issue to be processed with custom label")
	}
}

func TestProcessIssue_CallbackError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issue := Issue{
			Number:  42,
			Title:   "Test Issue",
			Body:    "Issue body",
			State:   "open",
			HTMLURL: "https://github.com/org/repo/issues/42",
			Labels:  []Label{{Name: "pilot"}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issue)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	handler := NewWebhookHandler(client, "", "pilot")

	expectedErr := errors.New("callback error")
	handler.OnIssue(func(ctx context.Context, issue *Issue, repo *Repository) error {
		return expectedErr
	})

	payload := map[string]interface{}{
		"action": "labeled",
		"label": map[string]interface{}{
			"id":   float64(456),
			"name": "pilot",
		},
		"issue": map[string]interface{}{
			"number":   float64(42),
			"title":    "Test Issue",
			"body":     "Issue body",
			"state":    "open",
			"html_url": "https://github.com/org/repo/issues/42",
			"labels": []interface{}{
				map[string]interface{}{
					"id":   float64(456),
					"name": "pilot",
				},
			},
		},
		"repository": map[string]interface{}{
			"name":      "repo",
			"full_name": "org/repo",
			"html_url":  "https://github.com/org/repo",
			"owner": map[string]interface{}{
				"login": "org",
			},
		},
	}

	err := handler.Handle(context.Background(), "issues", payload)

	if err == nil {
		t.Error("expected error from callback")
	}
	if !errors.Is(err, expectedErr) {
		t.Errorf("error = %v, want %v", err, expectedErr)
	}
}

func TestProcessIssue_NoCallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issue := Issue{
			Number:  42,
			Title:   "Test Issue",
			Body:    "Issue body",
			State:   "open",
			HTMLURL: "https://github.com/org/repo/issues/42",
			Labels:  []Label{{Name: "pilot"}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issue)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	handler := NewWebhookHandler(client, "", "pilot")
	// No callback set

	payload := map[string]interface{}{
		"action": "labeled",
		"label": map[string]interface{}{
			"id":   float64(456),
			"name": "pilot",
		},
		"issue": map[string]interface{}{
			"number":   float64(42),
			"title":    "Test Issue",
			"body":     "Issue body",
			"state":    "open",
			"html_url": "https://github.com/org/repo/issues/42",
			"labels": []interface{}{
				map[string]interface{}{
					"id":   float64(456),
					"name": "pilot",
				},
			},
		},
		"repository": map[string]interface{}{
			"name":      "repo",
			"full_name": "org/repo",
			"html_url":  "https://github.com/org/repo",
			"owner": map[string]interface{}{
				"login": "org",
			},
		},
	}

	err := handler.Handle(context.Background(), "issues", payload)

	if err != nil {
		t.Errorf("Handle() without callback should not error, got %v", err)
	}
}

func TestProcessIssue_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message": "Internal Server Error"}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	handler := NewWebhookHandler(client, "", "pilot")

	handler.OnIssue(func(ctx context.Context, issue *Issue, repo *Repository) error {
		return nil
	})

	payload := map[string]interface{}{
		"action": "labeled",
		"label": map[string]interface{}{
			"id":   float64(456),
			"name": "pilot",
		},
		"issue": map[string]interface{}{
			"number":   float64(42),
			"title":    "Test Issue",
			"body":     "Issue body",
			"state":    "open",
			"html_url": "https://github.com/org/repo/issues/42",
			"labels": []interface{}{
				map[string]interface{}{
					"id":   float64(456),
					"name": "pilot",
				},
			},
		},
		"repository": map[string]interface{}{
			"name":      "repo",
			"full_name": "org/repo",
			"html_url":  "https://github.com/org/repo",
			"owner": map[string]interface{}{
				"login": "org",
			},
		},
	}

	err := handler.Handle(context.Background(), "issues", payload)

	if err == nil {
		t.Error("expected error from API failure")
	}
}

func TestHasPilotLabel(t *testing.T) {
	tests := []struct {
		name       string
		pilotLabel string
		labels     []Label
		want       bool
	}{
		{
			name:       "has pilot label",
			pilotLabel: "pilot",
			labels: []Label{
				{Name: "bug"},
				{Name: "pilot"},
			},
			want: true,
		},
		{
			name:       "no pilot label",
			pilotLabel: "pilot",
			labels: []Label{
				{Name: "bug"},
				{Name: "enhancement"},
			},
			want: false,
		},
		{
			name:       "empty labels",
			pilotLabel: "pilot",
			labels:     []Label{},
			want:       false,
		},
		{
			name:       "custom pilot label",
			pilotLabel: "ai-assist",
			labels: []Label{
				{Name: "ai-assist"},
			},
			want: true,
		},
		{
			name:       "case sensitive - no match",
			pilotLabel: "pilot",
			labels: []Label{
				{Name: "Pilot"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewWebhookHandler(nil, "", tt.pilotLabel)
			issue := &Issue{Labels: tt.labels}
			got := h.hasPilotLabel(issue)
			if got != tt.want {
				t.Errorf("hasPilotLabel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractIssueAndRepo(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]interface{}
		wantErr bool
		check   func(t *testing.T, issue *Issue, repo *Repository)
	}{
		{
			name: "complete payload",
			payload: map[string]interface{}{
				"action": "labeled",
				"issue": map[string]interface{}{
					"number":   float64(42),
					"title":    "Fix authentication bug",
					"body":     "The login form is broken",
					"state":    "open",
					"html_url": "https://github.com/org/repo/issues/42",
					"labels": []interface{}{
						map[string]interface{}{
							"id":   float64(123),
							"name": "bug",
						},
						map[string]interface{}{
							"id":   float64(456),
							"name": "pilot",
						},
					},
				},
				"repository": map[string]interface{}{
					"name":      "repo",
					"full_name": "org/repo",
					"html_url":  "https://github.com/org/repo",
					"clone_url": "https://github.com/org/repo.git",
					"ssh_url":   "git@github.com:org/repo.git",
					"owner": map[string]interface{}{
						"login": "org",
					},
				},
			},
			wantErr: false,
			check: func(t *testing.T, issue *Issue, repo *Repository) {
				if issue.Number != 42 {
					t.Errorf("issue.Number = %d, want 42", issue.Number)
				}
				if issue.Title != "Fix authentication bug" {
					t.Errorf("issue.Title = %s", issue.Title)
				}
				if issue.Body != "The login form is broken" {
					t.Errorf("issue.Body = %s", issue.Body)
				}
				if len(issue.Labels) != 2 {
					t.Errorf("len(issue.Labels) = %d, want 2", len(issue.Labels))
				}
				if repo.Owner.Login != "org" {
					t.Errorf("repo.Owner.Login = %s", repo.Owner.Login)
				}
				if repo.CloneURL != "https://github.com/org/repo.git" {
					t.Errorf("repo.CloneURL = %s", repo.CloneURL)
				}
				if repo.SSHURL != "git@github.com:org/repo.git" {
					t.Errorf("repo.SSHURL = %s", repo.SSHURL)
				}
			},
		},
		{
			name: "payload without body",
			payload: map[string]interface{}{
				"issue": map[string]interface{}{
					"number":   float64(42),
					"title":    "Issue without body",
					"state":    "open",
					"html_url": "https://github.com/org/repo/issues/42",
					"labels":   []interface{}{},
				},
				"repository": map[string]interface{}{
					"name":      "repo",
					"full_name": "org/repo",
					"html_url":  "https://github.com/org/repo",
					"owner": map[string]interface{}{
						"login": "org",
					},
				},
			},
			wantErr: false,
			check: func(t *testing.T, issue *Issue, repo *Repository) {
				if issue.Body != "" {
					t.Errorf("issue.Body = %s, want empty", issue.Body)
				}
			},
		},
		{
			name:    "missing issue",
			payload: map[string]interface{}{"repository": map[string]interface{}{}},
			wantErr: true,
		},
		{
			name:    "missing repository",
			payload: map[string]interface{}{"issue": map[string]interface{}{}},
			wantErr: true,
		},
		{
			name:    "empty payload",
			payload: map[string]interface{}{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewWebhookHandler(nil, "", "pilot")
			issue, repo, err := h.extractIssueAndRepo(tt.payload)

			if (err != nil) != tt.wantErr {
				t.Errorf("extractIssueAndRepo() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && tt.check != nil {
				tt.check(t, issue, repo)
			}
		})
	}
}

func TestWebhookEventTypeConstants(t *testing.T) {
	if EventIssuesOpened != "issues.opened" {
		t.Errorf("EventIssuesOpened = %s, want 'issues.opened'", EventIssuesOpened)
	}
	if EventIssuesLabeled != "issues.labeled" {
		t.Errorf("EventIssuesLabeled = %s, want 'issues.labeled'", EventIssuesLabeled)
	}
	if EventIssuesClosed != "issues.closed" {
		t.Errorf("EventIssuesClosed = %s, want 'issues.closed'", EventIssuesClosed)
	}
	if EventIssueComment != "issue_comment.created" {
		t.Errorf("EventIssueComment = %s, want 'issue_comment.created'", EventIssueComment)
	}
}
