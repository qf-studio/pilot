package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeAcceptanceCommandRunner is a scripted AcceptanceCommandRunner double
// for unit-testing the paste-output and mutation harnesses without a real
// shell. Commands are matched by exact string; unmatched commands return an
// error.
type fakeAcceptanceCommandRunner struct {
	responses map[string]fakeCommandResponse
	calls     []string
}

type fakeCommandResponse struct {
	output string
	err    error
}

func (f *fakeAcceptanceCommandRunner) RunCommand(_ context.Context, _ string, command string) (string, error) {
	f.calls = append(f.calls, command)
	resp, ok := f.responses[command]
	if !ok {
		return "", errors.New("fakeAcceptanceCommandRunner: no scripted response for " + command)
	}
	return resp.output, resp.err
}

var defaultTestAllowedCommands = DefaultAcceptanceEvidenceAllowedCommands()

func TestRunPasteOutputItem(t *testing.T) {
	t.Run("captures output of every parsed command", func(t *testing.T) {
		runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
			"go test -run TestX ./pkg/": {output: "--- FAIL: TestX\nFAIL"},
		}}
		item := ClassifyAcceptanceItem("paste the output of `go test -run TestX ./pkg/`")
		result := runPasteOutputItem(context.Background(), runner, "/tmp/whatever", item, defaultTestAllowedCommands)

		if result.NotVerifiedReason != "" {
			t.Fatalf("unexpected NotVerifiedReason: %s", result.NotVerifiedReason)
		}
		if len(result.Commands) != 1 {
			t.Fatalf("expected 1 command result, got %d", len(result.Commands))
		}
		if result.Commands[0].Command != "go test -run TestX ./pkg/" {
			t.Errorf("unexpected command: %q", result.Commands[0].Command)
		}
		if !strings.Contains(result.Commands[0].Output, "FAIL") {
			t.Errorf("expected output to contain FAIL, got %q", result.Commands[0].Output)
		}
	})

	t.Run("a command's non-zero exit is captured as output, not an error", func(t *testing.T) {
		runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
			"go run ./false": {output: "", err: &exec.ExitError{}},
		}}
		item := ClassifyAcceptanceItem("paste the output of `go run ./false`")
		result := runPasteOutputItem(context.Background(), runner, "/tmp/whatever", item, defaultTestAllowedCommands)
		if result.NotVerifiedReason != "" {
			t.Fatalf("a non-zero exit should not be Not-verified, got reason: %s", result.NotVerifiedReason)
		}
		if result.Commands[0].Err != "" {
			t.Errorf("expected no Err for an ExitError, got %q", result.Commands[0].Err)
		}
	})

	t.Run("zero parsed commands is Not-verified", func(t *testing.T) {
		item := ClassifyAcceptanceItem("paste the output of the failing command")
		result := runPasteOutputItem(context.Background(), &fakeAcceptanceCommandRunner{}, "/tmp/whatever", item, defaultTestAllowedCommands)
		if result.NotVerifiedReason == "" {
			t.Fatal("expected a NotVerifiedReason when no commands were parsed")
		}
	})

	t.Run("a real execution error is Not-verified", func(t *testing.T) {
		runner := &fakeAcceptanceCommandRunner{} // no scripted response -> execution error
		item := ClassifyAcceptanceItem("paste the output of `go nonexistent-subcommand`")
		result := runPasteOutputItem(context.Background(), runner, "/tmp/whatever", item, defaultTestAllowedCommands)
		if result.NotVerifiedReason == "" {
			t.Fatal("expected a NotVerifiedReason when the command could not execute at all")
		}
	})

	// GH-5437: the security-critical case. PR #5436 ran any command
	// verbatim via sh -c with the daemon's full environment. A command
	// whose first token isn't in the allowlist must never reach the
	// injected runner at all — not just be reported as failed.
	t.Run("a non-allowlisted command is never spawned and is reported Not-verified", func(t *testing.T) {
		runner := &fakeAcceptanceCommandRunner{}
		item := ClassifyAcceptanceItem("paste the output of `env`")
		result := runPasteOutputItem(context.Background(), runner, "/tmp/whatever", item, defaultTestAllowedCommands)

		if len(runner.calls) != 0 {
			t.Fatalf("expected zero calls to the command runner for a disallowed command, got %v", runner.calls)
		}
		if result.NotVerifiedReason != "command not in allowlist" {
			t.Fatalf("NotVerifiedReason = %q, want %q", result.NotVerifiedReason, "command not in allowlist")
		}
	})

	t.Run("a disallowed command in a multi-command item blocks the whole item, zero calls", func(t *testing.T) {
		runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
			"go test ./...": {output: "ok"},
		}}
		item := ClassifyAcceptanceItem("paste the output of `go test ./...` and `cat ~/.pilot/config.yaml`")
		result := runPasteOutputItem(context.Background(), runner, "/tmp/whatever", item, defaultTestAllowedCommands)

		if len(runner.calls) != 0 {
			t.Fatalf("expected zero calls when any command in the item is disallowed, got %v", runner.calls)
		}
		if result.NotVerifiedReason != "command not in allowlist" {
			t.Fatalf("NotVerifiedReason = %q, want %q", result.NotVerifiedReason, "command not in allowlist")
		}
	})

	t.Run("an allowlisted command runs normally", func(t *testing.T) {
		runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
			"make test": {output: "ok"},
		}}
		item := ClassifyAcceptanceItem("paste the output of `make test`")
		result := runPasteOutputItem(context.Background(), runner, "/tmp/whatever", item, defaultTestAllowedCommands)
		if len(runner.calls) != 1 {
			t.Fatalf("expected exactly 1 call, got %v", runner.calls)
		}
		if result.NotVerifiedReason != "" {
			t.Fatalf("unexpected NotVerifiedReason: %s", result.NotVerifiedReason)
		}
	})
}

