package autopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ghadapter "github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// Canned job log: two steps. Step 1 is the runner-setup preamble GitHub
// Actions always emits first; step 2 is the actual test run that failed.
// Every line is prefixed with an RFC3339-nanosecond timestamp, matching
// GitHub's raw job-log format, so sliceLogByStepWindow can carve out the
// failing step from the combined blob using the steps' StartedAt/CompletedAt.
const gh4460FixtureJobLog = `2026-07-18T10:00:00.0000000Z Current runner version: '2.319.1'
2026-07-18T10:00:00.1000000Z Runner Image Provisioner
2026-07-18T10:00:00.2000000Z Operating System
2026-07-18T10:00:00.3000000Z Runner Image
2026-07-18T10:00:00.4000000Z ##[group]Job defaults
2026-07-18T10:00:00.5000000Z ##[endgroup]
2026-07-18T10:00:05.0000000Z ##[group]Run go test ./...
2026-07-18T10:00:05.1000000Z Run go test ./...
2026-07-18T10:00:06.0000000Z --- FAIL: TestForecastMonthlyBurn (0.00s)
2026-07-18T10:00:06.1000000Z     forecast_test.go:42: expected 1200, got 1100
2026-07-18T10:00:06.2000000Z FAIL
2026-07-18T10:00:06.3000000Z FAIL	github.com/qf-studio/pilot/internal/forecast	0.004s
2026-07-18T10:00:06.4000000Z ##[error]Process completed with exit code 1.
`

func gh4460FixtureSteps() []ghadapter.JobStep {
	return []ghadapter.JobStep{
		{
			Name:        "Set up job",
			Status:      "completed",
			Conclusion:  "success",
			Number:      1,
			StartedAt:   "2026-07-18T10:00:00.0000000Z",
			CompletedAt: "2026-07-18T10:00:04.9000000Z",
		},
		{
			Name:        "Run go test ./...",
			Status:      "completed",
			Conclusion:  "failure",
			Number:      2,
			StartedAt:   "2026-07-18T10:00:05.0000000Z",
			CompletedAt: "2026-07-18T10:00:06.4000000Z",
		},
	}
}

// gh4460TestServer builds one httptest.Server that answers both the
// studio-sdk client's endpoints (check-runs list, job log fetch) and the
// in-tree client's endpoints (jobs API, check-run annotations), so a single
// CIMonitor wired with both clients (as production does via
// WithStepLogClient) can be exercised end-to-end against canned fixtures.
type gh4460ServerOpts struct {
	// jobStatus, when non-zero, overrides the HTTP status returned by the
	// in-tree jobs-API endpoint (e.g. http.StatusNotFound to simulate a
	// check run that isn't backed by a GitHub Actions job).
	jobStatus int
	// annotations, when non-nil, is served by the check-run annotations
	// endpoint.
	annotations []ghadapter.CheckRunAnnotation
}

