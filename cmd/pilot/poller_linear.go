package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters/linear"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/logging"
)

func linearPollerRegistration() PollerRegistration {
	return PollerRegistration{
		Name: "linear",
		Enabled: func(cfg *config.Config) bool {
			return cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled &&
				cfg.Adapters.Linear.Polling != nil && cfg.Adapters.Linear.Polling.Enabled
		},
		CreateAndStart: func(ctx context.Context, deps *PollerDeps) {
			workspaces := deps.Cfg.Adapters.Linear.GetWorkspaces()
			for _, ws := range workspaces {
				// Determine interval: workspace override > global > default
				interval := 30 * time.Second
				if ws.Polling != nil && ws.Polling.Interval > 0 {
					interval = ws.Polling.Interval
				} else if deps.Cfg.Adapters.Linear.Polling.Interval > 0 {
					interval = deps.Cfg.Adapters.Linear.Polling.Interval
				}

				// Check if workspace polling is explicitly disabled
				if ws.Polling != nil && !ws.Polling.Enabled {
					continue
				}

				linearClient := linear.NewClient(ws.APIKey)
				// Capture workspace config for per-issue project resolution (GH-1348)
				wsConfig := ws

				// Build poller options
				linearPollerOpts := []linear.PollerOption{
					linear.WithOnLinearIssue(func(issueCtx context.Context, issue *linear.Issue) (*linear.IssueResult, error) {
						// GH-1348: Resolve project path per-issue using workspace→project mapping
						issueProjectPath := deps.ProjectPath // fallback to default
						var resolvedProject *config.ProjectConfig

						// GH-1684: Check project-level linear.project_id mapping first
						if issue.Project != nil {
							resolvedProject = deps.Cfg.GetProjectByLinearID(issue.Project.ID)
						}

						// Fall back to workspace-level resolution
						if resolvedProject == nil {
							pilotProject := wsConfig.ResolvePilotProject(issue)
							if pilotProject != "" {
								resolvedProject = deps.Cfg.GetProjectByName(pilotProject)
							}
						}

						if resolvedProject != nil {
							issueProjectPath = resolvedProject.Path
						}

						result, err := handleLinearIssueWithResult(issueCtx, deps.Cfg, linearClient, issue, issueProjectPath, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)

						// Wire PR to autopilot for CI monitoring + auto-merge
						if result != nil && result.PRNumber > 0 {
							// GH-1361: Polling mode uses per-repo autopilot controllers
							if deps.AutopilotControllers != nil && resolvedProject != nil && resolvedProject.GitHub != nil {
								repoFullName := fmt.Sprintf("%s/%s", resolvedProject.GitHub.Owner, resolvedProject.GitHub.Repo)
								if controller, ok := deps.AutopilotControllers[repoFullName]; ok && controller != nil {
									controller.OnPRCreated(result.PRNumber, result.PRURL, 0, result.HeadSHA, result.BranchName)
								}
							} else if deps.AutopilotController != nil {
								// GH-1700: Gateway mode uses single autopilot controller
								deps.AutopilotController.OnPRCreated(result.PRNumber, result.PRURL, 0, result.HeadSHA, result.BranchName)
							}
						}

						return result, err
					}),
				}

				// GH-1351: Wire processed issue persistence to prevent re-dispatch after hot upgrade
				if deps.AutopilotStateStore != nil {
					linearPollerOpts = append(linearPollerOpts, linear.WithProcessedStore(deps.AutopilotStateStore))
				}

				// GH-1700: Wire OnPRCreated callback for gateway mode (direct wiring)
				if deps.AutopilotController != nil && deps.AutopilotControllers == nil {
					linearPollerOpts = append(linearPollerOpts, linear.WithOnPRCreated(deps.AutopilotController.OnPRCreated))
				}

				linearPoller := linear.NewPoller(linearClient, ws, interval, linearPollerOpts...)

				logging.WithComponent("start").Info("Linear polling enabled",
					slog.String("workspace", ws.Name),
					slog.String("team", ws.TeamID),
					slog.Duration("interval", interval),
				)
				go func(p *linear.Poller, name string) {
					if err := p.Start(ctx); err != nil {
						logging.WithComponent("linear").Error("Linear poller failed",
							slog.String("workspace", name),
							slog.Any("error", err),
						)
					}
				}(linearPoller, ws.Name)
			}
		},
	}
}
