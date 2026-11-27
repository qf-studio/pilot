package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	planeSDK "github.com/qf-studio/studio-sdk/sdk/integrations/plane"

	"github.com/qf-studio/pilot/internal/adapters/plane"
	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/logging"
)

func planePollerRegistration() PollerRegistration {
	return PollerRegistration{
		Name: "plane",
		Enabled: func(cfg *config.Config) bool {
			return cfg.Adapters.Plane != nil && cfg.Adapters.Plane.Enabled &&
				cfg.Adapters.Plane.Polling != nil && cfg.Adapters.Plane.Polling.Enabled
		},
		CreateAndStart: func(ctx context.Context, deps *PollerDeps) {
			// Separate client for notifier calls (adapter creates its own internally).
			planeClient := planeSDK.NewClient(
				deps.Cfg.Adapters.Plane.BaseURL,
				deps.Cfg.Adapters.Plane.APIKey,
			)
			planeNotifier := planeSDK.NewNotifier(planeClient, deps.Cfg.Adapters.Plane.WorkspaceSlug)

			// GH-6739: the PR-created event has no Plane project ID, so the
			// handler records it for the callback. nil when no callback is
			// wired, so nothing is recorded that no one would take.
			links := newPlaneWorkItemLinks()
			onPRCreated := planeOnPRCreated(deps.Cfg, deps.AutopilotController, deps.AutopilotControllers, links)
			if onPRCreated == nil {
				links = nil
			}

			// GH-6751: a work item with a PR is completed at the merge, by the
			// merge leg of the controller that takes the PR — the poller only
			// sets the done label at the end of the run.
			sdkCfg, interval := planeSDKConfig(deps.Cfg.Adapters.Plane, onPRCreated != nil)
			registerPlaneDoneNotifier(newPlaneDoneNotifier(planeClient, deps.Cfg.Adapters.Plane.WorkspaceSlug), deps.AutopilotController, deps.AutopilotControllers)

			pollerDeps := sdkcore.PollerDeps{
				Handler: sdkcore.IssueHandlerFunc(func(issueCtx context.Context, ev sdkcore.IssueEvent) (*sdkcore.IssueResult, error) {
					// GH-2132: Notify task started
					if err := planeNotifier.NotifyTaskStarted(issueCtx, ev.ProjectID, ev.IssueID, ev.SequenceID); err != nil {
						logging.WithComponent("plane").Warn("Failed to notify task started",
							slog.String("work_item_id", ev.IssueID),
							slog.Any("error", err),
						)
					}

					// GH-6707: route the work item to the project entry paired with its Plane project.
					projectPath := resolvePlaneProjectPath(deps.Cfg, ev, deps.ProjectPath)

					result, err := handlePlaneIssueWithResult(issueCtx, deps.Cfg, planeClient, ev, projectPath, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)
					recordPlaneLink(links, ev, result, err)

					return result, err
				}),
			}

			if deps.AutopilotStateStore != nil {
				pollerDeps.ProcessedStore = deps.AutopilotStateStore
			}
			if deps.Cfg.Orchestrator.MaxConcurrent > 0 {
				pollerDeps.MaxConcurrent = deps.Cfg.Orchestrator.MaxConcurrent
			}
			if onPRCreated != nil {
				pollerDeps.OnPRCreated = onPRCreated
			}

			planePoller := planeSDK.New(sdkCfg).NewPoller(pollerDeps)

			logging.WithComponent("start").Info("Plane.so polling enabled",
				slog.String("workspace", deps.Cfg.Adapters.Plane.WorkspaceSlug),
				slog.Int("projects", len(deps.Cfg.Adapters.Plane.ProjectIDs)),
				slog.Duration("interval", interval),
			)
			// GH-5588: retry a failed Start with backoff instead of going dark.
			deps.SafeAdapterGo(ctx, "plane", func() {
				deps.SuperviseStart(ctx, "plane", "plane", planePoller.Start)
			})
		},
	}
}

// planeOnPRCreated builds the Plane poller's OnPRCreated callback, or nil when
// no autopilot controller can take the PR. With a project pairing, work items
// can land in several repos, so the PR is registered with the controller of
// the repo it was opened in (same routing as Linear, GH-5584/GH-6707); with no
// pairing the default controller takes it. In both branches the same
// controller then gets the Plane link of the PR (GH-6739).
func planeOnPRCreated(cfg *config.Config, defaultCtrl *autopilot.Controller, ctrls map[string]*autopilot.Controller, links *planeWorkItemLinks) func(sdkcore.PRCreatedEvent) {
	if planeHasProjectPairing(cfg) {
		if defaultCtrl == nil && len(ctrls) == 0 {
			return nil
		}
		defaultRepo, def, byRepo := routedRegistrars(cfg, defaultCtrl, ctrls)
		return planeRoutedOnPRCreated(defaultRepo, def, byRepo, links)
	}
	if defaultCtrl == nil {
		return nil
	}
	return planeDefaultOnPRCreated(defaultCtrl, links)
}

// planeRoutedOnPRCreated registers the PR with the controller of its repo and
// stores the Plane link on that controller.
func planeRoutedOnPRCreated(defaultRepo string, def prRegistrar, byRepo map[string]prRegistrar, links *planeWorkItemLinks) func(sdkcore.PRCreatedEvent) {
	return func(prEv sdkcore.PRCreatedEvent) {
		projectID := links.take(prEv.IssueID)
		ctrl, n := registerRoutedPR("plane", defaultRepo, def, byRepo, prEv)
		if ctrl == nil {
			return
		}
		setPlaneLink(ctrl, n, projectID, prEv.IssueID)
	}
}

