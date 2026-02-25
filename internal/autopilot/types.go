package autopilot

import (
	"fmt"
	"time"
)

// Environment defines deployment environment behavior.
// Different environments have different levels of automation and approval requirements.
type Environment string

const (
	// EnvDev is the development environment with auto-merge, no approval required.
	EnvDev Environment = "dev"
	// EnvStage is the staging environment with auto-merge after CI passes.
	EnvStage Environment = "stage"
	// EnvProd is the production environment requiring human approval.
	EnvProd Environment = "prod"
)

// ApprovalSource specifies which channel to use for approval requests.
type ApprovalSource string

const (
	// ApprovalSourceTelegram uses Telegram for approval requests.
	ApprovalSourceTelegram ApprovalSource = "telegram"
	// ApprovalSourceSlack uses Slack for approval requests.
	ApprovalSourceSlack ApprovalSource = "slack"
	// ApprovalSourceGitHubReview uses GitHub PR reviews for approval.
	ApprovalSourceGitHubReview ApprovalSource = "github-review"
)

// GitHubReviewConfig holds configuration for GitHub PR review approval.
type GitHubReviewConfig struct {
	// PollInterval is how often to poll for PR reviews (default: 30s).
	PollInterval time.Duration `yaml:"poll_interval"`
}

// EnvironmentConfig defines a deployment pipeline for one target environment.
type EnvironmentConfig struct {
	// Branch is the target branch for PRs (e.g., "main", "develop").
	Branch string `yaml:"branch"`
	// RequireApproval gates merge on human approval.
	RequireApproval bool `yaml:"require_approval"`
	// ApprovalSource specifies which channel for approvals (telegram, slack, github-review).
	ApprovalSource ApprovalSource `yaml:"approval_source,omitempty"`
	// ApprovalTimeout is how long to wait for human approval.
	ApprovalTimeout time.Duration `yaml:"approval_timeout,omitempty"`
	// CITimeout overrides the CI wait timeout for this environment.
	CITimeout time.Duration `yaml:"ci_timeout"`
	// SkipPostMergeCI skips post-merge CI monitoring (fast path).
	SkipPostMergeCI bool `yaml:"skip_post_merge_ci"`
	// MergeMethod overrides the default merge method for this environment.
	MergeMethod string `yaml:"merge_method,omitempty"`
	// PostMerge defines what happens after merge (deployment trigger).
	PostMerge *PostMergeConfig `yaml:"post_merge,omitempty"`
	// Release holds per-environment release configuration.
	Release *ReleaseConfig `yaml:"release,omitempty"`
}

// PostMergeConfig defines the deployment trigger action after PR merge.
type PostMergeConfig struct {
	// Action: "none", "tag", "webhook", "branch-push"
	Action string `yaml:"action"`
	// WebhookURL for action "webhook".
	WebhookURL string `yaml:"webhook_url,omitempty"`
	// WebhookHeaders for action "webhook".
	WebhookHeaders map[string]string `yaml:"webhook_headers,omitempty"`
	// WebhookSecret for action "webhook" HMAC signing.
	WebhookSecret string `yaml:"webhook_secret,omitempty"`
	// DeployBranch for action "branch-push".
	DeployBranch string `yaml:"deploy_branch,omitempty"`
}

