package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// mockJudgeRunner creates a test runner that returns canned text output.
func mockJudgeRunner(output string) func(ctx context.Context, args ...string) ([]byte, error) {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		return []byte(output), nil
	}
}

// mockJudgeRunnerError creates a test runner that returns an error.
func mockJudgeRunnerError(err error) func(ctx context.Context, args ...string) ([]byte, error) {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		return nil, err
	}
}

func TestIntentJudge_Pass(t *testing.T) {
	judge := newIntentJudgeWithRunner(mockJudgeRunner("VERDICT: PASS\nThe diff correctly implements the requested feature.\nCONFIDENCE: 0.95"))
	verdict, err := judge.Judge(context.Background(), "Add login button", "Add a login button to the header", "diff --git a/header.go\n+func LoginButton()")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !verdict.Passed {
		t.Error("expected PASS verdict")
	}
	if verdict.Confidence != 0.95 {
		t.Errorf("expected confidence 0.95, got %f", verdict.Confidence)
	}
}

func TestIntentJudge_Fail(t *testing.T) {
	judge := newIntentJudgeWithRunner(mockJudgeRunner("VERDICT: FAIL\nThe diff adds database migration but the issue only asked for a UI change.\nCONFIDENCE: 0.85"))
	verdict, err := judge.Judge(context.Background(), "Fix button color", "Change the submit button to blue", "diff --git a/db/migration.sql\n+ALTER TABLE users ADD COLUMN theme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Passed {
		t.Error("expected FAIL verdict")
	}
	if verdict.Reason == "" {
		t.Error("expected non-empty reason")
	}
	if verdict.Confidence != 0.85 {
		t.Errorf("expected confidence 0.85, got %f", verdict.Confidence)
	}
}

func TestIntentJudge_SubprocessError(t *testing.T) {
	judge := newIntentJudgeWithRunner(mockJudgeRunnerError(errors.New("exit status 1")))
	_, err := judge.Judge(context.Background(), "title", "body", "some diff")
	if err == nil {
		t.Fatal("expected error for subprocess failure")
	}
	if !strings.Contains(err.Error(), "intent judge subprocess") {
		t.Errorf("expected error to mention 'intent judge subprocess', got: %v", err)
	}
}

func TestIntentJudge_MalformedResponse(t *testing.T) {
	judge := newIntentJudgeWithRunner(mockJudgeRunner("I think this looks good but I'm not sure."))
	_, err := judge.Judge(context.Background(), "title", "body", "some diff")
	if err == nil {
		t.Fatal("expected error for malformed response")
	}
	if !strings.Contains(err.Error(), "VERDICT") {
		t.Errorf("expected error about missing VERDICT, got: %v", err)
	}
}

func TestIntentJudge_EmptyDiff(t *testing.T) {
	judge := newIntentJudgeWithRunner(mockJudgeRunner("VERDICT: PASS\nLooks good.\nCONFIDENCE: 0.9"))
	_, err := judge.Judge(context.Background(), "title", "body", "")
	if err == nil {
		t.Fatal("expected error for empty diff")
	}
	if !strings.Contains(err.Error(), "empty diff") {
		t.Errorf("expected 'empty diff' error, got: %v", err)
	}
}

func TestIntentJudge_DiffTruncation(t *testing.T) {
	var receivedPrompt string
	runner := func(ctx context.Context, args ...string) ([]byte, error) {
		// The -p arg is the prompt
		for i, arg := range args {
			if arg == "-p" && i+1 < len(args) {
				receivedPrompt = args[i+1]
				break
			}
		}
		return []byte("VERDICT: PASS\nLooks good.\nCONFIDENCE: 0.9"), nil
	}

	// Create a diff larger than maxDiffCharsDefault (8000)
	largeDiff := strings.Repeat("x", 10000)

	judge := newIntentJudgeWithRunner(runner)
	verdict, err := judge.Judge(context.Background(), "title", "body", largeDiff)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !verdict.Passed {
		t.Error("expected PASS verdict")
	}
	if !strings.Contains(receivedPrompt, "...[truncated]") {
		t.Error("expected diff to be truncated in prompt")
	}
}

