package executor

import (
	"strings"
	"testing"
)

func TestClassifyAcceptanceItem(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		wantKind     AcceptanceItemKind
		wantCommands []string
		wantDesc     string
		wantTarget   string
	}{
		{
			name:         "paste output backtick command",
			text:         "paste the output of `go test -run TestX ./pkg/`",
			wantKind:     AcceptanceItemPasteOutput,
			wantCommands: []string{"go test -run TestX ./pkg/"},
		},
		{
			name:         "pasted into the PR body phrasing",
			text:         "Mutation checks pasted into the PR body: `go vet ./...`",
			wantKind:     AcceptanceItemPasteOutput,
			wantCommands: []string{"go vet ./..."},
		},
		{
			name:         "terminal output phrasing",
			text:         "include the terminal output of `make test`",
			wantKind:     AcceptanceItemPasteOutput,
			wantCommands: []string{"make test"},
		},
		{
			name:         "multiple commands per item",
			text:         "paste the output of `go build ./...` and `go test ./...`",
			wantKind:     AcceptanceItemPasteOutput,
			wantCommands: []string{"go build ./...", "go test ./..."},
		},
		{
			name:     "paste output with no commands parsed",
			text:     "paste the output of the failing command",
			wantKind: AcceptanceItemPasteOutput,
		},
		{
			name:       "mutation delete line with arrow",
			text:       "delete line 42 in foo.go -> TestFoo fails",
			wantKind:   AcceptanceItemMutation,
			wantDesc:   "delete line 42 in foo.go",
			wantTarget: "TestFoo",
		},
		{
			name:       "mutation with unicode arrow",
			text:       "change the threshold from 5 to 50 → TestThreshold fails",
			wantKind:   AcceptanceItemMutation,
			wantDesc:   "change the threshold from 5 to 50",
			wantTarget: "TestThreshold",
		},
		{
			name:       "mutation remove with package path target",
			text:       "remove the nil check -> ./internal/foo/... fails",
			wantKind:   AcceptanceItemMutation,
			wantDesc:   "remove the nil check",
			wantTarget: "./internal/foo/...",
		},
		{
			name:       "mutation checkbox prefix stripped",
			text:       "- [ ] delete line 7 in bar.go -> TestBar fails",
			wantKind:   AcceptanceItemMutation,
			wantDesc:   "delete line 7 in bar.go",
			wantTarget: "TestBar",
		},
		{
			name:     "other: no arrow at all",
			text:     "update the README with usage instructions",
			wantKind: AcceptanceItemOther,
		},
		{
			name:     "other: arrow present but no edit cue",
			text:     "if X -> Y fails, that's a separate bug",
			wantKind: AcceptanceItemOther,
		},
		{
			name:     "other: edit cue present but no fails assertion",
			text:     "change the config -> restart the server",
			wantKind: AcceptanceItemOther,
		},
		{
			name:     "other: unrelated checklist item",
			text:     "Unit tests for the new parser",
			wantKind: AcceptanceItemOther,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := ClassifyAcceptanceItem(tt.text)
			if item.Kind != tt.wantKind {
				t.Fatalf("Kind = %q, want %q", item.Kind, tt.wantKind)
			}
			if tt.wantCommands != nil {
				if len(item.Commands) != len(tt.wantCommands) {
					t.Fatalf("Commands = %v, want %v", item.Commands, tt.wantCommands)
				}
				for i, c := range tt.wantCommands {
					if item.Commands[i] != c {
						t.Errorf("Commands[%d] = %q, want %q", i, item.Commands[i], c)
					}
				}
			}
			if tt.wantDesc != "" && item.MutationDescription != tt.wantDesc {
				t.Errorf("MutationDescription = %q, want %q", item.MutationDescription, tt.wantDesc)
			}
			if tt.wantTarget != "" && item.MutationTarget != tt.wantTarget {
				t.Errorf("MutationTarget = %q, want %q", item.MutationTarget, tt.wantTarget)
			}
		})
	}
}

func TestClassifyAcceptanceItem_PasteOutputWithNoCommandsHasNoCommands(t *testing.T) {
	item := ClassifyAcceptanceItem("paste the output of the failing command")
	if len(item.Commands) != 0 {
		t.Fatalf("expected zero commands, got %v", item.Commands)
	}
}

func TestParseAcceptanceItems(t *testing.T) {
	criteria := []string{
		"paste the output of `go test ./...`",
		"delete line 1 in main.go -> TestMain fails",
		"unrelated acceptance item",
	}
	items := ParseAcceptanceItems(criteria)
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	if items[0].Kind != AcceptanceItemPasteOutput {
		t.Errorf("item 0 kind = %q, want paste_output", items[0].Kind)
	}
	if items[1].Kind != AcceptanceItemMutation {
		t.Errorf("item 1 kind = %q, want mutation", items[1].Kind)
	}
	if items[2].Kind != AcceptanceItemOther {
		t.Errorf("item 2 kind = %q, want other", items[2].Kind)
	}
}