// GH-5442: the allowlist previously checked only the first whitespace-
// delimited token, and the runner handed the whole line to `sh -c` — so an
// allowlisted first token with a shell operator anywhere else in the line
// ran through a real shell. These three commands (from the GH-5442 issue,
// verified on main to pass the pre-fix allowlist) must now be refused with
// the shell-operator reason and never reach the command runner at all.
func TestRunPasteOutputItem_ShellOperatorRefusal(t *testing.T) {
	cases := []struct {
		name    string
		command string
	}{
		{"command sequencing with &&", "go test ./... && echo second"},
		{"command substitution with $(...)", "make $(echo build)"},
		{"pipe to another command", "npm test | tee out.txt"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeAcceptanceCommandRunner{}
			item := ClassifyAcceptanceItem("paste the output of `" + tc.command + "`")
			result := runPasteOutputItem(context.Background(), runner, "/tmp/whatever", item, defaultTestAllowedCommands)

			if len(runner.calls) != 0 {
				t.Fatalf("expected zero calls to the command runner, got %v", runner.calls)
			}
			if result.NotVerifiedReason != shellOperatorNotVerifiedReason {
				t.Fatalf("NotVerifiedReason = %q, want %q", result.NotVerifiedReason, shellOperatorNotVerifiedReason)
			}
		})
	}

	t.Run("allowed commands with plain arguments still run", func(t *testing.T) {
		plain := []string{
			"go test ./internal/executor/ -run TestX -v",
			"make check-secrets",
			"bun run test",
		}
		for _, command := range plain {
			runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
				command: {output: "ok"},
			}}
			item := ClassifyAcceptanceItem("paste the output of `" + command + "`")
			result := runPasteOutputItem(context.Background(), runner, "/tmp/whatever", item, defaultTestAllowedCommands)
			if result.NotVerifiedReason != "" {
				t.Fatalf("command %q: unexpected NotVerifiedReason: %s", command, result.NotVerifiedReason)
			}
			if len(runner.calls) != 1 {
				t.Fatalf("command %q: expected exactly 1 call, got %v", command, runner.calls)
			}
		}
	})
}

// --- Shell-operator detection and argv splitting ---

func TestHasDisallowedShellOperators(t *testing.T) {
	cases := []struct {
		command string
		want    bool
	}{
		{"go test ./...", false},
		{"go test ./internal/executor/ -run TestX -v", false},
		{"make check-secrets", false},
		{"bun run test", false},
		{"go test -run '^TestMain$' ./...", false},
		{"go test ./... && echo second", true},
		{"make $(echo build)", true},
		{"npm test | tee out.txt", true},
		{"go test ./...; rm -rf /", true},
		{"go test `whoami`", true},
		{"make ${HOME}", true},
		{"go test ./... > out.txt", true},
		{"go test ./... < input.txt", true},
		{"go test ./...\nrm -rf /", true},
		{"# a comment", true},
		{"go test ./... # trailing comment", true},
		{"go test ./...#not-a-comment", false}, // no preceding whitespace
	}
	for _, c := range cases {
		if got := hasDisallowedShellOperators(c.command); got != c.want {
			t.Errorf("hasDisallowedShellOperators(%q) = %v, want %v", c.command, got, c.want)
		}
	}
}

