package jira

import (
	"context"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters"
)

// AdapterName is the registry key for the Jira adapter.
const AdapterName = "jira"

// defaultPollingInterval is the default interval between Jira poll cycles.
const defaultPollingInterval = 30 * time.Second

// JiraAdapter implements adapters.Adapter and wraps the existing Jira
// client/poller/webhook to conform to the common adapter interface.
type JiraAdapter struct {
	config *Config
	client *Client
}

// NewAdapter creates a JiraAdapter from the given config.
func NewAdapter(cfg *Config) *JiraAdapter {
	client := NewClient(cfg.BaseURL, cfg.Username, cfg.APIToken, cfg.Platform)
	return &JiraAdapter{config: cfg, client: client}
}

func (a *JiraAdapter) Name() string { return AdapterName }

// CreatePoller creates a Jira poller using shared PollerDeps and a
// Jira-specific issue handler callback.
func (a *JiraAdapter) CreatePoller(deps adapters.PollerDeps, onIssue func(ctx context.Context, issue *Issue) (*IssueResult, error)) *Poller {
	interval := defaultPollingInterval
	if a.config.Polling != nil && a.config.Polling.Interval > 0 {
		interval = a.config.Polling.Interval
	}

	opts := []PollerOption{
		WithOnJiraIssue(onIssue),
	}

	if deps.ProcessedStore != nil {
		opts = append(opts, WithProcessedStore(&genericStoreShim{
			store:   deps.ProcessedStore,
			adapter: AdapterName,
		}))
	}

	if deps.MaxConcurrent > 0 {
		opts = append(opts, WithMaxConcurrent(deps.MaxConcurrent))
	}

	return NewPoller(a.client, a.config, interval, opts...)
}

// Client returns the underlying Jira API client.
func (a *JiraAdapter) Client() *Client { return a.client }

// Config returns the adapter configuration.
func (a *JiraAdapter) Config() *Config { return a.config }

// PollingEnabled returns whether polling is configured and enabled.
func (a *JiraAdapter) PollingEnabled() bool {
	return a.config.Polling != nil && a.config.Polling.Enabled
}

// genericStoreShim bridges adapters.ProcessedStore to the Jira-specific ProcessedStore interface.
// This allows the Jira poller to use the generic adapter_processed table without
// changing the poller's internal ProcessedStore interface.
type genericStoreShim struct {
	store   adapters.ProcessedStore
	adapter string
}

func (s *genericStoreShim) MarkJiraIssueProcessed(issueKey string, result string) error {
	return s.store.MarkAdapterProcessed(s.adapter, issueKey, result)
}

func (s *genericStoreShim) UnmarkJiraIssueProcessed(issueKey string) error {
	return s.store.UnmarkAdapterProcessed(s.adapter, issueKey)
}

func (s *genericStoreShim) IsJiraIssueProcessed(issueKey string) (bool, error) {
	return s.store.IsAdapterProcessed(s.adapter, issueKey)
}

func (s *genericStoreShim) LoadJiraProcessedIssues() (map[string]bool, error) {
	return s.store.LoadAdapterProcessed(s.adapter)
}
