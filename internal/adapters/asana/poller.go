package asana

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// Status tags for tracking task progress
const (
	TagInProgress = "pilot-in-progress"
	TagDone       = "pilot-done"
	TagFailed     = "pilot-failed"
)

// TaskResult is returned by the task handler
type TaskResult struct {
	Success    bool
	PRNumber   int
	PRURL      string
	HeadSHA    string // Head commit SHA of the PR (GH-1398: for autopilot wiring)
	BranchName string // Head branch name e.g. "pilot/TASK-123" (GH-1398: for autopilot wiring)
	Error      error
}

// ProcessedStore persists which Asana tasks have been processed across restarts.
// GH-1359: Asana uses string GIDs.
type ProcessedStore interface {
	MarkAsanaTaskProcessed(taskGID string, result string) error
	UnmarkAsanaTaskProcessed(taskGID string) error
	IsAsanaTaskProcessed(taskGID string) (bool, error)
	LoadAsanaProcessedTasks() (map[string]bool, error)
}

// Poller polls Asana for tasks with the pilot tag
type Poller struct {
	client    *Client
	config    *Config
	interval  time.Duration
	processed map[string]bool // Asana uses string GIDs
	mu        sync.RWMutex
	onTask    func(ctx context.Context, task *Task) (*TaskResult, error)
	logger    *slog.Logger

	// Tag GID cache
	pilotTagGID      string
	inProgressTagGID string
	doneTagGID       string
	failedTagGID     string

	// GH-1359: Persistent processed store (optional)
	processedStore ProcessedStore

	// GH-1359: Parallel execution configuration
	maxConcurrent int
	semaphore     chan struct{}
	activeWg      sync.WaitGroup
	stopping      atomic.Bool
	wgMu          sync.Mutex // protects stopping + activeWg Add/Wait coordination
}

// PollerOption configures a Poller
type PollerOption func(*Poller)

// WithOnAsanaTask sets the callback for new tasks
func WithOnAsanaTask(fn func(ctx context.Context, task *Task) (*TaskResult, error)) PollerOption {
	return func(p *Poller) {
		p.onTask = fn
	}
}

// WithAsanaPollerLogger sets the logger for the poller
func WithAsanaPollerLogger(logger *slog.Logger) PollerOption {
	return func(p *Poller) {
		p.logger = logger
	}
}

// WithProcessedStore sets the persistent store for processed task tracking.
// GH-1359: On startup, processed tasks are loaded from the store to prevent re-processing after hot upgrade.
func WithProcessedStore(store ProcessedStore) PollerOption {
	return func(p *Poller) {
		p.processedStore = store
	}
}

// WithMaxConcurrent sets the maximum number of parallel task executions.
// GH-1359: Ported parallel execution pattern from other adapters.
func WithMaxConcurrent(n int) PollerOption {
	return func(p *Poller) {
		if n < 1 {
			n = 1
		}
		p.maxConcurrent = n
	}
}

// NewPoller creates a new Asana task poller
func NewPoller(client *Client, config *Config, interval time.Duration, opts ...PollerOption) *Poller {
	p := &Poller{
		client:    client,
		config:    config,
		interval:  interval,
		processed: make(map[string]bool),
		logger:    logging.WithComponent("asana-poller"),
	}

	for _, opt := range opts {
		opt(p)
	}

	// GH-1359: Load processed tasks from persistent store if available
	if p.processedStore != nil {
		loaded, err := p.processedStore.LoadAsanaProcessedTasks()
		if err != nil {
			p.logger.Warn("Failed to load processed tasks from store", slog.Any("error", err))
		} else if len(loaded) > 0 {
			p.mu.Lock()
			for gid := range loaded {
				p.processed[gid] = true
			}
			p.mu.Unlock()
			p.logger.Info("Loaded processed tasks from store", slog.Int("count", len(loaded)))
		}
	}

	// GH-1359: Initialize parallel semaphore
	if p.maxConcurrent < 1 {
		p.maxConcurrent = 2 // default
	}
	p.semaphore = make(chan struct{}, p.maxConcurrent)

	return p
}

