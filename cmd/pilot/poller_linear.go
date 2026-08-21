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

// linearStatusLabels lists every "pilot-*" label the SDK poller manages
// mid-poll (poller.go's cacheLabelIDs/hasStatusLabel) beyond the trigger
// label itself.
var linearStatusLabels = []string{"pilot-in-progress", "pilot-done", "pilot-failed"}

// linearLabelClassifier abstracts *linearSDK.Client.ClassifyLabel so the
// startup preflight can be exercised against a stub in tests without a real
// Linear API call.
type linearLabelClassifier interface {
	ClassifyLabel(ctx context.Context, teamRef, labelName string) (*linearSDK.LabelClassificationResult, error)
}

// classifyWorkspaceLabels runs the SDK's label classifier once at poller
// startup (GH-5092) for a workspace's trigger label and every pilot-*
// status label, logging one line per label that isn't cleanly team-scoped
// so the specific remedy is visible before the poll loop begins.
//
// Runtime behavior is unchanged by this preflight:
//   - The trigger label still fails closed — cacheLabelIDs' GetLabelByName
//     call inside Start() dies the poller exactly as before. This preflight
//     just logs the classified remedy at Error immediately above that
//     failure, and returns the same diagnosis as an error so callers/tests
//     can observe it without acting on it.
//   - Status labels still degrade to Warn-and-continue mid-poll via
//     GetOrCreateLabel inside cacheLabelIDs. This preflight logs WARN
//     naming why each one will fail before that happens.
//   - A classifier error (network/API failure) is itself WARN-logged and
//     never blocks startup — the preflight is diagnostics-only.
func classifyWorkspaceLabels(ctx context.Context, logger *slog.Logger, classifier linearLabelClassifier, wsName, teamID, triggerLabel string) error {
	var triggerErr error

	if result, err := classifier.ClassifyLabel(ctx, teamID, triggerLabel); err != nil {
		logger.Warn("Trigger label classification preflight failed; continuing without it",
			slog.String("workspace", wsName),
			slog.String("label", triggerLabel),
			slog.Any("error", err),
		)
	} else if result.Classification != linearSDK.LabelTeamScoped {
		logger.Error("Trigger label is not cleanly team-scoped; poller will fail closed at startup",
			slog.String("workspace", wsName),
			slog.String("label", triggerLabel),
			slog.String("classification", string(result.Classification)),
			slog.String("remedy", result.Remedy),
		)
		triggerErr = fmt.Errorf("workspace %s: trigger label %q is %s: %s", wsName, triggerLabel, result.Classification, result.Remedy)
	}

	for _, label := range linearStatusLabels {
		result, err := classifier.ClassifyLabel(ctx, teamID, label)
		if err != nil {
			logger.Warn("Status label classification preflight failed; continuing without it",
				slog.String("workspace", wsName),
				slog.String("label", label),
				slog.Any("error", err),
			)
			continue
		}
		if result.Classification == linearSDK.LabelTeamScoped {
			continue
		}
		logger.Warn("Status label is not cleanly team-scoped; it will fail to sync mid-poll",
			slog.String("workspace", wsName),
			slog.String("label", label),
			slog.String("classification", string(result.Classification)),
			slog.String("remedy", result.Remedy),
		)
	}

	return triggerErr
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
				sdkWorkspaces = append(sdkWorkspaces, newSDKLinearWorkspace(ws.Name, ws.APIKey, ws.TeamID, triggerLabel, ws.ProjectIDs, ws.Projects, wsInterval))
				notifiersByTeamID[ws.TeamID] = linearSDK.NewNotifier(linearSDK.NewClient(ws.APIKey))

				// GH-5092: classify the trigger label and every pilot-*
				// status label against this workspace's team before the
				// poller starts, so a misconfigured label's remedy is in
				// the log ahead of (trigger label) the fail-closed error
				// or (status labels) the mid-poll Warn it will otherwise
				// cause. Diagnostics-only -- does not change what starts.
				_ = classifyWorkspaceLabels(ctx, logging.WithComponent("linear"), linearSDK.NewClient(ws.APIKey), ws.Name, ws.TeamID, triggerLabel)
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
