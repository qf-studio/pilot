package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// TASK-651: every model spawn site goes through agentCommand. Each case runs
// the real spawn code against a mock sudo and checks what sudo was handed: a
// site that still calls exec.CommandContext directly never reaches the mock.

type agentSite struct {
	name   string
	binary string // the model binary the site names after the --
	run    func(ctx context.Context, t *testing.T, proj, sudoLog string)
}

func agentSites(cli string) []agentSite {
	runner := func() *Runner {
		r := NewRunner()
		r.config = &BackendConfig{ClaudeCode: &ClaudeCodeConfig{Command: cli}}
		return r
	}
	return []agentSite{
		{"main backend", cli, func(ctx context.Context, t *testing.T, proj, sudoLog string) {
			_, _ = NewClaudeCodeBackend(&ClaudeCodeConfig{Command: cli}).Execute(ctx, ExecuteOptions{ProjectPath: proj, Prompt: "p"})
		}},
		{"epic planning", cli, func(ctx context.Context, t *testing.T, proj, sudoLog string) {
			_, _ = runner().PlanEpic(ctx, &Task{ID: "T-1", Title: "t", Description: "d", ProjectPath: proj}, proj)
		}},
		{"intent judge", cli, func(ctx context.Context, t *testing.T, proj, sudoLog string) {
			_, _ = NewIntentJudge(cli).defaultCmdRunner(ctx, "--print", "-p", "x")
		}},
		{"subtask parser", cli, func(ctx context.Context, t *testing.T, proj, sudoLog string) {
			_, _ = (&SubtaskParser{claudeCmd: cli}).defaultRunner(ctx, "--print", "-p", "x")
		}},
		{"effort classifier", "claude", func(ctx context.Context, t *testing.T, proj, sudoLog string) {
			_, _ = NewEffortClassifier().defaultCmdRunner(ctx, "--print", "-p", "x")
		}},
		{"complexity classifier", "claude", func(ctx context.Context, t *testing.T, proj, sudoLog string) {
			_, _ = NewComplexityClassifier().defaultCmdRunner(ctx, "--print", "-p", "x")
		}},
		{"research subagent", "claude", func(ctx context.Context, t *testing.T, proj, sudoLog string) {
			NewParallelRunner(nil, nil).executeSubagent(ctx, proj, researchTask{Type: "research", Prompt: "p", Model: "haiku"})
		}},
		{"post-execution summary", cli, func(ctx context.Context, t *testing.T, proj, sudoLog string) {
			_, _ = runner().getPostExecutionSummary(ctx, proj)
		}},
		{"contract evidence", cli, func(ctx context.Context, t *testing.T, proj, sudoLog string) {
			_, _ = runner().getContractEvidence(ctx, proj, []string{"field"})
		}},
		{"opencode server", "opencode", func(ctx context.Context, t *testing.T, proj, sudoLog string) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				// Up only once the mock sudo has been started.
				if _, err := os.Stat(sudoLog); err != nil {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()
			b := NewOpenCodeBackend(&OpenCodeConfig{ServerURL: srv.URL, AutoStartServer: true, ServerCommand: "opencode serve"})
			_ = b.startServer(ctx)
		}},
		{"qwen backend", cli, func(ctx context.Context, t *testing.T, proj, sudoLog string) {
			_, _ = NewQwenCodeBackend(&QwenCodeConfig{Command: cli}).Execute(ctx, ExecuteOptions{ProjectPath: proj, Prompt: "p"})
		}},
	}
}

func agentSiteEnv(t *testing.T) (cli string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() { SetReadGuardConfig(nil) })
	// The opencode and qwen cases hand the guard flag to a mock: it is not a
	// claude CLI, but the mock never parses it either.
	return "claude-model-cli"
}

func TestRunAsUserEverySpawnSiteGoesThroughSudo(t *testing.T) {
	cli := agentSiteEnv(t)
	for _, site := range agentSites(cli) {
		t.Run(site.name, func(t *testing.T) {
			s := testRunAsSettings(t)
			setRunAsStateForTest(t, s, "")
			logPath := mockSudo(t, 0)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			site.run(ctx, t, t.TempDir(), logPath)
			assertSudoSpawn(t, sudoArgv(t, logPath), s.user, site.binary)
		})
	}
}

// A failed preflight stops every site: nothing starts and nothing falls back
// to the daemon user.
func TestRunAsUserFailedPreflightStopsEverySpawnSite(t *testing.T) {
	cli := agentSiteEnv(t)
	for _, site := range agentSites(cli) {
		t.Run(site.name, func(t *testing.T) {
			setRunAsStateForTest(t, testRunAsSettings(t), "sudo -n -u pilot-agent true")
			logPath := mockSudo(t, 0)
			ran := plantFakeBinaries(t, cli)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			site.run(ctx, t, t.TempDir(), logPath)
			if got := sudoArgv(t, logPath); got != nil {
				t.Errorf("sudo ran after a failed preflight: %q", got)
			}
			if _, err := os.Stat(ran); err == nil {
				t.Error("the model binary ran as the daemon user after a failed preflight")
			}
		})
	}
}

// plantFakeBinaries puts a fake model CLI named cli, claude and opencode on
// PATH that leave a marker when they run, and returns the marker path.
func plantFakeBinaries(t *testing.T, cli string) (marker string) {
	t.Helper()
	dir := t.TempDir()
	marker = dir + "/ran"
	body := "#!/bin/sh\n: > '" + marker + "'\nprintf '{}'\nexit 0\n"
	for _, name := range []string{cli, "claude", "opencode"} {
		if err := os.WriteFile(dir+"/"+name, []byte(body), 0o755); err != nil { //nolint:gosec // fake CLI must be executable
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
}