func TestSplitCommandFields(t *testing.T) {
	cases := []struct {
		command string
		want    []string
	}{
		{"go test ./...", []string{"go", "test", "./..."}},
		{"go test -run '^TestMain$' ./...", []string{"go", "test", "-run", "^TestMain$", "./..."}},
		{`go test -run "^TestMain$" ./...`, []string{"go", "test", "-run", "^TestMain$", "./..."}},
		{"  make   check-secrets  ", []string{"make", "check-secrets"}},
		{"", nil},
	}
	for _, c := range cases {
		got, err := splitCommandFields(c.command)
		if err != nil {
			t.Fatalf("splitCommandFields(%q) unexpected error: %v", c.command, err)
		}
		if len(got) != len(c.want) {
			t.Fatalf("splitCommandFields(%q) = %v, want %v", c.command, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitCommandFields(%q)[%d] = %q, want %q", c.command, i, got[i], c.want[i])
			}
		}
	}

	t.Run("unterminated quote is an error", func(t *testing.T) {
		if _, err := splitCommandFields("go test 'unterminated"); err == nil {
			t.Fatal("expected an error for an unterminated quote")
		}
	})
}

func TestRunMutationItem_DeleteLineMutation(t *testing.T) {
	dir := t.TempDir()
	targetFile := "main.go"
	original := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	if err := os.WriteFile(filepath.Join(dir, targetFile), []byte(original), 0o600); err != nil {
		t.Fatalf("failed to seed test file: %v", err)
	}

	t.Run("failing mutation records the failing test name and reverts the file", func(t *testing.T) {
		runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
			"go test -run '^TestMain$' ./...": {
				output: "--- FAIL: TestMain (0.00s)\nFAIL",
				err:    &exec.ExitError{},
			},
		}}
		item := ClassifyAcceptanceItem("delete line 4 in main.go -> TestMain fails")
		result := runMutationItem(context.Background(), runner, dir, item, defaultTestAllowedCommands)

		if result.NotVerifiedReason != "" {
			t.Fatalf("unexpected NotVerifiedReason: %s", result.NotVerifiedReason)
		}
		if result.MutationOutcome == nil {
			t.Fatal("expected a MutationOutcome")
		}
		if result.MutationOutcome.NoTestFailed {
			t.Error("expected NoTestFailed=false since a test failed")
		}
		if len(result.MutationOutcome.FailingTests) != 1 || result.MutationOutcome.FailingTests[0] != "TestMain" {
			t.Errorf("expected failing test TestMain, got %v", result.MutationOutcome.FailingTests)
		}

		if len(runner.calls) != 1 {
			t.Fatalf("expected exactly 1 command invocation, got %d: %v", len(runner.calls), runner.calls)
		}
		got, err := os.ReadFile(filepath.Join(dir, targetFile))
		if err != nil {
			t.Fatalf("failed to read file after revert: %v", err)
		}
		if string(got) != original {
			t.Fatalf("file was not reverted to its original content: got %q, want %q", got, original)
		}
	})

	t.Run("a mutation that fails no test is reported, not omitted", func(t *testing.T) {
		runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
			"go test -run '^TestMain$' ./...": {output: "ok  \tpkg\t0.01s"},
		}}
		item := ClassifyAcceptanceItem("delete line 4 in main.go -> TestMain fails")
		result := runMutationItem(context.Background(), runner, dir, item, defaultTestAllowedCommands)

		if result.NotVerifiedReason != "" {
			t.Fatalf("unexpected NotVerifiedReason: %s", result.NotVerifiedReason)
		}
		if !result.MutationOutcome.NoTestFailed {
			t.Error("expected NoTestFailed=true, this is a real finding that must be reported")
		}
		if len(result.MutationOutcome.FailingTests) != 0 {
			t.Errorf("expected no failing tests, got %v", result.MutationOutcome.FailingTests)
		}

		got, err := os.ReadFile(filepath.Join(dir, targetFile))
		if err != nil {
			t.Fatalf("failed to read file after revert: %v", err)
		}
		if string(got) != original {
			t.Fatalf("file was not reverted to its original content: got %q, want %q", got, original)
		}
	})

	t.Run("unparseable freeform mutation is Not-verified and leaves the worktree untouched", func(t *testing.T) {
		item := ClassifyAcceptanceItem("change the retry backoff to exponential -> TestBackoff fails")
		result := runMutationItem(context.Background(), &fakeAcceptanceCommandRunner{}, dir, item, defaultTestAllowedCommands)
		if result.NotVerifiedReason == "" {
			t.Fatal("expected a NotVerifiedReason for a freeform mutation description")
		}
		got, err := os.ReadFile(filepath.Join(dir, targetFile))
		if err != nil {
			t.Fatalf("failed to read file: %v", err)
		}
		if string(got) != original {
			t.Fatal("worktree file should not have been touched for an unparseable mutation")
		}
	})

	t.Run("mutation naming a nonexistent file is Not-verified", func(t *testing.T) {
		item := ClassifyAcceptanceItem("delete line 1 in does-not-exist.go -> TestFoo fails")
		result := runMutationItem(context.Background(), &fakeAcceptanceCommandRunner{}, dir, item, defaultTestAllowedCommands)
		if result.NotVerifiedReason == "" {
			t.Fatal("expected a NotVerifiedReason for a missing file")
		}
		if result.NotVerifiedReason == "path escapes worktree" {
			t.Fatal("a missing file is a read failure, not an escape attempt")
		}
	})
}