// Config holds autopilot configuration for automated PR handling.
type Config struct {
	// Enabled controls whether autopilot mode is active.
	Enabled bool `yaml:"enabled"`
	// Environment determines the automation level (dev/stage/prod).
	// DEPRECATED: use Environments map + DefaultEnvironment instead.
	Environment Environment `yaml:"environment,omitempty"`

	// DefaultEnvironment is the name of the environment used when --env is not specified.
	DefaultEnvironment string `yaml:"default_environment,omitempty"`
	// Environments is a map of named environment pipeline configs.
	Environments map[string]*EnvironmentConfig `yaml:"environments,omitempty"`

	// Runtime fields (not serialized to YAML).
	activeEnvName   string
	activeEnvConfig *EnvironmentConfig

	// Approval
	// ApprovalSource specifies which channel to use for approvals (telegram, slack, github-review).
	ApprovalSource ApprovalSource `yaml:"approval_source"`
	// GitHubReview holds configuration for GitHub PR review approval.
	GitHubReview *GitHubReviewConfig `yaml:"github_review"`

	// PR Handling
	// AutoReview enables automatic PR review comments.
	AutoReview bool `yaml:"auto_review"`
	// AutoMerge enables automatic PR merging when conditions are met.
	AutoMerge bool `yaml:"auto_merge"`
	// MergeMethod specifies how to merge PRs: merge, squash, or rebase.
	MergeMethod string `yaml:"merge_method"`

	// CI Monitoring
	// CIWaitTimeout is the maximum time to wait for CI to complete.
	CIWaitTimeout time.Duration `yaml:"ci_wait_timeout"`
	// DevCITimeout is the CI timeout for dev environment (default 5m, shorter than stage/prod).
	DevCITimeout time.Duration `yaml:"dev_ci_timeout"`
	// CIPollInterval is how often to check CI status.
	CIPollInterval time.Duration `yaml:"ci_poll_interval"`
	// RequiredChecks lists CI checks that must pass before merge.
	// Deprecated: Use CIChecks.Required instead.
	RequiredChecks []string `yaml:"required_checks"`
	// CIChecks holds CI check discovery configuration.
	CIChecks *CIChecksConfig `yaml:"ci_checks"`

	// Feedback Loop
	// AutoCreateIssues enables automatic issue creation for CI failures.
	AutoCreateIssues bool `yaml:"auto_create_issues"`
	// IssueLabels are labels applied to auto-created issues.
	IssueLabels []string `yaml:"issue_labels"`
	// NotifyOnFailure enables notifications when CI fails.
	NotifyOnFailure bool `yaml:"notify_on_failure"`

	// Safety
	// MaxFailures is the circuit breaker threshold before pausing autopilot.
	MaxFailures int `yaml:"max_failures"`
	// MaxCIFixIterations limits how many CI fix issues can be chained before giving up.
	// Prevents infinite fix cascades where each fix creates a new issue that also fails CI.
	// Default: 3. Set to 0 to disable the limit.
	MaxCIFixIterations int `yaml:"max_ci_fix_iterations"`
	// FailureResetTimeout is how long after the last failure before the per-PR counter resets.
	// Default: 30 minutes.
	FailureResetTimeout time.Duration `yaml:"failure_reset_timeout"`
	// MaxMergesPerHour limits merge rate to prevent runaway automation.
	MaxMergesPerHour int `yaml:"max_merges_per_hour"`
	// ApprovalTimeout is how long to wait for human approval in prod.
	ApprovalTimeout time.Duration `yaml:"approval_timeout"`

	// Release holds auto-release configuration.
	Release *ReleaseConfig `yaml:"release"`

	// MergedPRScanWindow is how far back to look for merged PRs on startup (default: 30m).
	// This catches PRs that were merged while Pilot was offline.
	MergedPRScanWindow time.Duration `yaml:"merged_pr_scan_window"`

	// Name is a user-friendly label for this environment (e.g. "staging", "production").
	// When empty, defaults to the Environment value.
	Name string `yaml:"name"`
}

// CIChecksConfig holds configuration for CI check monitoring.
type CIChecksConfig struct {
	// Mode: "auto" (discover from API) or "manual" (use Required list).
	Mode string `yaml:"mode"`

	// Exclude lists check names to ignore in auto mode (supports glob patterns).
	Exclude []string `yaml:"exclude"`

	// Required lists check names for manual mode.
	Required []string `yaml:"required"`

	// DiscoveryGracePeriod: how long to wait for checks to appear (default 60s).
	DiscoveryGracePeriod time.Duration `yaml:"discovery_grace_period"`
}

// defaultEnvironments returns built-in environment configs matching legacy behavior.
func defaultEnvironments() map[string]*EnvironmentConfig {
	return map[string]*EnvironmentConfig{
		"dev": {
			Branch:          "main",
			RequireApproval: false,
			CITimeout:       5 * time.Minute,
			SkipPostMergeCI: true,
			PostMerge:       &PostMergeConfig{Action: "none"},
		},
		"stage": {
			Branch:          "main",
			RequireApproval: false,
			CITimeout:       30 * time.Minute,
			SkipPostMergeCI: false,
			PostMerge:       &PostMergeConfig{Action: "none"},
		},
		"prod": {
			Branch:          "main",
			RequireApproval: true,
			ApprovalSource:  ApprovalSourceTelegram,
			ApprovalTimeout: 1 * time.Hour,
			CITimeout:       30 * time.Minute,
			SkipPostMergeCI: false,
			PostMerge:       &PostMergeConfig{Action: "tag"},
		},
	}
}

