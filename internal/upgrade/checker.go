package upgrade

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// DefaultCheckInterval is the default interval between version checks
const DefaultCheckInterval = 5 * time.Minute

// VersionChecker periodically checks for new versions in the background
type VersionChecker struct {
	currentVersion string
	checkInterval  time.Duration
	onUpdate       func(info *VersionInfo)

	// staleThreshold and onStale implement the GH-3790 fail-loud check: if
	// the daemon falls this many releases behind, self-upgrade has likely
	// stopped firing (root cause: it previously only ran when a human
	// pressed 'u' in the TUI) and someone needs to notice. 0 disables it.
	staleThreshold int
	onStale        func(info *VersionInfo)

	mu          sync.RWMutex
	latestInfo  *VersionInfo
	lastCheck   time.Time
	isRunning   bool
	stopCh      chan struct{}
	isHomebrew  bool
	homebrewErr error
}

// NewVersionChecker creates a new VersionChecker instance
func NewVersionChecker(currentVersion string, interval time.Duration) *VersionChecker {
	if interval == 0 {
		interval = DefaultCheckInterval
	}

	vc := &VersionChecker{
		currentVersion: currentVersion,
		checkInterval:  interval,
		stopCh:         make(chan struct{}),
	}

	// Pre-check for Homebrew installation
	// We do this once at startup to avoid repeated error messages
	_, err := NewUpgrader(currentVersion)
	if err != nil {
		vc.isHomebrew = true
		vc.homebrewErr = err
	}

	return vc
}

// OnUpdate sets the callback function that's called when an update is available
func (c *VersionChecker) OnUpdate(fn func(info *VersionInfo)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onUpdate = fn
}

// SetStaleThreshold configures the fail-loud staleness check (GH-3790): once
// the daemon is at least n releases behind, each check logs a WARN and (if
// OnStale is set) fires a callback so an alert can be raised. n <= 0 disables
// the check (the default).
func (c *VersionChecker) SetStaleThreshold(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.staleThreshold = n
}

// OnStale sets the callback invoked when the staleness threshold is met or
// exceeded. Called on every check while the daemon remains stale, so callers
// that want deduplication/cooldown should implement it in fn.
func (c *VersionChecker) OnStale(fn func(info *VersionInfo)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onStale = fn
}

// Start begins periodic version checking in the background
func (c *VersionChecker) Start(ctx context.Context) {
	c.mu.Lock()
	if c.isRunning {
		c.mu.Unlock()
		return
	}
	c.isRunning = true
	c.stopCh = make(chan struct{})
	c.mu.Unlock()

	go c.run(ctx)
}

// Stop stops the background version checker
func (c *VersionChecker) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.isRunning {
		return
	}

	close(c.stopCh)
	c.isRunning = false
}

// run is the main loop for background checking
func (c *VersionChecker) run(ctx context.Context) {
	// Check immediately on start
	c.check(ctx)

	ticker := time.NewTicker(c.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.check(ctx)
		}
	}
}

// check performs a single version check
func (c *VersionChecker) check(ctx context.Context) {
	// Skip if Homebrew installation
	if c.isHomebrew {
		slog.Debug("version check skipped: Homebrew installation")
		return
	}

	upgrader, err := NewUpgrader(c.currentVersion)
	if err != nil {
		slog.Debug("version check failed: could not create upgrader", slog.Any("error", err))
		return
	}

	// Use a shorter timeout for background checks
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	info, err := upgrader.CheckVersion(checkCtx)
	if err != nil {
		slog.Debug("version check failed", slog.Any("error", err))
		return
	}

	c.mu.Lock()
	c.latestInfo = info
	c.lastCheck = time.Now()
	c.mu.Unlock()

	c.evaluate(info)
}

// evaluate fires the OnUpdate/OnStale callbacks for a freshly fetched
// VersionInfo. Split out from check() so the callback-decision logic (which
// doesn't touch the network) can be unit-tested directly with a synthetic
// VersionInfo, instead of only indirectly via a live GitHub API call.
func (c *VersionChecker) evaluate(info *VersionInfo) {
	c.mu.RLock()
	callback := c.onUpdate
	staleThreshold := c.staleThreshold
	staleCallback := c.onStale
	c.mu.RUnlock()

	if info.UpdateAvail && callback != nil {
		slog.Info("update available",
			slog.String("current", info.Current),
			slog.String("latest", info.Latest),
		)
		callback(info)
	}

	// GH-3790: fail loud instead of letting staleness go unnoticed the way
	// it did for 7+ hours across 8 releases — self-upgrade previously had no
	// automatic trigger at all, so nothing ever surfaced how far behind the
	// daemon had drifted.
	if staleThreshold > 0 && info.ReleasesBehind >= staleThreshold {
		slog.Warn("daemon is running a stale release — self-upgrade may not be firing",
			slog.String("current", info.Current),
			slog.String("latest", info.Latest),
			slog.Int("releases_behind", info.ReleasesBehind),
			slog.Int("stale_threshold", staleThreshold),
		)
		if staleCallback != nil {
			staleCallback(info)
		}
	}
}

// CheckNow performs an immediate version check and returns the result
func (c *VersionChecker) CheckNow(ctx context.Context) (*VersionInfo, error) {
	if c.isHomebrew {
		return nil, c.homebrewErr
	}

	upgrader, err := NewUpgrader(c.currentVersion)
	if err != nil {
		return nil, err
	}

	return upgrader.CheckVersion(ctx)
}

// GetLatestInfo returns the most recent version info from the last check
func (c *VersionChecker) GetLatestInfo() *VersionInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.latestInfo
}

// LastCheck returns the time of the last version check
func (c *VersionChecker) LastCheck() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastCheck
}

// IsHomebrew returns true if this is a Homebrew installation
func (c *VersionChecker) IsHomebrew() bool {
	return c.isHomebrew
}

// GetHomebrewError returns the Homebrew detection error if applicable
func (c *VersionChecker) GetHomebrewError() error {
	return c.homebrewErr
}