// Start begins polling for tasks
func (p *Poller) Start(ctx context.Context) error {
	// Cache tag GIDs on startup
	if err := p.cacheTagGIDs(ctx); err != nil {
		return fmt.Errorf("failed to cache tag GIDs: %w", err)
	}

	p.logger.Info("Starting Asana poller",
		slog.String("workspace", p.client.workspaceID),
		slog.String("tag", p.config.PilotTag),
		slog.Duration("interval", p.interval),
		slog.Int("max_concurrent", p.maxConcurrent),
	)

	// GH-1355: Recover orphaned in-progress tasks from previous run before starting poll loop
	p.recoverOrphanedTasks(ctx)

	// Initial check
	p.checkForNewTasks(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("Asana poller stopping, waiting for active tasks...")
			p.wgMu.Lock()
			p.stopping.Store(true)
			p.wgMu.Unlock()
			p.activeWg.Wait()
			p.logger.Info("Asana poller stopped")
			return nil
		case <-ticker.C:
			p.checkForNewTasks(ctx)
		}
	}
}

// cacheTagGIDs fetches and caches the GIDs for pilot-related tags
func (p *Poller) cacheTagGIDs(ctx context.Context) error {
	pilotTag := p.config.PilotTag
	if pilotTag == "" {
		pilotTag = "pilot"
	}

	// Find or create pilot tag
	tag, err := p.client.FindTagByName(ctx, pilotTag)
	if err != nil {
		return fmt.Errorf("failed to find pilot tag: %w", err)
	}
	if tag == nil {
		return fmt.Errorf("pilot tag %q not found in workspace", pilotTag)
	}
	p.pilotTagGID = tag.GID

	// Find status tags (optional - don't fail if not found)
	if tag, _ := p.client.FindTagByName(ctx, TagInProgress); tag != nil {
		p.inProgressTagGID = tag.GID
	}
	if tag, _ := p.client.FindTagByName(ctx, TagDone); tag != nil {
		p.doneTagGID = tag.GID
	}
	if tag, _ := p.client.FindTagByName(ctx, TagFailed); tag != nil {
		p.failedTagGID = tag.GID
	}

	p.logger.Debug("Cached tag GIDs",
		slog.String("pilot", p.pilotTagGID),
		slog.String("in_progress", p.inProgressTagGID),
		slog.String("done", p.doneTagGID),
		slog.String("failed", p.failedTagGID),
	)

	return nil
}

// recoverOrphanedTasks finds tasks with pilot-in-progress tag from a previous run
// and removes the tag so they can be picked up again.
// GH-1355: This handles restart/crash scenarios where tasks were left orphaned.
func (p *Poller) recoverOrphanedTasks(ctx context.Context) {
	if p.inProgressTagGID == "" {
		return
	}

	// Get tasks with in-progress tag
	tasks, err := p.client.GetActiveTasksByTag(ctx, p.inProgressTagGID)
	if err != nil {
		p.logger.Warn("Failed to check for orphaned tasks", slog.Any("error", err))
		return
	}

	if len(tasks) == 0 {
		return
	}

	p.logger.Info("Recovering orphaned in-progress tasks",
		slog.Int("count", len(tasks)),
	)

	for _, task := range tasks {
		if err := p.client.RemoveTag(ctx, task.GID, p.inProgressTagGID); err != nil {
			p.logger.Warn("Failed to remove in-progress tag from orphaned task",
				slog.String("gid", task.GID),
				slog.Any("error", err),
			)
			continue
		}
		p.logger.Info("Recovered orphaned task",
			slog.String("gid", task.GID),
			slog.String("name", task.Name),
		)
	}
}

func (p *Poller) checkForNewTasks(ctx context.Context) {
	// Get tasks with pilot tag
	tasks, err := p.client.GetActiveTasksByTag(ctx, p.pilotTagGID)
	if err != nil {
		p.logger.Warn("Failed to fetch tasks", slog.Any("error", err))
		return
	}

	// Sort by creation date (oldest first)
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})

	for _, task := range tasks {
		// Skip if already processed
		p.mu.RLock()
		processed := p.processed[task.GID]
		p.mu.RUnlock()

		if processed {
			continue
		}

		// Skip if has status tag (in-progress, done, or failed)
		if p.hasStatusTag(&task) {
			p.markProcessed(task.GID)
			continue
		}

		// Mark processed immediately to prevent duplicate dispatch on next tick
		p.markProcessed(task.GID)

		// Acquire semaphore slot (blocks if max_concurrent reached)
		select {
		case <-ctx.Done():
			return
		case p.semaphore <- struct{}{}:
		}

		p.logger.Info("Dispatching Asana task for parallel execution",
			slog.String("gid", task.GID),
			slog.String("name", task.Name),
		)

		// Use mutex to coordinate stopping flag check with WaitGroup Add
		p.wgMu.Lock()
		if p.stopping.Load() {
			p.wgMu.Unlock()
			<-p.semaphore // release slot we acquired
			return
		}
		p.activeWg.Add(1)
		p.wgMu.Unlock()

		go p.processTaskAsync(ctx, &task)
	}
}