// ResolvedEnv returns the active environment config.
// If activeEnvName is set and the Environments map contains it, that entry is returned.
// Otherwise falls back to the legacy Environment field and synthesizes from defaultEnvironments.
func (c *Config) ResolvedEnv() *EnvironmentConfig {
	// New-style: runtime-selected environment takes priority.
	if c.activeEnvName != "" {
		if c.activeEnvConfig != nil {
			return c.activeEnvConfig
		}
		if c.Environments != nil {
			if env, ok := c.Environments[c.activeEnvName]; ok {
				return env
			}
		}
	}

	// Legacy: derive from the Environment field using built-in defaults.
	envName := string(c.Environment)
	if envName == "" {
		envName = "stage"
	}
	defaults := defaultEnvironments()
	if env, ok := defaults[envName]; ok {
		return env
	}
	// Unknown legacy environment: treat as stage (safe default).
	return defaults["stage"]
}

// EnvironmentName returns the human-readable active environment name.
// Checks Name field first (user-friendly label), then activeEnvName,
// then falls back to the Environment enum value.
func (c *Config) EnvironmentName() string {
	if c.Name != "" {
		return c.Name
	}
	if c.activeEnvName != "" {
		return c.activeEnvName
	}
	if c.Environment != "" {
		return string(c.Environment)
	}
	return "stage"
}

// SetActiveEnvironment sets the runtime-resolved environment by name.
// Checks the Environments map first, then falls back to built-in defaults.
// Called during CLI flag processing.
func (c *Config) SetActiveEnvironment(name string) error {
	// New-style: check user-defined Environments map first.
	if c.Environments != nil {
		if env, ok := c.Environments[name]; ok {
			c.activeEnvName = name
			c.activeEnvConfig = env
			c.Environment = Environment(name) // keep legacy field in sync
			return nil
		}
	}

	// Fall back to built-in defaults.
	defaults := defaultEnvironments()
	if env, ok := defaults[name]; ok {
		c.activeEnvName = name
		c.activeEnvConfig = env
		c.Environment = Environment(name) // keep legacy field in sync
		return nil
	}

	return fmt.Errorf("unknown environment %q: must be one of dev, stage, prod or defined in environments config", name)
}

// DefaultConfig returns sensible defaults for autopilot configuration.
func DefaultConfig() *Config {
	return &Config{
		Enabled:        false,
		Environment:    EnvStage,
		ApprovalSource: ApprovalSourceTelegram, // Default to Telegram for backward compatibility
		GitHubReview: &GitHubReviewConfig{
			PollInterval: 30 * time.Second,
		},
		AutoReview:     true,
		AutoMerge:      true,
		MergeMethod:    "squash",
		CIWaitTimeout:  30 * time.Minute,
		DevCITimeout:   5 * time.Minute,
		CIPollInterval: 30 * time.Second,
		RequiredChecks: nil, // Deprecated, use CIChecks
		CIChecks: &CIChecksConfig{
			Mode:                 "auto",
			Exclude:              []string{},
			Required:             []string{},
			DiscoveryGracePeriod: 60 * time.Second,
		},
		AutoCreateIssues:    true,
		IssueLabels:         []string{"pilot", "autopilot-fix"},
		NotifyOnFailure:     true,
		MaxFailures:         3,
		MaxCIFixIterations:  3,
		FailureResetTimeout: 30 * time.Minute,
		MaxMergesPerHour:    10,
		ApprovalTimeout:     1 * time.Hour,
		Release:             nil, // Disabled by default
		MergedPRScanWindow:  30 * time.Minute,
		Environments:        defaultEnvironments(),
	}
}

// ReleaseConfig holds configuration for automatic release creation.
type ReleaseConfig struct {
	// Enabled controls whether auto-release is active.
	Enabled bool `yaml:"enabled"`
	// Trigger determines when to release: "on_merge" or "manual".
	Trigger string `yaml:"trigger"`
	// VersionStrategy determines how to bump version: "conventional_commits" or "pr_labels".
	VersionStrategy string `yaml:"version_strategy"`
	// TagPrefix is prepended to version (default "v").
	TagPrefix string `yaml:"tag_prefix"`
	// GenerateChangelog enables changelog generation from commits.
	GenerateChangelog bool `yaml:"generate_changelog"`
	// NotifyOnRelease sends notification when release is created.
	NotifyOnRelease bool `yaml:"notify_on_release"`
	// RequireCI waits for post-merge CI before releasing.
	RequireCI bool `yaml:"require_ci"`
}

