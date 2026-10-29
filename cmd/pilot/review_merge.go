package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// reviewMergeGatherTimeout bounds the GitHub and git calls that gather a
// review's inputs, as the daemon's gather step does.
const reviewMergeGatherTimeout = 2 * time.Minute

// reviewMergeGitHub is what `pilot review-merge` needs from GitHub: the gather
// surface, and AddComment for --post.
type reviewMergeGitHub interface {
	autopilot.PostMergeGitHub
	AddComment(ctx context.Context, owner, repo string, number int, body string) (*githubSDK.Comment, error)
}

// reviewMergeEnv is everything a run needs besides its flags. Tests build one
// around fakes.
type reviewMergeEnv struct {
	GH          reviewMergeGitHub
	Reviewer    autopilot.PostMergeReviewer
	Model       string // reported in the output; "" means the CLI's default
	ProjectPath string // local checkout, "" when the project has none
	NoMemories  bool   // executor.memory_injection is off: no memory recall
}

// reviewMergeOptions are the command's flags.
type reviewMergeOptions struct {
	Repo  string // owner/name
	PR    int
	Issue int // 0 = resolve from the PR
	JSON  bool
	Post  bool
}

// reviewMergeEnvFactory builds the real environment for repo from config. A
// var so command tests can substitute fakes without a config, token or claude.
var reviewMergeEnvFactory = newReviewMergeEnv

// validate checks the flags and splits --repo.
func (o reviewMergeOptions) validate() error {
	if _, _, err := splitOwnerRepo(o.Repo); err != nil {
		return err
	}
	if o.PR <= 0 {
		return errors.New("--pr is required and must be a positive PR number")
	}
	if o.Issue < 0 {
		return errors.New("--issue must be a positive issue number")
	}
	return nil
}

func splitOwnerRepo(repo string) (owner, name string, err error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("--repo must be owner/name, got %q", repo)
	}
	return parts[0], parts[1], nil
}

