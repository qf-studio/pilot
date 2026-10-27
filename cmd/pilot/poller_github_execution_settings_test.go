package main

import (
	"strings"
	"testing"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/config"
)

func execSettingsCfg(exec *config.ExecutionConfig) *config.Config {
	return &config.Config{Orchestrator: &config.OrchestratorConfig{Execution: exec}}
}

func TestGitHubPollerDepsSequentialSettings_ConfiguredValuesCarried(t *testing.T) {
	tests := []struct {
		name         string
		wait         bool
		pollInterval time.Duration
		prTimeout    time.Duration
	}{
		{"wait with custom interval and timeout", true, 45 * time.Second, 10 * time.Minute},
		{"not waiting", false, 15 * time.Second, 2 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := execSettingsCfg(&config.ExecutionConfig{
				Mode:         "sequential",
				WaitForMerge: tt.wait,
				PollInterval: tt.pollInterval,
				PRTimeout:    tt.prTimeout,
			})
			var pd sdkcore.PollerDeps
			applyGithubExecutionSettings(&pd, cfg)

			if pd.ExecutionMode != "sequential" {
				t.Errorf("ExecutionMode = %q, want sequential", pd.ExecutionMode)
			}
			if pd.WaitForMerge == nil || *pd.WaitForMerge != tt.wait {
				t.Errorf("WaitForMerge = %v, want &%v", pd.WaitForMerge, tt.wait)
			}
			if pd.PRPollInterval != tt.pollInterval {
				t.Errorf("PRPollInterval = %s, want %s", pd.PRPollInterval, tt.pollInterval)
			}
			if pd.PRTimeout != tt.prTimeout {
				t.Errorf("PRTimeout = %s, want %s", pd.PRTimeout, tt.prTimeout)
			}
		})
	}
}

func TestGitHubPollerDepsSequentialSettings_UnsetLeavesSDKDefaults(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
	}{
		{"no orchestrator", &config.Config{}},
		{"no execution block", execSettingsCfg(nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var pd sdkcore.PollerDeps
			applyGithubExecutionSettings(&pd, tt.cfg)
			if pd.WaitForMerge != nil || pd.PRPollInterval != 0 || pd.PRTimeout != 0 {
				t.Errorf("got wait=%v interval=%s timeout=%s, want nil/0/0", pd.WaitForMerge, pd.PRPollInterval, pd.PRTimeout)
			}
		})
	}

	t.Run("execution block with zero durations", func(t *testing.T) {
		var pd sdkcore.PollerDeps
		applyGithubExecutionSettings(&pd, execSettingsCfg(&config.ExecutionConfig{WaitForMerge: true}))
		if pd.PRPollInterval != 0 || pd.PRTimeout != 0 {
			t.Errorf("got interval=%s timeout=%s, want 0/0", pd.PRPollInterval, pd.PRTimeout)
		}
	})
}

func TestExecutionWaitSettings_EffectiveValuesAndBanner(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		wait     bool
		interval time.Duration
		timeout  time.Duration
		banner   []string
	}{
		{"unset", &config.Config{}, true, 30 * time.Second, time.Hour,
			[]string{"waiting for PR merge", "poll: 30s", "timeout: 1h0m0s"}},
		{"configured", execSettingsCfg(&config.ExecutionConfig{WaitForMerge: true, PollInterval: time.Minute, PRTimeout: 10 * time.Minute}),
			true, time.Minute, 10 * time.Minute,
			[]string{"waiting for PR merge", "poll: 1m0s", "timeout: 10m0s"}},
		{"not waiting", execSettingsCfg(&config.ExecutionConfig{WaitForMerge: false, PRTimeout: 10 * time.Minute}),
			false, 30 * time.Second, 10 * time.Minute,
			[]string{"sequential mode · not waiting for PR merge"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := resolveExecutionWaitSettings(tt.cfg)
			if s.Wait() != tt.wait || s.Interval() != tt.interval || s.Timeout() != tt.timeout {
				t.Errorf("got wait=%v interval=%s timeout=%s, want %v/%s/%s", s.Wait(), s.Interval(), s.Timeout(), tt.wait, tt.interval, tt.timeout)
			}
			line := s.sequentialBannerLine()
			for _, want := range tt.banner {
				if !strings.Contains(line, want) {
					t.Errorf("banner %q missing %q", line, want)
				}
			}
		})
	}
}
