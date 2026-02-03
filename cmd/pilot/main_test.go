package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestStartCommandFlags verifies all expected flags exist on the start command
func TestStartCommandFlags(t *testing.T) {
	cmd := newStartCmd()

	expectedFlags := []struct {
		name      string
		shorthand string
	}{
		{"daemon", "d"},
		{"dashboard", ""},
		{"project", "p"},
		{"replace", ""},
		{"no-gateway", ""},
		{"sequential", ""},
		{"telegram", ""},
		{"github", ""},
		{"linear", ""},
	}

	for _, ef := range expectedFlags {
		flag := cmd.Flags().Lookup(ef.name)
		if flag == nil {
			t.Errorf("missing flag: --%s", ef.name)
			continue
		}
		if ef.shorthand != "" && flag.Shorthand != ef.shorthand {
			t.Errorf("flag --%s: expected shorthand -%s, got -%s", ef.name, ef.shorthand, flag.Shorthand)
		}
	}
}

// TestTaskCommandFlags verifies all expected flags exist on the task command
func TestTaskCommandFlags(t *testing.T) {
	cmd := newTaskCmd()

	expectedFlags := []struct {
		name      string
		shorthand string
	}{
		{"project", "p"},
		{"dry-run", ""},
		{"verbose", "v"},
		{"alerts", ""},
	}

	for _, ef := range expectedFlags {
		flag := cmd.Flags().Lookup(ef.name)
		if flag == nil {
			t.Errorf("missing flag: --%s", ef.name)
			continue
		}
		if ef.shorthand != "" && flag.Shorthand != ef.shorthand {
			t.Errorf("flag --%s: expected shorthand -%s, got -%s", ef.name, ef.shorthand, flag.Shorthand)
		}
	}
}

// TestGitHubRunCommandFlags verifies all expected flags exist on the github run command
func TestGitHubRunCommandFlags(t *testing.T) {
	cmd := newGitHubRunCmd()

	expectedFlags := []struct {
		name      string
		shorthand string
	}{
		{"project", "p"},
		{"repo", ""},
		{"dry-run", ""},
		{"verbose", "v"},
	}

	for _, ef := range expectedFlags {
		flag := cmd.Flags().Lookup(ef.name)
		if flag == nil {
			t.Errorf("missing flag: --%s", ef.name)
			continue
		}
		if ef.shorthand != "" && flag.Shorthand != ef.shorthand {
			t.Errorf("flag --%s: expected shorthand -%s, got -%s", ef.name, ef.shorthand, flag.Shorthand)
		}
	}
}

// TestFlagParsing verifies flags can be parsed correctly using ParseFlags
// (not Execute which also validates args)
func TestFlagParsing(t *testing.T) {
	tests := []struct {
		name    string
		cmdFunc func() *cobra.Command
		args    []string
		wantErr bool
	}{
		{
			name:    "start with dashboard",
			cmdFunc: newStartCmd,
			args:    []string{"--dashboard"},
			wantErr: false,
		},
		{
			name:    "start with no-gateway and telegram",
			cmdFunc: newStartCmd,
			args:    []string{"--no-gateway", "--telegram=true"},
			wantErr: false,
		},
		{
			name:    "start with sequential",
			cmdFunc: newStartCmd,
			args:    []string{"--sequential"},
			wantErr: false,
		},
		{
			name:    "start with all adapter flags",
			cmdFunc: newStartCmd,
			args:    []string{"--telegram=true", "--github=true", "--linear=false"},
			wantErr: false,
		},
		{
			name:    "task with dry-run",
			cmdFunc: newTaskCmd,
			args:    []string{"--dry-run"},
			wantErr: false,
		},
		{
			name:    "task with verbose",
			cmdFunc: newTaskCmd,
			args:    []string{"--verbose"},
			wantErr: false,
		},
		{
			name:    "task with all flags",
			cmdFunc: newTaskCmd,
			args:    []string{"--dry-run", "--verbose", "--alerts"},
			wantErr: false,
		},
		{
			name:    "github run with dry-run",
			cmdFunc: newGitHubRunCmd,
			args:    []string{"--dry-run"},
			wantErr: false,
		},
		{
			name:    "github run with repo",
			cmdFunc: newGitHubRunCmd,
			args:    []string{"--repo", "owner/repo"},
			wantErr: false,
		},
		{
			name:    "github run with all flags",
			cmdFunc: newGitHubRunCmd,
			args:    []string{"--dry-run", "--verbose", "--repo", "owner/repo"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := tt.cmdFunc()
			err := cmd.ParseFlags(tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseFlags() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestFlagDefaults verifies default values for important flags
func TestFlagDefaults(t *testing.T) {
	t.Run("start command defaults", func(t *testing.T) {
		cmd := newStartCmd()

		// Dashboard should default to false
		if flag := cmd.Flags().Lookup("dashboard"); flag != nil {
			if flag.DefValue != "false" {
				t.Errorf("dashboard default should be false, got %s", flag.DefValue)
			}
		}

		// sequential should default to false
		if flag := cmd.Flags().Lookup("sequential"); flag != nil {
			if flag.DefValue != "false" {
				t.Errorf("sequential default should be false, got %s", flag.DefValue)
			}
		}
	})

	t.Run("task command defaults", func(t *testing.T) {
		cmd := newTaskCmd()

		// dry-run should default to false
		if flag := cmd.Flags().Lookup("dry-run"); flag != nil {
			if flag.DefValue != "false" {
				t.Errorf("dry-run default should be false, got %s", flag.DefValue)
			}
		}
	})

	t.Run("github run command defaults", func(t *testing.T) {
		cmd := newGitHubRunCmd()

		// dry-run should default to false
		if flag := cmd.Flags().Lookup("dry-run"); flag != nil {
			if flag.DefValue != "false" {
				t.Errorf("dry-run default should be false, got %s", flag.DefValue)
			}
		}
	})
}

// TestRemovedFlags verifies that removed flags are no longer present
func TestRemovedFlags(t *testing.T) {
	t.Run("start command removed flags", func(t *testing.T) {
		cmd := newStartCmd()

		removedFlags := []string{"no-pr", "direct-commit", "parallel"}
		for _, name := range removedFlags {
			if flag := cmd.Flags().Lookup(name); flag != nil {
				t.Errorf("flag --%s should be removed but still exists", name)
			}
		}
	})

	t.Run("task command removed flags", func(t *testing.T) {
		cmd := newTaskCmd()

		removedFlags := []string{"no-pr", "create-pr", "no-branch"}
		for _, name := range removedFlags {
			if flag := cmd.Flags().Lookup(name); flag != nil {
				t.Errorf("flag --%s should be removed but still exists", name)
			}
		}
	})

	t.Run("github run command removed flags", func(t *testing.T) {
		cmd := newGitHubRunCmd()

		removedFlags := []string{"no-pr", "create-pr"}
		for _, name := range removedFlags {
			if flag := cmd.Flags().Lookup(name); flag != nil {
				t.Errorf("flag --%s should be removed but still exists", name)
			}
		}
	})
}