// GH-5437: worktree confinement. PR #5436 joined the issue-supplied
// relative path directly onto the worktree dir and wrote to it — a
// relative "..", an absolute path, or a symlink inside the worktree
// pointing outside it all escaped. Every vector must be refused with the
// tree left untouched.
func TestRunMutationItem_WorktreeConfinement(t *testing.T) {
	worktree := t.TempDir()
	outside := t.TempDir()

	seed := func(dir, name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("failed to seed %s: %v", p, err)
		}
		return p
	}

	outsideContent := "package outside\n\nfunc Outside() {}\n"
	outsideFile := seed(outside, "secret.go", outsideContent)

	t.Run("relative .. escape is refused", func(t *testing.T) {
		item := ClassifyAcceptanceItem("delete line 1 in ../" + filepath.Base(outside) + "/secret.go -> TestFoo fails")
		result := runMutationItem(context.Background(), &fakeAcceptanceCommandRunner{}, worktree, item, defaultTestAllowedCommands)
		if result.NotVerifiedReason != "path escapes worktree" {
			t.Fatalf("NotVerifiedReason = %q, want %q", result.NotVerifiedReason, "path escapes worktree")
		}
		if got, err := os.ReadFile(outsideFile); err != nil || string(got) != outsideContent {
			t.Fatalf("outside file was modified: err=%v content=%q", err, got)
		}
	})

	t.Run("absolute path escape is refused", func(t *testing.T) {
		item := ClassifyAcceptanceItem("delete line 1 in " + outsideFile + " -> TestFoo fails")
		result := runMutationItem(context.Background(), &fakeAcceptanceCommandRunner{}, worktree, item, defaultTestAllowedCommands)
		if result.NotVerifiedReason != "path escapes worktree" {
			t.Fatalf("NotVerifiedReason = %q, want %q", result.NotVerifiedReason, "path escapes worktree")
		}
		if got, err := os.ReadFile(outsideFile); err != nil || string(got) != outsideContent {
			t.Fatalf("outside file was modified: err=%v content=%q", err, got)
		}
	})

	t.Run("a symlink inside the worktree pointing outside is refused", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation requires elevated privileges on windows")
		}
		linkPath := filepath.Join(worktree, "link.go")
		if err := os.Symlink(outsideFile, linkPath); err != nil {
			t.Fatalf("failed to create symlink: %v", err)
		}
		item := ClassifyAcceptanceItem("delete line 1 in link.go -> TestFoo fails")
		result := runMutationItem(context.Background(), &fakeAcceptanceCommandRunner{}, worktree, item, defaultTestAllowedCommands)
		if result.NotVerifiedReason != "path escapes worktree" {
			t.Fatalf("NotVerifiedReason = %q, want %q", result.NotVerifiedReason, "path escapes worktree")
		}
		if got, err := os.ReadFile(outsideFile); err != nil || string(got) != outsideContent {
			t.Fatalf("outside file (via symlink) was modified: err=%v content=%q", err, got)
		}
	})

	t.Run("a symlink inside the worktree pointing to another file inside it is allowed", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation requires elevated privileges on windows")
		}
		realContent := "package main\n\nfunc main() {\n\tprintln(\"ok\")\n}\n"
		realFile := seed(worktree, "real.go", realContent)
		linkPath := filepath.Join(worktree, "insidelink.go")
		if err := os.Symlink(realFile, linkPath); err != nil {
			t.Fatalf("failed to create symlink: %v", err)
		}
		runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
			"go test -run '^TestFoo$' ./...": {output: "ok"},
		}}
		item := ClassifyAcceptanceItem("delete line 1 in insidelink.go -> TestFoo fails")
		result := runMutationItem(context.Background(), runner, worktree, item, defaultTestAllowedCommands)
		if result.NotVerifiedReason != "" {
			t.Fatalf("unexpected NotVerifiedReason for an in-worktree symlink: %s", result.NotVerifiedReason)
		}
	})
}

