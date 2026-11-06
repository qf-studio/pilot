package executor

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// callLog is the order in which a run reached the backend and the quality gates.
type callLog struct {
	mu     sync.Mutex
	events []string // "backend" or "gate"
	prompt []string // the prompt of each "backend" event, in order
}

func (l *callLog) add(event, prompt string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
	if event == "backend" {
		l.prompt = append(l.prompt, prompt)
	}
}

// afterGates returns the prompts of the backend calls that came after the first
// quality-gate run, or after the implementation call when no gates are configured.
func (l *callLog) afterGates() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	start := -1
	for i, e := range l.events {
		if e == "gate" {
			start = i
			break
		}
	}
	backends := 0
	var after []string
	for i, e := range l.events {
		if e != "backend" {
			continue
		}
		backends++
		if start >= 0 && i > start || start < 0 && backends > 1 {
			after = append(after, l.prompt[backends-1])
		}
	}
	return after
}

// orderedChecker is a passing quality checker that logs each run.
type orderedChecker struct{ log *callLog }

func (c orderedChecker) Check(context.Context) (*QualityOutcome, error) {
	c.log.add("gate", "")
	return &QualityOutcome{Passed: true}, nil
}

// TestNoSelfReviewBackendCallAfterGates drives Execute end to end and reads the
// backend calls that follow the quality gates. GH-6157 retired the self-review
// phase and the in-run intent judge: in every pre-PR review mode the only call
// that may follow the gates is the fix round, and only in enforce mode when the
// review has a blocking defect. A backend call added back after the gates (a
// self-review under any name) changes these counts.
func TestNoSelfReviewBackendCallAfterGates(t *testing.T) {
	blocking := &PostMergeReview{
		Verdict: PostMergeRequestChanges,
		Defects: []PostMergeDefect{
			{Severity: PostMergeSeverityBlocking, Location: "feature.go:3", Claim: "wrong constant"},
		},
		FilesRead: 4,
	}
	tests := []struct {
		name   string
		mode   string
		review *PostMergeReview
		gates  bool
		// wantFix is the number of fix-round calls that may follow the gates.
		wantFix int
	}{
		{name: "off, gates", mode: PrePRReviewModeOff, review: approveWithNotes(), gates: true},
		{name: "off, no gates configured", mode: PrePRReviewModeOff, review: approveWithNotes()},
		{name: "shadow, gates", mode: PrePRReviewModeShadow, review: blocking, gates: true},
		{name: "shadow, no gates configured", mode: PrePRReviewModeShadow, review: blocking},
		{name: "enforce, approved", mode: PrePRReviewModeEnforce, review: approveWithNotes(), gates: true},
		{name: "enforce, blocking defect", mode: PrePRReviewModeEnforce, review: blocking, gates: true, wantFix: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const branch = "pilot/GH-6157-noselfreview"
			dir, _ := setupFreshnessRepo(t)
			runGit(t, dir, "checkout", "-b", branch)

			log := &callLog{}
			backend := &mockGH4964Backend{
				perCall: func(call int, opts ExecuteOptions) *BackendResult {
					log.add("backend", opts.Prompt)
					if call == 1 {
						writeUncommittedFile(t, dir, "real-work.go")
						runGit(t, dir, "add", "real-work.go")
						runGit(t, dir, "commit", "-m", "real work")
						return &BackendResult{Success: true, Output: "done"}
					}
					writeUncommittedFile(t, dir, "review-fix.go")
					runGit(t, dir, "add", "review-fix.go")
					runGit(t, dir, "commit", "-m", "fix: address review")
					return &BackendResult{Success: true, Output: "REVIEW_FIXED: feature.go:3 wrong constant"}
				},
			}
			runner := newGH4964Runner(backend)
			// The deprecated key must not bring the phase back or keep it away.
			runner.config.SkipSelfReview = false
			runner.config.PrePRReview = &PrePRReviewConfig{Mode: tt.mode, SkipComplexity: []string{}}
			runner.prePRReviewer = &fakePrePRReviewer{review: tt.review}
			if tt.gates {
				runner.qualityCheckerFactory = func(string, string, string) QualityChecker { return orderedChecker{log: log} }
			}

			task := newGH4964Task("GH-6157", branch, dir)
			task.Title = "Add multi-component feature with config wiring"
			task.Description = "Implement the feature across the config, the runner and the adapters, with tests."
			task.SkipQualityGates = !tt.gates
			creator := &bodyCapturingPRCreator{}
			task.SourceAdapter = "linear"
			runner.SetPRCreator(creator)

			result, err := runner.Execute(context.Background(), task)
			if err != nil {
				t.Fatalf("Execute() returned error: %v", err)
			}
			if !result.Success || result.PRUrl == "" {
				t.Fatalf("expected a created PR, got success=%v url=%q error=%q", result.Success, result.PRUrl, result.Error)
			}

			after := log.afterGates()
			if len(after) != tt.wantFix {
				t.Fatalf("%d backend call(s) followed the gates, want %d (only the fix round may):\n%q", len(after), tt.wantFix, after)
			}
			for _, prompt := range after {
				if want := runner.buildReviewFixPrompt(tt.review.BlockingDefects()); prompt != want {
					t.Errorf("the call after the gates is not the review fix round:\n%s", prompt)
				}
			}
			for _, prompt := range log.prompt {
				if strings.Contains(prompt, "Self-Review Phase") {
					t.Errorf("a self-review prompt reached the backend:\n%s", prompt)
				}
			}
		})
	}
}
