package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	linearSDK "github.com/qf-studio/studio-sdk/sdk/integrations/linear"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/logging"
)

// linearStatusLabels are the pilot-* labels the SDK poller creates/uses for
// lifecycle tracking (see studio-sdk's Poller.cacheLabelIDs). They degrade to
// Warn-and-continue mid-poll if misconfigured, unlike the trigger label.
var linearStatusLabels = []string{"pilot-in-progress", "pilot-done", "pilot-failed"}

// linearLabelClassifier abstracts studio-sdk's *linear.Client.ClassifyLabel
// so the preflight can be exercised with a stub in tests.
type linearLabelClassifier interface {
	ClassifyLabel(ctx context.Context, teamRef, labelName string) (*linearSDK.LabelClassificationResult, error)
}

// preflightLinearLabels classifies the trigger label and every pilot-*
// status label for one workspace, once at poller startup, and logs a line
// for any label that isn't cleanly team-scoped (GH-5092).
//
// The trigger label is logged at Error, immediately above the existing
// startup-death log that fires when the SDK poller's own GetLabelByName
// check fails inside Start() — this preflight does not change that failure
// path, it only names the remedy ahead of it. Status labels are logged at
// Warn, since they already degrade to Warn-and-continue mid-poll; the
// startup line just explains why before the poll loop begins.
//
// If the classification call itself errors (network/API failure), that is
// logged at Warn and the label is skipped — the preflight must never block
// startup on its own failure.
func preflightLinearLabels(ctx context.Context, log *slog.Logger, classifier linearLabelClassifier, teamRef, triggerLabel string, statusLabels []string) {
	classify := func(label string, isTrigger bool) {
		result, err := classifier.ClassifyLabel(ctx, teamRef, label)
		if err != nil {
			log.Warn("Linear label preflight: classification call failed, skipping",
				slog.String("label", label),
				slog.Any("error", err),
			)
			return
		}

		if result.Classification == linearSDK.LabelTeamScoped {
			return
		}

		if isTrigger {
			log.Error("Linear trigger label is not team-scoped; poller startup will fail",
				slog.String("label", label),
				slog.String("classification", string(result.Classification)),
				slog.String("remedy", result.Remedy),
			)
			return
		}

		log.Warn("Linear status label is not team-scoped; label will fail mid-poll",
			slog.String("label", label),
			slog.String("classification", string(result.Classification)),
			slog.String("remedy", result.Remedy),
		)
	}

	classify(triggerLabel, true)
	for _, label := range statusLabels {
		classify(label, false)
	}
}

func newSDKLinearWorkspace(name, apiKey, teamID, triggerLabel string, projectIDs, projects []string, interval time.Duration) *linearSDK.WorkspaceConfig {
	return &linearSDK.WorkspaceConfig{
		Name:         name,
		APIKey:       apiKey,
		TeamID:       teamID,
		TriggerLabel: triggerLabel,
		ProjectIDs:   projectIDs,
		Projects:     projects,
		Polling: &linearSDK.PollingConfig{
			Enabled:  true,
			Interval: interval,
		},
	}
}

