package main

import (
	"context"
	"testing"

	"github.com/alekspetrov/pilot/internal/config"
)

func TestStartAdapterPollers_OnlyStartsEnabled(t *testing.T) {
	var started []string

	registrations := []PollerRegistration{
		{
			Name:    "enabled-adapter",
			Enabled: func(_ *config.Config) bool { return true },
			CreateAndStart: func(_ context.Context, _ *PollerDeps) {
				started = append(started, "enabled-adapter")
			},
		},
		{
			Name:    "disabled-adapter",
			Enabled: func(_ *config.Config) bool { return false },
			CreateAndStart: func(_ context.Context, _ *PollerDeps) {
				started = append(started, "disabled-adapter")
			},
		},
		{
			Name:    "another-enabled",
			Enabled: func(_ *config.Config) bool { return true },
			CreateAndStart: func(_ context.Context, _ *PollerDeps) {
				started = append(started, "another-enabled")
			},
		},
	}

	deps := &PollerDeps{Cfg: &config.Config{}}
	StartAdapterPollers(context.Background(), deps, registrations)

	if len(started) != 2 {
		t.Fatalf("expected 2 pollers started, got %d", len(started))
	}
	if started[0] != "enabled-adapter" {
		t.Errorf("expected first started = enabled-adapter, got %s", started[0])
	}
	if started[1] != "another-enabled" {
		t.Errorf("expected second started = another-enabled, got %s", started[1])
	}
}

func TestAdapterPollerRegistrations_ReturnsAllFive(t *testing.T) {
	regs := adapterPollerRegistrations()
	if len(regs) != 6 {
		t.Fatalf("expected 6 registrations, got %d", len(regs))
	}

	expected := []string{"linear", "jira", "asana", "azuredevops", "plane", "discord"}
	for i, name := range expected {
		if regs[i].Name != name {
			t.Errorf("registration[%d]: expected name %q, got %q", i, name, regs[i].Name)
		}
	}
}

func TestPollerRegistrations_EnabledChecksConfig(t *testing.T) {
	regs := adapterPollerRegistrations()
	// Config with initialized but empty Adapters
	emptyCfg := &config.Config{
		Adapters: &config.AdaptersConfig{},
	}

	for _, reg := range regs {
		if reg.Enabled(emptyCfg) {
			t.Errorf("registration %q should be disabled with empty config", reg.Name)
		}
	}
}