func gh4460NewTestServer(t *testing.T, checkRuns []github.CheckRun, opts gh4460ServerOpts) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc123/check-runs":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.CheckRunsResponse{
				TotalCount: len(checkRuns),
				CheckRuns:  checkRuns,
			})
		case r.URL.Path == "/repos/owner/repo/actions/jobs/100/logs":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(gh4460FixtureJobLog))
		case r.URL.Path == "/repos/owner/repo/actions/jobs/101/logs":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(strings.ReplaceAll(gh4460FixtureJobLog, "TestForecastMonthlyBurn", "TestOtherThing")))
		case r.URL.Path == "/repos/owner/repo/actions/jobs/100":
			if opts.jobStatus != 0 {
				w.WriteHeader(opts.jobStatus)
				return
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ghadapter.WorkflowJob{
				ID:     100,
				Name:   "test",
				Status: "completed",
				Steps:  gh4460FixtureSteps(),
			})
		case r.URL.Path == "/repos/owner/repo/actions/jobs/101":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ghadapter.WorkflowJob{
				ID:     101,
				Name:   "lint",
				Status: "completed",
				Steps:  gh4460FixtureSteps(),
			})
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/check-runs/") && strings.HasSuffix(r.URL.Path, "/annotations"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(opts.annotations)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// TestCIMonitor_GetFailedCheckExcerpts_ResolvesFailingStep verifies that when
// a StepLogClient is wired, the excerpt for a failed check is the tail of
// the actual failing step ("Run go test ./..."), not the runner-setup
// preamble ("Set up job") that GH-4415's continuations kept bouncing on.
func TestCIMonitor_GetFailedCheckExcerpts_ResolvesFailingStep(t *testing.T) {
	server := gh4460NewTestServer(t, []github.CheckRun{
		{ID: 100, Name: "test", Status: "completed", Conclusion: "failure", DetailsURL: "https://github.com/owner/repo/runs/100"},
	}, gh4460ServerOpts{})
	defer server.Close()

	sdkClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	adapterClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	monitor := NewCIMonitor(sdkClient, "owner", "repo", DefaultConfig())
	monitor.SetStepLogClient(adapterClient)

	body := monitor.GetFailedCheckExcerpts(context.Background(), "abc123")

	if body == "" {
		t.Fatal("expected non-empty excerpt body")
	}
	if !strings.Contains(body, "TestForecastMonthlyBurn") {
		t.Errorf("expected failing step's actual failure content, got:\n%s", body)
	}
	if !strings.Contains(body, "forecast_test.go:42") {
		t.Errorf("expected failing assertion line, got:\n%s", body)
	}
	if strings.Contains(body, "Current runner version") || strings.Contains(body, "Runner Image Provisioner") {
		t.Errorf("excerpt should not contain runner-setup preamble, got:\n%s", body)
	}
	if !strings.Contains(body, "failing step: Run go test ./...") {
		t.Errorf("expected heading to name the failing step, got:\n%s", body)
	}
	if !strings.Contains(body, "https://github.com/owner/repo/runs/100") {
		t.Errorf("expected permalink fallback in body, got:\n%s", body)
	}
}

// TestCIMonitor_GetFailedCheckExcerpts_FallsBackToAnnotations verifies the
// second fallback tier: when the jobs API can't resolve a failing step (here,
// a 404 simulating a check run with no GitHub Actions job/step breakdown —
// e.g. a third-party Checks-API integration), buildFailingStepExcerpt falls
// back to failure-level check-run annotations instead of the whole job log.
func TestCIMonitor_GetFailedCheckExcerpts_FallsBackToAnnotations(t *testing.T) {
	server := gh4460NewTestServer(t, []github.CheckRun{
		{ID: 100, Name: "test", Status: "completed", Conclusion: "failure", DetailsURL: "https://github.com/owner/repo/runs/100"},
	}, gh4460ServerOpts{
		jobStatus: http.StatusNotFound,
		annotations: []ghadapter.CheckRunAnnotation{
			{Path: "forecast.go", StartLine: 42, AnnotationLevel: "failure", Message: "expected 1200, got 1100"},
			{Path: "forecast.go", StartLine: 10, AnnotationLevel: "notice", Message: "consider caching this"},
		},
	})
	defer server.Close()

	sdkClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	adapterClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	monitor := NewCIMonitor(sdkClient, "owner", "repo", DefaultConfig())
	monitor.SetStepLogClient(adapterClient)

	excerpt := monitor.buildFailingStepExcerpt(context.Background(), github.CheckRun{
		ID: 100, Name: "test", Conclusion: "failure", DetailsURL: "https://github.com/owner/repo/runs/100",
	})

	if excerpt.Source != "annotations" {
		t.Fatalf("Source = %q, want %q", excerpt.Source, "annotations")
	}
	if !strings.Contains(excerpt.Tail, "expected 1200, got 1100") {
		t.Errorf("expected failure-level annotation message, got: %q", excerpt.Tail)
	}
	if strings.Contains(excerpt.Tail, "consider caching this") {
		t.Errorf("non-failure-level annotation should be excluded, got: %q", excerpt.Tail)
	}
}