func TestRunAcceptanceEvidence_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatalf("failed to seed test file: %v", err)
	}

	runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
		"go test -run TestX ./pkg/":       {output: "ok\tpkg\t0.01s"},
		"go test -run '^TestMain$' ./...": {output: "--- FAIL: TestMain\nFAIL", err: &exec.ExitError{}},
	}}

	criteria := []string{
		"paste the output of `go test -run TestX ./pkg/`",
		"delete line 3 in main.go -> TestMain fails",
		"a plain acceptance item with no evidence requirement",
	}

	results := RunAcceptanceEvidence(context.Background(), runner, dir, criteria, defaultTestAllowedCommands)
	if len(results) != 2 {
		t.Fatalf("expected 2 evidence-requiring results (paste + mutation), got %d", len(results))
	}
	if results[0].Item.Kind != AcceptanceItemPasteOutput {
		t.Errorf("expected first result to be paste-output, got %v", results[0].Item.Kind)
	}
	if results[1].Item.Kind != AcceptanceItemMutation {
		t.Errorf("expected second result to be mutation, got %v", results[1].Item.Kind)
	}

	section := RenderAcceptanceEvidenceSections(results)
	if !strings.Contains(section, "## Evidence") {
		t.Errorf("expected an Evidence section, got %q", section)
	}
}

// --- Command allowlist ---

func TestIsCommandAllowed(t *testing.T) {
	allowed := []string{"go", "make", "npm"}
	cases := []struct {
		command string
		want    bool
	}{
		{"go test ./...", true},
		{"make test", true},
		{"npm run test", true},
		{"env", false},
		{"cat ~/.pilot/config.yaml", false},
		{"  ", false},
		{"", false},
		{"gorm-migrate", false}, // must be an exact token match, not a prefix
	}
	for _, c := range cases {
		if got := isCommandAllowed(c.command, allowed); got != c.want {
			t.Errorf("isCommandAllowed(%q) = %v, want %v", c.command, got, c.want)
		}
	}
}

func TestDefaultAcceptanceEvidenceAllowedCommands(t *testing.T) {
	allowed := DefaultAcceptanceEvidenceAllowedCommands()
	want := []string{"go", "make", "npm", "npx", "bun", "bunx", "pnpm", "yarn", "pytest", "cargo"}
	if len(allowed) != len(want) {
		t.Fatalf("got %v, want %v", allowed, want)
	}
	for i, w := range want {
		if allowed[i] != w {
			t.Errorf("allowed[%d] = %q, want %q", i, allowed[i], w)
		}
	}
}

// --- Sanitised environment ---

