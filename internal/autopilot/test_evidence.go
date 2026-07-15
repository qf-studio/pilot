package autopilot

import (
	"fmt"
	"log/slog"
	"regexp"
	"strconv"

	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-4329: escalate-only test-evidence gate at the handleCIPassed chokepoint.
// Same defense-in-depth pattern as scope_guard.go — this only forces human
// approval when green CI looks like it verified nothing; it never blocks CI,
// never fails a check, and never relaxes an already-required approval.

const (
	defaultMinTests     = 1
	defaultMaxSkipRatio = 0.5
)

var (
	reGoPass        = regexp.MustCompile(`(?m)^\s*--- PASS: `)
	reGoFail        = regexp.MustCompile(`(?m)^\s*--- FAIL: `)
	reGoSkip        = regexp.MustCompile(`(?m)^\s*--- SKIP: `)
	reGoOK          = regexp.MustCompile(`(?m)^ok\s+\S+`)
	reGoNoTestFiles = regexp.MustCompile(`(?m)^\?\s+\S+\s+\[no test files\]`)

	// vitest summary: "Tests  42 passed | 5 skipped (47)" (skip clause optional).
	reVitestTests = regexp.MustCompile(`(?mi)^\s*Tests\s+(\d+)\s+passed(?:\s*\|\s*(\d+)\s+skipped)?\s*\(\d+\)`)
	// jest summary: "Tests:       5 skipped, 40 passed, 45 total".
	reJestTests = regexp.MustCompile(`(?mi)^\s*Tests:\s+(?:(\d+)\s+skipped,\s*)?(\d+)\s+passed,\s*\d+\s+total`)
)

// parseTestEvidence scans a combined CI job log for common test-runner summary
// shapes and returns how many tests ran and how many were skipped. parsed is
// false when none of the known shapes are found — an unparseable log must
// fail open (abstain), never be treated as "zero tests ran".
//
// Go is checked first (per-test `--- PASS/FAIL/SKIP` lines, falling back to
// per-package `ok` summary lines, falling back to `[no test files]`), then
// vitest/jest summary lines.
func parseTestEvidence(log string) (testsRun, testsSkipped int, parsed bool) {
	if pass, fail, skip := len(reGoPass.FindAllString(log, -1)), len(reGoFail.FindAllString(log, -1)), len(reGoSkip.FindAllString(log, -1)); pass+fail+skip > 0 {
		return pass + fail, skip, true
	}

	if m := reVitestTests.FindStringSubmatch(log); m != nil {
		passed, _ := strconv.Atoi(m[1])
		skipped := 0
		if m[2] != "" {
			skipped, _ = strconv.Atoi(m[2])
		}
		return passed, skipped, true
	}

	if m := reJestTests.FindStringSubmatch(log); m != nil {
		skipped := 0
		if m[1] != "" {
			skipped, _ = strconv.Atoi(m[1])
		}
		passed, _ := strconv.Atoi(m[2])
		return passed, skipped, true
	}

	// No per-test lines found — a non-verbose `go test ./...` run only prints
	// one `ok`/`FAIL` line per package. Count `ok` lines as a coarse tests-ran
	// proxy since individual test counts aren't recoverable from this shape.
	if okLines := len(reGoOK.FindAllString(log, -1)); okLines > 0 {
		return okLines, 0, true
	}

	// Only "[no test files]" markers and no ok/PASS/FAIL lines at all: the
	// job ran but exercised zero test files.
	if reGoNoTestFiles.MatchString(log) {
		return 0, 0, true
	}

	return 0, 0, false
}

// TestEvidenceReason returns a non-empty escalation reason when the test-
// evidence gate should hold auto-merge: the combined CI job log shows fewer
// than cfg.MinTests executed tests, or a skip ratio above cfg.MaxSkipRatio,
// on a PR that touches production source (files with a nonzero production
// addition per productionAdditions — see scope_guard.go). Empty string means
// no escalation: the gate is disabled, the log didn't parse (fail-open), or
// the PR doesn't touch production code (nothing to expect test coverage for).
func TestEvidenceReason(logger *slog.Logger, cfg *TestEvidenceConfig, files []*github.PRFile, log string) string {
	if cfg == nil || !cfg.Enabled {
		return ""
	}

	production, _, _ := productionAdditions(files)
	if production == 0 {
		return ""
	}

	testsRun, testsSkipped, parsed := parseTestEvidence(log)
	if !parsed {
		if logger != nil {
			logger.Warn("test-evidence gate abstained: could not parse CI job log (fail-open)")
		}
		return ""
	}

	minTests := cfg.MinTests
	if minTests <= 0 {
		minTests = defaultMinTests
	}
	maxSkipRatio := cfg.MaxSkipRatio
	if maxSkipRatio <= 0 {
		maxSkipRatio = defaultMaxSkipRatio
	}

	if testsRun < minTests {
		return fmt.Sprintf("test-evidence gate: CI log shows %d test(s) run (< min_tests=%d) on a PR with %d production-line additions — green CI did not verify this change",
			testsRun, minTests, production)
	}

	if total := testsRun + testsSkipped; total > 0 {
		skipRatio := float64(testsSkipped) / float64(total)
		if skipRatio > maxSkipRatio {
			return fmt.Sprintf("test-evidence gate: %d/%d tests skipped (%.0f%% > max_skip_ratio=%.0f%%)",
				testsSkipped, total, skipRatio*100, maxSkipRatio*100)
		}
	}

	return ""
}