// TestCIMonitor_GetFailedCheckExcerpts_FallsBackToWholeJobWithoutStepLogClient
// verifies the third fallback tier: when no StepLogClient is wired at all
// (e.g. an older Controller wiring, or SetStepLogClient was never called),
// buildFailingStepExcerpt still produces a useful excerpt — match-anchored
// against the whole job log rather than the isolated failing step.
func TestCIMonitor_GetFailedCheckExcerpts_FallsBackToWholeJobWithoutStepLogClient(t *testing.T) {
	server := gh4460NewTestServer(t, []github.CheckRun{
		{ID: 100, Name: "test", Status: "completed", Conclusion: "failure", DetailsURL: "https://github.com/owner/repo/runs/100"},
	}, gh4460ServerOpts{})
	defer server.Close()

	sdkClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	monitor := NewCIMonitor(sdkClient, "owner", "repo", DefaultConfig())
	// Deliberately do not call SetStepLogClient.

	body := monitor.GetFailedCheckExcerpts(context.Background(), "abc123")

	if body == "" {
		t.Fatal("expected non-empty excerpt body")
	}
	// This tier is a fallback, not a fix, for checks with no wired
	// StepLogClient. What matters is it still surfaces the actual failure
	// line rather than truncating before reaching it.
	if !strings.Contains(body, "TestForecastMonthlyBurn") {
		t.Errorf("expected whole-job tail to still include the failure, got:\n%s", body)
	}
}

// TestCIMonitor_GetFailedCheckExcerpts_MultipleFailuresFitBudget is the
// multi-check body-assembly test: two failing checks, each independently
// resolved to their failing step, both need to survive
// failedCheckExcerptBudgetChars intact — this is the defect GH-4460 fixes,
// where one check's preamble used to consume the whole budget before a
// second check's content was ever reached.
func TestCIMonitor_GetFailedCheckExcerpts_MultipleFailuresFitBudget(t *testing.T) {
	server := gh4460NewTestServer(t, []github.CheckRun{
		{ID: 100, Name: "test", Status: "completed", Conclusion: "failure", DetailsURL: "https://github.com/owner/repo/runs/100"},
		{ID: 101, Name: "lint", Status: "completed", Conclusion: "failure", DetailsURL: "https://github.com/owner/repo/runs/101"},
		{ID: 102, Name: "build", Status: "completed", Conclusion: "success"},
	}, gh4460ServerOpts{})
	defer server.Close()

	sdkClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	adapterClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	monitor := NewCIMonitor(sdkClient, "owner", "repo", DefaultConfig())
	monitor.SetStepLogClient(adapterClient)

	body := monitor.GetFailedCheckExcerpts(context.Background(), "abc123")

	if len(body) > failedCheckExcerptBudgetChars+len(ciExcerptSentinel) {
		t.Errorf("body length = %d, want <= %d (budget + sentinel)", len(body), failedCheckExcerptBudgetChars+len(ciExcerptSentinel))
	}
	if !strings.Contains(body, "=== test") {
		t.Errorf("expected 'test' check heading, got:\n%s", body)
	}
	if !strings.Contains(body, "=== lint") {
		t.Errorf("expected 'lint' check heading, got:\n%s", body)
	}
	if strings.Contains(body, "=== build") {
		t.Errorf("passing 'build' check should not be included, got:\n%s", body)
	}
	if !strings.Contains(body, "TestForecastMonthlyBurn") {
		t.Errorf("expected 'test' check's failure content, got:\n%s", body)
	}
	if !strings.Contains(body, "TestOtherThing") {
		t.Errorf("expected 'lint' check's failure content, got:\n%s", body)
	}
}