// newReviewMergeEnv resolves repo to a configured project and builds the
// GitHub clients and the reviewer the daemon would use for it.
func newReviewMergeEnv(repo string) (*reviewMergeEnv, error) {
	configPath := cfgFile
	if configPath == "" {
		configPath = config.DefaultConfigPath()
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	proj := cfg.FindProjectByRepo(repo)
	if proj == nil {
		return nil, fmt.Errorf("no configured project has github repo %s (add it under projects: in %s)", repo, configPath)
	}
	if token, _ := resolveGitHubToken(cfg); token == "" {
		return nil, errors.New("no GitHub token resolves: set adapters.github.token, GITHUB_TOKEN or GitHub App credentials")
	}
	reviewer, err := newPostMergeReviewer(cfg)
	if err != nil {
		return nil, err
	}
	return &reviewMergeEnv{
		GH:          autopilot.NewPostMergeGitHubClient(newGitHubSDKClient(cfg), newGitHubClient(cfg)),
		Reviewer:    reviewer,
		Model:       reviewer.Model(),
		ProjectPath: proj.Path,
		NoMemories:  !memoryInjectionEnabled(cfg),
	}, nil
}

// reviewMergeDefect is one defect in the output; Location is split into File
// and Line when it has the file:line shape.
type reviewMergeDefect struct {
	Severity string `json:"severity,omitempty"`
	File     string `json:"file"`
	Line     string `json:"line,omitempty"`
	Claim    string `json:"claim"`
	Evidence string `json:"evidence,omitempty"`
}

// reviewMergeResult is the --json shape: the review plus the input sizes.
type reviewMergeResult struct {
	Repo           string              `json:"repo"`
	PR             int                 `json:"pr"`
	Issue          int                 `json:"issue"`
	MergeCommit    string              `json:"merge_commit"`
	DiffSource     string              `json:"diff_source"`
	Verdict        string              `json:"verdict"`
	Defects        []reviewMergeDefect `json:"defects"`
	NotVerified    []string            `json:"not_verified"`
	Notes          string              `json:"notes"`
	FilesRead      int                 `json:"files_read"`
	Checkout       bool                `json:"checkout"`
	RawOutput      string              `json:"raw_output,omitempty"`
	DiffBytes      int                 `json:"diff_bytes"`
	DiffFiles      int                 `json:"diff_files"`
	DiffTrimmed    bool                `json:"diff_trimmed"`
	TrimmedFiles   []string            `json:"trimmed_files"`
	InputDiffBytes int                 `json:"input_diff_bytes"`
	InputIssueSize int                 `json:"input_issue_bytes"`
	InputPRBodyLen int                 `json:"input_pr_body_bytes"`
	Model          string              `json:"model"`
	DurationMS     int64               `json:"duration_ms"`
	Posted         bool                `json:"posted"`
	CommentID      int64               `json:"comment_id,omitempty"`

	diffLine string // rendered "Diff: ..." line, text output only
}

// runReviewMerge gathers a merged PR's inputs, runs the reviewer and prints
// the outcome to out (errOut gets the raw model text of an unusable review). It posts the verdict comment only when opts.Post is set
// and the verdict is usable, and does nothing else outward-facing.
func runReviewMerge(ctx context.Context, opts reviewMergeOptions, env *reviewMergeEnv, out, errOut io.Writer) error {
	owner, repo, err := splitOwnerRepo(opts.Repo)
	if err != nil {
		return err
	}

	gatherCtx, cancel := context.WithTimeout(ctx, reviewMergeGatherTimeout)
	var gatherOpts []autopilot.GatherOption
	if env.NoMemories {
		gatherOpts = append(gatherOpts, autopilot.WithoutMemoryInjection())
	}
	gathered, err := autopilot.GatherPostMergeReviewInput(gatherCtx, env.GH, owner, repo, env.ProjectPath, opts.PR, opts.Issue, gatherOpts...)
	cancel()
	if err != nil {
		return err
	}

	start := time.Now()
	review, err := env.Reviewer.Review(ctx, gathered.Input)
	if err != nil {
		return fmt.Errorf("review failed: %w", err)
	}
	if review == nil {
		return errors.New("review failed: reviewer returned no review")
	}
	elapsed := time.Since(start)

	res := &reviewMergeResult{
		Repo:           opts.Repo,
		PR:             opts.PR,
		Issue:          gathered.IssueNumber,
		MergeCommit:    gathered.MergeCommitSHA,
		DiffSource:     gathered.DiffSource,
		Verdict:        string(review.Verdict),
		Defects:        make([]reviewMergeDefect, 0, len(review.Defects)),
		NotVerified:    append([]string{}, review.NotVerified...),
		Notes:          review.Notes,
		FilesRead:      review.FilesRead,
		Checkout:       review.Checkout,
		DiffBytes:      review.DiffBytes,
		DiffFiles:      review.DiffFiles,
		DiffTrimmed:    review.DiffTrimmed,
		TrimmedFiles:   append([]string{}, review.TrimmedFiles...),
		InputDiffBytes: len(gathered.Input.Diff),
		InputIssueSize: len(gathered.Input.IssueTitle) + len(gathered.Input.IssueBody),
		InputPRBodyLen: len(gathered.Input.PRBody),
		Model:          env.Model,
		DurationMS:     elapsed.Milliseconds(),
		diffLine:       autopilot.PostMergeDiffLine(review),
	}
	if res.Model == "" {
		res.Model = "cli-default"
	}
	if !review.Verdict.Postable() {
		// An unusable review keeps the model text so the failure can be diagnosed;
		// it goes to stderr and the JSON object, never to GitHub.
		res.RawOutput = review.RawOutput
	}
	for _, d := range review.Defects {
		res.Defects = append(res.Defects, splitDefect(d))
	}

	// Same rule as the controller: a verdict that broke the output contract is
	// never posted.
	usable := review.Verdict.Postable()
	var postErr error
	if opts.Post && usable {
		cm, err := env.GH.AddComment(ctx, owner, repo, opts.PR, autopilot.BuildPostMergeReviewComment(review))
		if err != nil {
			postErr = fmt.Errorf("post comment: %w", err)
		} else {
			res.Posted = true
			if cm != nil {
				res.CommentID = cm.ID
			}
		}
	}

	if opts.JSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			return err
		}
	} else {
		printReviewMerge(out, res, opts)
	}
	if !usable {
		_, _ = fmt.Fprintf(errOut, "verdict %s\n%s\n", terminalSafe(res.Verdict), terminalSafe(res.RawOutput))
	}
	if postErr != nil {
		return postErr
	}
	if !usable {
		return errors.New("reviewer output did not follow the contract (verdict unknown)")
	}
	return nil
}

