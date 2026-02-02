package autopilot

import "time"

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

// Config holds autopilot configuration for automated PR handling.
type Config struct {
	// Enabled controls whether autopilot mode is active.
	Enabled bool `yaml:"enabled"`
	// Environment determines the automation level (dev/stage/prod).
	Environment Environment `yaml:"environment"`

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
	RequiredChecks []string `yaml:"required_checks"`

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
	// MaxMergesPerHour limits merge rate to prevent runaway automation.
	MaxMergesPerHour int `yaml:"max_merges_per_hour"`
	// ApprovalTimeout is how long to wait for human approval in prod.
	ApprovalTimeout time.Duration `yaml:"approval_timeout"`

	// Release holds auto-release configuration.
	Release *ReleaseConfig `yaml:"release"`
}

// DefaultConfig returns sensible defaults for autopilot configuration.
func DefaultConfig() *Config {
	return &Config{
		Enabled:          false,
		Environment:      EnvStage,
		ApprovalSource:   ApprovalSourceTelegram, // Default to Telegram for backward compatibility
		GitHubReview: &GitHubReviewConfig{
			PollInterval: 30 * time.Second,
		},
		AutoReview:       true,
		AutoMerge:        true,
		MergeMethod:      "squash",
		CIWaitTimeout:    30 * time.Minute,
		DevCITimeout:     5 * time.Minute,
		CIPollInterval:   30 * time.Second,
		RequiredChecks:   []string{"build", "test", "lint"},
		AutoCreateIssues: true,
		IssueLabels:      []string{"pilot", "autopilot-fix"},
		NotifyOnFailure:  true,
		MaxFailures:      3,
		MaxMergesPerHour: 10,
		ApprovalTimeout:  1 * time.Hour,
		Release:          nil, // Disabled by default
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

// PRState tracks a PR through the autopilot pipeline.
type PRState struct {
	// PRNumber is the GitHub PR number.
	PRNumber int
	// PRURL is the full URL to the PR.
	PRURL string
	// IssueNumber is the linked issue number (if any).
	IssueNumber int
	// HeadSHA is the commit SHA at the head of the PR.
	HeadSHA string
	// Stage is the current stage in the PR lifecycle.
	Stage PRStage
	// CIStatus is the current CI check status.
	CIStatus CIStatus
	// LastChecked is when the PR status was last polled.
	LastChecked time.Time
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
}