// TestAssembleFailureExcerptsBody_MultipleFailuresFitBudget is a direct,
// HTTP-free unit test of the assembly step: given oversized excerpts for
// several checks, the combined body must stay within maxTotalChars while
// every check's heading and some of its content survives — no single
// excerpt should be able to starve the others out of the budget.
func TestAssembleFailureExcerptsBody_MultipleFailuresFitBudget(t *testing.T) {
	longTail := strings.Repeat("line of failure output\n", 500) // ~11500 chars, alone exceeds the budget below

	excerpts := []FailingStepExcerpt{
		{CheckName: "test", StepName: "Run go test ./...", Tail: longTail, PermalinkURL: "https://example.com/test"},
		{CheckName: "lint", StepName: "Run golangci-lint", Tail: longTail, PermalinkURL: "https://example.com/lint"},
		{CheckName: "build", StepName: "Run go build", Tail: longTail, PermalinkURL: "https://example.com/build"},
	}

	const budget = 3000
	body := AssembleFailureExcerptsBody(excerpts, budget)

	if len(body) > budget+500 {
		// Small slack: headings/permalinks/minTailBudget floors can push
		// slightly over an evenly-split budget on tiny budgets; the point of
		// this assertion is "roughly capped", not exact-to-the-byte.
		t.Errorf("body length = %d, want roughly <= %d", len(body), budget)
	}
	for _, name := range []string{"test", "lint", "build"} {
		if !strings.Contains(body, "=== "+name) {
			t.Errorf("expected heading for %q to survive budget capping, got:\n%s", name, body)
		}
	}
	for _, url := range []string{"https://example.com/test", "https://example.com/lint", "https://example.com/build"} {
		if !strings.Contains(body, url) {
			t.Errorf("expected permalink %q to survive budget capping, got:\n%s", url, body)
		}
	}
}

// TestAssembleFailureExcerptsBody_Empty verifies the no-failures case returns
// an empty string, so GetFailedCheckExcerpts's sentinel-prefix step can
// correctly detect "nothing to report" and skip the CI Error Logs section.
func TestAssembleFailureExcerptsBody_Empty(t *testing.T) {
	if got := AssembleFailureExcerptsBody(nil, failedCheckExcerptBudgetChars); got != "" {
		t.Errorf("AssembleFailureExcerptsBody(nil) = %q, want empty", got)
	}
}

func TestResolveFailingStep(t *testing.T) {
	t.Run("prefers failure over cancelled", func(t *testing.T) {
		steps := []ghadapter.JobStep{
			{Name: "Set up job", Conclusion: "success"},
			{Name: "Run tests", Conclusion: "failure"},
			{Name: "Upload artifacts", Conclusion: "cancelled"},
		}
		step, found := resolveFailingStep(steps)
		if !found {
			t.Fatal("expected a failing step to be found")
		}
		if step.Name != "Run tests" {
			t.Errorf("step.Name = %q, want %q", step.Name, "Run tests")
		}
	})

	t.Run("falls back to timed_out when no failure step", func(t *testing.T) {
		steps := []ghadapter.JobStep{
			{Name: "Set up job", Conclusion: "success"},
			{Name: "Run long test", Conclusion: "timed_out"},
			{Name: "Upload artifacts", Conclusion: "cancelled"},
		}
		step, found := resolveFailingStep(steps)
		if !found {
			t.Fatal("expected the timed_out step to be found")
		}
		if step.Name != "Run long test" {
			t.Errorf("step.Name = %q, want %q", step.Name, "Run long test")
		}
	})

	t.Run("no signal returns false", func(t *testing.T) {
		steps := []ghadapter.JobStep{
			{Name: "Set up job", Conclusion: "success"},
			{Name: "Upload artifacts", Conclusion: "cancelled"},
		}
		if _, found := resolveFailingStep(steps); found {
			t.Error("expected no failing step to be found")
		}
	})
}

func TestSliceLogByStepWindow(t *testing.T) {
	step := ghadapter.JobStep{
		Name:        "Run go test ./...",
		StartedAt:   "2026-07-18T10:00:05.0000000Z",
		CompletedAt: "2026-07-18T10:00:06.4000000Z",
	}

	window, ok := sliceLogByStepWindow(gh4460FixtureJobLog, step)
	if !ok {
		t.Fatal("expected a matching window")
	}
	if !strings.Contains(window, "TestForecastMonthlyBurn") {
		t.Errorf("expected window to contain the failure, got:\n%s", window)
	}
	if strings.Contains(window, "Current runner version") {
		t.Errorf("expected window to exclude the preamble, got:\n%s", window)
	}

	t.Run("missing timestamps returns false", func(t *testing.T) {
		if _, ok := sliceLogByStepWindow(gh4460FixtureJobLog, ghadapter.JobStep{Name: "no timestamps"}); ok {
			t.Error("expected false when step has no StartedAt/CompletedAt")
		}
	})
}