// splitDefect splits "path:line" locations; anything else stays in File.
func splitDefect(d executor.PostMergeDefect) reviewMergeDefect {
	out := reviewMergeDefect{Severity: string(d.Severity), File: d.Location, Claim: d.Claim, Evidence: d.Evidence}
	if i := strings.LastIndex(d.Location, ":"); i > 0 && i < len(d.Location)-1 {
		line := d.Location[i+1:]
		if strings.Trim(line, "0123456789-,") == "" {
			out.File, out.Line = d.Location[:i], line
		}
	}
	return out
}

// printReviewMerge renders the human-readable report. Every model-produced
// string goes through terminalSafe: the reviewer reads attacker-influenceable
// text and this output lands in a terminal.
func printReviewMerge(out io.Writer, res *reviewMergeResult, opts reviewMergeOptions) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format, a...) }

	p("Post-merge review of %s#%d (issue #%d, merge %s)\n", res.Repo, res.PR, res.Issue, shortSHA(res.MergeCommit))
	p("Diff source: %s\n\n", terminalSafe(res.DiffSource))
	p("VERDICT: %s\n\n", terminalSafe(res.Verdict))

	if len(res.Defects) == 0 {
		p("Defects: none\n\n")
	} else {
		p("Defects (%d):\n", len(res.Defects))
		for _, d := range res.Defects {
			loc := terminalSafe(d.File)
			if d.Line != "" {
				loc += ":" + terminalSafe(d.Line)
			}
			tag := ""
			if d.Severity != "" {
				tag = "[" + terminalSafe(d.Severity) + "] "
			}
			p("  - %s%s — %s\n", tag, loc, terminalSafe(d.Claim))
			if d.Evidence != "" {
				p("    Evidence: %s\n", terminalSafe(d.Evidence))
			}
		}
		p("\n")
	}

	if len(res.NotVerified) > 0 {
		p("Not verified:\n")
		for _, item := range res.NotVerified {
			p("  - %s\n", terminalSafe(item))
		}
		p("\n")
	}

	if notes := strings.TrimSpace(terminalSafe(res.Notes)); notes != "" {
		p("Notes:\n")
		for _, line := range strings.Split(notes, "\n") {
			p("  %s\n", line)
		}
		p("\n")
	}

	// The shared Diff line names the checkout and files read only when there was
	// one; the command always states both.
	diffLine := terminalSafe(res.diffLine)
	if !res.Checkout {
		diffLine += fmt.Sprintf(", checkout: no, files read: %d", res.FilesRead)
	}
	p("%s\n", diffLine)
	p("Model: %s\n", terminalSafe(res.Model))
	p("Duration: %s\n", (time.Duration(res.DurationMS) * time.Millisecond).Round(time.Millisecond))
	switch {
	case res.Posted:
		p("Posted: yes (comment %d)\n", res.CommentID)
	case opts.Post:
		p("Posted: no (verdict unusable)\n")
	default:
		p("Posted: no (--post not given)\n")
	}
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// terminalSafe drops control characters (ANSI escapes included) and invisible
// format characters, keeping newlines and tabs.
func terminalSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}
