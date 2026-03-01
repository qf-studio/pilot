// Package wiring provides test harnesses that mirror cmd/pilot/main.go's
// two initialization paths (polling mode and gateway mode).
// It validates that Runner wiring is consistent across both paths.
package wiring

import (
	"path/filepath"
	"testing"

	"github.com/alekspetrov/pilot/e2e/mocks"
	"github.com/alekspetrov/pilot/internal/adapters/github"
	"github.com/alekspetrov/pilot/internal/approval"
	"github.com/alekspetrov/pilot/internal/autopilot"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/executor"
	"github.com/alekspetrov/pilot/internal/memory"
	"github.com/alekspetrov/pilot/internal/teams"
)

// Harness holds all components wired together for a single test scenario.
type Harness struct {
	Runner     *executor.Runner
	Store      *memory.Store
	Controller *autopilot.Controller
	GHMock     *mocks.GitHubMock
	GHClient   *github.Client

	// Optional components (nil when config disables them)
	LearningLoop   *memory.LearningLoop
	PatternContext *executor.PatternContext
	KnowledgeStore *memory.KnowledgeStore
}

// NewPollingHarness mirrors main.go's runPollingMode wiring path.
// This is the "full" path that wires learning loop and pattern context.
func NewPollingHarness(t *testing.T, cfg *config.Config) *Harness {
	t.Helper()

	h := &Harness{}
	h.GHMock = mocks.NewGitHubMock()
	t.Cleanup(h.GHMock.Close)

	h.GHClient = github.NewClientWithBaseURL("test-github-token", h.GHMock.URL())

	// SQLite store in test temp dir
	dataPath := t.TempDir()
	store, err := memory.NewStore(dataPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h.Store = store

	// Runner from config (mirrors main.go NewRunnerWithConfig)
	runner, err := executor.NewRunnerWithConfig(cfg.Executor)
	if err != nil {
		t.Fatalf("NewRunnerWithConfig: %v", err)
	}
	h.Runner = runner

	// Quality checker factory
	if cfg.Quality != nil && cfg.Quality.Enabled {
		h.Runner.SetQualityCheckerFactory(func(taskID, projectPath string) executor.QualityChecker {
			return nil // stub for wiring tests
		})
	}

	// Knowledge store
	ks := memory.NewKnowledgeStore(store.DB())
	h.KnowledgeStore = ks
	h.Runner.SetKnowledgeStore(ks)

	// Log store
	h.Runner.SetLogStore(store)

	// Monitor
	h.Runner.SetMonitor(executor.NewMonitor())

	// Team checker (GH-634)
	if cfg.Team != nil && cfg.Team.Enabled {
		teamStore, err := teams.NewStore(store.DB())
		if err != nil {
			t.Fatalf("teams.NewStore: %v", err)
		}
		teamSvc := teams.NewService(teamStore)
		h.Runner.SetTeamChecker(teams.NewServiceAdapter(teamSvc))
	}

	// Learning loop + pattern context (polling path ONLY — this is the parity gap)
	if cfg.Memory != nil && cfg.Memory.Learning != nil && cfg.Memory.Learning.Enabled {
		patternStore, err := memory.NewGlobalPatternStore(dataPath)
		if err != nil {
			t.Fatalf("NewGlobalPatternStore: %v", err)
		}
		extractor := memory.NewPatternExtractor(patternStore, store)
		ll := memory.NewLearningLoop(store, extractor, nil)
		h.LearningLoop = ll
		h.Runner.SetLearningLoop(ll)

		pc := executor.NewPatternContext(store)
		h.PatternContext = pc
		h.Runner.SetPatternContext(pc)
	}

	// Autopilot controller
	apCfg := cfg.Orchestrator.Autopilot
	if apCfg == nil {
		apCfg = autopilot.DefaultConfig()
	}
	approvalMgr := approval.NewManager(cfg.Approval)
	ctrl := autopilot.NewController(apCfg, h.GHClient, approvalMgr, "test-owner", "test-repo")
	h.Controller = ctrl

	// Wire learning loop to controller (polling path)
	if h.LearningLoop != nil {
		ctrl.SetLearningLoop(h.LearningLoop)
	}

	// State store for crash recovery
	if _, err := autopilot.NewStateStore(store.DB()); err != nil {
		t.Fatalf("NewStateStore: %v", err)
	}

	// Budget per-task limiter
	if cfg.Budget != nil && cfg.Budget.Enabled {
		h.Runner.SetTokenLimitCheck(func(taskID string, deltaInput, deltaOutput int64) bool {
			return true // always allow in tests
		})
	}

	// OnSubIssuePRCreated callback → autopilot
	h.Runner.SetOnSubIssuePRCreated(ctrl.OnPRCreated)

	return h
}

// NewGatewayHarness mirrors main.go's gateway mode wiring path.
// Since GH-1935, both paths wire learning loop and pattern context identically.
func NewGatewayHarness(t *testing.T, cfg *config.Config) *Harness {
	t.Helper()

	h := &Harness{}
	h.GHMock = mocks.NewGitHubMock()
	t.Cleanup(h.GHMock.Close)

	h.GHClient = github.NewClientWithBaseURL("test-github-token", h.GHMock.URL())

	// SQLite store in test temp dir
	dataPath := t.TempDir()
	store, err := memory.NewStore(dataPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h.Store = store

	// Runner from config
	runner, err := executor.NewRunnerWithConfig(cfg.Executor)
	if err != nil {
		t.Fatalf("NewRunnerWithConfig: %v", err)
	}
	h.Runner = runner

	// Quality checker factory
	if cfg.Quality != nil && cfg.Quality.Enabled {
		h.Runner.SetQualityCheckerFactory(func(taskID, projectPath string) executor.QualityChecker {
			return nil
		})
	}

	// Knowledge store
	ks := memory.NewKnowledgeStore(store.DB())
	h.KnowledgeStore = ks
	h.Runner.SetKnowledgeStore(ks)

	// Log store
	h.Runner.SetLogStore(store)

	// Monitor
	h.Runner.SetMonitor(executor.NewMonitor())

	// Team checker (GH-634)
	if cfg.Team != nil && cfg.Team.Enabled {
		teamStore, err := teams.NewStore(store.DB())
		if err != nil {
			t.Fatalf("teams.NewStore: %v", err)
		}
		teamSvc := teams.NewService(teamStore)
		h.Runner.SetTeamChecker(teams.NewServiceAdapter(teamSvc))
	}

	// GH-1935: Gateway mode now wires LearningLoop and PatternContext (parity gap closed).
	if cfg.Memory != nil && cfg.Memory.Learning != nil && cfg.Memory.Learning.Enabled {
		patternStore, err := memory.NewGlobalPatternStore(dataPath)
		if err != nil {
			t.Fatalf("NewGlobalPatternStore: %v", err)
		}
		extractor := memory.NewPatternExtractor(patternStore, store)
		ll := memory.NewLearningLoop(store, extractor, nil)
		h.LearningLoop = ll
		h.Runner.SetLearningLoop(ll)

		pc := executor.NewPatternContext(store)
		h.PatternContext = pc
		h.Runner.SetPatternContext(pc)
	}

	// Autopilot controller
	apCfg := cfg.Orchestrator.Autopilot
	if apCfg == nil {
		apCfg = autopilot.DefaultConfig()
	}
	approvalMgr := approval.NewManager(cfg.Approval)
	ctrl := autopilot.NewController(apCfg, h.GHClient, approvalMgr, "test-owner", "test-repo")
	h.Controller = ctrl

	// Wire learning loop to controller (gateway path, mirrors polling)
	if h.LearningLoop != nil {
		ctrl.SetLearningLoop(h.LearningLoop)
	}

	// State store
	if _, err := autopilot.NewStateStore(store.DB()); err != nil {
		t.Fatalf("NewStateStore: %v", err)
	}

	// Budget per-task limiter
	if cfg.Budget != nil && cfg.Budget.Enabled {
		h.Runner.SetTokenLimitCheck(func(taskID string, deltaInput, deltaOutput int64) bool {
			return true
		})
	}

	// OnSubIssuePRCreated callback → autopilot
	h.Runner.SetOnSubIssuePRCreated(ctrl.OnPRCreated)

	return h
}

// DataPath returns a stable data path for test artifacts within t.TempDir().
func DataPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "pilot-test-data")
}
