package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
)

func TestUseWorktreeStartupWarning(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		wantWarn bool
	}{
		{"explicit false warns", &config.Config{Executor: &executor.BackendConfig{UseWorktree: boolPtr(false)}}, true},
		{"explicit true is silent", &config.Config{Executor: &executor.BackendConfig{UseWorktree: boolPtr(true)}}, false},
		{"unset is silent", &config.Config{Executor: &executor.BackendConfig{}}, false},
		{"default config is silent", &config.Config{Executor: executor.DefaultBackendConfig()}, false},
		{"nil executor is silent", &config.Config{}, false},
		{"nil config is silent", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

			got := warnUseWorktreeDisabled(tt.cfg, log)

			if got != tt.wantWarn {
				t.Errorf("warnUseWorktreeDisabled() = %v, want %v", got, tt.wantWarn)
			}
			out := buf.String()
			if tt.wantWarn {
				if n := strings.Count(out, "level=WARN"); n != 1 {
					t.Errorf("want exactly one WARN line, got %d:\n%s", n, out)
				}
				if !strings.Contains(out, "executor.use_worktree is false: tasks edit the project clone in place") {
					t.Errorf("WARN line missing the documented text:\n%s", out)
				}
			} else if out != "" {
				t.Errorf("want no log output, got:\n%s", out)
			}
		})
	}
}

// TestUseWorktreeStartupWarning_NilIsSilent pins that an unset key (the new
// default) never produces the direct-mode warning.
func TestUseWorktreeStartupWarning_NilIsSilent(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := &config.Config{Executor: &executor.BackendConfig{}}
	if cfg.Executor.UseWorktree != nil {
		t.Fatal("precondition: UseWorktree must be nil")
	}
	if warnUseWorktreeDisabled(cfg, log) || buf.Len() != 0 {
		t.Errorf("nil use_worktree must be silent, got %q", buf.String())
	}
}