func linearPollerRegistration() PollerRegistration {
	return PollerRegistration{
		Name: "linear",
		Enabled: func(cfg *config.Config) bool {
			return cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled &&
				cfg.Adapters.Linear.Polling != nil && cfg.Adapters.Linear.Polling.Enabled
		},
		CreateAndStart: func(ctx context.Context, deps *PollerDeps) {
			interval := 30 * time.Second
			if deps.Cfg.Adapters.Linear.Polling.Interval > 0 {
				interval = deps.Cfg.Adapters.Linear.Polling.Interval
			}

			// Map internal workspace configs → SDK workspace configs, and
			// build one SDK-native notifier per workspace (GH-4717). Linear
			// supports multiple workspaces, each authenticating with its own
			// API key, so a single global notifier/client would silently
			// post every workspace's "started" comment using just one
			// workspace's credentials.
			internalWss := deps.Cfg.Adapters.Linear.GetWorkspaces()
			sdkWorkspaces := make([]*linearSDK.WorkspaceConfig, 0, len(internalWss))
			notifiersByTeamID := make(map[string]*linearSDK.Notifier, len(internalWss))
			for _, ws := range internalWss {
				triggerLabel := ws.PilotLabel
				if triggerLabel == "" {
					triggerLabel = "pilot"
				}
				wsInterval := interval
				if ws.Polling != nil && ws.Polling.Interval > 0 {
					wsInterval = ws.Polling.Interval
				}

				// GH-5092: classify the trigger label and every pilot-*
				// status label once at startup, before the SDK poller's
				// own (opaque) label lookups run.
				preflightLinearLabels(ctx, logging.WithComponent("linear"), linearSDK.NewClient(ws.APIKey), ws.TeamID, triggerLabel, linearStatusLabels)

				sdkWorkspaces = append(sdkWorkspaces, newSDKLinearWorkspace(ws.Name, ws.APIKey, ws.TeamID, triggerLabel, ws.ProjectIDs, ws.Projects, wsInterval))
				notifiersByTeamID[ws.TeamID] = linearSDK.NewNotifier(linearSDK.NewClient(ws.APIKey))
			}

			sdkCfg := &linearSDK.Config{
				Enabled:    deps.Cfg.Adapters.Linear.Enabled,
				Workspaces: sdkWorkspaces,
				Polling: &linearSDK.PollingConfig{
					Enabled:  true,
					Interval: interval,
				},
			}

			pollerDeps := sdkcore.PollerDeps{
				Handler: sdkcore.IssueHandlerFunc(func(issueCtx context.Context, ev sdkcore.IssueEvent) (*sdkcore.IssueResult, error) {
					// GH-4717: notify Linear the task has started, mirroring
					// GH-2132's Plane wiring (poller_plane.go). ev.ProjectID
					// carries the Linear team ID for this adapter (see
					// linearSDK's toIssueEvent), which selects the
					// per-workspace notifier authenticated with that
					// workspace's own API key. Failure is WARN-logged only —
					// a comment failure must never abort dispatch.
					if notifier := notifiersByTeamID[ev.ProjectID]; notifier != nil {
						if err := notifier.NotifyTaskStarted(issueCtx, ev.IssueID, ev.SequenceID); err != nil {
							logging.WithComponent("linear").Warn("Failed to notify task started",
								slog.String("issue_id", ev.IssueID),
								slog.Any("error", err),
							)
						}
					}

					return handleLinearIssueWithResult(issueCtx, deps.Cfg, ev, deps.ProjectPath, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)
				}),
			}

			if deps.AutopilotStateStore != nil {
				pollerDeps.ProcessedStore = deps.AutopilotStateStore
			}
			if deps.Cfg.Orchestrator.MaxConcurrent > 0 {
				pollerDeps.MaxConcurrent = deps.Cfg.Orchestrator.MaxConcurrent
			}
			if deps.AutopilotController != nil {
				ctrl := deps.AutopilotController
				pollerDeps.OnPRCreated = func(prEv sdkcore.PRCreatedEvent) {
					ctrl.OnPRCreated(prEv.PRNumber, prEv.PRURL, 0, prEv.HeadSHA, prEv.BranchName, "")
				}
			}

			linearPoller := linearSDK.New(sdkCfg).NewPoller(pollerDeps)

			logging.WithComponent("start").Info("Linear polling enabled",
				slog.String("workspaces", fmt.Sprintf("%d workspace(s)", len(internalWss))),
				slog.Duration("interval", interval),
			)
			deps.SafeAdapterGo(ctx, "linear", func() {
				if err := linearPoller.Start(ctx); err != nil {
					logging.WithComponent("linear").Error("Linear poller failed",
						slog.Any("error", err),
					)
				}
			})
		},
	}
}
