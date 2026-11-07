package autopilot

import (
	"os"
	"strings"
	"testing"
)

// failBlock renders one `go test -v` failure: assertion line, then FAIL line.
func failBlock(name, assertion string) string {
	return "    " + assertion + "\n--- FAIL: " + name + " (0.03s)\n=== RUN   Next\n"
}

func TestGenerateBody_FailingTestsFromFullLog(t *testing.T) {
	// Three FAIL blocks; the filler pushes two of them past the 4000-char
	// excerpt budget (the excerpt is head-truncated, GH-6182).
	filler := strings.Repeat("some unrelated log line that is not a marker\n", 200)
	logs := failBlock("TestAlpha", "alpha_test.go:10: want 2, got 1") +
		filler + failBlock("TestBeta", "beta_test.go:20: want 3, got 2") +
		filler + failBlock("TestGamma", "gamma_test.go:30: want 4, got 3") +
		failBlock("TestAlpha", "alpha_test.go:10: want 2, got 1") // duplicate

	fl := &FeedbackLoop{}
	prState := &PRState{PRNumber: 7, IssueNumber: 5, BranchName: "pilot/GH-5", HeadSHA: "abcdef1234567890"}
	body := fl.generateBody(prState, FailureCIPreMerge, []string{"test"}, logs, 1, nil)

	_, list, ok := strings.Cut(body, "## Failing tests")
	if !ok {
		t.Fatalf("body has no ## Failing tests section:\n%s", body)
	}
	list, _, _ = strings.Cut(list, "\n## ")
	for _, want := range []string{"`TestAlpha`", "`TestBeta`", "`TestGamma`", "beta_test.go:20: want 3, got 2"} {
		if !strings.Contains(list, want) {
			t.Errorf("failing tests list missing %q:\n%s", want, list)
		}
	}
	if n := strings.Count(list, "`TestAlpha`"); n != 1 {
		t.Errorf("TestAlpha listed %d times, want 1 (de-duplicated)", n)
	}
	if strings.Index(body, "## Failing tests") > strings.Index(body, "<details>") {
		t.Error("## Failing tests must sit above the log excerpt")
	}
	if !strings.Contains(body, "First decide per failing test whether the test is stale (asserts behaviour the PR removed) or the change is wrong; port stale tests, fix wrong code; say which in the PR body.") {
		t.Errorf("Task section lacks the decision sentence:\n%s", body)
	}
	if !strings.Contains(body, "The PR's change is the intended one.") {
		t.Error("Task section lacks the intended-change statement")
	}
	if !strings.Contains(body, "\nCloses #5\n") {
		t.Error("Closes #<source> line must be kept")
	}
	if strings.Contains(body, "Depends on") {
		t.Error("body must not carry a Depends on line")
	}
	if !strings.Contains(body, "<!-- autopilot-meta branch:pilot/GH-5 pr:7 iteration:1 source:5 sha:abcdef1234567890 -->") {
		t.Errorf("footer changed:\n%s", body)
	}
}

func TestGenerateBody_FailingTestsPostMerge(t *testing.T) {
	logs := failBlock("TestPost", "post_test.go:1: boom")
	body := (&FeedbackLoop{}).generateBody(&PRState{PRNumber: 9}, FailureCIPostMerge, nil, logs, 1, nil)
	if !strings.Contains(body, "## Failing tests") || !strings.Contains(body, "`TestPost`") {
		t.Errorf("post-merge body lacks failing tests:\n%s", body)
	}
	if !strings.Contains(body, "PR was merged but CI failed afterward") || !strings.Contains(body, "First decide per failing test") {
		t.Errorf("post-merge Task text wrong:\n%s", body)
	}
}

func TestGenerateBody_NoFailingTestsSectionForOtherFailures(t *testing.T) {
	logs := failBlock("TestX", "x_test.go:1: boom")
	body := (&FeedbackLoop{}).generateBody(&PRState{PRNumber: 9}, FailureMerge, nil, logs, 1, nil)
	if strings.Contains(body, "## Failing tests") {
		t.Errorf("merge-conflict body must not list failing tests:\n%s", body)
	}
}

func TestGenerateBody_FailingTestsFromExcerptBundle(t *testing.T) {
	tests := []FailingTest{
		{Name: "TestOne", Assertion: "one_test.go:1: a --> b"},
		{Name: "TestTwo"},
	}
	bundle := ciExcerptSentinel + encodeFailingTestsBlock(tests) + "=== test ===\nexcerpt tail\n"
	body := (&FeedbackLoop{}).generateBody(&PRState{PRNumber: 3}, FailureCIPreMerge, []string{"test"}, bundle, 1, nil)

	for _, want := range []string{"`TestOne`", "`TestTwo`", "=== test ===\nexcerpt tail"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "gh6182:failing-tests") {
		t.Error("machine-readable failing-tests block leaked into the rendered excerpt")
	}
	// A "-->" inside an assertion must not terminate the HTML comment early.
	got, rest := splitFailingTestsBlock(encodeFailingTestsBlock(tests) + "tail")
	if len(got) != 2 || rest != "tail" {
		t.Errorf("round trip = %+v rest %q", got, rest)
	}
}

// TestGenerateBody_ReplayPR6165 replays the failed-job log of PR#6165 run
// 42251277383 (issue #6167, rejected reject_vague 2026-11-06): the body must
// name all three failing quality-gate tests.
func TestGenerateBody_ReplayPR6165(t *testing.T) {
	data, err := os.ReadFile("testdata/gh6182_pr6165_run42251277383_excerpt.txt")
	if err != nil {
		t.Fatal(err)
	}
	body := (&FeedbackLoop{}).generateBody(&PRState{PRNumber: 6165}, FailureCIPreMerge, []string{"test"}, string(data), 1, nil)
	for _, name := range []string{"TestQualityGatesHappyPath", "TestQualityGatesRetrySuccess", "TestQualityGatesRetryFeedbackPropagation"} {
		if !strings.Contains(body, "- `"+name+"`") {
			t.Errorf("replay body does not list %s:\n%s", name, body)
		}
	}
	if strings.Contains(body, "`TestX`") {
		t.Error("a --- FAIL: quoted mid-line inside a structured log record must not be listed")
	}
	if !strings.Contains(body, "quality_integration_test.go:182: Expected backend to be called 2 times") {
		t.Error("assertion line preceding the FAIL line should accompany the test name")
	}
}
