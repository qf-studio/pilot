package executor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
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

	// Create a diff larger than maxDiffCharsDefault (32000), with no
	// "diff --git" boundaries - exercises the plain-cutoff fallback path.
	largeDiff := strings.Repeat("x", 40000)

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

// TestIntentJudge_UnderCapNotTruncated verifies diffs within the (raised)
// cap are sent through unmodified - GH-15 (23923 chars) previously exceeded
// the old 8000-char cap and got a mid-file cutoff; it must now fit whole.
func TestIntentJudge_UnderCapNotTruncated(t *testing.T) {
	var receivedPrompt string
	runner := func(ctx context.Context, args ...string) ([]byte, error) {
		for i, arg := range args {
			if arg == "-p" && i+1 < len(args) {
				receivedPrompt = args[i+1]
				break
			}
		}
		return []byte("VERDICT: PASS\nLooks good.\nCONFIDENCE: 0.9"), nil
	}

	diff := "diff --git a/foo.go b/foo.go\n" + strings.Repeat("+line of code\n", 1000) // ~24923 chars incl header, mirrors GH-15's 23923

	judge := newIntentJudgeWithRunner(runner)
	verdict, err := judge.Judge(context.Background(), "title", "body", diff)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !verdict.Passed {
		t.Error("expected PASS verdict")
	}
	if strings.Contains(receivedPrompt, "...[truncated]") {
		t.Error("diff under maxDiffCharsDefault must not be truncated")
	}
	if !strings.Contains(receivedPrompt, diff) {
		t.Error("expected full, unmodified diff to be present in prompt")
	}
}

// TestIntentJudge_PerFileTruncationPreservesManifest is the direct
// regression test for GH-4407: a diff that spans many files and exceeds the
// cap must (1) list every touched file in a never-truncated manifest and
// (2) never zero out any single file's visible content, so the judge can't
// mistake a truncation marker for "this file wasn't touched".
func TestIntentJudge_PerFileTruncationPreservesManifest(t *testing.T) {
	var receivedPrompt string
	runner := func(ctx context.Context, args ...string) ([]byte, error) {
		for i, arg := range args {
			if arg == "-p" && i+1 < len(args) {
				receivedPrompt = args[i+1]
				break
			}
		}
		return []byte("VERDICT: PASS\nLooks good.\nCONFIDENCE: 0.9"), nil
	}

	// Simulate GH-12: many files, total diff far over the cap.
	var sb strings.Builder
	var paths []string
	for i := 0; i < 25; i++ {
		path := fmt.Sprintf("internal/pkg/file%d.go", i)
		paths = append(paths, path)
		sb.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n", path, path))
		sb.WriteString("--- a/" + path + "\n+++ b/" + path + "\n")
		sb.WriteString(strings.Repeat(fmt.Sprintf("+func Impl%d() {}\n", i), 200))
	}
	diff := sb.String()
	if len(diff) <= maxDiffCharsDefault {
		t.Fatalf("test fixture diff (%d chars) must exceed maxDiffCharsDefault (%d) to exercise truncation", len(diff), maxDiffCharsDefault)
	}

	judge := newIntentJudgeWithRunner(runner)
	verdict, err := judge.Judge(context.Background(), "title", "body", diff)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !verdict.Passed {
		t.Error("expected PASS verdict")
	}

	if !strings.Contains(receivedPrompt, "## Changed Files (25 total") {
		t.Error("expected a complete Changed Files manifest header")
	}
	for _, path := range paths {
		if !strings.Contains(receivedPrompt, path) {
			t.Errorf("expected manifest to list every file, missing %q", path)
		}
	}
	if !strings.Contains(receivedPrompt, "...[truncated:") {
		t.Error("expected per-file truncation markers for a diff this large")
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

// --- GH-4377: judge subprocess kill-cause RCA ---

// TestNewJudgeSubprocessError_ExternalSIGKILL verifies that a SIGKILL-signaled
// exit with a still-live (non-expired) context is classified as an external
// kill (e.g. OS OOM killer) rather than our own timeout.
func TestNewJudgeSubprocessError_ExternalSIGKILL(t *testing.T) {
	cmd := exec.Command("sh", "-c", "kill -9 $$")
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected the subprocess to report a kill error")
	}

	jerr := newJudgeSubprocessError(context.Background(), err, "some stderr", 42)
	if jerr.Cause != judgeFailureCauseExternalSIGKILL {
		t.Errorf("expected cause %q, got %q", judgeFailureCauseExternalSIGKILL, jerr.Cause)
	}
	if jerr.PeakRSSMB != 42 {
		t.Errorf("expected peak_rss_mb 42, got %d", jerr.PeakRSSMB)
	}
	if jerr.StderrTail != "some stderr" {
		t.Errorf("expected stderr tail %q, got %q", "some stderr", jerr.StderrTail)
	}
	if !strings.Contains(jerr.Error(), "cause=external_sigkill") {
		t.Errorf("expected Error() to mention cause=external_sigkill, got: %v", jerr.Error())
	}
	if !strings.Contains(jerr.Error(), "peak_rss_mb=42") {
		t.Errorf("expected Error() to mention peak_rss_mb=42, got: %v", jerr.Error())
	}
	if !strings.Contains(jerr.Error(), `stderr_tail="some stderr"`) {
		t.Errorf("expected Error() to mention the stderr tail, got: %v", jerr.Error())
	}
	if !errors.Is(jerr, jerr.Err) {
		t.Error("expected JudgeSubprocessError to unwrap to the original error")
	}
}

// TestNewJudgeSubprocessError_ContextDeadline verifies that a kill observed
// after ctx's own deadline has already fired is classified as our own
// timeout, not an external kill — even though the raw error text
// ("signal: killed") is identical either way.
func TestNewJudgeSubprocessError_ContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond) // guarantee ctx.Err() == DeadlineExceeded

	cmd := exec.Command("sh", "-c", "kill -9 $$")
	err := cmd.Run()

	jerr := newJudgeSubprocessError(ctx, err, "", 10)
	if jerr.Cause != judgeFailureCauseContextDeadline {
		t.Errorf("expected cause %q, got %q", judgeFailureCauseContextDeadline, jerr.Cause)
	}
}

