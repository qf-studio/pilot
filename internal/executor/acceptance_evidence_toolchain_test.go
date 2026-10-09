package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// seedToolchainMarker creates an empty marker file (go.mod, package.json, …)
// in dir so detectMutationToolchain recognises the worktree's toolchain.
func seedToolchainMarker(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("failed to seed %s: %v", name, err)
	}
}

func TestDetectMutationToolchain(t *testing.T) {
	tests := []struct {
		name    string
		markers []string
		want    string
	}{
		{"empty dir", nil, "unknown"},
		{"go.mod", []string{"go.mod"}, "go"},
		{"package.json", []string{"package.json"}, "vitest"},
		{"pyproject.toml", []string{"pyproject.toml"}, "pytest"},
		{"pytest.ini", []string{"pytest.ini"}, "pytest"},
		{"Cargo.toml", []string{"Cargo.toml"}, "cargo"},
		{"go.mod wins over package.json", []string{"package.json", "go.mod"}, "go"},
		{"go.mod wins over everything", []string{"Cargo.toml", "pyproject.toml", "package.json", "go.mod"}, "go"},
		{"package.json wins over python and cargo", []string{"Cargo.toml", "pyproject.toml", "package.json"}, "vitest"},
		{"unrelated files only", []string{"README.md"}, "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, m := range tt.markers {
				seedToolchainMarker(t, dir, m)
			}
			if got := detectMutationToolchain(dir); got != tt.want {
				t.Errorf("detectMutationToolchain() = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("a directory named go.mod is not a marker", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "go.mod"), 0o750); err != nil {
			t.Fatal(err)
		}
		if got := detectMutationToolchain(dir); got != "unknown" {
			t.Errorf("got %q, want unknown", got)
		}
	})
}

