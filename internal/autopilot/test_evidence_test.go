package autopilot

import (
	"strings"
	"testing"

	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

func TestParseTestEvidence(t *testing.T) {
	cases := []struct {
		name        string
		log         string
		wantRun     int
		wantSkipped int
		wantParsed  bool
	}{
		{
			name: "go verbose PASS/SKIP",
			log: strings.Join([]string{
				"=== RUN   TestFoo",
				"--- PASS: TestFoo (0.00s)",
				"=== RUN   TestBar",
				"--- SKIP: TestBar (0.00s)",
				"=== RUN   TestBaz",
				"--- PASS: TestBaz (0.00s)",
				"PASS",
				"ok  \tgithub.com/qf-studio/pilot-console/internal/fleet\t0.010s",
			}, "\n"),
			wantRun:     2,
			wantSkipped: 1,
			wantParsed:  true,
		},
		{
			name: "go non-verbose ok summary",
			log: strings.Join([]string{
				"ok  \tgithub.com/qf-studio/pilot/internal/executor\t0.512s",
				"ok  \tgithub.com/qf-studio/pilot/internal/memory\t0.223s",
			}, "\n"),
			wantRun:     2,
			wantSkipped: 0,
			wantParsed:  true,
		},
		{
			name: "go no test files",
			log: strings.Join([]string{
				"?   \tgithub.com/qf-studio/pilot/internal/fleet\t[no test files]",
			}, "\n"),
			wantRun:     0,
			wantSkipped: 0,
			wantParsed:  true,
		},
		{
			name: "go all skipped, still ok per package (the pilot-console PR #13 shape)",
			log: strings.Join([]string{
				"=== RUN   TestStore_Create",
				"    store_test.go:12: skipping: DATABASE_URL not set",
				"--- SKIP: TestStore_Create (0.00s)",
				"=== RUN   TestStore_Update",
				"    store_test.go:30: skipping: DATABASE_URL not set",
				"--- SKIP: TestStore_Update (0.00s)",
				"PASS",
				"ok  \tgithub.com/qf-studio/pilot-console/internal/fleet\t0.005s",
			}, "\n"),
			wantRun:     0,
			wantSkipped: 2,
			wantParsed:  true,
		},
		{
			name: "vitest summary with skips",
			log: strings.Join([]string{
				" Test Files  3 passed (3)",
				"      Tests  42 passed | 5 skipped (47)",
				"   Start at  10:00:00",
				"   Duration  1.20s",
			}, "\n"),
			wantRun:     42,
			wantSkipped: 5,
			wantParsed:  true,
		},
		{
			name: "vitest summary with no skips",
			log: strings.Join([]string{
				" Test Files  1 passed (1)",
				"      Tests  12 passed (12)",
			}, "\n"),
			wantRun:     12,
			wantSkipped: 0,
			wantParsed:  true,
		},
		{
			name: "jest summary with skips",
			log: strings.Join([]string{
				"Tests:       5 skipped, 40 passed, 45 total",
				"Test Suites: 6 passed, 6 total",
			}, "\n"),
			wantRun:     40,
			wantSkipped: 5,
			wantParsed:  true,
		},
		{
			name:        "garbage log fails open",
			log:         "Building Docker image...\nPushing to registry...\ndone.",
			wantRun:     0,
			wantSkipped: 0,
			wantParsed:  false,
		},
		{
			name:        "empty log fails open",
			log:         "",
			wantRun:     0,
			wantSkipped: 0,
			wantParsed:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run, skipped, parsed := parseTestEvidence(tc.log)
			if parsed != tc.wantParsed {
				t.Fatalf("parsed = %v, want %v", parsed, tc.wantParsed)
			}
			if run != tc.wantRun {
				t.Errorf("testsRun = %d, want %d", run, tc.wantRun)
			}
			if skipped != tc.wantSkipped {
				t.Errorf("testsSkipped = %d, want %d", skipped, tc.wantSkipped)
			}
		})
	}
}

