# GH-350: Add GitHub Polling to Gateway Mode

**Status**: 🚧 In Progress
**Created**: 2026-02-02
**Priority**: P1

---

## Context

**Problem**:
`pilot start --github --linear` doesn't start GitHub polling because gateway mode only supports webhook adapters.

**Root Cause**:
Mode selection at `cmd/pilot/main.go:300` routes to polling mode only when no Linear/Jira. GitHub poller is only created in `runPollingMode()`, not in gateway mode.

**Goal**:
Enable GitHub polling in gateway mode, following the Telegram pattern from GH-349.

---

## Implementation Plan

### Phase 1: Create `internal/pilot/github_poller.go` (new file)

```go
package pilot

import (
    "context"
    "fmt"
    "time"

    "github.com/alekspetrov/pilot/internal/adapters/github"
    "github.com/alekspetrov/pilot/internal/alerts"
    "github.com/alekspetrov/pilot/internal/autopilot"
    "github.com/alekspetrov/pilot/internal/executor"
    "github.com/alekspetrov/pilot/internal/logging"
)

// GitHubPollerConfig holds configuration for GitHub polling in gateway mode
type GitHubPollerConfig struct {
    Client           *github.Client
    Repo             string
    Label            string
    Interval         time.Duration
    ExecutionMode    github.ExecutionMode
    WaitForMerge     bool
    PollInterval     time.Duration
    PRTimeout        time.Duration
    ProjectPath      string
    CreatePR         bool
    DirectCommit     bool
    Runner           *executor.Runner
    Dispatcher       *executor.Dispatcher
    Monitor          *executor.Monitor
    Scheduler        *executor.Scheduler
    AlertsEngine     *alerts.Engine
    AutopilotController *autopilot.Controller
}

// GitHubPollerHandler wraps the poller for gateway mode
type GitHubPollerHandler struct {
    poller    *github.Poller
    scheduler *executor.Scheduler
    config    *GitHubPollerConfig
}

// Start begins polling
func (h *GitHubPollerHandler) Start(ctx context.Context) {
    go h.poller.Start(ctx)
    if h.scheduler != nil {
        h.scheduler.Start(ctx)
    }
}

// Stop halts polling
func (h *GitHubPollerHandler) Stop() {
    // Poller stops via context cancellation
    logging.WithComponent("pilot").Info("GitHub polling stopped")
}
```

### Phase 2: Update `internal/pilot/pilot.go`

Add fields to Pilot struct (~line 44):
```go
githubPollerHandler *GitHubPollerHandler
githubPollerConfig  *GitHubPollerConfig
```

Add option function (after WithTelegramHandler):
```go
// WithGitHubPoller enables GitHub polling in gateway mode (GH-350)
func WithGitHubPoller(cfg *GitHubPollerConfig) Option {
    return func(p *Pilot) {
        p.githubPollerConfig = cfg
    }
}
```

In `New()` after Telegram init (~line 310), add:
```go
// Initialize GitHub poller if config provided (GH-350)
if p.githubPollerConfig != nil {
    cfg := p.githubPollerConfig
    var pollerOpts []github.PollerOption

    pollerOpts = append(pollerOpts, github.WithExecutionMode(cfg.ExecutionMode))

    if cfg.ExecutionMode == github.ExecutionModeSequential {
        pollerOpts = append(pollerOpts,
            github.WithSequentialConfig(cfg.WaitForMerge, cfg.PollInterval, cfg.PRTimeout),
        )
    }

    if cfg.Scheduler != nil {
        pollerOpts = append(pollerOpts, github.WithScheduler(cfg.Scheduler))
    }

    if cfg.AutopilotController != nil {
        pollerOpts = append(pollerOpts, github.WithOnPRCreated(cfg.AutopilotController.OnPRCreated))
    }

    // Create poller with simple issue handler
    pollerOpts = append(pollerOpts, github.WithOnIssue(func(ctx context.Context, issue *github.Issue) error {
        // Simplified handler - execute task directly
        return p.executeGitHubIssue(ctx, issue)
    }))

    poller, err := github.NewPoller(cfg.Client, cfg.Repo, cfg.Label, cfg.Interval, pollerOpts...)
    if err == nil {
        p.githubPollerHandler = &GitHubPollerHandler{
            poller:    poller,
            scheduler: cfg.Scheduler,
            config:    cfg,
        }
        logging.WithComponent("pilot").Info("GitHub poller initialized for gateway mode")
    }
}
```

In `Start()` after Telegram polling (~line 350):
```go
// Start GitHub polling if handler initialized (GH-350)
if p.githubPollerHandler != nil {
    p.githubPollerHandler.Start(p.ctx)
    logging.WithComponent("pilot").Info("GitHub polling started in gateway mode")
}
```