func TestAcceptanceItem_RequiresEvidence(t *testing.T) {
	if !(AcceptanceItem{Kind: AcceptanceItemPasteOutput}).RequiresEvidence() {
		t.Error("paste_output should require evidence")
	}
	if !(AcceptanceItem{Kind: AcceptanceItemMutation}).RequiresEvidence() {
		t.Error("mutation should require evidence")
	}
	if (AcceptanceItem{Kind: AcceptanceItemOther}).RequiresEvidence() {
		t.Error("other should not require evidence")
	}
}

func TestTrimOutputForEvidence(t *testing.T) {
	t.Run("short output unchanged", func(t *testing.T) {
		out := trimOutputForEvidence("line1\nline2")
		if out != "line1\nline2" {
			t.Fatalf("got %q", out)
		}
	})

	t.Run("empty output stays empty", func(t *testing.T) {
		if out := trimOutputForEvidence(""); out != "" {
			t.Fatalf("got %q", out)
		}
	})

	t.Run("long output truncated to cap with marker", func(t *testing.T) {
		lines := make([]string, 0, 100)
		for i := 0; i < 100; i++ {
			lines = append(lines, "line")
		}
		out := trimOutputForEvidence(strings.Join(lines, "\n"))
		outLines := strings.Split(out, "\n")
		// maxEvidenceOutputLines kept lines + 1 marker line.
		if len(outLines) != maxEvidenceOutputLines+1 {
			t.Fatalf("expected %d lines, got %d", maxEvidenceOutputLines+1, len(outLines))
		}
		if !strings.Contains(out, "truncated: 40 line(s) omitted") {
			t.Fatalf("expected truncation marker mentioning 40 omitted lines, got %q", out)
		}
	})
}

func TestRenderAcceptanceEvidenceSections(t *testing.T) {
	t.Run("empty results renders nothing", func(t *testing.T) {
		if got := RenderAcceptanceEvidenceSections(nil); got != "" {
			t.Fatalf("expected empty string, got %q", got)
		}
	})

	t.Run("paste-output evidence renders Evidence section with command and output", func(t *testing.T) {
		results := []AcceptanceEvidenceResult{
			{
				Item: AcceptanceItem{Text: "paste the output of `go test ./...`", Kind: AcceptanceItemPasteOutput},
				Commands: []AcceptanceCommandResult{
					{Command: "go test ./...", Output: "ok  \tpkg\t0.5s"},
				},
			},
		}
		got := RenderAcceptanceEvidenceSections(results)
		if !strings.Contains(got, "## Evidence") {
			t.Errorf("expected an ## Evidence section, got %q", got)
		}
		if !strings.Contains(got, "$ go test ./...") {
			t.Errorf("expected the command to be rendered, got %q", got)
		}
		if !strings.Contains(got, "ok  \tpkg\t0.5s") {
			t.Errorf("expected the output to be rendered, got %q", got)
		}
		if strings.Contains(got, "## Not verified") {
			t.Errorf("did not expect a Not verified section, got %q", got)
		}
	})

	t.Run("mutation with failing test renders failing test name", func(t *testing.T) {
		results := []AcceptanceEvidenceResult{
			{
				Item: AcceptanceItem{Text: "delete line 1 in main.go -> TestMain fails", Kind: AcceptanceItemMutation},
				MutationOutcome: &MutationOutcome{
					Description:  "delete line 1 in main.go",
					Target:       "TestMain",
					Output:       "--- FAIL: TestMain",
					FailingTests: []string{"TestMain"},
				},
			},
		}
		got := RenderAcceptanceEvidenceSections(results)
		if !strings.Contains(got, "failing test(s): TestMain") {
			t.Errorf("expected failing test name in output, got %q", got)
		}
	})

	t.Run("mutation with no failing test is reported, not omitted", func(t *testing.T) {
		results := []AcceptanceEvidenceResult{
			{
				Item: AcceptanceItem{Text: "delete line 1 in main.go -> TestMain fails", Kind: AcceptanceItemMutation},
				MutationOutcome: &MutationOutcome{
					Description:  "delete line 1 in main.go",
					Target:       "TestMain",
					Output:       "ok",
					NoTestFailed: true,
				},
			},
		}
		got := RenderAcceptanceEvidenceSections(results)
		if !strings.Contains(got, "no test failed") {
			t.Errorf("expected a 'no test failed' finding to be reported, got %q", got)
		}
	})

	t.Run("not-verified item renders Not verified section with reason", func(t *testing.T) {
		results := []AcceptanceEvidenceResult{
			{
				Item:              AcceptanceItem{Text: "paste the output of the thing"},
				NotVerifiedReason: "no commands parsed from acceptance item",
			},
		}
		got := RenderAcceptanceEvidenceSections(results)
		if !strings.Contains(got, "## Not verified") {
			t.Errorf("expected a ## Not verified section, got %q", got)
		}
		if !strings.Contains(got, "no commands parsed from acceptance item") {
			t.Errorf("expected the reason to be rendered, got %q", got)
		}
		if strings.Contains(got, "## Evidence") {
			t.Errorf("did not expect an Evidence section, got %q", got)
		}
	})

	t.Run("mixed evidence and not-verified renders both sections", func(t *testing.T) {
		results := []AcceptanceEvidenceResult{
			{
				Item:     AcceptanceItem{Text: "paste the output of `echo hi`", Kind: AcceptanceItemPasteOutput},
				Commands: []AcceptanceCommandResult{{Command: "echo hi", Output: "hi"}},
			},
			{
				Item:              AcceptanceItem{Text: "change X -> TestY fails"},
				NotVerifiedReason: "could not parse mutation into an applicable file edit",
			},
		}
		got := RenderAcceptanceEvidenceSections(results)
		if !strings.Contains(got, "## Evidence") || !strings.Contains(got, "## Not verified") {
			t.Fatalf("expected both sections, got %q", got)
		}
		if strings.Index(got, "## Evidence") > strings.Index(got, "## Not verified") {
			t.Errorf("expected Evidence section before Not verified section, got %q", got)
		}
	})
}

