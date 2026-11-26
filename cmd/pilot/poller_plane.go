package main

import (
	"context"
	"log/slog"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	planeSDK "github.com/qf-studio/studio-sdk/sdk/integrations/plane"

	"github.com/qf-studio/pilot/internal/adapters/plane"
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
			sdkCfg, interval := planeSDKConfig(deps.Cfg.Adapters.Plane)

			// Separate client for notifier calls (adapter creates its own internally).
			planeClient := planeSDK.NewClient(
				deps.Cfg.Adapters.Plane.BaseURL,
				deps.Cfg.Adapters.Plane.APIKey,
			)
			planeNotifier := planeSDK.NewNotifier(planeClient, deps.Cfg.Adapters.Plane.WorkspaceSlug)

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

					// GH-2132: Link PR via notifier
					if result != nil && result.PRNumber > 0 {
						if linkErr := planeNotifier.LinkPR(issueCtx, ev.ProjectID, ev.IssueID, result.PRNumber, result.PRURL); linkErr != nil {
							logging.WithComponent("plane").Warn("Failed to link PR",
								slog.String("work_item_id", ev.IssueID),
								slog.Any("error", linkErr),
							)
						}
					}

					return result, err
				}),
			}

			if deps.AutopilotStateStore != nil {
				pollerDeps.ProcessedStore = deps.AutopilotStateStore
			}
			if deps.Cfg.Orchestrator.MaxConcurrent > 0 {
				pollerDeps.MaxConcurrent = deps.Cfg.Orchestrator.MaxConcurrent
			}
			if planeHasProjectPairing(deps.Cfg) {
				// GH-6707: work items can land in several repos, so the PR is
				// registered with the controller of the repo it was opened in
				// (same routing as Linear, GH-5584), not the default repo's.
				if deps.AutopilotController != nil || len(deps.AutopilotControllers) > 0 {
					pollerDeps.OnPRCreated = newRoutedOnPRCreated("plane", deps.Cfg, deps.AutopilotController, deps.AutopilotControllers)
				}
			} else if deps.AutopilotController != nil {
				ctrl := deps.AutopilotController
				pollerDeps.OnPRCreated = func(prEv sdkcore.PRCreatedEvent) {
					ctrl.OnPRCreated(prEv.PRNumber, prEv.PRURL, 0, prEv.HeadSHA, prEv.BranchName, "")
				}
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

// planeSDKConfig maps the internal Plane config to the SDK config (field
// names differ: PilotLabel vs TriggerLabel) and returns the poll interval.
func planeSDKConfig(cfg *plane.Config) (*planeSDK.Config, time.Duration) {
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
