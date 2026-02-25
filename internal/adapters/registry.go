package adapters

import (
	"context"
	"sync"
)

// Adapter is the common interface all ticket-source adapters implement.
// New adapters register via Register() in their init() function.
type Adapter interface {
	// Name returns the adapter identifier (e.g. "jira", "linear", "github").
	Name() string
}

// Pollable is implemented by adapters that support polling for new issues.
type Pollable interface {
	Adapter

	// NewPoller creates a poller for this adapter using the given options.
	NewPoller(opts PollerDeps) Poller
}

// WebhookCapable is implemented by adapters that can receive webhooks.
type WebhookCapable interface {
	Adapter

	// WebhookSource returns the source key for webhook routing (e.g. "jira").
	WebhookSource() string
}

// Poller abstracts a polling loop that discovers new issues.
type Poller interface {
	Start(ctx context.Context) error
}

// IssueEvent is the normalized issue event emitted by all adapters.
type IssueEvent struct {
	Action    string   // "created", "updated"
	IssueID   string   // Adapter-specific ID (issue key, number, GID, etc.)
	Title     string
	Body      string
	Labels    []string
	ProjectID string
}

// IssueResult is the normalized result from processing an issue.
type IssueResult struct {
	Success    bool
	PRNumber   int
	PRURL      string
	HeadSHA    string
	BranchName string
	Error      error
}

// ProcessedStore is the generic interface for tracking which issues
// have been processed across restarts. It uses string IDs for all adapters;
// integer-based adapters convert their IDs to strings.
type ProcessedStore interface {
	MarkAdapterProcessed(adapter, issueID, result string) error
	UnmarkAdapterProcessed(adapter, issueID string) error
	IsAdapterProcessed(adapter, issueID string) (bool, error)
	LoadAdapterProcessed(adapter string) (map[string]bool, error)
}

// PollerDeps provides shared infrastructure to adapter pollers.
type PollerDeps struct {
	ProcessedStore ProcessedStore
	MaxConcurrent  int
	OnPRCreated    func(prNumber int, prURL string, issueNumber int, headSHA, branchName string)
}

// --- Registry ---

var (
	registryMu sync.RWMutex
	registry   = map[string]Adapter{}
)

// Register adds an adapter to the global registry.
// Typically called from an adapter package's init() function.
func Register(a Adapter) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[a.Name()] = a
}

// Get returns a registered adapter by name, or nil if not found.
func Get(name string) Adapter {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[name]
}

// All returns a copy of all registered adapters.
func All() map[string]Adapter {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make(map[string]Adapter, len(registry))
	for k, v := range registry {
		out[k] = v
	}
	return out
}

// Reset clears the registry. Used for testing only.
func Reset() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = map[string]Adapter{}
}