// TestNewJudgeSubprocessError_Other verifies a plain non-signal exit
// (e.g. a genuine nonzero exit code) is classified as "other", not a kill.
func TestNewJudgeSubprocessError_Other(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 3")
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected exit 3 to produce an error")
	}

	jerr := newJudgeSubprocessError(context.Background(), err, "", 0)
	if jerr.Cause != judgeFailureCauseOther {
		t.Errorf("expected cause %q, got %q", judgeFailureCauseOther, jerr.Cause)
	}
}

// TestDefaultCmdRunner_ContextDeadlineKill drives the real defaultCmdRunner
// (not a mock) end to end: a subprocess that outlives its context gets
// killed, and the resulting error carries the context_deadline cause plus a
// *JudgeSubprocessError the caller can type-assert on.
func TestDefaultCmdRunner_ContextDeadlineKill(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available on this system")
	}

	judge := NewIntentJudge("sleep")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := judge.defaultCmdRunner(ctx, "5")
	if err == nil {
		t.Fatal("expected error from a subprocess killed by context deadline")
	}

	var jerr *JudgeSubprocessError
	if !errors.As(err, &jerr) {
		t.Fatalf("expected *JudgeSubprocessError, got %T: %v", err, err)
	}
	if jerr.Cause != judgeFailureCauseContextDeadline {
		t.Errorf("expected cause %q, got %q", judgeFailureCauseContextDeadline, jerr.Cause)
	}
}

// TestDefaultCmdRunner_StderrCaptured verifies stderr from a failing
// subprocess survives into the wrapped error (previously only
// "signal: killed" — the exit reason — made it through).
func TestDefaultCmdRunner_StderrCaptured(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available on this system")
	}

	judge := NewIntentJudge("sh")
	_, err := judge.defaultCmdRunner(context.Background(), "-c", "echo boom-from-stderr >&2; exit 1")
	if err == nil {
		t.Fatal("expected error from nonzero exit")
	}

	var jerr *JudgeSubprocessError
	if !errors.As(err, &jerr) {
		t.Fatalf("expected *JudgeSubprocessError, got %T: %v", err, err)
	}
	if !strings.Contains(jerr.StderrTail, "boom-from-stderr") {
		t.Errorf("expected stderr tail to contain subprocess stderr, got: %q", jerr.StderrTail)
	}
}