// planeDefaultOnPRCreated registers the PR with the default controller (no
// project pairing) and stores the Plane link on it.
func planeDefaultOnPRCreated(ctrl prRegistrar, links *planeWorkItemLinks) func(sdkcore.PRCreatedEvent) {
	return func(prEv sdkcore.PRCreatedEvent) {
		projectID := links.take(prEv.IssueID)
		ctrl.OnPRCreated(prEv.PRNumber, prEv.PRURL, 0, prEv.HeadSHA, prEv.BranchName, "")
		setPlaneLink(ctrl, prEv.PRNumber, projectID, prEv.IssueID)
	}
}

// setPlaneLink stores the Plane link of a just-registered PR. No call when the
// handler recorded no project for the work item.
func setPlaneLink(ctrl prRegistrar, prNumber int, projectID, workItemID string) {
	if projectID == "" || workItemID == "" {
		return
	}
	if !ctrl.SetPlaneLink(prNumber, projectID, workItemID) {
		logging.WithComponent("plane").Warn("Plane link not stored: the controller does not track the PR",
			slog.Int("pr_number", prNumber),
			slog.String("work_item_id", workItemID),
		)
	}
}

// planeWorkItemLinks maps a work item UUID to its Plane project UUID between
// the handler (which knows both) and the OnPRCreated callback (whose event
// has only the work item) (GH-6739). Safe for parallel runs; a nil receiver
// records nothing and takes nothing.
type planeWorkItemLinks struct {
	mu sync.Mutex
	m  map[string]string
}

func newPlaneWorkItemLinks() *planeWorkItemLinks {
	return &planeWorkItemLinks{m: make(map[string]string)}
}

func (l *planeWorkItemLinks) record(workItemID, projectID string) {
	if l == nil || workItemID == "" || projectID == "" {
		return
	}
	l.mu.Lock()
	l.m[workItemID] = projectID
	l.mu.Unlock()
}

// take returns the project recorded for workItemID and removes the entry.
func (l *planeWorkItemLinks) take(workItemID string) string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	projectID := l.m[workItemID]
	delete(l.m, workItemID)
	return projectID
}

// recordPlaneLink records the work item's project when the run returned a PR,
// under the same condition on which the SDK poller fires OnPRCreated (no
// error, PR number above zero), so every entry is taken by the callback.
func recordPlaneLink(links *planeWorkItemLinks, ev sdkcore.IssueEvent, result *sdkcore.IssueResult, err error) {
	if err != nil || result == nil || result.PRNumber <= 0 {
		return
	}
	links.record(ev.IssueID, ev.ProjectID)
}

// planeSDKConfig maps the internal Plane config to the SDK config (field
// names differ: PilotLabel vs TriggerLabel) and returns the poll interval.
//
// mergeStep reports whether an autopilot controller takes the PRs of the
// poller (planeOnPRCreated is not nil). Only then is CompletedStateAtMerge
// set (GH-6751): the merge leg of that controller completes the work item.
// With no controller nothing observes the merge, so the poller must complete
// the work item at the end of the run as before — else a work item with a PR
// would never reach the completed state.
func planeSDKConfig(cfg *plane.Config, mergeStep bool) (*planeSDK.Config, time.Duration) {
	interval := 30 * time.Second
	if cfg.Polling != nil && cfg.Polling.Interval > 0 {
		interval = cfg.Polling.Interval
	}

	pilotLabel := cfg.PilotLabel
	if pilotLabel == "" {
		pilotLabel = "pilot"
	}
	return &planeSDK.Config{
		Enabled:       cfg.Enabled,
		BaseURL:       cfg.BaseURL,
		APIKey:        cfg.APIKey,
		WebhookSecret: cfg.WebhookSecret,
		WorkspaceSlug: cfg.WorkspaceSlug,
		ProjectIDs:    cfg.ProjectIDs,
		TriggerLabel:  pilotLabel,
		Polling: &planeSDK.PollingConfig{
			Enabled:  true,
			Interval: interval,
		},
		CompletedStateAtMerge: mergeStep,
	}, interval
}

// resolvePlaneProjectPath picks the project path for a polled Plane work item
// (GH-6707): the path of the project entry whose plane.project_id equals the
// event's Plane project UUID, else defaultPath (the daemon's default project,
// the behaviour before GH-6707). It logs one line per task naming the work
// item, the Plane project ID and the chosen project; the line is a Warn when
// the fallback is taken although some project entry has a plane block.
func resolvePlaneProjectPath(cfg *config.Config, ev sdkcore.IssueEvent, defaultPath string) string {
	log := logging.WithComponent("plane")

	if proj := cfg.GetProjectByPlaneID(ev.ProjectID); proj != nil && proj.Path != "" {
		log.Info("Plane work item routed to project",
			slog.String("work_item", ev.SequenceID),
			slog.String("plane_project_id", ev.ProjectID),
			slog.String("project", proj.Name),
		)
		return proj.Path
	}

	projectName := ""
	if proj := cfg.FindProjectByPath(defaultPath); proj != nil {
		projectName = proj.Name
	}
	attrs := []any{
		slog.String("work_item", ev.SequenceID),
		slog.String("plane_project_id", ev.ProjectID),
		slog.String("project", projectName),
	}
	if planeHasProjectPairing(cfg) {
		log.Warn("No project entry has this plane.project_id; using the default project", attrs...)
		return defaultPath
	}
	log.Info("Plane work item routed to the default project", attrs...)
	return defaultPath
}

// planeHasProjectPairing reports whether at least one project entry has a
// plane block (GH-6707).
func planeHasProjectPairing(cfg *config.Config) bool {
	for _, p := range cfg.Projects {
		if p != nil && p.Plane != nil {
			return true
		}
	}
	return false
}
