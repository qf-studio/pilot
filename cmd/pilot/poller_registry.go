package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/qf-studio/pilot/internal/adapterhealth"
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

	// GitHubPollers is the repo-keyed registry the SDK poller adds itself to so the
	// main.go sub-issue-skip / done-remark / stale-label loops can reach it — its
	// handle otherwise never leaves githubPollerRegistration() (GH-4110). Nil when
	// GitHub polling is off.
	GitHubPollers *githubPollerRegistry

	// AdapterHealth tracks panic/restart/disabled state for every adapter
	// goroutine started via SafeAdapterGo (GH-4314). Nil is tolerated —
	// SafeAdapterGo falls back to plain logging.SafeGo with no restart/health
	// tracking so callers that don't wire it (e.g. tests) still work.
	AdapterHealth *adapterhealth.Registry

	// PollerAlertAfter is how many consecutive failed poller Start attempts
	// raise an alert (GH-5588). Zero means defaultPollerAlertAfter.
	PollerAlertAfter int
}

// PollerRegistration describes a single adapter poller that can be conditionally started.
type PollerRegistration struct {
	Name           string
	Enabled        func(cfg *config.Config) bool
	CreateAndStart func(ctx context.Context, deps *PollerDeps)
}

// adapterPollerRegistrations returns the standard set of adapter poller registrations.
// The github registration (M7 4b/4d.2b) fans out one SDK poller per GitHub repo — the
// default adapter repo plus every projects[] entry; the in-tree fallback poller has
// been removed (GH-4170), so GitHub polling is SDK-only.
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
	// GH-5583: per-project gitlab: MR creators are registered unconditionally
	// (not gated on adapters.gitlab.enabled / polling.enabled, which only govern
	// issue intake), before any poller can dispatch a task.
	registerProjectGitLabPRCreators(deps.Cfg, deps.Runner)

	for _, reg := range registrations {
		if reg.Enabled(deps.Cfg) {
			logging.WithComponent("start").Info("Starting adapter poller",
				slog.String("adapter", reg.Name),
			)
			reg.CreateAndStart(ctx, deps)
		}
	}
}

// SafeAdapterGo is the single entry point every adapter poller goroutine
// must use to launch its listen loop (GH-4314): a panic in one adapter
// (e.g. the studio-sdk Discord gateway's nil-conn deref that took down the
// whole daemon) is recovered, logged with the adapter name, and the
// goroutine is restarted with backoff instead of crashing the process. Core
// executor/dispatcher goroutines must NOT go through this path — their
// panics indicate real corruption and should keep crashing loudly.
func (d *PollerDeps) SafeAdapterGo(ctx context.Context, name string, fn func()) {
	if d.AdapterHealth == nil {
		logging.SafeGo(name, fn)
		return
	}
	d.AdapterHealth.Go(ctx, name, d.alertAdapterPanic, fn)
}

// alertAdapterPanic raises a WARN-level service_unhealthy alert when an
// adapter goroutine panics — an adapter going down is an ops signal, not a
// silent degrade. Mirrors the config_error alerting pattern already used
// for adapter credential failures in adapter_preflight.go.
func (d *PollerDeps) alertAdapterPanic(name, message string) {
	if d.AlertsEngine == nil {
		return
	}
	d.AlertsEngine.ProcessEvent(alerts.Event{
		Type:      alerts.EventTypeConfigError,
		Error:     message,
		Metadata:  map[string]string{"adapter": name},
		Timestamp: time.Now(),
	})
}

// Poller Start supervision (GH-5588). The SDK pollers' Start returns an error
// only before their ticker loop (e.g. a transient failure caching label IDs),
// and blocks until ctx is cancelled otherwise. Calling it once therefore made
// a single startup error terminal for the daemon's lifetime.
const (
	defaultPollerAlertAfter  = 3
	pollerRetryInitialDelay  = 30 * time.Second
	pollerRetryMaxDelay      = 10 * time.Minute
	pollerStartSettleTimeout = 10 * time.Second
)