func TestExtractFailingTests(t *testing.T) {
	output := "=== RUN   TestFoo\n--- FAIL: TestFoo (0.00s)\n=== RUN   TestBar\n--- PASS: TestBar (0.00s)\nFAIL\n"
	got := extractFailingTests(output)
	if len(got) != 1 || got[0] != "TestFoo" {
		t.Fatalf("got %v, want [TestFoo]", got)
	}
}

func TestExtractFailingTests_NoFailures(t *testing.T) {
	output := "=== RUN   TestFoo\n--- PASS: TestFoo (0.00s)\nPASS\nok\tpkg\t0.01s\n"
	got := extractFailingTests(output)
	if len(got) != 0 {
		t.Fatalf("expected no failing tests, got %v", got)
	}
}

func TestParseLineMutation(t *testing.T) {
	tests := []struct {
		desc     string
		wantFile string
		wantLine int
		wantOK   bool
	}{
		{"delete line 42 in foo.go", "foo.go", 42, true},
		{"remove line 7 from internal/bar.go", "internal/bar.go", 7, true},
		{"delete line 3 of baz.go", "baz.go", 3, true},
		{"DELETE LINE 5 IN Qux.go", "Qux.go", 5, true},
		{"delete line 42", "", 0, false},
		{"change the threshold to 50", "", 0, false},
	}
	for _, tt := range tests {
		file, line, ok := parseLineMutation(tt.desc)
		if ok != tt.wantOK {
			t.Fatalf("parseLineMutation(%q) ok = %v, want %v", tt.desc, ok, tt.wantOK)
		}
		if !ok {
			continue
		}
		if file != tt.wantFile || line != tt.wantLine {
			t.Errorf("parseLineMutation(%q) = (%q, %d), want (%q, %d)", tt.desc, file, line, tt.wantFile, tt.wantLine)
		}
	}
}

func TestDeleteLineFromContent(t *testing.T) {
	t.Run("deletes the requested line, preserving trailing newline", func(t *testing.T) {
		content := "a\nb\nc\n"
		got, err := deleteLineFromContent(content, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "a\nc\n" {
			t.Fatalf("got %q, want %q", got, "a\nc\n")
		}
	})

	t.Run("no trailing newline is preserved", func(t *testing.T) {
		content := "a\nb\nc"
		got, err := deleteLineFromContent(content, 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "b\nc" {
			t.Fatalf("got %q, want %q", got, "b\nc")
		}
	})

	t.Run("out of range line errors", func(t *testing.T) {
		if _, err := deleteLineFromContent("a\nb\n", 10); err == nil {
			t.Fatal("expected an error for an out-of-range line")
		}
	})
}

func TestBuildMutationTestCommand(t *testing.T) {
	tests := []struct {
		target string
		want   string
	}{
		{"TestFoo", "go test -run '^TestFoo$' ./..."},
		{"./internal/foo/...", "go test ./internal/foo/..."},
		{"some freeform outcome text", "go test ./..."},
	}
	for _, tt := range tests {
		if got := buildMutationTestCommand(tt.target); got != tt.want {
			t.Errorf("buildMutationTestCommand(%q) = %q, want %q", tt.target, got, tt.want)
		}
	}
}