func TestSanitizedAcceptanceEnv(t *testing.T) {
	// Set a representative "secret-named" var alongside an allowlisted one,
	// mirroring the daemon's real environment.
	t.Setenv("GITHUB_TOKEN", "should-not-leak-value")
	t.Setenv("PATH", os.Getenv("PATH"))
	t.Setenv("LIN_API_KEY", "also-should-not-leak")

	env := sanitizedAcceptanceEnv()

	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		allowedName := false
		for _, a := range acceptanceEvidenceEnvAllowlist {
			if name == a {
				allowedName = true
				break
			}
		}
		if !allowedName {
			t.Errorf("sanitizedAcceptanceEnv() included non-allowlisted var %q", name)
		}
	}

	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "should-not-leak-value") || strings.Contains(joined, "also-should-not-leak") {
		t.Fatalf("sanitizedAcceptanceEnv() leaked a secret-named var's value: %v", env)
	}
	if !strings.Contains(joined, "PATH=") {
		t.Errorf("expected PATH to be forwarded, got %v", env)
	}
}

// GH-5437 acceptance: "an allowlisted command runs with the sanitised
// environment only (test asserts the spawned env has none of the parent's
// secret-named vars)". This exercises the real shellAcceptanceCommandRunner
// end-to-end (not the fake double) so the assertion is about what actually
// gets spawned, not just the helper function in isolation.
func TestShellAcceptanceCommandRunner_EnvSanitization(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on /bin/sh")
	}
	t.Setenv("GITHUB_TOKEN", "super-secret-parent-token-value")
	t.Setenv("PILOT_API_SECRET", "another-secret-parent-value")

	runner := shellAcceptanceCommandRunner{timeout: 5 * time.Second}
	out, err := runner.RunCommand(context.Background(), t.TempDir(), "env")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, "super-secret-parent-token-value") || strings.Contains(out, "another-secret-parent-value") {
		t.Fatalf("spawned command's environment leaked a parent secret: %q", out)
	}
	if strings.Contains(out, "GITHUB_TOKEN") || strings.Contains(out, "PILOT_API_SECRET") {
		t.Fatalf("spawned command's environment contained a parent secret-named var at all: %q", out)
	}
	if !strings.Contains(out, "PATH=") {
		t.Errorf("expected the sanitised env to still forward PATH, got %q", out)
	}
}

// --- Timeout ---

func TestShellAcceptanceCommandRunner_Timeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on /bin/sh")
	}
	runner := shellAcceptanceCommandRunner{timeout: 100 * time.Millisecond}
	start := time.Now()
	_, err := runner.RunCommand(context.Background(), t.TempDir(), "sleep 30")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error")
	}
	var te *acceptanceCommandTimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("expected an *acceptanceCommandTimeoutError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "timed out after") {
		t.Errorf("expected error message to mention the timeout, got %q", err.Error())
	}
	if elapsed > 10*time.Second {
		t.Fatalf("expected the blocking command to be killed promptly at the timeout, took %s", elapsed)
	}
}

func TestRunPasteOutputItem_TimeoutIsNotVerified(t *testing.T) {
	runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
		"go test ./...": {err: &acceptanceCommandTimeoutError{timeout: 10 * time.Minute}},
	}}
	item := ClassifyAcceptanceItem("paste the output of `go test ./...`")
	result := runPasteOutputItem(context.Background(), runner, "/tmp/whatever", item, defaultTestAllowedCommands)
	if result.NotVerifiedReason != "timed out after 10m0s" {
		t.Fatalf("NotVerifiedReason = %q, want %q", result.NotVerifiedReason, "timed out after 10m0s")
	}
}

func TestRunMutationItem_TimeoutIsNotVerifiedAndReverts(t *testing.T) {
	dir := t.TempDir()
	original := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(original), 0o600); err != nil {
		t.Fatalf("failed to seed test file: %v", err)
	}
	runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
		"go test -run '^TestMain$' ./...": {err: &acceptanceCommandTimeoutError{timeout: 10 * time.Minute}},
	}}
	item := ClassifyAcceptanceItem("delete line 1 in main.go -> TestMain fails")
	result := runMutationItem(context.Background(), runner, dir, item, defaultTestAllowedCommands)

	if result.NotVerifiedReason != "timed out after 10m0s" {
		t.Fatalf("NotVerifiedReason = %q, want %q", result.NotVerifiedReason, "timed out after 10m0s")
	}
	got, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil || string(got) != original {
		t.Fatalf("file was not reverted after a timed-out mutation test run: err=%v content=%q", err, got)
	}
}

// --- Redaction ---