// DefaultReleaseConfig returns sensible defaults for release configuration.
func DefaultReleaseConfig() *ReleaseConfig {
	return &ReleaseConfig{
		Enabled:           false,
		Trigger:           "on_merge",
		VersionStrategy:   "conventional_commits",
		TagPrefix:         "v",
		GenerateChangelog: true,
		NotifyOnRelease:   true,
		RequireCI:         true,
	}
}

// PRStage represents stages in the PR lifecycle.
type PRStage string

const (
	// StagePRCreated indicates a PR has been created and is ready for processing.
	StagePRCreated PRStage = "pr_created"
	// StageWaitingCI indicates the PR is waiting for CI checks to complete.
	StageWaitingCI PRStage = "waiting_ci"
	// StageCIPassed indicates all CI checks have passed.
	StageCIPassed PRStage = "ci_passed"
	// StageCIFailed indicates one or more CI checks have failed.
	StageCIFailed PRStage = "ci_failed"
	// StageAwaitApproval indicates the PR is waiting for human approval.
	StageAwaitApproval PRStage = "awaiting_approval"
	// StageMerging indicates the PR is being merged.
	StageMerging PRStage = "merging"
	// StageMerged indicates the PR has been successfully merged.
	StageMerged PRStage = "merged"
	// StagePostMergeCI indicates post-merge CI is running on main branch.
	StagePostMergeCI PRStage = "post_merge_ci"
	// StageReleasing indicates the PR is triggering an automatic release.
	StageReleasing PRStage = "releasing"
	// StageFailed indicates the PR pipeline has failed and requires intervention.
	StageFailed PRStage = "failed"
)

// CIStatus represents the current CI check state.
type CIStatus string

const (
	// CIPending indicates CI checks have not started yet.
	CIPending CIStatus = "pending"
	// CIRunning indicates CI checks are currently executing.
	CIRunning CIStatus = "running"
	// CISuccess indicates all CI checks have passed.
	CISuccess CIStatus = "success"
	// CIFailure indicates one or more CI checks have failed.
	CIFailure CIStatus = "failure"
)

// BumpType represents semantic version bump types.
type BumpType string

const (
	// BumpNone indicates no version bump is needed.
	BumpNone BumpType = "none"
	// BumpPatch indicates a patch version bump (bug fixes).
	BumpPatch BumpType = "patch"
	// BumpMinor indicates a minor version bump (new features).
	BumpMinor BumpType = "minor"
	// BumpMajor indicates a major version bump (breaking changes).
	BumpMajor BumpType = "major"
)

// ShortSHA returns a short version of a SHA, safely handling short strings.
func ShortSHA(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

// PRState tracks a PR through the autopilot pipeline.
type PRState struct {
	// PRNumber is the GitHub PR number.
	PRNumber int
	// PRURL is the full URL to the PR.
	PRURL string
	// IssueNumber is the linked issue number (if any).
	IssueNumber int
	// BranchName is the head branch of the PR (e.g. "pilot/GH-123").
	BranchName string
	// HeadSHA is the commit SHA at the head of the PR.
	HeadSHA string
	// Stage is the current stage in the PR lifecycle.
	Stage PRStage
	// CIStatus is the current CI check status.
	CIStatus CIStatus
	// LastChecked is when the PR status was last polled.
	LastChecked time.Time
	// CIWaitStartedAt is when CI monitoring started (for timeout tracking).
	CIWaitStartedAt time.Time
	// MergeAttempts counts how many times merge has been attempted.
	MergeAttempts int
	// Error holds the last error message if Stage is StageFailed.
	Error string
	// CreatedAt is when the PR entered the autopilot pipeline.
	CreatedAt time.Time
	// ReleaseVersion is the version that was released (if any).
	ReleaseVersion string
	// ReleaseBumpType is the detected bump type from commits.
	ReleaseBumpType BumpType
	// DiscoveredChecks holds check names found in auto mode.
	DiscoveredChecks []string
	// ConsecutiveAPIFailures counts consecutive CI check API failures.
	ConsecutiveAPIFailures int
	// EnvironmentName is the user-friendly environment label (e.g. "staging").
	EnvironmentName string
	// PRTitle is the title of the pull request.
	PRTitle string
	// TargetBranch is the base branch the PR merges into (e.g. "main").
	TargetBranch string
	// IssueNodeID is the GraphQL global node ID of the linked issue, used for board sync.
	IssueNodeID string
}
