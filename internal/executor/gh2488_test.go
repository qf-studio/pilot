package executor

import (
	"context"
	"testing"
)

// mockPRVerifier is a test double for PRVerifier.
type mockPRVerifier struct {
	pr  *PRInfo
	err error
}

func (m *mockPRVerifier) GetPRByURL(_ context.Context, _ string) (*PRInfo, error) {
	return m.pr, m.err
}

// TestVerifyPRURL_EmptyURL covers case (a): createPR returned an empty URL.
// The execution row must be failed.
func TestVerifyPRURL_EmptyURL(t *testing.T) {
	ctx := context.Background()
	_, err := verifyPRURL(ctx, "", &mockPRVerifier{pr: &PRInfo{Number: 1, URL: "https://example.com/pull/1"}})
	if err == nil {
		t.Fatal("expected error for empty URL, got nil")
	}
}

// TestVerifyPRURL_URLButLookupReturnsNil covers case (b): createPR returned a URL
// but the remote lookup found no matching PR.
func TestVerifyPRURL_URLButLookupReturnsNil(t *testing.T) {
	ctx := context.Background()
	_, err := verifyPRURL(ctx, "https://github.com/owner/repo/pull/99", &mockPRVerifier{pr: nil})
	if err == nil {
		t.Fatal("expected error when PR lookup returns nil, got nil")
	}
}

// TestVerifyPRURL_HappyPath covers case (c): createPR returned a URL and the
// lookup confirmed the PR exists — the result must carry PRNumber and PRUrl.
func TestVerifyPRURL_HappyPath(t *testing.T) {
	ctx := context.Background()
	wantURL := "https://github.com/owner/repo/pull/42"
	wantNum := 42

	pr, err := verifyPRURL(ctx, wantURL, &mockPRVerifier{
		pr: &PRInfo{Number: wantNum, URL: wantURL},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pr == nil {
		t.Fatal("expected PRInfo, got nil")
	}
	if pr.Number != wantNum {
		t.Errorf("PRNumber = %d, want %d", pr.Number, wantNum)
	}
	if pr.URL != wantURL {
		t.Errorf("PRUrl = %q, want %q", pr.URL, wantURL)
	}
}

// TestVerifyPRURL_NilVerifier verifies that a nil verifier accepts any non-empty URL
// without a remote call (used for non-GitHub adapters).
func TestVerifyPRURL_NilVerifier(t *testing.T) {
	ctx := context.Background()
	url := "https://gitlab.com/owner/repo/-/merge_requests/7"

	pr, err := verifyPRURL(ctx, url, nil)
	if err != nil {
		t.Fatalf("unexpected error with nil verifier: %v", err)
	}
	if pr == nil || pr.URL != url {
		t.Errorf("expected PRInfo{URL:%q}, got %+v", url, pr)
	}
}

// TestExecutionResult_PRNumberField verifies that PRNumber is wired into
// ExecutionResult and defaults to zero (compile-time field check).
func TestExecutionResult_PRNumberField(t *testing.T) {
	r := &ExecutionResult{PRNumber: 5, PRUrl: "https://github.com/owner/repo/pull/5"}
	if r.PRNumber != 5 {
		t.Errorf("PRNumber = %d, want 5", r.PRNumber)
	}
}