// TestCIMonitor_GetFailedCheckExcerpts_MidStepFailureSurvivesNoise covers
// GH-4825 in the step-log tier specifically (buildFailingStepExcerpt's
// "step" source — the common production path when a StepLogClient is
// wired): a failing step's own log can keep emitting output well past the
// actual failure line (more lint findings, cleanup noise). A plain
// last-N-lines tail on the step window would drop the failure entirely;
// this verifies GetFailedCheckExcerpts survives it end-to-end via
// match-anchored extraction.
func TestCIMonitor_GetFailedCheckExcerpts_MidStepFailureSurvivesNoise(t *testing.T) {
	const failingLine = "internal/executor/gh4405_test.go:167:2: Error return value of `w.Write` is not checked (errcheck)"

	stepStart, err := time.Parse(time.RFC3339Nano, "2026-08-09T10:00:05.0000000Z")
	if err != nil {
		t.Fatalf("failed to parse fixture step start: %v", err)
	}
	ts := func(offsetMillis int) string {
		return stepStart.Add(time.Duration(offsetMillis) * time.Millisecond).Format("2006-01-02T15:04:05.0000000Z")
	}

	var logBuilder strings.Builder
	logBuilder.WriteString("2026-08-09T10:00:00.0000000Z Current runner version: '2.319.1'\n")
	logBuilder.WriteString("2026-08-09T10:00:00.1000000Z Runner Image Provisioner\n")
	fmt.Fprintf(&logBuilder, "%s ##[group]Run golangci-lint run\n", ts(0))
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&logBuilder, "%s golangci-lint: checking package %d\n", ts(i), i)
	}
	fmt.Fprintf(&logBuilder, "%s %s\n", ts(31), failingLine)
	for i := 32; i <= 431; i++ {
		fmt.Fprintf(&logBuilder, "%s post-lint noise line %d\n", ts(i), i)
	}
	fmt.Fprintf(&logBuilder, "%s ##[error]Process completed with exit code 1.\n", ts(432))
	jobLog := logBuilder.String()

	steps := []ghadapter.JobStep{
		{Name: "Set up job", Status: "completed", Conclusion: "success", Number: 1,
			StartedAt: "2026-08-09T10:00:00.0000000Z", CompletedAt: "2026-08-09T10:00:04.9000000Z"},
		{Name: "Run golangci-lint run", Status: "completed", Conclusion: "failure", Number: 2,
			StartedAt: ts(0), CompletedAt: ts(432)},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/abc123/check-runs":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{ID: 200, Name: "lint", Status: "completed", Conclusion: "failure", DetailsURL: "https://github.com/owner/repo/runs/200"},
				},
			})
		case "/repos/owner/repo/actions/jobs/200/logs":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(jobLog))
		case "/repos/owner/repo/actions/jobs/200":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ghadapter.WorkflowJob{ID: 200, Name: "lint", Status: "completed", Steps: steps})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	sdkClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	adapterClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	monitor := NewCIMonitor(sdkClient, "owner", "repo", DefaultConfig())
	monitor.SetStepLogClient(adapterClient)

	body := monitor.GetFailedCheckExcerpts(context.Background(), "abc123")

	if !strings.Contains(body, failingLine) {
		t.Errorf("expected the mid-step errcheck finding to survive, got:\n%s", body)
	}
	if strings.Contains(body, "Current runner version") {
		t.Errorf("excerpt should not contain runner-setup preamble, got:\n%s", body)
	}
	if len(body) > failedCheckExcerptBudgetChars+len(ciExcerptSentinel) {
		t.Errorf("body length = %d, want <= %d", len(body), failedCheckExcerptBudgetChars+len(ciExcerptSentinel))
	}
}