func TestRedactOutputForEvidence(t *testing.T) {
	t.Run("redacts a secret-named parent env var's value", func(t *testing.T) {
		t.Setenv("MY_SERVICE_TOKEN", "leaked-value-12345")
		out := redactOutputForEvidence("here is the config: token=leaked-value-12345 end")
		if strings.Contains(out, "leaked-value-12345") {
			t.Fatalf("expected the secret value to be redacted, got %q", out)
		}
		if !strings.Contains(out, "[REDACTED]") {
			t.Fatalf("expected a [REDACTED] marker, got %q", out)
		}
	})

	t.Run("redacts ghp_-shaped tokens", func(t *testing.T) {
		token := "ghp_" + strings.Repeat("a1B2c3", 6) // 36 chars
		out := redactOutputForEvidence("Authorization: Bearer " + token)
		if strings.Contains(out, token) {
			t.Fatalf("expected the ghp_ token to be redacted, got %q", out)
		}
		if !strings.Contains(out, "[REDACTED]") {
			t.Fatalf("expected a [REDACTED] marker, got %q", out)
		}
	})

	t.Run("redacts pdl_-shaped tokens", func(t *testing.T) {
		token := "pdl_live_abcdefghijklmnopqrstuvwxyz"
		out := redactOutputForEvidence("PADDLE_API_KEY=" + token)
		if strings.Contains(out, token) {
			t.Fatalf("expected the pdl_ token to be redacted, got %q", out)
		}
	})

	t.Run("does not redact short/trivial secret-named env values", func(t *testing.T) {
		t.Setenv("SOME_KEY", "1")
		out := redactOutputForEvidence("count: 1 items processed, key value is 1")
		if strings.Contains(out, "[REDACTED]") {
			t.Fatalf("a trivial 1-char env value should not trigger blanket redaction, got %q", out)
		}
	})

	t.Run("empty output is unchanged", func(t *testing.T) {
		if got := redactOutputForEvidence(""); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
}

// GH-5442: TestRedactOutputForEvidence above pins redactOutputForEvidence in
// isolation, but nothing asserted the *runner* actually applies it before
// output reaches the PR body — a refactor of finalizeEvidenceOutput
// (acceptance_evidence_run.go) that dropped the redactOutputForEvidence call
// left the whole executor suite green. These two tests go through the real
// pipeline — RunAcceptanceEvidence -> RenderAcceptanceEvidenceSections — with
// a fake runner returning secret-shaped output, and assert the rendered
// section contains [REDACTED] and neither raw value:
//   - skip redactOutputForEvidence in finalizeEvidenceOutput -> these fail
//     (the raw secret would appear in the rendered section)
//   - restore `sh -c` with the shell-operator check removed -> the sibling
//     TestRunPasteOutputItem_ShellOperatorRefusal fails instead
func TestRenderAcceptanceEvidenceSections_RedactsSecrets_PasteOutputItem(t *testing.T) {
	ghpToken := "ghp_" + strings.Repeat("a1B2c3", 6) // 36 chars, matches secretpatterns' ghp_ shape
	t.Setenv("PILOT_PARENT_SECRET", "leaked-parent-env-value")

	runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
		"go test ./...": {output: "Authorization: Bearer " + ghpToken + "\nparent secret: leaked-parent-env-value\nok"},
	}}
	criteria := []string{"paste the output of `go test ./...`"}

	results := RunAcceptanceEvidence(context.Background(), runner, "/tmp/whatever", criteria, defaultTestAllowedCommands)
	section := RenderAcceptanceEvidenceSections(results)

	if !strings.Contains(section, "[REDACTED]") {
		t.Fatalf("expected rendered section to contain [REDACTED], got %q", section)
	}
	if strings.Contains(section, ghpToken) {
		t.Fatalf("rendered section leaked the raw ghp_ token: %q", section)
	}
	if strings.Contains(section, "leaked-parent-env-value") {
		t.Fatalf("rendered section leaked the raw parent env secret value: %q", section)
	}
}