// processTaskAsync handles a single task in a goroutine.
// GH-1359: Extracted to enable parallel execution.
func (p *Poller) processTaskAsync(ctx context.Context, task *Task) {
	defer p.activeWg.Done()
	defer func() { <-p.semaphore }() // release slot

	if p.onTask == nil {
		return
	}

	// Add in-progress tag
	if p.inProgressTagGID != "" {
		_ = p.client.AddTag(ctx, task.GID, p.inProgressTagGID)
	}

	result, err := p.onTask(ctx, task)
	if err != nil {
		p.logger.Error("Failed to process task",
			slog.String("gid", task.GID),
			slog.Any("error", err),
		)
		// Remove in-progress tag, add failed tag
		if p.inProgressTagGID != "" {
			_ = p.client.RemoveTag(ctx, task.GID, p.inProgressTagGID)
		}
		if p.failedTagGID != "" {
			_ = p.client.AddTag(ctx, task.GID, p.failedTagGID)
		}
		return
	}

	// Remove in-progress tag
	if p.inProgressTagGID != "" {
		_ = p.client.RemoveTag(ctx, task.GID, p.inProgressTagGID)
	}

	// Add done tag on success
	if result != nil && result.Success && p.doneTagGID != "" {
		_ = p.client.AddTag(ctx, task.GID, p.doneTagGID)
	}
}

// hasStatusTag checks if task has any status tag
func (p *Poller) hasStatusTag(task *Task) bool {
	return p.hasTag(task, TagInProgress) ||
		p.hasTag(task, TagDone) ||
		p.hasTag(task, TagFailed)
}

// hasTag checks if task has a specific tag by name (case-insensitive)
func (p *Poller) hasTag(task *Task, tagName string) bool {
	for _, tag := range task.Tags {
		if strings.EqualFold(tag.Name, tagName) {
			return true
		}
	}
	return false
}

func (p *Poller) markProcessed(gid string) {
	p.mu.Lock()
	p.processed[gid] = true
	p.mu.Unlock()

	// GH-1359: Persist to store if available
	if p.processedStore != nil {
		if err := p.processedStore.MarkAsanaTaskProcessed(gid, "processed"); err != nil {
			p.logger.Warn("Failed to persist processed task", slog.String("gid", gid), slog.Any("error", err))
		}
	}
}

// IsProcessed checks if a task has been processed
func (p *Poller) IsProcessed(gid string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.processed[gid]
}

// ProcessedCount returns the number of processed tasks
func (p *Poller) ProcessedCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.processed)
}

// Reset clears the processed tasks map
func (p *Poller) Reset() {
	p.mu.Lock()
	p.processed = make(map[string]bool)
	p.mu.Unlock()
}

// ClearProcessed removes a single task from the processed map.
// GH-1359: Used when pilot-failed tag is removed to allow the task to be retried.
func (p *Poller) ClearProcessed(gid string) {
	p.mu.Lock()
	delete(p.processed, gid)
	p.mu.Unlock()

	// Also clear from persistent store
	if p.processedStore != nil {
		if err := p.processedStore.UnmarkAsanaTaskProcessed(gid); err != nil {
			p.logger.Warn("Failed to unmark task in store",
				slog.String("gid", gid),
				slog.Any("error", err))
		}
	}

	p.logger.Debug("Cleared task from processed map",
		slog.String("gid", gid))
}

// Drain stops accepting new tasks and waits for active executions to finish.
// GH-1359: Used during hot upgrade to let in-flight work complete before process restart.
func (p *Poller) Drain() {
	p.logger.Info("Draining poller — no new tasks will be accepted")
	p.wgMu.Lock()
	p.stopping.Store(true)
	p.wgMu.Unlock()
	p.activeWg.Wait()
	p.logger.Info("Poller drained — all active tasks completed")
}

// WaitForActive waits for all active parallel goroutines to finish.
// GH-1359: Used in tests to synchronize after checkForNewTasks.
func (p *Poller) WaitForActive() {
	p.wgMu.Lock()
	p.stopping.Store(true)
	p.wgMu.Unlock()
	p.activeWg.Wait()
}
