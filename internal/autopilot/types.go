package autopilot

import (
	"fmt"
	"strings"
	"sync"
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

	// Review Feedback
	// ReviewFeedback configures automatic handling of PR review change requests.
	ReviewFeedback *ReviewFeedbackConfig `yaml:"review_feedback"`

	// Safety
	// MaxFailures is the circuit breaker threshold before pausing autopilot.
	MaxFailures int `yaml:"max_failures"`
	// MaxCIFixIterations limits how many CI fix issues can be chained before giving up.
	// Prevents infinite fix cascades where each fix creates a new issue that also fails CI.
	// Default: 3. Set to 0 to disable the limit.
	MaxCIFixIterations int `yaml:"max_ci_fix_iterations"`
	// MaxCIFixPRSize is the net-addition threshold above which autopilot refuses to spawn
	// a fix(ci) issue for the failing PR. A large failing PR is a cascade-contamination
	// signal — the same threshold (#2594) that gates auto-merge also gates fix-issue spawn.
	// Default: 200. Set to 0 to disable the guard.
	MaxCIFixPRSize int `yaml:"max_ci_fix_pr_size"`
	// FailureResetTimeout is how long after the last failure before the per-PR counter resets.
	// Default: 30 minutes.
	FailureResetTimeout time.Duration `yaml:"failure_reset_timeout"`
	// MaxMergesPerHour limits merge rate to prevent runaway automation.
	MaxMergesPerHour int `yaml:"max_merges_per_hour"`
	// MaxMergeAttempts is the hard cap on non-conflict merge retries before the PR
	// is transitioned to StageFailed and escalated to a human. The circuit breaker
	// (MaxFailures) provides transient backoff; this cap makes persistent failures
	// terminal so they don't loop indefinitely. Default: 5.
	MaxMergeAttempts int `yaml:"max_merge_attempts"`
	// MaxRebaseAttempts is the hard cap on successful auto-rebases (GitHub
	// UpdatePullRequestBranch) for the same PR before autopilot stops
	// re-rebasing and escalates to StageFailed for human attention. Without
	// this cap a PR can cycle conflict -> rebase-success -> CI -> conflict
	// indefinitely, since a successful rebase consumes no other retry budget.
	// Default: 3.
	MaxRebaseAttempts int `yaml:"max_rebase_attempts"`
	// MaxReleasingAttempts is the hard cap on handleReleasing retries before the PR
	// is transitioned to StageFailed. Prevents a release that can never succeed (e.g.
	// persistent GitHub API errors or a tag creation race) from looping indefinitely.
	// Default: 10.
	MaxReleasingAttempts int `yaml:"max_releasing_attempts"`
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

// ReviewFeedbackConfig holds configuration for handling PR review change requests.
type ReviewFeedbackConfig struct {
	// Enabled controls whether review feedback handling is active.
	Enabled bool `yaml:"enabled"`
	// MaxIterations limits how many revision issues can be chained before giving up.
	// Prevents infinite review-fix cycles. Default: 3. Set to 0 to disable the limit.
	MaxIterations int `yaml:"max_iterations"`
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
		ReviewFeedback: &ReviewFeedbackConfig{
			Enabled:       true,
			MaxIterations: 3,
		},
		AutoCreateIssues:     true,
		IssueLabels:          []string{"pilot", "autopilot-fix"},
		NotifyOnFailure:      true,
		MaxFailures:          3,
		MaxCIFixIterations:   3,
		MaxCIFixPRSize:       200,
		FailureResetTimeout:  30 * time.Minute,
		MaxMergesPerHour:     10,
		MaxMergeAttempts:     5,
		MaxRebaseAttempts:    3,
		MaxReleasingAttempts: 10,
		ApprovalTimeout:      1 * time.Hour,
		Release:              nil, // Disabled by default
		MergedPRScanWindow:   30 * time.Minute,
		Environments:         defaultEnvironments(),
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
	// GenerateSummary enables LLM-generated release summary prepended to GoReleaser changelog.
	GenerateSummary bool `yaml:"generate_summary"`
	// Publish selects how releases are published: "workflow" (default — GoReleaser
	// via CI creates the GitHub Release), "api" (Pilot calls the GitHub Releases
	// API directly), or "tag_only" (push the tag, publish nothing). Empty
	// behaves like "workflow". See internal/config Config.Validate for the
	// enum check (GH-3930).
	Publish string `yaml:"publish,omitempty"`
	// VerifyRelease controls post-tag verification that a GitHub Release
	// actually appears (GH-3927). Nil defers to VerifyReleaseEnabled's
	// per-publish-mode default (true in "workflow" mode); explicit false
	// opts out regardless of mode.
	VerifyRelease *bool `yaml:"verify_release,omitempty"`
	// VerifyTimeout bounds how long post-tag verification polls for the
	// release to appear before firing a release_missing alert. Zero means
	// unset; DefaultReleaseConfig sets the 10m default (GH-3927).
	VerifyTimeout time.Duration `yaml:"verify_timeout,omitempty"`
	// TagHumanMerges opts ScanRecentlyMergedPRs into considering merged PRs
	// whose head branch is NOT pilot/* for release tagging. Default false —
	// zero behavior change for existing configs. Conventional-commit hygiene
	// (via DetectBumpType on the squash-merge PR title) still decides
	// whether a release is actually cut, so enabling this is safe even for
	// repos with mixed commit-message discipline (GH-3928).
	TagHumanMerges bool `yaml:"tag_human_merges,omitempty"`
}

// Publish mode values for ReleaseConfig.Publish / ProjectReleaseConfig.Publish (GH-3926).
const (
	// ReleasePublishWorkflow leaves publishing to the repo's own tag-triggered
	// CI (e.g. GoReleaser). This is the default when Publish is empty.
	ReleasePublishWorkflow = "workflow"
	// ReleasePublishAPI has Pilot publish the GitHub Release itself via the
	// REST API immediately after tagging.
	ReleasePublishAPI = "api"
	// ReleasePublishTagOnly pushes the tag and publishes nothing.
	ReleasePublishTagOnly = "tag_only"
)

// PublishMode returns the normalized publish mode, defaulting empty to
// ReleasePublishWorkflow so callers never need to special-case "". A nil
// receiver also returns the default. See internal/config Config.Validate for
// the enum check (GH-3930).
func (r *ReleaseConfig) PublishMode() string {
	if r == nil || r.Publish == "" {
		return ReleasePublishWorkflow
	}
	return r.Publish
}

// VerifyReleaseEnabled resolves whether post-tag release verification
// (GH-3927) should run. An explicit VerifyRelease always wins; an unset
// (nil) value defaults to true only in "workflow" publish mode, since that
// is the mode where the chain can break silently after the tag (a broken
// or missing release workflow). A nil receiver returns false.
func (r *ReleaseConfig) VerifyReleaseEnabled() bool {
	if r == nil {
		return false
	}
	if r.VerifyRelease != nil {
		return *r.VerifyRelease
	}
	return r.PublishMode() == ReleasePublishWorkflow
}

// ProjectReleaseConfig overlays release settings for a single project on top
// of the global and per-environment ReleaseConfig blocks. Unset fields
// inherit the base value — e.g. a project may override Publish while leaving
// Enabled nil to inherit the global setting. Trigger, VersionStrategy, and
// RequireCI are intentionally NOT overlayable here — they stay env/global-only
// (a project overriding when releases fire or how versions bump would make
// the release cadence inconsistent across repos sharing one autopilot config).
// See internal/config ProjectConfig.Release (GH-3930); overlay resolution
// (Apply) and controller wiring land here and in GH-3931.
type ProjectReleaseConfig struct {
	// Enabled overrides ReleaseConfig.Enabled for this project. Nil inherits.
	Enabled *bool `yaml:"enabled,omitempty"`
	// Publish overrides ReleaseConfig.Publish for this project. Empty inherits.
	Publish string `yaml:"publish,omitempty"`
	// TagPrefix overrides ReleaseConfig.TagPrefix for this project. Empty inherits.
	TagPrefix string `yaml:"tag_prefix,omitempty"`
	// GenerateChangelog overrides ReleaseConfig.GenerateChangelog for this project. Nil inherits.
	GenerateChangelog *bool `yaml:"generate_changelog,omitempty"`
	// NotifyOnRelease overrides ReleaseConfig.NotifyOnRelease for this project. Nil inherits.
	NotifyOnRelease *bool `yaml:"notify_on_release,omitempty"`
	// VerifyRelease overrides ReleaseConfig.VerifyRelease for this project. Nil inherits.
	VerifyRelease *bool `yaml:"verify_release,omitempty"`
	// VerifyTimeout overrides ReleaseConfig.VerifyTimeout for this project. Zero inherits.
	VerifyTimeout time.Duration `yaml:"verify_timeout,omitempty"`
	// TagHumanMerges overrides ReleaseConfig.TagHumanMerges for this project. Nil inherits.
	TagHumanMerges *bool `yaml:"tag_human_merges,omitempty"`
}

// Apply overlays this project-level config on top of base (the resolved
// global/environment ReleaseConfig), returning a new *ReleaseConfig with only
// the fields this overlay explicitly sets overridden. Trigger, VersionStrategy,
// and RequireCI always come from base — see the ProjectReleaseConfig doc.
//
// A nil receiver returns base unchanged (no overlay configured). When base is
// nil (no release configured at the env/global level), Apply returns nil
// unless the overlay itself turns releasing on (Enabled != nil && *Enabled),
// in which case it starts from DefaultReleaseConfig() — a project block
// consisting only of `release: { enabled: true, publish: api }` must work
// without a global release block. GH-3926/GH-3930.
func (p *ProjectReleaseConfig) Apply(base *ReleaseConfig) *ReleaseConfig {
	if p == nil {
		return base
	}

	var result ReleaseConfig
	switch {
	case base != nil:
		result = *base
	case p.Enabled != nil && *p.Enabled:
		result = *DefaultReleaseConfig()
	default:
		return nil
	}

	if p.Enabled != nil {
		result.Enabled = *p.Enabled
	}
	if p.Publish != "" {
		result.Publish = p.Publish
	}
	if p.TagPrefix != "" {
		result.TagPrefix = p.TagPrefix
	}
	if p.GenerateChangelog != nil {
		result.GenerateChangelog = *p.GenerateChangelog
	}
	if p.NotifyOnRelease != nil {
		result.NotifyOnRelease = *p.NotifyOnRelease
	}
	if p.VerifyRelease != nil {
		result.VerifyRelease = p.VerifyRelease
	}
	if p.VerifyTimeout != 0 {
		result.VerifyTimeout = p.VerifyTimeout
	}
	if p.TagHumanMerges != nil {
		result.TagHumanMerges = *p.TagHumanMerges
	}
	return &result
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
		GenerateSummary:   true,
		VerifyTimeout:     10 * time.Minute,
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
	// StageReviewRequested indicates a human reviewer requested changes on the PR.
	StageReviewRequested PRStage = "review_requested"
	// StageFailed indicates the PR pipeline has failed and requires intervention.
	StageFailed PRStage = "failed"
)

// AllPRStages returns every defined PRStage value. Used by the Prometheus exporter
// to emit zero-values for stages absent from the current snapshot, preventing
// Prometheus's 5-min lookback from holding stale non-zero values.
func AllPRStages() []PRStage {
	return []PRStage{
		StagePRCreated,
		StageWaitingCI,
		StageCIPassed,
		StageCIFailed,
		StageAwaitApproval,
		StageMerging,
		StageMerged,
		StagePostMergeCI,
		StageReleasing,
		StageReviewRequested,
		StageFailed,
	}
}

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

// PRState tracks the lifecycle state of a pull request through the autopilot pipeline.
//
// Concurrency: a live *PRState stored in Controller.activePRs is shared across the
// main processing loop and webhook goroutines. The embedded mu guards every field
// below. Holders of the live pointer MUST take mu before reading/writing fields
// (see TASK-324). The no-deadlock invariant is: always acquire PRState.mu BEFORE
// Controller.mu, never the reverse.
//
// Because PRState now contains a sync.Mutex, a populated value must never be
// copied (go vet copylocks). Use snapshot() to hand a detached, lock-free copy to
// read-only consumers. state_store.go constructs a fresh zero-value `var pr PRState`
// before populating it, which is fine.
type PRState struct {
	// mu guards all fields below for the live pointer held in Controller.activePRs.
	mu sync.Mutex
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
	// RebaseAttempts counts how many times auto-rebase (UpdatePullRequestBranch)
	// has succeeded for this PR. A successful rebase returns the PR to
	// StageWaitingCI without consuming MergeAttempts, so without this counter
	// a PR can cycle conflict -> rebase-success -> CI -> conflict indefinitely.
	// Reset to 0 on merge success. Persisted so the cap survives restarts.
	RebaseAttempts int
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
	// NotFoundCount tracks consecutive 404s fetching this PR from c.owner/c.repo
	// in processAllPRs. In-memory only (not persisted): a foreign or stale row
	// should evict quickly regardless of restart cadence, and a restart resets
	// the counter anyway, giving a freshly-restored row a few more tries before
	// eviction (GH-3903 404-eviction guard).
	NotFoundCount int
	// EnvironmentName is the user-friendly environment label (e.g. "staging").
	EnvironmentName string
	// PRTitle is the title of the pull request.
	PRTitle string
	// TargetBranch is the base branch the PR merges into (e.g. "main").
	TargetBranch string
	// IssueNodeID is the GraphQL global node ID of the linked issue, used for board sync.
	IssueNodeID string
	// MergeNotificationPosted is true once the merge-completion comment has been
	// posted to the linked issue. Prevents duplicate comments on state-machine
	// re-entry for an already-merged PR (GH-2345).
	MergeNotificationPosted bool
	// ApprovalRequestID holds the ID of the submitted async approval request (set on first tick in StageAwaitApproval).
	ApprovalRequestID string
	// ApprovalDecision holds the recorded async approval decision ("approved", "rejected", "timeout").
	ApprovalDecision string
	// ApprovalRequestedAt is when the async approval request was first submitted.
	ApprovalRequestedAt time.Time
	// PostMergeSHA is the main branch SHA captured on first entry to StagePostMergeCI.
	// Persisted so a daemon restart resumes monitoring the same commit.
	PostMergeSHA string
	// PostMergeCIStartedAt is when StagePostMergeCI monitoring began (for timeout tracking).
	PostMergeCIStartedAt time.Time
	// ReleasingAttempts counts how many times handleReleasing has been called for this PR.
	// Used to cap retries before escalating to StageFailed.
	ReleasingAttempts int
	// ReleasingFirstAt is when StageReleasing was first attempted. Set on the first call.
	ReleasingFirstAt time.Time
	// EscalationReason records why the PR entered StageAwaitApproval (size-floor
	// gate, scope-drift gate, or env require_approval) so misconfig reporting
	// names the actual trigger (GH-3569). In-memory only; lost on restart, which
	// degrades to the env-based fallback wording.
	EscalationReason string
	// TerminalLabel overrides the default pilot-retry-ready label that
	// notifyExternalClose applies once it observes this PR closed on GitHub.
	// Set by a close path that already determined the issue must NOT be
	// auto-retried under its own number — either because the failure is
	// terminal (iteration/size-guard cap reached) or because a dependent
	// follow-up issue was already created to continue the work, and re-queuing
	// the original would cause a duplicate dispatch. Empty means "use the
	// default retry-ready flow" (GH-3806). In-memory only; lost on restart —
	// safe because a restart re-enters the handler that sets it before the PR
	// can reach notifyExternalClose again.
	TerminalLabel string
}

// snapshot returns a detached, field-by-field copy of the PRState with a fresh
// (zero-value) mutex. The caller MUST hold ps.mu while calling this so the read of
// every field is race-free; the returned *PRState is independent of the live one
// and safe to hand to read-only consumers (metrics, dashboard, gateway) without any
// lock. It deliberately does NOT use `cp := *ps`, which would copy the mutex and
// trip go vet copylocks.
func (ps *PRState) snapshot() *PRState {
	cp := &PRState{
		PRNumber:                ps.PRNumber,
		PRURL:                   ps.PRURL,
		IssueNumber:             ps.IssueNumber,
		BranchName:              ps.BranchName,
		HeadSHA:                 ps.HeadSHA,
		Stage:                   ps.Stage,
		CIStatus:                ps.CIStatus,
		LastChecked:             ps.LastChecked,
		CIWaitStartedAt:         ps.CIWaitStartedAt,
		MergeAttempts:           ps.MergeAttempts,
		RebaseAttempts:          ps.RebaseAttempts,
		Error:                   ps.Error,
		CreatedAt:               ps.CreatedAt,
		ReleaseVersion:          ps.ReleaseVersion,
		ReleaseBumpType:         ps.ReleaseBumpType,
		ConsecutiveAPIFailures:  ps.ConsecutiveAPIFailures,
		NotFoundCount:           ps.NotFoundCount,
		EnvironmentName:         ps.EnvironmentName,
		PRTitle:                 ps.PRTitle,
		TargetBranch:            ps.TargetBranch,
		IssueNodeID:             ps.IssueNodeID,
		MergeNotificationPosted: ps.MergeNotificationPosted,
		ApprovalRequestID:       ps.ApprovalRequestID,
		ApprovalDecision:        ps.ApprovalDecision,
		ApprovalRequestedAt:     ps.ApprovalRequestedAt,
		PostMergeSHA:            ps.PostMergeSHA,
		PostMergeCIStartedAt:    ps.PostMergeCIStartedAt,
		ReleasingAttempts:       ps.ReleasingAttempts,
		ReleasingFirstAt:        ps.ReleasingFirstAt,
		EscalationReason:        ps.EscalationReason,
		TerminalLabel:           ps.TerminalLabel,
	}
	// DiscoveredChecks is a slice — copy the backing array so consumers can't
	// mutate the live PR's slice through the snapshot.
	if ps.DiscoveredChecks != nil {
		cp.DiscoveredChecks = make([]string, len(ps.DiscoveredChecks))
		copy(cp.DiscoveredChecks, ps.DiscoveredChecks)
	}
	return cp
}

// RepoOwnerAndName extracts the repository owner and name from the PR URL.
// Falls back to the provided defaults if the URL is missing or unparseable.
func (ps *PRState) RepoOwnerAndName(fallbackOwner, fallbackRepo string) (string, string) {
	if ps.PRURL != "" {
		trimmed := strings.TrimPrefix(ps.PRURL, "https://github.com/")
		if trimmed != ps.PRURL { // prefix was actually present
			parts := strings.Split(trimmed, "/")
			if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
				return parts[0], parts[1]
			}
		}
	}
	return fallbackOwner, fallbackRepo
}