func TestRenderAcceptanceEvidenceSections_RedactsSecrets_MutationItem(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatalf("failed to seed test file: %v", err)
	}

	ghpToken := "ghp_" + strings.Repeat("d4E5f6", 6)
	t.Setenv("PILOT_PARENT_SECRET", "leaked-mutation-env-value")

	runner := &fakeAcceptanceCommandRunner{responses: map[string]fakeCommandResponse{
		"go test -run '^TestMain$' ./...": {
			output: "--- FAIL: TestMain (0.00s)\ntoken leaked: " + ghpToken + "\nparent secret: leaked-mutation-env-value\nFAIL",
			err:    &exec.ExitError{},
		},
	}}
	criteria := []string{"delete line 3 in main.go -> TestMain fails"}

	results := RunAcceptanceEvidence(context.Background(), runner, dir, criteria, defaultTestAllowedCommands)
	section := RenderAcceptanceEvidenceSections(results)

	if !strings.Contains(section, "[REDACTED]") {
		t.Fatalf("expected rendered section to contain [REDACTED], got %q", section)
	}
	if strings.Contains(section, ghpToken) {
		t.Fatalf("rendered section leaked the raw ghp_ token: %q", section)
	}
	if strings.Contains(section, "leaked-mutation-env-value") {
		t.Fatalf("rendered section leaked the raw parent env secret value: %q", section)
	}
}

// --- Flag-off byte-identical PR body ---

func TestAppendAcceptanceEvidence_FlagOff_ByteIdentical(t *testing.T) {
	prBody := "## Summary\n\nAutomated PR created by Pilot for task GH-1.\n\nCloses #1\n\n## Changes\n\nsome description"
	task := &Task{ID: "GH-1", AcceptanceCriteria: []string{"paste the output of `go test ./...`"}}

	disabled := false
	r := &Runner{config: &BackendConfig{AcceptanceEvidence: &AcceptanceEvidenceConfig{Enabled: &disabled}}}

	got := r.appendAcceptanceEvidence(context.Background(), task, t.TempDir(), prBody)
	if got != prBody {
		t.Fatalf("expected byte-identical PR body when disabled, got %q", got)
	}
}

func TestAppendAcceptanceEvidence_NoAcceptanceCriteria_ByteIdentical(t *testing.T) {
	prBody := "## Summary\n\nsome PR body"
	task := &Task{ID: "GH-1"}
	r := &Runner{config: DefaultBackendConfig()}

	got := r.appendAcceptanceEvidence(context.Background(), task, t.TempDir(), prBody)
	if got != prBody {
		t.Fatalf("expected byte-identical PR body with no acceptance criteria, got %q", got)
	}
}

func TestAppendAcceptanceEvidence_NilRunnerOrTask_ByteIdentical(t *testing.T) {
	prBody := "## Summary\n\nsome PR body"
	var r *Runner
	if got := r.appendAcceptanceEvidence(context.Background(), &Task{}, "/tmp", prBody); got != prBody {
		t.Fatalf("nil Runner: expected byte-identical PR body, got %q", got)
	}

	r2 := &Runner{config: DefaultBackendConfig()}
	if got := r2.appendAcceptanceEvidence(context.Background(), nil, "/tmp", prBody); got != prBody {
		t.Fatalf("nil Task: expected byte-identical PR body, got %q", got)
	}
}

// --- Config accessors ---

func TestAcceptanceEvidenceConfig_Effective(t *testing.T) {
	var nilCfg *AcceptanceEvidenceConfig
	if !nilCfg.IsEnabled() {
		t.Error("nil config should default to enabled")
	}
	if got := nilCfg.EffectiveCommandTimeout(); got != defaultAcceptanceEvidenceCommandTimeout {
		t.Errorf("nil config timeout = %s, want %s", got, defaultAcceptanceEvidenceCommandTimeout)
	}
	if got := nilCfg.EffectiveAllowedCommands(); len(got) != len(DefaultAcceptanceEvidenceAllowedCommands()) {
		t.Errorf("nil config allowed commands = %v", got)
	}

	timeoutStr := "2m"
	cfg := &AcceptanceEvidenceConfig{
		AllowedCommands: []string{"go"},
		CommandTimeout:  timeoutStr,
	}
	if got := cfg.EffectiveCommandTimeout(); got != 2*time.Minute {
		t.Errorf("configured timeout = %s, want 2m", got)
	}
	if got := cfg.EffectiveAllowedCommands(); len(got) != 1 || got[0] != "go" {
		t.Errorf("configured allowed commands = %v, want [go]", got)
	}

	invalid := &AcceptanceEvidenceConfig{CommandTimeout: "not-a-duration"}
	if got := invalid.EffectiveCommandTimeout(); got != defaultAcceptanceEvidenceCommandTimeout {
		t.Errorf("invalid timeout should fall back to default, got %s", got)
	}
}
