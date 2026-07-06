package main

import (
	"context"
	"log/slog"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/budget"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/memory"
)

// PollerDeps groups shared infrastructure used by all adapter poller startup blocks.
type PollerDeps struct {
	Cfg         *config.Config
	ProjectPath string

	Dispatcher   *executor.Dispatcher
	Runner       *executor.Runner
	Monitor      *executor.Monitor
	Program      *tea.Program
	AlertsEngine *alerts.Engine
	Enforcer     *budget.Enforcer
	Store        *memory.Store

	AutopilotController  *autopilot.Controller
	AutopilotStateStore  *autopilot.StateStore
	AutopilotControllers map[string]*autopilot.Controller // polling mode: per-repo controllers
}

// PollerRegistration describes a single adapter poller that can be conditionally started.
type PollerRegistration struct {
	Name           string
	Enabled        func(cfg *config.Config) bool
	CreateAndStart func(ctx context.Context, deps *PollerDeps)
}

// adapterPollerRegistrations returns the standard set of adapter poller registrations.
// The github registration (M7 4b) is gated on adapters.github.use_sdk_poller and covers
// the DEFAULT repo only; the in-tree GitHub poller still handles multi-repo `projects:`
// entries (and everything, when the flag is off) until Phase 4d.
func adapterPollerRegistrations() []PollerRegistration {
	return []PollerRegistration{
		linearPollerRegistration(),
		jiraPollerRegistration(),
		asanaPollerRegistration(),
		azuredevopsPollerRegistration(),
		planePollerRegistration(),
		discordPollerRegistration(),
		gitlabPollerRegistration(),
		githubPollerRegistration(),
	}
}

// StartAdapterPollers iterates registrations and starts each enabled poller.
func StartAdapterPollers(ctx context.Context, deps *PollerDeps, registrations []PollerRegistration) {
	for _, reg := range registrations {
		if reg.Enabled(deps.Cfg) {
			logging.WithComponent("start").Info("Starting adapter poller",
				slog.String("adapter", reg.Name),
			)
			reg.CreateAndStart(ctx, deps)
		}
	}
}
