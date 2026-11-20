package executor

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/workflow"
)

// agentToolEnvModelNames are the names of the model the test process sets:
// the last one stands for a name the CLI adds tomorrow, which no list of
// single names holds.
var agentToolEnvModelNames = map[string]string{
	"ANTHROPIC_API_KEY":                 "fake-api-key",
	"ANTHROPIC_CUSTOM_HEADERS":          "X-Daemon: fake-gateway-header",
	"ANTHROPIC_BASE_URL":                "https://gateway.invalid/fake",
	"CLAUDE_CODE_CLIENT_KEY_PASSPHRASE": "fake-passphrase",
	"CLAUDE_CODE_USE_BEDROCK":           "1",
	"CLAUDE_NEW_NAME_OF_TOMORROW":       "fake-new-value",
}

// envFileModelNames returns the names of the environment file at path with the
// prefix ANTHROPIC_ or CLAUDE_.
func envFileModelNames(t *testing.T, path string) []string {
	t.Helper()
	var names []string
	for _, line := range strings.Split(readTestFile(t, path), "\n") {
		name, _, ok := strings.Cut(line, "=")
		if ok && (strings.HasPrefix(name, "ANTHROPIC_") || strings.HasPrefix(name, "CLAUDE_")) {
			names = append(names, name)
		}
	}
	return names
}

// mutation pin: the git route gets the environment of gitAgentBaseEnv with no filter (setAgentEnvIn in gitCommand) -> this test fails
//
// TestAgentToolEnvDropsModelNames (GH-6635, TASK-651): with run_as_user set
// no command of the agent side that is not the model session gets a name with
// the prefix ANTHROPIC_ or CLAUDE_: a gate command, a workflow hook, the lint
// program, an evidence command and a pre-commit hook of the workspace that
// runs in the amend after the lint fix each write their environment to a
// file. The backend command of the model session still gets
// ANTHROPIC_BASE_URL.
func TestAgentToolEnvDropsModelNames(t *testing.T) {
	isolateHostGitIdentity(t)
	stubRunAsGOOS(t)
	root, ws := lintWorkspace(t)
	lintOut := mockLint(t)
	sudoLog := mockSudoAgent(t, "", "")
	setRunAsStateForTest(t, gitRouteSettings(t, root), "")
	clearModelEnvForTest(t)
	for name, v := range agentToolEnvModelNames {
		t.Setenv(name, v)
	}
	out := t.TempDir()
	ctx := context.Background()

	gateEnvFile := filepath.Join(out, "gate.env")
	cmd, err := gateCommandBuilder{}.Build(ctx, ws, nil, "sh", "-c", "env > '"+gateEnvFile+"'")
	if err != nil {
		t.Fatal(err)
	}
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gate command: %v: %s", err, b)
	}

	hookEnvFile := filepath.Join(out, "hook.env")
	runWorkflowHook(ctx, "after_create", workflow.HookValue{"env > '" + hookEnvFile + "'"}, ws, "GH-6635", slog.New(slog.NewTextHandler(io.Discard, nil)))

	evidenceEnvFile := filepath.Join(out, "evidence.env")
	cmd, err = evidenceCommand(ctx, ws, []string{"sh", "-c", "env > '" + evidenceEnvFile + "'"})
	if err != nil {
		t.Fatal(err)
	}
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("evidence command: %v: %s", err, b)
	}

	preCommitEnvFile := filepath.Join(out, "pre-commit.env")
	writeTestFile(t, filepath.Join(ws, ".git", "hooks", "pre-commit"), fmt.Sprintf("#!/bin/sh\nenv > '%s'\n", preCommitEnvFile))
	if err := os.Chmod(filepath.Join(ws, ".git", "hooks", "pre-commit"), 0o755); err != nil { //nolint:gosec // a hook must be executable
		t.Fatal(err)
	}
	if res := NewGitOperations(ws).autoFixLint(ctx, "main"); !res.FixedAll || !res.Clean {
		t.Fatalf("autoFixLint = %+v, want the fix applied and amended", res)
	}

	for kind, path := range map[string]string{
		"gate command":                      gateEnvFile,
		"workflow hook":                     hookEnvFile,
		"lint program (check)":              filepath.Join(lintOut, "check.env"),
		"lint program (fix)":                filepath.Join(lintOut, "fix.env"),
		"evidence command":                  evidenceEnvFile,
		"pre-commit hook of the lint amend": preCommitEnvFile,
	} {
		if names := envFileModelNames(t, path); len(names) != 0 {
			t.Errorf("%s environment has the model names %v", kind, names)
		}
	}
	if calls := strings.Join(sudoCalls(t, sudoLog), "\n"); !strings.Contains(calls, " -- git commit --amend --no-edit") {
		t.Errorf("the amend did not run through sudo:\n%s", calls)
	}

	t.Run("the backend command of the model session keeps ANTHROPIC_BASE_URL", func(t *testing.T) {
		bin := t.TempDir()
		modelEnvFile := filepath.Join(out, "model.env")
		cli := filepath.Join(bin, "claude-model-cli")
		writeTestFile(t, cli, fmt.Sprintf("#!/bin/sh\nenv > '%s'\nexit 0\n", modelEnvFile))
		if err := os.Chmod(cli, 0o755); err != nil { //nolint:gosec // mock must be executable
			t.Fatal(err)
		}
		b := NewClaudeCodeBackend(&ClaudeCodeConfig{Command: cli})
		_, _ = b.Execute(ctx, ExecuteOptions{ProjectPath: ws, Prompt: "p"})
		env := readTestFile(t, modelEnvFile)
		if want := "ANTHROPIC_BASE_URL=" + agentToolEnvModelNames["ANTHROPIC_BASE_URL"]; !strings.Contains(env, want+"\n") {
			t.Errorf("the model session has no %s:\n%s", want, env)
		}
	})
}
