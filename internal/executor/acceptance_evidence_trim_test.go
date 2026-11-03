package executor

import (
	"fmt"
	"strings"
	"testing"
)

// noiseLines returns n log-noise lines that match no failure pattern.
func noiseLines(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("    %s noise %d", prefix, i)
	}
	return out
}

func TestTrimOutputForEvidence_KeepsFailLinesFromOmittedMiddle(t *testing.T) {
	var lines []string
	lines = append(lines, noiseLines("head", 150)...)
	lines = append(lines,
		"--- FAIL: TestMiddleOne (0.01s)",
		"    one_test.go:42: expected 1, got 2",
		"panic: boom",
		"WARNING: DATA RACE",
	)
	lines = append(lines, noiseLines("mid", 150)...)
	lines = append(lines, noiseLines("tail", 78)...)
	lines = append(lines, "FAIL\tgithub.com/x/pkg\t1.5s", "FAIL")

	got := trimOutputForEvidence(strings.Join(lines, "\n"))

	for _, want := range []string{
		"--- FAIL: TestMiddleOne (0.01s)",
		"    one_test.go:42: expected 1, got 2",
		"panic: boom",
		"WARNING: DATA RACE",
		"failure lines kept: ...",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("trimmed output lost %q:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, "    head noise 0\n") || !strings.HasSuffix(got, "FAIL") {
		t.Errorf("head and tail must be kept, got:\n%s", got)
	}
	// The marker counts only lines that are really gone: 284 middle - 4 kept.
	if !strings.Contains(got, "... 280 lines omitted; 4 failure lines kept: ...") {
		t.Errorf("unexpected marker:\n%s", got)
	}
}

func TestTrimOutputForEvidence_NoFailureLinesUnchanged(t *testing.T) {
	lines := noiseLines("n", 400)
	got := trimOutputForEvidence(strings.Join(lines, "\n"))

	want := strings.Join(lines[:evidenceHeadLines], "\n") +
		fmt.Sprintf("\n... %d lines omitted ...\n", 400-maxEvidenceOutputLines) +
		strings.Join(lines[400-evidenceTailLines:], "\n")
	if got != want {
		t.Errorf("output without failure lines changed:\n got: %q\nwant: %q", got, want)
	}
}

func TestTrimOutputForEvidence_FailureLineBudgetCapped(t *testing.T) {
	var lines []string
	lines = append(lines, noiseLines("head", 50)...)
	for i := 0; i < 100; i++ {
		lines = append(lines, fmt.Sprintf("--- FAIL: TestMany%d (0.00s)", i))
	}
	lines = append(lines, noiseLines("tail", 100)...)

	got := trimOutputForEvidence(strings.Join(lines, "\n"))

	if n := strings.Count(got, "--- FAIL: TestMany"); n != evidenceFailureLineBudget {
		t.Errorf("kept %d failure lines, want the budget %d", n, evidenceFailureLineBudget)
	}
	if n := len(strings.Split(got, "\n")); n > maxEvidenceOutputLines+evidenceFailureLineBudget+1 {
		t.Errorf("trimmed block has %d lines, hard cap is %d", n, maxEvidenceOutputLines+evidenceFailureLineBudget+1)
	}
	if !strings.Contains(got, "30 failure lines kept") || !strings.Contains(got, "TestMany29 ") || strings.Contains(got, "TestMany30 ") {
		t.Errorf("the first %d failure lines must be kept, in order:\n%s", evidenceFailureLineBudget, got)
	}
}
