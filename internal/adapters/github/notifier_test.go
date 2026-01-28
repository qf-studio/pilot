package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewNotifier(t *testing.T) {
	client := NewClient("test-token")
	notifier := NewNotifier(client, "pilot")

	if notifier == nil {
		t.Fatal("NewNotifier returned nil")
	}
	if notifier.client != client {
		t.Error("notifier.client not set correctly")
	}
	if notifier.pilotLabel != "pilot" {
		t.Errorf("notifier.pilotLabel = %s, want 'pilot'", notifier.pilotLabel)
	}
}

func TestNotifyTaskStarted(t *testing.T) {
	tests := []struct {
		name          string
		labelStatus   int
		commentStatus int
		wantErr       bool
	}{
		{
			name:          "success",
			labelStatus:   http.StatusOK,
			commentStatus: http.StatusCreated,
			wantErr:       false,
		},
		{
			name:          "label add fails",
			labelStatus:   http.StatusInternalServerError,
			commentStatus: http.StatusCreated,
			wantErr:       true,
		},
		{
			name:          "comment add fails",
			labelStatus:   http.StatusOK,
			commentStatus: http.StatusInternalServerError,
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Distinguish between label and comment endpoints
				if strings.HasSuffix(r.URL.Path, "/labels") {
					var body map[string][]string
					_ = json.NewDecoder(r.Body).Decode(&body)

					// Verify in-progress label is being added
					labels := body["labels"]
					found := false
					for _, l := range labels {
						if l == LabelInProgress {
							found = true
							break
						}
					}
					if !found {
						t.Error("expected pilot-in-progress label to be added")
					}

					w.WriteHeader(tt.labelStatus)
				} else if strings.HasSuffix(r.URL.Path, "/comments") {
					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)

					// Verify comment contains task ID
					if !strings.Contains(body["body"], "TASK-123") {
						t.Error("comment should contain task ID")
					}
					if !strings.Contains(body["body"], "Pilot started") {
						t.Error("comment should indicate pilot started")
					}

					w.WriteHeader(tt.commentStatus)
					if tt.commentStatus < 300 {
						_ = json.NewEncoder(w).Encode(Comment{ID: 123, Body: body["body"]})
					}
				} else {
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL("test-token", server.URL)
			notifier := NewNotifier(client, "pilot")

			err := notifier.NotifyTaskStarted(context.Background(), "owner", "repo", 42, "TASK-123")

			if (err != nil) != tt.wantErr {
				t.Errorf("NotifyTaskStarted() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNotifyProgress(t *testing.T) {
	tests := []struct {
		name       string
		phase      string
		details    string
		statusCode int
		wantErr    bool
		wantEmoji  string
	}{
		{
			name:       "exploring phase",
			phase:      "exploring",
			details:    "Analyzing codebase structure",
			statusCode: http.StatusCreated,
			wantErr:    false,
			wantEmoji:  "",
		},
		{
			name:       "research phase",
			phase:      "research",
			details:    "Reading documentation",
			statusCode: http.StatusCreated,
			wantErr:    false,
			wantEmoji:  "",
		},
		{
			name:       "implementing phase",
			phase:      "implementing",
			details:    "Writing new feature code",
			statusCode: http.StatusCreated,
			wantErr:    false,
			wantEmoji:  "",
		},
		{
			name:       "testing phase",
			phase:      "testing",
			details:    "Running test suite",
			statusCode: http.StatusCreated,
			wantErr:    false,
			wantEmoji:  "",
		},
		{
			name:       "committing phase",
			phase:      "committing",
			details:    "Creating commit with changes",
			statusCode: http.StatusCreated,
			wantErr:    false,
			wantEmoji:  "",
		},
		{
			name:       "unknown phase",
			phase:      "unknown",
			details:    "Some other work",
			statusCode: http.StatusCreated,
			wantErr:    false,
			wantEmoji:  "",
		},
		{
			name:       "API error",
			phase:      "testing",
			details:    "Running tests",
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
			wantEmoji:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}

				var body map[string]string
				_ = json.NewDecoder(r.Body).Decode(&body)

				// Verify comment contains phase and details
				if !strings.Contains(body["body"], tt.phase) {
					t.Errorf("comment should contain phase: %s", tt.phase)
				}
				if !strings.Contains(body["body"], tt.details) {
					t.Errorf("comment should contain details: %s", tt.details)
				}

				w.WriteHeader(tt.statusCode)
				if tt.statusCode < 300 {
					_ = json.NewEncoder(w).Encode(Comment{ID: 123})
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL("test-token", server.URL)
			notifier := NewNotifier(client, "pilot")

			err := notifier.NotifyProgress(context.Background(), "owner", "repo", 42, tt.phase, tt.details)

			if (err != nil) != tt.wantErr {
				t.Errorf("NotifyProgress() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNotifyTaskCompleted(t *testing.T) {
	tests := []struct {
		name       string
		prURL      string
		summary    string
		labelOK    bool
		commentOK  bool
		wantErr    bool
		checkPRURL bool
	}{
		{
			name:       "success with PR and summary",
			prURL:      "https://github.com/owner/repo/pull/42",
			summary:    "Implemented feature X with tests",
			labelOK:    true,
			commentOK:  true,
			wantErr:    false,
			checkPRURL: true,
		},
		{
			name:       "success without PR URL",
			prURL:      "",
			summary:    "Fixed the bug",
			labelOK:    true,
			commentOK:  true,
			wantErr:    false,
			checkPRURL: false,
		},
		{
			name:       "success without summary",
			prURL:      "https://github.com/owner/repo/pull/42",
			summary:    "",
			labelOK:    true,
			commentOK:  true,
			wantErr:    false,
			checkPRURL: true,
		},
		{
			name:       "add label fails",
			prURL:      "https://github.com/owner/repo/pull/42",
			summary:    "Summary",
			labelOK:    false,
			commentOK:  true,
			wantErr:    true,
			checkPRURL: false,
		},
		{
			name:       "comment fails",
			prURL:      "https://github.com/owner/repo/pull/42",
			summary:    "Summary",
			labelOK:    true,
			commentOK:  false,
			wantErr:    true,
			checkPRURL: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestCount := 0

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestCount++

				// DELETE requests are for removing labels - always succeed
				if r.Method == http.MethodDelete {
					w.WriteHeader(http.StatusOK)
					return
				}

				if strings.HasSuffix(r.URL.Path, "/labels") && r.Method == http.MethodPost {
					if !tt.labelOK {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}

					var body map[string][]string
					_ = json.NewDecoder(r.Body).Decode(&body)

					// Verify done label is being added
					labels := body["labels"]
					found := false
					for _, l := range labels {
						if l == LabelDone {
							found = true
							break
						}
					}
					if !found {
						t.Error("expected pilot-done label to be added")
					}

					w.WriteHeader(http.StatusOK)
				} else if strings.HasSuffix(r.URL.Path, "/comments") && r.Method == http.MethodPost {
					if !tt.commentOK {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}

					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)

					// Verify comment contains expected content
					if !strings.Contains(body["body"], "completed") {
						t.Error("comment should indicate completion")
					}

					if tt.checkPRURL && tt.prURL != "" {
						if !strings.Contains(body["body"], tt.prURL) {
							t.Errorf("comment should contain PR URL: %s", tt.prURL)
						}
					}

					if tt.summary != "" && !strings.Contains(body["body"], tt.summary) {
						t.Errorf("comment should contain summary: %s", tt.summary)
					}

					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(Comment{ID: 123})
				} else {
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL("test-token", server.URL)
			notifier := NewNotifier(client, "pilot")

			err := notifier.NotifyTaskCompleted(context.Background(), "owner", "repo", 42, tt.prURL, tt.summary)

			if (err != nil) != tt.wantErr {
				t.Errorf("NotifyTaskCompleted() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNotifyTaskFailed(t *testing.T) {
	tests := []struct {
		name      string
		reason    string
		labelOK   bool
		commentOK bool
		wantErr   bool
	}{
		{
			name:      "success",
			reason:    "Tests failed with 3 errors",
			labelOK:   true,
			commentOK: true,
			wantErr:   false,
		},
		{
			name:      "add label fails",
			reason:    "Tests failed",
			labelOK:   false,
			commentOK: true,
			wantErr:   true,
		},
		{
			name:      "comment fails",
			reason:    "Tests failed",
			labelOK:   true,
			commentOK: false,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// DELETE requests are for removing labels - always succeed
				if r.Method == http.MethodDelete {
					w.WriteHeader(http.StatusOK)
					return
				}

				if strings.HasSuffix(r.URL.Path, "/labels") && r.Method == http.MethodPost {
					if !tt.labelOK {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}

					var body map[string][]string
					_ = json.NewDecoder(r.Body).Decode(&body)

					// Verify failed label is being added
					labels := body["labels"]
					found := false
					for _, l := range labels {
						if l == LabelFailed {
							found = true
							break
						}
					}
					if !found {
						t.Error("expected pilot-failed label to be added")
					}

					w.WriteHeader(http.StatusOK)
				} else if strings.HasSuffix(r.URL.Path, "/comments") && r.Method == http.MethodPost {
					if !tt.commentOK {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}

					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)

					// Verify comment contains failure reason
					if !strings.Contains(body["body"], tt.reason) {
						t.Errorf("comment should contain reason: %s", tt.reason)
					}
					if !strings.Contains(body["body"], "could not complete") {
						t.Error("comment should indicate failure")
					}

					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(Comment{ID: 123})
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL("test-token", server.URL)
			notifier := NewNotifier(client, "pilot")

			err := notifier.NotifyTaskFailed(context.Background(), "owner", "repo", 42, tt.reason)

			if (err != nil) != tt.wantErr {
				t.Errorf("NotifyTaskFailed() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLinkPR(t *testing.T) {
	tests := []struct {
		name       string
		prNumber   int
		prURL      string
		statusCode int
		wantErr    bool
	}{
		{
			name:       "success",
			prNumber:   42,
			prURL:      "https://github.com/owner/repo/pull/42",
			statusCode: http.StatusCreated,
			wantErr:    false,
		},
		{
			name:       "API error",
			prNumber:   42,
			prURL:      "https://github.com/owner/repo/pull/42",
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}

				var body map[string]string
				_ = json.NewDecoder(r.Body).Decode(&body)

				// Verify comment contains PR info
				if !strings.Contains(body["body"], "#42") {
					t.Error("comment should contain PR number")
				}
				if !strings.Contains(body["body"], tt.prURL) {
					t.Errorf("comment should contain PR URL: %s", tt.prURL)
				}
				if !strings.Contains(body["body"], "Pull Request Created") {
					t.Error("comment should indicate PR creation")
				}

				w.WriteHeader(tt.statusCode)
				if tt.statusCode < 300 {
					_ = json.NewEncoder(w).Encode(Comment{ID: 123})
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL("test-token", server.URL)
			notifier := NewNotifier(client, "pilot")

			err := notifier.LinkPR(context.Background(), "owner", "repo", 1, tt.prNumber, tt.prURL)

			if (err != nil) != tt.wantErr {
				t.Errorf("LinkPR() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNotifyProgress_PhaseEmojis(t *testing.T) {
	phases := []struct {
		phase string
		emoji string
	}{
		{"exploring", ""},
		{"research", ""},
		{"implementing", ""},
		{"impl", ""},
		{"testing", ""},
		{"verify", ""},
		{"committing", ""},
		{"unknown", ""},
	}

	for _, p := range phases {
		t.Run(p.phase, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]string
				_ = json.NewDecoder(r.Body).Decode(&body)

				// Just verify the phase name is in the comment
				if !strings.Contains(body["body"], p.phase) {
					t.Errorf("comment should contain phase: %s", p.phase)
				}

				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(Comment{ID: 123})
			}))
			defer server.Close()

			client := NewClientWithBaseURL("test-token", server.URL)
			notifier := NewNotifier(client, "pilot")

			err := notifier.NotifyProgress(context.Background(), "owner", "repo", 42, p.phase, "details")
			if err != nil {
				t.Errorf("NotifyProgress() error = %v", err)
			}
		})
	}
}