func TestDetectJSRunnerPrefix(t *testing.T) {
	tests := []struct {
		name     string
		lockfile string
		want     string
	}{
		{"no lockfile", "", "npx"},
		{"bun.lock", "bun.lock", "bunx"},
		{"bun.lockb", "bun.lockb", "bunx"},
		{"pnpm-lock.yaml", "pnpm-lock.yaml", "pnpm exec"},
		{"package-lock.json", "package-lock.json", "npx"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.lockfile != "" {
				seedToolchainMarker(t, dir, tt.lockfile)
			}
			if got := detectJSRunnerPrefix(dir); got != tt.want {
				t.Errorf("detectJSRunnerPrefix() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildMutationTestCommand(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		toolchain string
		prefix    string
		want      string
	}{
		{"go test name", "TestFoo", "go", "", "go test -run '^TestFoo$' ./..."},
		{"go package path", "./internal/foo/...", "go", "", "go test ./internal/foo/..."},
		{"go fallback", "the suite", "go", "", "go test ./..."},

		{"vitest spec path", "src/stores/chat.spec.ts fails", "vitest", "bunx", "bunx vitest run src/stores/chat.spec.ts"},
		{"vitest test path", "src/x.test.tsx", "vitest", "npx", "npx vitest run src/x.test.tsx"},
		{"vitest pnpm prefix", "src/x.spec.ts", "vitest", "pnpm exec", "pnpm exec vitest run src/x.spec.ts"},
		{"vitest empty prefix defaults to npx", "src/x.spec.ts", "vitest", "", "npx vitest run src/x.spec.ts"},
		{"vitest quoted title", `"resets the cursor" fails`, "vitest", "bunx", "bunx vitest run -t 'resets the cursor'"},
		{"vitest backtick title", "`resets the cursor` fails", "vitest", "npx", "npx vitest run -t 'resets the cursor'"},
		{"vitest title with shell operator falls back", `"a; b" fails`, "vitest", "npx", "npx vitest run"},
		{"vitest fallback", "the GH-217 specs fail", "vitest", "npx", "npx vitest run"},

		{"pytest function name", "test_reset_clears_cursor fails", "pytest", "", "pytest -k 'test_reset_clears_cursor'"},
		{"pytest class name", "TestReset fails", "pytest", "", "pytest -k 'TestReset'"},
		{"pytest path", "tests/test_chat.py fails", "pytest", "", "pytest tests/test_chat.py"},
		{"pytest fallback", "the suite fails", "pytest", "", "pytest"},

		{"cargo test name", "test_reset fails", "cargo", "", "cargo test test_reset"},
		{"cargo module path", "chat::tests::reset fails", "cargo", "", "cargo test chat::tests::reset"},
		{"cargo fallback", "the suite fails", "cargo", "", "cargo test"},

		{"unknown toolchain", "TestFoo", "unknown", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildMutationTestCommand(tt.target, tt.toolchain, tt.prefix)
			if got != tt.want {
				t.Fatalf("buildMutationTestCommand(%q, %q, %q) = %q, want %q", tt.target, tt.toolchain, tt.prefix, got, tt.want)
			}
			if got != "" {
				if reason := validateEvidenceCommand(got, defaultTestAllowedCommands); reason != "" {
					t.Errorf("command %q rejected by validateEvidenceCommand: %s", got, reason)
				}
				if _, err := splitCommandFields(got); err != nil {
					t.Errorf("command %q not splittable: %v", got, err)
				}
			}
		})
	}

	t.Run("vitest title stays one argv field", func(t *testing.T) {
		fields, err := splitCommandFields(buildMutationTestCommand(`"resets the cursor"`, "vitest", "bunx"))
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"bunx", "vitest", "run", "-t", "resets the cursor"}
		if strings.Join(fields, "|") != strings.Join(want, "|") {
			t.Errorf("fields = %q, want %q", fields, want)
		}
	})
}

func TestRunMutationItem_Toolchains(t *testing.T) {
	t.Run("package.json + bun.lock runs bunx vitest and reverts byte-identical", func(t *testing.T) {
		dir := t.TempDir()
		seedToolchainMarker(t, dir, "package.json")
		seedToolchainMarker(t, dir, "bun.lock")
		if err := os.MkdirAll(filepath.Join(dir, "src", "stores"), 0o750); err != nil {
			t.Fatal(err)
		}
		original := "let generation = 0\nexport function reset() {\n  generation++\n}\n"
		target := filepath.Join(dir, "src", "stores", "chat.ts")
		if err := os.WriteFile(target, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}

		runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
			"bunx vitest run src/x.spec.ts": {
				output: " × reset clears the cursor 12ms\n FAIL  src/x.spec.ts > reset clears the cursor\n",
				err:    &exec.ExitError{},
			},
		}}
		item := ClassifyAcceptanceItem("delete line 3 in src/stores/chat.ts -> src/x.spec.ts fails")
		result := runMutationItem(context.Background(), runner, dir, item, defaultTestAllowedCommands)

		if result.NotVerifiedReason != "" {
			t.Fatalf("unexpected NotVerifiedReason: %s", result.NotVerifiedReason)
		}
		if result.MutationOutcome == nil {
			t.Fatal("expected a MutationOutcome")
		}
		if result.MutationOutcome.NoTestFailed {
			t.Error("vitest run exited non-zero with a × line: NoTestFailed must be false")
		}
		if got := strings.Join(result.MutationOutcome.FailingTests, "|"); got != "reset clears the cursor|src/x.spec.ts > reset clears the cursor" {
			t.Errorf("FailingTests = %q, want the parsed vitest failures (not the unparsed fallback)", got)
		}
		if len(runner.calls) != 1 || runner.calls[0] != "bunx vitest run src/x.spec.ts" {
			t.Fatalf("runner calls = %q, want exactly [bunx vitest run src/x.spec.ts]", runner.calls)
		}
		for _, c := range runner.calls {
			if strings.HasPrefix(c, "go ") {
				t.Errorf("JS repo must not build a go test command, got %q", c)
			}
		}
		got, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != original {
			t.Fatalf("file not reverted byte-identical: got %q, want %q", got, original)
		}
	})

	t.Run("non-zero exit with unparseable output is a kill, not no-test-failed", func(t *testing.T) {
		dir := t.TempDir()
		seedToolchainMarker(t, dir, "package.json")
		original := "a\nb\nc\n"
		if err := os.WriteFile(filepath.Join(dir, "f.ts"), []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
			"npx vitest run src/x.spec.ts": {output: "something exploded\n", err: &exec.ExitError{}},
		}}
		item := ClassifyAcceptanceItem("delete line 2 in f.ts -> src/x.spec.ts fails")
		result := runMutationItem(context.Background(), runner, dir, item, defaultTestAllowedCommands)

		mo := result.MutationOutcome
		if mo == nil {
			t.Fatalf("expected a MutationOutcome, NotVerifiedReason=%q", result.NotVerifiedReason)
		}
		if mo.NoTestFailed {
			t.Error("non-zero exit must not be reported as NoTestFailed")
		}
		if len(mo.FailingTests) != 1 || mo.FailingTests[0] != unparsedFailingTest {
			t.Errorf("FailingTests = %q, want [%s]", mo.FailingTests, unparsedFailingTest)
		}
		if !strings.Contains(mo.Output, "something exploded") {
			t.Errorf("Output should carry the real run, got %q", mo.Output)
		}
	})

	t.Run("exit 0 with no fail lines is NoTestFailed", func(t *testing.T) {
		dir := t.TempDir()
		seedToolchainMarker(t, dir, "package.json")
		if err := os.WriteFile(filepath.Join(dir, "f.ts"), []byte("a\nb\nc\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
			"npx vitest run src/x.spec.ts": {output: " ✓ all good 3ms\n"},
		}}
		item := ClassifyAcceptanceItem("delete line 2 in f.ts -> src/x.spec.ts fails")
		result := runMutationItem(context.Background(), runner, dir, item, defaultTestAllowedCommands)

		mo := result.MutationOutcome
		if mo == nil {
			t.Fatalf("expected a MutationOutcome, NotVerifiedReason=%q", result.NotVerifiedReason)
		}
		if !mo.NoTestFailed || len(mo.FailingTests) != 0 {
			t.Errorf("NoTestFailed=%v FailingTests=%q, want true/empty", mo.NoTestFailed, mo.FailingTests)
		}
	})

	t.Run("no supported toolchain is Not-verified and leaves the file untouched", func(t *testing.T) {
		dir := t.TempDir()
		original := "a\nb\nc\n"
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		runner := &fakeAcceptanceCommandRunner{}
		item := ClassifyAcceptanceItem("delete line 2 in f.txt -> TestFoo fails")
		result := runMutationItem(context.Background(), runner, dir, item, defaultTestAllowedCommands)

		if result.NotVerifiedReason != "no supported test toolchain detected in worktree" {
			t.Fatalf("NotVerifiedReason = %q", result.NotVerifiedReason)
		}
		if len(runner.calls) != 0 {
			t.Errorf("no command should run, got %q", runner.calls)
		}
		got, err := os.ReadFile(filepath.Join(dir, "f.txt"))
		if err != nil || string(got) != original {
			t.Fatalf("file modified: err=%v content=%q", err, got)
		}
	})
}