func TestIntentJudge_Timeout(t *testing.T) {
	judge := newIntentJudgeWithRunner(mockJudgeRunnerError(context.DeadlineExceeded))
	_, err := judge.Judge(context.Background(), "title", "body", "some diff")
	if err == nil {
		t.Fatal("expected error for timeout")
	}
	if !strings.Contains(err.Error(), "intent judge subprocess") {
		t.Errorf("expected wrapped subprocess error, got: %v", err)
	}
}

func TestNewIntentJudge_Defaults(t *testing.T) {
	judge := NewIntentJudge("")
	if judge.claudeCmd != "claude" {
		t.Errorf("expected claudeCmd 'claude', got %q", judge.claudeCmd)
	}
	if judge.model != "claude-haiku-4-5-20251001" {
		t.Errorf("unexpected model: %s", judge.model)
	}
	if judge.judgeTimeout != 30*1e9 {
		t.Errorf("unexpected judgeTimeout: %v", judge.judgeTimeout)
	}
	if judge.preflightTimeout != 20*1e9 {
		t.Errorf("unexpected preflightTimeout: %v", judge.preflightTimeout)
	}
	if judge.cmdRunner == nil {
		t.Error("expected non-nil cmdRunner")
	}
}

func TestNewIntentJudge_CustomCmd(t *testing.T) {
	judge := NewIntentJudge("/usr/local/bin/claude")
	if judge.claudeCmd != "/usr/local/bin/claude" {
		t.Errorf("expected custom claudeCmd, got %q", judge.claudeCmd)
	}
}