func TestTestEvidenceReason(t *testing.T) {
	sourceFiles := []*github.PRFile{
		{Filename: "internal/fleet/store.go", Status: "modified", Additions: 40},
	}
	docsOnlyFiles := []*github.PRFile{
		{Filename: ".agent/tasks/TASK-1.md", Status: "modified", Additions: 40},
	}

	cases := []struct {
		name         string
		cfg          *TestEvidenceConfig
		files        []*github.PRFile
		log          string
		wantContains string // empty = expect no escalation
	}{
		{
			name:         "disabled config never escalates",
			cfg:          &TestEvidenceConfig{Enabled: false},
			files:        sourceFiles,
			log:          "?   \tpkg\t[no test files]",
			wantContains: "",
		},
		{
			name:         "nil config never escalates",
			cfg:          nil,
			files:        sourceFiles,
			log:          "?   \tpkg\t[no test files]",
			wantContains: "",
		},
		{
			name:         "zero tests run escalates",
			cfg:          &TestEvidenceConfig{Enabled: true},
			files:        sourceFiles,
			log:          "?   \tpkg\t[no test files]",
			wantContains: "0 test(s) run",
		},
		{
			name:  "all-skipped escalates over max_skip_ratio",
			cfg:   &TestEvidenceConfig{Enabled: true},
			files: sourceFiles,
			log: strings.Join([]string{
				"--- SKIP: TestA (0.00s)",
				"--- SKIP: TestB (0.00s)",
				"--- SKIP: TestC (0.00s)",
				"--- PASS: TestD (0.00s)",
			}, "\n"),
			wantContains: "3/4 tests skipped",
		},
		{
			name:  "skip ratio under threshold does not escalate",
			cfg:   &TestEvidenceConfig{Enabled: true},
			files: sourceFiles,
			log: strings.Join([]string{
				"--- SKIP: TestA (0.00s)",
				"--- PASS: TestB (0.00s)",
				"--- PASS: TestC (0.00s)",
				"--- PASS: TestD (0.00s)",
			}, "\n"),
			wantContains: "",
		},
		{
			name:         "rigorous pass does not escalate",
			cfg:          &TestEvidenceConfig{Enabled: true},
			files:        sourceFiles,
			log:          "--- PASS: TestA (0.00s)\n--- PASS: TestB (0.00s)\nok  \tpkg\t0.010s",
			wantContains: "",
		},
		{
			name:         "bookkeeping-only PR abstains even with zero tests",
			cfg:          &TestEvidenceConfig{Enabled: true},
			files:        docsOnlyFiles,
			log:          "?   \tpkg\t[no test files]",
			wantContains: "",
		},
		{
			name:         "garbage log fails open even when enabled",
			cfg:          &TestEvidenceConfig{Enabled: true},
			files:        sourceFiles,
			log:          "Building Docker image...\ndone.",
			wantContains: "",
		},
		{
			name:         "custom min_tests threshold",
			cfg:          &TestEvidenceConfig{Enabled: true, MinTests: 5},
			files:        sourceFiles,
			log:          "--- PASS: TestA (0.00s)\n--- PASS: TestB (0.00s)",
			wantContains: "< min_tests=5",
		},
		{
			name:  "custom max_skip_ratio threshold",
			cfg:   &TestEvidenceConfig{Enabled: true, MaxSkipRatio: 0.9},
			files: sourceFiles,
			log: strings.Join([]string{
				"--- SKIP: TestA (0.00s)",
				"--- SKIP: TestB (0.00s)",
				"--- SKIP: TestC (0.00s)",
				"--- PASS: TestD (0.00s)",
			}, "\n"),
			// 75% skipped is below the raised 90% threshold — no escalation.
			wantContains: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TestEvidenceReason(nil, tc.cfg, tc.files, tc.log)
			if tc.wantContains == "" {
				if got != "" {
					t.Fatalf("TestEvidenceReason = %q, want no escalation", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantContains) {
				t.Fatalf("TestEvidenceReason = %q, want substring %q", got, tc.wantContains)
			}
		})
	}
}
