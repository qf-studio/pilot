package main

import (
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/logging"
)

func TestDaemonLogPath(t *testing.T) {
	tests := []struct {
		name      string
		logging   *logging.Config
		dashboard bool
		want      string
	}{
		{"stdout", &logging.Config{Output: "stdout"}, false, ""},
		{"empty output", &logging.Config{}, false, ""},
		{"file output", &logging.Config{Output: "/var/log/pilot.log"}, false, "/var/log/pilot.log"},
		{"dashboard default", &logging.Config{Output: "stdout"}, true, logging.DefaultDaemonLogPath()},
		{"dashboard custom", &logging.Config{DashboardLog: "/tmp/d.log"}, true, "/tmp/d.log"},
		{"dashboard off", &logging.Config{DashboardLog: "off"}, true, ""},
		{"nil logging, dashboard", nil, true, logging.DefaultDaemonLogPath()},
		{"nil logging", nil, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Logging: tt.logging}
			if got := daemonLogPath(cfg, tt.dashboard); got != tt.want {
				t.Errorf("daemonLogPath = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLeakScanConfigDefaults(t *testing.T) {
	tests := []struct {
		name         string
		cfg          *config.Config
		wantEnabled  bool
		wantInterval time.Duration
	}{
		{"nil security", &config.Config{}, true, 5 * time.Minute},
		{"default config", config.DefaultConfig(), true, 5 * time.Minute},
		{"disabled", &config.Config{Security: &config.SecurityConfig{LeakScan: &config.LeakScanConfig{Enabled: false}}}, false, 5 * time.Minute},
		{"custom interval", &config.Config{Security: &config.SecurityConfig{LeakScan: &config.LeakScanConfig{Enabled: true, Interval: time.Minute}}}, true, time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.LeakScanEnabled(); got != tt.wantEnabled {
				t.Errorf("enabled = %v, want %v", got, tt.wantEnabled)
			}
			if got := tt.cfg.LeakScanInterval(); got != tt.wantInterval {
				t.Errorf("interval = %v, want %v", got, tt.wantInterval)
			}
		})
	}
}

func TestGitHubAlertWatchConfigDefaults(t *testing.T) {
	tests := []struct {
		name         string
		cfg          *config.Config
		wantEnabled  bool
		wantInterval time.Duration
	}{
		{"nil security", &config.Config{}, true, time.Hour},
		{"default config", config.DefaultConfig(), true, time.Hour},
		{"disabled", &config.Config{Security: &config.SecurityConfig{GitHubAlertWatch: &config.GitHubAlertWatchConfig{Enabled: false}}}, false, time.Hour},
		{"custom interval", &config.Config{Security: &config.SecurityConfig{GitHubAlertWatch: &config.GitHubAlertWatchConfig{Enabled: true, Interval: 2 * time.Hour}}}, true, 2 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.GitHubAlertWatchEnabled(); got != tt.wantEnabled {
				t.Errorf("enabled = %v, want %v", got, tt.wantEnabled)
			}
			if got := tt.cfg.GitHubAlertWatchInterval(); got != tt.wantInterval {
				t.Errorf("interval = %v, want %v", got, tt.wantInterval)
			}
		})
	}
}

func TestSecretAlertRepos(t *testing.T) {
	cfg := &config.Config{
		Projects: []*config.ProjectConfig{
			{Name: "a", GitHub: &config.ProjectGitHubConfig{Owner: "o", Repo: "a"}},
			{Name: "none"},
			{Name: "dup", GitHub: &config.ProjectGitHubConfig{Owner: "o", Repo: "a"}},
		},
		Adapters: &config.AdaptersConfig{GitHub: &github.Config{Repo: "o/default"}},
	}
	got := secretAlertRepos(cfg)
	if len(got) != 2 || got[0].String() != "o/a" || got[1].String() != "o/default" {
		t.Errorf("repos = %v, want [o/a o/default]", got)
	}
}