// supervisorClock abstracts time so backoff behaviour is testable.
type supervisorClock interface {
	// Sleep waits d or until ctx is done, returning ctx.Err() in the latter case.
	Sleep(ctx context.Context, d time.Duration) error
	// AfterFunc runs f after d unless the returned stop func is called first.
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type realSupervisorClock struct{}

func (realSupervisorClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (realSupervisorClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// pollerSupervisor re-runs a poller's Start with capped exponential backoff
// until ctx is cancelled.
type pollerSupervisor struct {
	adapter  string // metrics/alert key, e.g. "linear"
	instance string // e.g. the workspace name; same as adapter when single
	start    func(ctx context.Context) error

	clock        supervisorClock
	initialDelay time.Duration
	maxDelay     time.Duration
	settle       time.Duration // Start still running after this long counts as up
	alertAfter   int           // consecutive failures that raise the alert

	setUp   func(up bool)
	onAlert func(failures int, err error)
	onClear func()
	log     *slog.Logger
}

// run blocks until ctx is cancelled or Start returns nil (a clean exit).
func (p *pollerSupervisor) run(ctx context.Context) {
	// State shared between this loop and the settle callbacks.
	var (
		stateMu  sync.Mutex
		delay    = p.initialDelay
		failures int // consecutive failed attempts since Start last stayed up
		alerted  bool
	)
	p.setUp(false)

	for ctx.Err() == nil {
		// attemptMu stops a late-firing settle callback from marking an
		// attempt up after it has already returned.
		var (
			attemptMu sync.Mutex
			finished  bool
		)
		stop := p.clock.AfterFunc(p.settle, func() {
			attemptMu.Lock()
			defer attemptMu.Unlock()
			if finished {
				return
			}
			p.setUp(true)
			stateMu.Lock()
			failures, delay = 0, p.initialDelay
			wasAlerted := alerted
			alerted = false
			stateMu.Unlock()
			if wasAlerted {
				p.onClear()
			}
		})
		err := p.start(ctx)
		stop()
		attemptMu.Lock()
		finished = true
		attemptMu.Unlock()
		p.setUp(false)

		if ctx.Err() != nil || err == nil {
			return
		}

		stateMu.Lock()
		failures++
		n, wait := failures, delay
		fire := n >= p.alertAfter && !alerted
		if fire {
			alerted = true
		}
		delay *= 2
		if delay > p.maxDelay {
			delay = p.maxDelay
		}
		stateMu.Unlock()

		p.log.Error("Poller Start failed; will retry",
			slog.String("adapter", p.adapter),
			slog.String("workspace", p.instance),
			slog.Int("consecutive_failures", n),
			slog.Duration("retry_in", wait),
			slog.Any("error", err),
		)
		if fire {
			p.onAlert(n, err)
		}
		if p.clock.Sleep(ctx, wait) != nil {
			return
		}
	}
}

// SuperviseStart runs start under a pollerSupervisor: a Start error is logged,
// retried with capped exponential backoff (30s doubling to 10m) until ctx is
// cancelled, reflected in pilot_poller_up, and alerted after
// PollerAlertAfter consecutive failures (GH-5588). instance distinguishes
// several pollers of one adapter (e.g. Linear workspaces); the adapter is up
// only when every instance is. Blocks like Start does — call it inside
// SafeAdapterGo.
func (d *PollerDeps) SuperviseStart(ctx context.Context, adapter, instance string, start func(ctx context.Context) error) {
	d.newPollerSupervisor(adapter, instance, start).run(ctx)
}

func (d *PollerDeps) newPollerSupervisor(adapter, instance string, start func(ctx context.Context) error) *pollerSupervisor {
	alertAfter := d.PollerAlertAfter
	if alertAfter <= 0 {
		alertAfter = defaultPollerAlertAfter
	}
	source := "poller:" + adapter
	if instance != "" && instance != adapter {
		source += ":" + instance
	}
	return &pollerSupervisor{
		adapter:      adapter,
		instance:     instance,
		start:        start,
		clock:        realSupervisorClock{},
		initialDelay: pollerRetryInitialDelay,
		maxDelay:     pollerRetryMaxDelay,
		settle:       pollerStartSettleTimeout,
		alertAfter:   alertAfter,
		setUp:        func(up bool) { d.AdapterHealth.SetPollerUp(adapter, instance, up) },
		onAlert: func(failures int, err error) {
			if d.AlertsEngine == nil {
				return
			}
			d.AlertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeConfigError,
				Source:    source,
				Error:     fmt.Sprintf("%s poller (%s) has failed to start %d times in a row, intake is down: %v", adapter, instance, failures, err),
				Metadata:  map[string]string{"adapter": adapter, "workspace": instance},
				Timestamp: time.Now(),
			})
		},
		onClear: func() {
			if d.AlertsEngine == nil {
				return
			}
			d.AlertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeConfigHealthy,
				Source:    source,
				Timestamp: time.Now(),
			})
		},
		log: logging.WithComponent(adapter),
	}
}