// TestIntentJudge_IncompleteMultiFileChanges tests detection of dropped features across backends (GH-1321)
func TestIntentJudge_IncompleteMultiFileChanges(t *testing.T) {
	judge := newIntentJudgeWithRunner(mockJudgeRunner(
		"VERDICT: FAIL\nThe issue requests adding rate limiting to ALL backends, but the diff only modifies backend_claudecode.go. Missing changes for backend_opencode.go and backend_qwencode.go.\nCONFIDENCE: 0.92",
	))
	verdict, err := judge.Judge(
		context.Background(),
		"Add rate limiting to all backends",
		"Implement rate limiting for all backend engines (ClaudeCode, OpenCode, QwenCode)",
		"diff --git a/internal/executor/backend_claudecode.go\n+func (b *ClaudeCode) RateLimit()",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Passed {
		t.Error("expected FAIL verdict for incomplete multi-file change")
	}
	if !strings.Contains(verdict.Reason, "backend") {
		t.Error("reason should mention incomplete backend changes")
	}
}

// TestIntentJudge_SingleBackendPass tests that single-backend issues pass when only one backend is modified (GH-1321)
func TestIntentJudge_SingleBackendPass(t *testing.T) {
	judge := newIntentJudgeWithRunner(mockJudgeRunner(
		"VERDICT: PASS\nThe issue requests adding timeout to ClaudeCode backend only, and the diff correctly modifies only backend_claudecode.go.\nCONFIDENCE: 0.95",
	))
	verdict, err := judge.Judge(
		context.Background(),
		"Add timeout to ClaudeCode backend",
		"Add a configurable timeout for the ClaudeCode backend engine",
		"diff --git a/internal/executor/backend_claudecode.go\n+func (b *ClaudeCode) SetTimeout()",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !verdict.Passed {
		t.Error("expected PASS verdict for single-backend change")
	}
}

// --- Pre-flight judge tests (GH-2802) ---

func TestJudgeIssue_Accept(t *testing.T) {
	judge := newIntentJudgeWithRunner(mockJudgeRunner("DECISION: accept\nREASON: Clear and specific implementation request.\nCONFIDENCE: 0.95"))
	v, err := judge.JudgeIssue(context.Background(), "Add login button", "Add a login button to the header nav", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Decision != PreFlightAccept {
		t.Errorf("expected accept, got %q", v.Decision)
	}
	if v.IsRejection() {
		t.Error("IsRejection() should be false for accept")
	}
	if v.Confidence != 0.95 {
		t.Errorf("expected confidence 0.95, got %f", v.Confidence)
	}
}

func TestJudgeIssue_RejectQuestion(t *testing.T) {
	judge := newIntentJudgeWithRunner(mockJudgeRunner("DECISION: reject_question\nREASON: Issue asks a question rather than requesting implementation.\nCONFIDENCE: 0.88"))
	v, err := judge.JudgeIssue(context.Background(), "How does auth work?", "Can you explain the auth flow?", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Decision != PreFlightRejectQuestion {
		t.Errorf("expected reject_question, got %q", v.Decision)
	}
	if !v.IsRejection() {
		t.Error("IsRejection() should be true for reject_question")
	}
}

func TestJudgeIssue_RejectVague(t *testing.T) {
	judge := newIntentJudgeWithRunner(mockJudgeRunner("DECISION: reject_vague\nREASON: Issue body is too vague to implement.\nCONFIDENCE: 0.90"))
	v, err := judge.JudgeIssue(context.Background(), "Fix it", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Decision != PreFlightRejectVague {
		t.Errorf("expected reject_vague, got %q", v.Decision)
	}
	if !v.IsRejection() {
		t.Error("IsRejection() should be true for reject_vague")
	}
}

func TestJudgeIssue_AllDecisionConstants(t *testing.T) {
	decisions := []PreFlightDecision{
		PreFlightAccept,
		PreFlightRejectQuestion,
		PreFlightRejectVague,
		PreFlightRejectConflicting,
		PreFlightRejectStale,
		PreFlightRejectOutOfScope,
	}
	for _, dec := range decisions {
		dec := dec
		t.Run(string(dec), func(t *testing.T) {
			output := "DECISION: " + string(dec) + "\nREASON: Test reason.\nCONFIDENCE: 0.85"
			judge := newIntentJudgeWithRunner(mockJudgeRunner(output))
			v, err := judge.JudgeIssue(context.Background(), "title", "body", "")
			if err != nil {
				t.Fatalf("unexpected error for decision %q: %v", dec, err)
			}
			if v.Decision != dec {
				t.Errorf("expected %q, got %q", dec, v.Decision)
			}
		})
	}
}

func TestJudgeIssue_EmptyBodyReturnsVague(t *testing.T) {
	judge := newIntentJudgeWithRunner(mockJudgeRunner("DECISION: reject_vague\nREASON: No description provided.\nCONFIDENCE: 0.95"))
	v, err := judge.JudgeIssue(context.Background(), "title", "", "")
	if err != nil {
		t.Fatalf("empty body should not return error, got: %v", err)
	}
	if v.Decision != PreFlightRejectVague {
		t.Errorf("expected reject_vague for empty body, got %q", v.Decision)
	}
}

func TestJudgeIssue_SubprocessError(t *testing.T) {
	judge := newIntentJudgeWithRunner(mockJudgeRunnerError(errors.New("exit status 1")))
	_, err := judge.JudgeIssue(context.Background(), "title", "body", "")
	if err == nil {
		t.Fatal("expected error for subprocess failure")
	}
	if !strings.Contains(err.Error(), "intent judge subprocess") {
		t.Errorf("expected wrapped subprocess error, got: %v", err)
	}
}

func TestJudgeIssue_MalformedResponse(t *testing.T) {
	judge := newIntentJudgeWithRunner(mockJudgeRunner("This looks fine to me."))
	_, err := judge.JudgeIssue(context.Background(), "title", "body", "")
	if err == nil {
		t.Fatal("expected error for malformed response missing DECISION")
	}
	if !strings.Contains(err.Error(), "DECISION") {
		t.Errorf("expected error about missing DECISION, got: %v", err)
	}
}

func TestJudgeIssue_BodyTruncation(t *testing.T) {
	var receivedPrompt string
	runner := func(ctx context.Context, args ...string) ([]byte, error) {
		for i, arg := range args {
			if arg == "-p" && i+1 < len(args) {
				receivedPrompt = args[i+1]
				break
			}
		}
		return []byte("DECISION: accept\nREASON: Looks good.\nCONFIDENCE: 0.9"), nil
	}

	largeBody := strings.Repeat("x", 5000) // > maxPreflightBodyChars (4000)

	judge := newIntentJudgeWithRunner(runner)
	_, err := judge.JudgeIssue(context.Background(), "title", largeBody, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(receivedPrompt, "...[truncated]") {
		t.Error("expected body to be truncated in prompt")
	}
}

func TestJudgeIssue_ConfidenceParsed(t *testing.T) {
	judge := newIntentJudgeWithRunner(mockJudgeRunner("DECISION: accept\nREASON: Good issue.\nCONFIDENCE: 0.85"))
	v, err := judge.JudgeIssue(context.Background(), "title", "body", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Confidence != 0.85 {
		t.Errorf("expected confidence 0.85, got %f", v.Confidence)
	}
}
