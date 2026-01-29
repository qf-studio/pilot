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

// Config holds autopilot configuration for automated PR handling.
type Config struct {
	// Enabled controls whether autopilot mode is active.
	Enabled bool `yaml:"enabled"`
	// Environment determines the automation level (dev/stage/prod).
	Environment Environment `yaml:"environment"`

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
}

// DefaultConfig returns sensible defaults for autopilot configuration.
func DefaultConfig() *Config {
	return &Config{
		Enabled:          false,
		Environment:      EnvStage,
		AutoReview:       true,
		AutoMerge:        true,
		MergeMethod:      "squash",
		CIWaitTimeout:    30 * time.Minute,
		CIPollInterval:   30 * time.Second,
		RequiredChecks:   []string{"build", "test", "lint"},
		AutoCreateIssues: true,
		IssueLabels:      []string{"pilot", "autopilot-fix"},
		NotifyOnFailure:  true,
		MaxFailures:      3,
		MaxMergesPerHour: 10,
		ApprovalTimeout:  1 * time.Hour,
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
}
