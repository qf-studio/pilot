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
				output: "FAIL src/x.spec.ts > reset\n",
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