func TestExtractFailingTests(t *testing.T) {
	tests := []struct {
		name      string
		toolchain string
		output    string
		want      []string
	}{
		{"go", "go", "=== RUN   TestFoo\n--- FAIL: TestFoo (0.00s)\n    --- FAIL: TestFoo/sub (0.00s)\n--- FAIL: TestFoo (0.00s)\nFAIL\n", []string{"TestFoo", "TestFoo/sub"}},
		{"go no failure", "go", "ok  \tpkg\t0.01s\n", []string{}},
		{
			"vitest x line and FAIL summary",
			"vitest",
			" ❯ src/x.spec.ts (2 tests | 1 failed) 15ms\n   ✓ keeps cursor 2ms\n   × reset clears the cursor 12ms\n\n FAIL  src/x.spec.ts > reset clears the cursor\nAssertionError: expected 0 to be 1\n Test Files  1 failed (1)\n",
			[]string{"reset clears the cursor", "src/x.spec.ts > reset clears the cursor"},
		},
		{"vitest x line nested title", "vitest", "   × chat store > reset > clears the cursor 12ms\n", []string{"chat store > reset > clears the cursor"}},
		{"vitest dedupes", "vitest", " × a 1ms\n × a 2ms\n", []string{"a"}},
		{"vitest passing", "vitest", " ✓ a 1ms\n Test Files  1 passed (1)\n", []string{}},
		{
			"pytest",
			"pytest",
			"=== short test summary info ===\nFAILED tests/test_x.py::test_y - AssertionError: assert 0 == 1\nFAILED tests/test_x.py::test_z\n=== 2 failed in 0.12s ===\n",
			[]string{"tests/test_x.py::test_y", "tests/test_x.py::test_z"},
		},
		{"pytest passing", "pytest", "=== 3 passed in 0.01s ===\n", []string{}},
		{
			"cargo",
			"cargo",
			"running 2 tests\ntest chat::tests::ok ... ok\ntest chat::tests::reset ... FAILED\n\nfailures:\n    chat::tests::reset\n",
			[]string{"chat::tests::reset"},
		},
		{"cargo passing", "cargo", "test chat::tests::ok ... ok\n", []string{}},
		{"unknown", "unknown", "--- FAIL: TestFoo\nFAILED a::b\n × x 1ms\n", []string{}},
		{"go pattern is not applied to pytest", "pytest", "--- FAIL: TestFoo\n", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFailingTests(tt.output, tt.toolchain)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("extractFailingTests(%q) = %q, want %q", tt.toolchain, got, tt.want)
			}
		})
	}
}

func TestRenderAcceptanceEvidenceSections_VitestKillNotVacuous(t *testing.T) {
	dir := t.TempDir()
	seedToolchainMarker(t, dir, "package.json")
	if err := os.WriteFile(filepath.Join(dir, "f.ts"), []byte("a\nb\nc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
		"npx vitest run src/x.spec.ts": {output: " × reset clears the cursor 12ms\n", err: &exec.ExitError{}},
	}}
	item := ClassifyAcceptanceItem("delete line 2 in f.ts -> src/x.spec.ts fails")
	result := runMutationItem(context.Background(), runner, dir, item, defaultTestAllowedCommands)

	out := RenderAcceptanceEvidenceSections([]AcceptanceEvidenceResult{result})
	if !strings.Contains(out, "## Evidence") {
		t.Fatalf("missing ## Evidence:\n%s", out)
	}
	if strings.Contains(out, "no test failed") {
		t.Errorf("vitest kill rendered as vacuous:\n%s", out)
	}
	if !strings.Contains(out, "failing test(s): reset clears the cursor") {
		t.Errorf("failing test not listed:\n%s", out)
	}
}
