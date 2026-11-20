package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// funcAcceptanceCommandRunner is an AcceptanceCommandRunner made of one
// function.
type funcAcceptanceCommandRunner func(ctx context.Context, dir, command string) (string, error)

func (f funcAcceptanceCommandRunner) RunCommand(ctx context.Context, dir, command string) (string, error) {
	return f(ctx, dir, command)
}

// sudoCallsWith returns the calls of the mock sudo that contain part.
func sudoCallsWith(calls []string, part string) []string {
	var out []string
	for _, c := range calls {
		if strings.Contains(c, part) {
			out = append(out, c)
		}
	}
	return out
}

// TestEvidenceCommandRunsAsAgent is item 5 of GH-6594 (TASK-651): with
// run_as_user set an evidence command runs through sudo without the secrets
// of the daemon, the mutate and revert writes of a mutation line take the
// agent write route (the fixture's workspace is read-only for the daemon) and
// the first content comes back whatever the command returned, and a link in
// place of the env context file is read with the rights of the agent user
// only. With the option empty no sudo call is made.
func TestEvidenceCommandRunsAsAgent(t *testing.T) {
	const secret = "daemon-only-value-6594"

	t.Run("evidence command runs through sudo without the daemon secret", func(t *testing.T) {
		t.Setenv("DAEMON_ONLY_SECRET", secret)
		f := newAgentFilesFixture(t, "")
		f.lock(t)
		out, err := shellAcceptanceCommandRunner{timeout: 10 * time.Second}.RunCommand(t.Context(), f.ws, "env")
		if err != nil {
			t.Fatalf("RunCommand: %v: %s", err, out)
		}
		if strings.Contains(out, "DAEMON_ONLY_SECRET") || strings.Contains(out, secret) {
			t.Errorf("output has the daemon secret:\n%s", out)
		}
		if home := runAsSettingsNow().home; !strings.Contains(out, "HOME="+home+"\n") {
			t.Errorf("output has no HOME of the agent user %s:\n%s", home, out)
		}
		calls := f.calls(t)
		if len(calls) != 1 || !strings.HasSuffix(calls[0], " -- env") {
			t.Errorf("sudo calls = %q, want one call of env as the agent user", calls)
		}
	})

	t.Run("option empty: no sudo call", func(t *testing.T) {
		t.Setenv("DAEMON_ONLY_SECRET", secret)
		f := newAgentFilesFixture(t, "")
		setRunAsStateForTest(t, nil, "")
		out, err := shellAcceptanceCommandRunner{timeout: 10 * time.Second}.RunCommand(t.Context(), f.ws, "env")
		if err != nil {
			t.Fatalf("RunCommand: %v: %s", err, out)
		}
		if strings.Contains(out, secret) {
			t.Errorf("output has the daemon secret:\n%s", out)
		}
		if calls := f.calls(t); len(calls) != 0 {
			t.Errorf("sudo calls = %q, want none", calls)
		}
	})

	exitErr := exec.Command("false").Run()
	mutationCases := []struct {
		name string
		// err is what the test command returns; cancel cancels the context
		// of the run while the command runs.
		err    error
		cancel bool
	}{
		{name: "mutation: the target test fails", err: exitErr},
		{name: "mutation: the command errors", err: errors.New("harness failure")},
		{name: "mutation: the run is cancelled", err: context.Canceled, cancel: true},
	}
	for _, tc := range mutationCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentFilesFixture(t, "")
			original := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
			main := filepath.Join(f.ws, "main.go")
			writeTestFile(t, main, original)
			seedToolchainMarker(t, f.ws, "go.mod")
			f.lock(t)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var during string
			var ran int
			runner := funcAcceptanceCommandRunner(func(_ context.Context, _, _ string) (string, error) {
				ran++
				during = readTestFile(t, main)
				if tc.cancel {
					cancel()
				}
				return "--- FAIL: TestMain (0.00s)\nFAIL\n", tc.err
			})
			item := ClassifyAcceptanceItem("delete line 4 in main.go -> TestMain fails")
			result := runMutationItem(ctx, runner, f.ws, item, defaultTestAllowedCommands)

			if ran != 1 {
				t.Fatalf("test command ran %d times, want 1 (NotVerifiedReason %q)", ran, result.NotVerifiedReason)
			}
			if want := "package main\n\nfunc main() {\n}\n"; during != want {
				t.Errorf("main.go during the run = %q, want %q", during, want)
			}
			if got := readTestFile(t, main); got != original {
				t.Errorf("main.go after the run = %q, want the first content %q", got, original)
			}
			if strings.Contains(result.NotVerifiedReason, "revert") {
				t.Errorf("NotVerifiedReason = %q, want the revert to succeed", result.NotVerifiedReason)
			}
			calls := f.calls(t)
			if n := len(sudoCallsWith(calls, agentReadScript)); n != 2 {
				t.Errorf("%d reads as the agent user, want the first read and the check (GH-6606): %q", n, calls)
			}
			if n := len(sudoCallsWith(calls, agentReplaceScript)); n != 2 {
				t.Errorf("%d writes as the agent user, want the mutate and the revert: %q", n, calls)
			}
		})
	}

	t.Run("env context", testEvidenceEnvContextRead)
}

// testEvidenceEnvContextRead is item 4 of GH-6594: readEnvContext reads the
// env context file as the agent user. A link in place of the file to a file
// of the daemon the agent user cannot read gives the empty text; with the
// option empty the os call of today follows it and no sudo call is made.
func testEvidenceEnvContextRead(t *testing.T) {
	const content = "daemon-env-context-6594"
	tests := []struct {
		name  string
		link  bool
		empty bool
		want  string
	}{
		{name: "a plain file is read as the agent user", want: content},
		{name: "a link to a file of the daemon gives the empty text", link: true, want: ""},
		{name: "option empty: os.ReadFile follows the link", link: true, empty: true, want: content},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentFilesFixture(t, "")
			p := filepath.Join(f.ws, ".pilot-env-context.txt")
			if tc.link {
				target := filepath.Join(t.TempDir(), "daemon-file")
				writeTestFile(t, target, content)
				if !tc.empty {
					if err := os.Chmod(target, 0o000); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, p); err != nil {
					t.Fatal(err)
				}
			} else {
				writeTestFile(t, p, content)
			}
			f.lock(t)
			if tc.empty {
				setRunAsStateForTest(t, nil, "")
			}
			if got := readEnvContext(f.ws); got != tc.want {
				t.Errorf("readEnvContext = %q, want %q", got, tc.want)
			}
			calls := f.calls(t)
			if tc.empty {
				if len(calls) != 0 {
					t.Errorf("sudo calls = %q, want none", calls)
				}
				return
			}
			if len(calls) != 1 || !strings.Contains(calls[0], agentReadRegularScript) || !strings.HasSuffix(calls[0], " "+p) {
				t.Errorf("sudo calls = %q, want one read of %s as the agent user", calls, p)
			}
		})
	}
}
