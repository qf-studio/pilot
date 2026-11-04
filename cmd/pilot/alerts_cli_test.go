package main

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
)

// fakeCLIAlertProcessor records the events a CLI runner delivers.
type fakeCLIAlertProcessor struct {
	mu     sync.Mutex
	events []executor.AlertEvent
}

func (f *fakeCLIAlertProcessor) ProcessEvent(e executor.AlertEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

func (f *fakeCLIAlertProcessor) snapshot() []executor.AlertEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]executor.AlertEvent(nil), f.events...)
}

// GH-6050: the CLI runners (pilot task, pilot navigator bootstrap) had no
// alert processor, so a fail-closed run in api_key mode alerted nobody.
func TestCLIRunner_InstallsAlertProcessor(t *testing.T) {
	t.Run("navigator bootstrap in api_key mode delivers the billing key alert", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("api_key_command runs through a POSIX shell")
		}
		fake := &fakeCLIAlertProcessor{}
		stopped := 0
		orig := newCLIAlertProcessor
		newCLIAlertProcessor = func(*config.Config) (executor.AlertEventProcessor, func()) {
			return fake, func() { stopped++ }
		}
		defer func() { newCLIAlertProcessor = orig }()

		tmp := t.TempDir()
		cfg := config.DefaultConfig()
		cfg.Memory.Path = filepath.Join(tmp, "data")
		cfg.Executor.ClaudeCode = &executor.ClaudeCodeConfig{
			Billing: &executor.BillingConfig{Mode: executor.BillingModeAPIKey, APIKeyCommand: "exit 1"},
		}
		configPath := filepath.Join(tmp, "config.yaml")
		if err := config.Save(cfg, configPath); err != nil {
			t.Fatalf("save config: %v", err)
		}
		oldCfgFile := cfgFile
		cfgFile = configPath
		defer func() { cfgFile = oldCfgFile }()

		project := filepath.Join(tmp, "project")
		if err := os.MkdirAll(project, 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := newNavigatorBootstrapCmd()
		cmd.SetArgs([]string{"--project", project})
		err := cmd.Execute()
		if !executor.IsBillingKeyUnavailable(err) {
			t.Fatalf("Execute err = %v, want a billing key unavailable error", err)
		}

		got := fake.snapshot()
		if len(got) != 1 || got[0].Type != executor.AlertEventTypeBillingKeyUnavailable {
			t.Fatalf("events = %+v, want exactly one billing_key_unavailable", got)
		}
		if got[0].Source != "bootstrap" || got[0].AdmissionPaused {
			t.Errorf("event = %+v, want source bootstrap and AdmissionPaused false", got[0])
		}
		if stopped != 1 {
			t.Errorf("alert engine stop called %d times, want 1", stopped)
		}
	})

	t.Run("alerts disabled leaves the runner without a processor", func(t *testing.T) {
		runner := executor.NewRunnerWithBackend(nil)
		cfg := config.DefaultConfig()
		cfg.Alerts = &config.AlertsConfig{Enabled: false}
		stop := installCLIAlertProcessor(runner, cfg)
		defer stop()
		if runner.AlertProcessor() != nil {
			t.Error("runner has an alert processor with alerts disabled")
		}
	})

	t.Run("alerts enabled installs the engine processor", func(t *testing.T) {
		runner := executor.NewRunnerWithBackend(nil)
		cfg := config.DefaultConfig()
		cfg.Alerts = &config.AlertsConfig{Enabled: true}
		stop := installCLIAlertProcessor(runner, cfg)
		defer stop()
		if runner.AlertProcessor() == nil {
			t.Error("runner has no alert processor with alerts enabled")
		}
	})
}
