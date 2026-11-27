package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/gateway"
	"github.com/qf-studio/pilot/internal/health"
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
