package main

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/gateway"
	"github.com/qf-studio/pilot/internal/health"
	"github.com/spf13/cobra"
)

// GH-6767: pilot doctor shows a row for run_as_user with the result of the
// start check.
// mutation pin: return nil also when the option is set -> this test fails.
// mutation pin: give the status OK when the error read returns an error ->
// this test fails.
func TestDoctorRunAsUserCheck(t *testing.T) {
	runAsCfg := func(user string) *config.Config {
		return &config.Config{
			Gateway: &gateway.Config{Host: "127.0.0.1", Port: 9345},
			Executor: &executor.BackendConfig{ClaudeCode: &executor.ClaudeCodeConfig{
				RunAsUser:  user,
				RunAsGroup: "pilot",
			}},
		}
	}
	const check = "sudo -n -u pilot-agent -g pilot true"

	tests := []struct {
		name       string
		cfg        *config.Config
		readErr    error
		wantNil    bool
		wantStatus health.Status
		wantMsg    string
		wantFix    string
	}{
		{name: "option empty", cfg: runAsCfg(""), wantNil: true},
		{name: "no executor block", cfg: &config.Config{}, wantNil: true},
		{name: "preflight passes", cfg: runAsCfg("pilot-agent"), wantStatus: health.StatusOK, wantMsg: "pilot-agent"},
		{
			name:       "preflight fails",
			cfg:        runAsCfg("pilot-agent"),
			readErr:    errors.New("run_as_user preflight failed: " + check),
			wantStatus: health.StatusError,
			wantMsg:    check,
			wantFix:    "scripts/box/setup-agent-user.sh",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, calls := capturePreflightArgs(t)
			prev := runAsUserPreflightError
			runAsUserPreflightError = func() error { return tt.readErr }
			t.Cleanup(func() { runAsUserPreflightError = prev })

			row := doctorRunAsUserCheck(tt.cfg)
			if tt.wantNil {
				if row != nil {
					t.Errorf("row %+v, want nil", *row)
				}
				if *calls != 0 {
					t.Errorf("preflight ran %d time(s), want none", *calls)
				}
				return
			}
			if row == nil {
				t.Fatal("row nil, want a run_as_user row")
			}
			if *calls != 1 {
				t.Errorf("preflight ran %d time(s), want 1", *calls)
			}
			if row.Name != "run_as_user" || row.Status != tt.wantStatus {
				t.Errorf("row %q status %v, want run_as_user status %v", row.Name, row.Status, tt.wantStatus)
			}
			if !strings.Contains(row.Message, tt.wantMsg) {
				t.Errorf("message %q does not hold %q", row.Message, tt.wantMsg)
			}
			if !strings.Contains(row.Fix, tt.wantFix) {
				t.Errorf("fix %q does not hold %q", row.Fix, tt.wantFix)
			}
			if tt.wantStatus == health.StatusError && !strings.Contains(row.Fix, "agent-isolation") {
				t.Errorf("fix %q does not name the documentation page", row.Fix)
			}
			// The values that reach the preflight.
			if got.cfg.RunAsUser != "pilot-agent" || got.cfg.RunAsGroup != "pilot" ||
				got.cfg.RunAsGatewayPort != 9345 || got.cfg.RunAsGatewayHost != "127.0.0.1" ||
				got.cfg.RunAsGatewayRequired || got.cfg.ConfigPath == "" {
				t.Errorf("preflight got %+v, want user pilot-agent, group pilot, gateway 127.0.0.1:9345, no daemon start and a config path", got.cfg)
			}
		})
	}
}

// GH-6772: one pilot doctor run logs a failed run_as_user check once. The
// test runs the real pre-run of the root command, the real doctor command and
// the real preflight; only the live health checks are left out.
// mutation pin: drop the PersistentPreRun of newDoctorCmd, so the root
// pre-run runs the preflight a first time -> this test fails.
func TestDoctorLogsFailedRunAsUserCheckOnce(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	body := "version: \"1.0\"\ngateway:\n  host: \"127.0.0.1\"\n  port: 9345\n" +
		"executor:\n  claude_code:\n    run_as_user: pilot-no-such-user-6772\n    run_as_group: pilot\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	prevRunChecks := doctorRunChecks
	doctorRunChecks = func(*config.Config, string) *health.HealthReport { return &health.HealthReport{} }
	var buf bytes.Buffer
	prevLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	prevCfgFile := cfgFile
	t.Cleanup(func() {
		doctorRunChecks = prevRunChecks
		slog.SetDefault(prevLog)
		cfgFile = prevCfgFile
		executor.SetReadGuardConfig(nil)
		executor.PreflightRunAsUser(nil, "", true, 0)
	})

	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "pilot doctor", args: []string{"doctor"}},
		// The root pre-run still runs the preflight for every other command.
		{name: "other command", args: []string{"other"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			buf.Reset()
			executor.PreflightRunAsUser(nil, "", true, 0)
			root := &cobra.Command{Use: "pilot", SilenceErrors: true, SilenceUsage: true}
			root.PersistentPreRun = rootPersistentPreRun
			root.PersistentFlags().StringVar(&cfgFile, "config", "", "config file")
			root.AddCommand(newDoctorCmd(), &cobra.Command{Use: "other", Run: func(*cobra.Command, []string) {}})
			root.SetArgs(append(tt.args, "--config", cfgPath))
			_ = root.Execute()

			if n := strings.Count(buf.String(), "run_as_user preflight failed"); n != 1 {
				t.Errorf("the failed check was logged %d time(s), want once:\n%s", n, buf.String())
			}
			if executor.RunAsUserPreflightError() == nil {
				t.Error("preflight error nil, want the failed check")
			}
		})
	}
}
