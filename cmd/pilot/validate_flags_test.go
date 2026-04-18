package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/adapters/linear"
	"github.com/qf-studio/pilot/internal/config"
)

// newTestStartCmd creates a start command and sets the given flag-name pairs
// so cmd.Flags().Changed() reports them as set.
func newTestStartCmd(t *testing.T, setFlags ...string) *cobra.Command {
	t.Helper()
	cmd := newStartCmd()
	for _, name := range setFlags {
		if err := cmd.Flags().Set(name, "true"); err != nil {
			t.Fatalf("failed to set flag %q: %v", name, err)
		}
	}
	return cmd
}

// GH-2361: --github with no adapter block must fail loudly.
func TestValidateAdapterFlags_GithubMissingBlock(t *testing.T) {
	cfg := &config.Config{
		Projects: []*config.ProjectConfig{{Name: "myrepo", Path: "/tmp"}},
		Adapters: &config.AdaptersConfig{},
	}
	cmd := newTestStartCmd(t, "github")

	err := validateAdapterFlags(cfg, cmd)
	if err == nil {
		t.Fatal("expected error when --github set and adapter block missing")
	}
	msg := err.Error()
	if !strings.Contains(msg, "--github") || !strings.Contains(msg, "missing") {
		t.Errorf("error = %q, want mention of --github and 'missing'", msg)
	}
}

// GH-2361: --github with adapter block disabled must fail loudly.
func TestValidateAdapterFlags_GithubDisabled(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{Enabled: false},
		},
	}
	cmd := newTestStartCmd(t, "github")

	err := validateAdapterFlags(cfg, cmd)
	if err == nil {
		t.Fatal("expected error when --github set and adapter disabled")
	}
	msg := err.Error()
	if !strings.Contains(msg, "adapters.github.enabled is false") {
		t.Errorf("error = %q, want mention of 'adapters.github.enabled is false'", msg)
	}
}

// GH-2361: --github with adapter enabled passes validation.
func TestValidateAdapterFlags_GithubEnabled(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{Enabled: true},
		},
	}
	cmd := newTestStartCmd(t, "github")

	if err := validateAdapterFlags(cfg, cmd); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// No flags set → no validation failures even if all adapters absent.
func TestValidateAdapterFlags_NoFlagsSet(t *testing.T) {
	cfg := &config.Config{Adapters: &config.AdaptersConfig{}}
	cmd := newStartCmd()

	if err := validateAdapterFlags(cfg, cmd); err != nil {
		t.Errorf("unexpected error when no adapter flags set: %v", err)
	}
}

// GH-2361: --linear with adapter block disabled must fail loudly.
func TestValidateAdapterFlags_LinearDisabled(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			Linear: &linear.Config{Enabled: false},
		},
	}
	cmd := newTestStartCmd(t, "linear")

	err := validateAdapterFlags(cfg, cmd)
	if err == nil {
		t.Fatal("expected error when --linear set and adapter disabled")
	}
	if !strings.Contains(err.Error(), "adapters.linear.enabled is false") {
		t.Errorf("error = %q, want mention of 'adapters.linear.enabled is false'", err.Error())
	}
}
