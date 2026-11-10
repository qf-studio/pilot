package github

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
	"github.com/qf-studio/pilot/internal/testutil"
)

// GH-6272: every method that posts free text to GitHub masks it. The request
// body is captured and must hold neither the registered value nor the shape.
func TestSecretGate_TextPostingMethodsRedactBody(t *testing.T) {
	secretpatterns.ResetSecretValuesForTest()
	t.Cleanup(secretpatterns.ResetSecretValuesForTest)
	const secret = "hunter2-not-a-known-shape"
	secretpatterns.RegisterSecretValues(secret)
	shaped := "1234567890" + ":AA" + strings.Repeat("d", 33)
	dirty := "value " + secret + " and " + shaped + " end"

	tests := []struct {
		name string
		call func(c *Client) error
	}{
		{"AddComment", func(c *Client) error {
			_, err := c.AddComment(context.Background(), "o", "r", 1, dirty)
			return err
		}},
		{"AddPRComment", func(c *Client) error {
			_, err := c.AddPRComment(context.Background(), "o", "r", 1, dirty)
			return err
		}},
		{"CreateIssue title and body", func(c *Client) error {
			_, err := c.CreateIssue(context.Background(), "o", "r", &IssueInput{Title: dirty, Body: dirty, Labels: []string{"pilot"}})
			return err
		}},
		{"CreatePullRequest title and body", func(c *Client) error {
			_, err := c.CreatePullRequest(context.Background(), "o", "r", &PullRequestInput{Title: dirty, Body: dirty, Head: "h", Base: "main"})
			return err
		}},
		{"CreateRelease name and body", func(c *Client) error {
			_, err := c.CreateRelease(context.Background(), "o", "r", &ReleaseInput{TagName: "v1", Name: dirty, Body: dirty})
			return err
		}},
		{"UpdateRelease name and body", func(c *Client) error {
			_, err := c.UpdateRelease(context.Background(), "o", "r", 7, &ReleaseInput{TagName: "v1", Name: dirty, Body: dirty})
			return err
		}},
		{"CreateCheckRun output", func(c *Client) error {
			_, err := c.CreateCheckRun(context.Background(), "o", "r", &CheckRun{HeadSHA: "abc", Name: "pilot", Output: &CheckOutput{Title: dirty, Summary: dirty, Text: dirty}})
			return err
		}},
		{"UpdateCheckRun output", func(c *Client) error {
			_, err := c.UpdateCheckRun(context.Background(), "o", "r", 5, &CheckRun{HeadSHA: "abc", Name: "pilot", Output: &CheckOutput{Title: dirty, Summary: dirty, Text: dirty}})
			return err
		}},
		{"CreateCommitStatus description", func(c *Client) error {
			_, err := c.CreateCommitStatus(context.Background(), "o", "r", "abc", &CommitStatus{State: "success", Description: dirty, Context: "pilot"})
			return err
		}},
		{"MergePullRequest commit title", func(c *Client) error {
			return c.MergePullRequest(context.Background(), "o", "r", 1, MergeMethodSquash, dirty)
		}},
		{"ApprovePullRequest body", func(c *Client) error {
			return c.ApprovePullRequest(context.Background(), "o", "r", 1, dirty)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read body: %v", err)
				}
				got = string(b)
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
			if err := tt.call(client); err != nil {
				t.Fatalf("call: %v", err)
			}
			if got == "" {
				t.Fatal("no request reached the server")
			}
			if strings.Contains(got, secret) || strings.Contains(got, shaped) {
				t.Errorf("request body carries a secret: %s", got)
			}
			// encoding/json escapes the angle brackets.
			if !strings.Contains(got, `value \u003credacted\u003e and \u003credacted\u003e end`) {
				t.Errorf("request body was not masked in place: %s", got)
			}
		})
	}
}

// The caller's input is left as it was: masking copies.
func TestSecretGate_RedactDoesNotMutateInput(t *testing.T) {
	secretpatterns.ResetSecretValuesForTest()
	t.Cleanup(secretpatterns.ResetSecretValuesForTest)
	const secret = "hunter2-not-a-known-shape"
	secretpatterns.RegisterSecretValues(secret)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	in := &IssueInput{Title: secret, Body: secret}
	if _, err := NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL).CreateIssue(context.Background(), "o", "r", in); err != nil {
		t.Fatal(err)
	}
	if in.Title != secret || in.Body != secret {
		t.Errorf("input was rewritten: %+v", in)
	}
}

// mutation pin: redactTextPayload masks fixed fields (merge_method, event) and CheckRun.Name, not only free text -> this test fails
func TestSecretGate_RedactLeavesFixedFieldsAndCheckName(t *testing.T) {
	secretpatterns.ResetSecretValuesForTest()
	t.Cleanup(secretpatterns.ResetSecretValuesForTest)
	const secret = "hunter2-not-a-known-shape"
	secretpatterns.RegisterSecretValues(secret)

	// A registered value that equals a fixed field's value is still sent as is.
	const method = "hunter2-merge-method"
	secretpatterns.RegisterSecretValues(method, "pilot-check-identity")

	tests := []struct {
		name string
		call func(c *Client) error
		want []string
	}{
		{"MergePullRequest merge_method", func(c *Client) error {
			return c.MergePullRequest(context.Background(), "o", "r", 1, method, "")
		}, []string{`"merge_method":"` + method + `"`}},
		{"CreateCheckRun name", func(c *Client) error {
			_, err := c.CreateCheckRun(context.Background(), "o", "r", &CheckRun{HeadSHA: "abc", Name: "pilot-check-identity", Output: &CheckOutput{Summary: secret}})
			return err
		}, []string{`"name":"pilot-check-identity"`, `"summary":"\u003credacted`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read body: %v", err)
				}
				got = string(b)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			if err := tt.call(NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)); err != nil {
				t.Fatalf("call: %v", err)
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("request body lacks %s: %s", w, got)
				}
			}
		})
	}
}