In `Stop()` after Telegram stop (~line 368):
```go
if p.githubPollerHandler != nil {
    p.githubPollerHandler.Stop()
}
```

Add helper method:
```go
// executeGitHubIssue handles a GitHub issue in gateway mode (GH-350)
func (p *Pilot) executeGitHubIssue(ctx context.Context, issue *github.Issue) error {
    cfg := p.githubPollerConfig
    taskID := fmt.Sprintf("GH-%d", issue.Number)

    task := &executor.Task{
        ID:          taskID,
        Title:       issue.Title,
        Description: issue.Body,
        ProjectPath: cfg.ProjectPath,
        CreatePR:    cfg.CreatePR,
    }

    result, err := cfg.Runner.Execute(ctx, task)
    if err != nil {
        return fmt.Errorf("task execution failed: %w", err)
    }

    if result.Success {
        logging.WithComponent("pilot").Info("GitHub issue completed",
            slog.String("task_id", taskID),
            slog.String("pr_url", result.PRURL))
    }

    return nil
}
```

### Phase 3: Wire in `cmd/pilot/main.go`

After Telegram setup (~line 337), add:
```go
// Enable GitHub polling in gateway mode if configured (GH-350)
hasGithubPolling := cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled &&
    cfg.Adapters.GitHub.Polling != nil && cfg.Adapters.GitHub.Polling.Enabled

if hasGithubPolling {
    token := cfg.Adapters.GitHub.Token
    if token == "" {
        token = os.Getenv("GITHUB_TOKEN")
    }

    if token != "" && cfg.Adapters.GitHub.Repo != "" {
        label := cfg.Adapters.GitHub.Polling.Label
        if label == "" {
            label = cfg.Adapters.GitHub.PilotLabel
        }
        interval := cfg.Adapters.GitHub.Polling.Interval
        if interval == 0 {
            interval = 30 * time.Second
        }

        // Determine execution mode
        execMode := github.ExecutionModeSequential
        waitForMerge := true
        pollInterval := 30 * time.Second
        prTimeout := 1 * time.Hour

        if cfg.Orchestrator != nil && cfg.Orchestrator.Execution != nil {
            execCfg := cfg.Orchestrator.Execution
            if execCfg.Mode == "parallel" {
                execMode = github.ExecutionModeParallel
            }
            waitForMerge = execCfg.WaitForMerge
            if execCfg.PollInterval > 0 {
                pollInterval = execCfg.PollInterval
            }
            if execCfg.PRTimeout > 0 {
                prTimeout = execCfg.PRTimeout
            }
        }

        ghPollerCfg := &pilot.GitHubPollerConfig{
            Client:        github.NewClient(token),
            Repo:          cfg.Adapters.GitHub.Repo,
            Label:         label,
            Interval:      interval,
            ExecutionMode: execMode,
            WaitForMerge:  waitForMerge,
            PollInterval:  pollInterval,
            PRTimeout:     prTimeout,
            ProjectPath:   projectPath,
            CreatePR:      !noPR,
            Runner:        runner, // Reuse runner from Telegram setup
        }

        pilotOpts = append(pilotOpts, pilot.WithGitHubPoller(ghPollerCfg))
        logging.WithComponent("start").Info("GitHub polling enabled in gateway mode")
    }
}
```

Update startup banner (after "📱 Telegram polling active"):
```go
if hasGithubPolling && cfg.Adapters.GitHub.Token != "" {
    fmt.Printf("🐙 GitHub polling active: %s\n", cfg.Adapters.GitHub.Repo)
}
```

---

## Technical Decisions

| Decision | Chosen | Reasoning |
|----------|--------|-----------|
| Handler location | internal/pilot | Matches Telegram pattern, keeps gateway mode self-contained |
| Reuse runner | Yes | Runner already created for Telegram, share it |
| Simplified handler | OnIssue not OnIssueWithResult | Simpler, can enhance later |

---

## Verify

```bash
# Test 1: Gateway mode with GitHub
pilot start --github --linear
# Expected: "🐙 GitHub polling active" + "GitHub polling started in gateway mode"

# Test 2: Create labeled issue
gh issue create --title "Test GH-350" --label pilot --body "Test body"
# Expected: Task picked up and executed

# Test 3: Regression - polling mode
pilot start --github --telegram
# Expected: Works as before

# Test 4: Clean shutdown
# Ctrl+C
# Expected: "GitHub polling stopped"
```

---

## Done

- [ ] `internal/pilot/github_poller.go` created with types
- [ ] `internal/pilot/pilot.go` updated with fields and lifecycle
- [ ] `cmd/pilot/main.go` wires GitHub poller in gateway mode
- [ ] "🐙 GitHub polling active" shows in gateway mode banner
- [ ] Tests pass, build succeeds

---

**Last Updated**: 2026-02-02
