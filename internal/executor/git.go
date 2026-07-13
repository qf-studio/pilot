package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// defaultExcludeDirs are top-level directory prefixes whose contents are never
// auto-staged by the executor's commit step. They represent project-management
// state, build artifacts, or third-party caches that should not appear in
// Pilot-authored commits unless the task explicitly references them.
var defaultExcludeDirs = []string{
	".agent/",
	".claude/",
	"node_modules/",
	"dist/",
	"build/",
	"coverage/",
	".cache/",
}

// defaultExcludeGlobs are base-name patterns whose matching files are never
// auto-staged. Lock files and OS-junk dominate this list.
var defaultExcludeGlobs = []string{
	"*.lock",
	"package-lock.json",
	"pnpm-lock.yaml",
	".DS_Store",
	"Thumbs.db",
}

// ErrNoStageableChanges signals that all dirty paths matched the exclude list —
// the commit step refuses to produce an empty commit rather than committing
// excluded paths.
var ErrNoStageableChanges = fmt.Errorf("no stageable changes after applying default excludes")

// isExcluded returns true if the given porcelain path matches any default
// exclude rule.
func isExcluded(path string) bool {
	for _, dir := range defaultExcludeDirs {
		if strings.HasPrefix(path, dir) {
			return true
		}
	}
	base := filepath.Base(path)
	for _, glob := range defaultExcludeGlobs {
		if matched, _ := filepath.Match(glob, base); matched {
			return true
		}
	}
	return false
}

// GitOperations handles git operations for tasks
type GitOperations struct {
	projectPath string
}

// NewGitOperations creates new git operations for a project
func NewGitOperations(projectPath string) *GitOperations {
	return &GitOperations{projectPath: projectPath}
}

// CreateBranch creates a new branch
func (g *GitOperations) CreateBranch(ctx context.Context, branchName string) error {
	cmd := exec.CommandContext(ctx, "git", "checkout", "-b", branchName)
	cmd.Dir = g.projectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create branch: %w: %s", err, output)
	}
	return nil
}

// CreateOrResetBranch creates a branch or resets it if it already exists.
// Uses git checkout -B (uppercase) which force-creates the branch.
// This is safe when worktree already created the branch (GH-1235).
func (g *GitOperations) CreateOrResetBranch(ctx context.Context, branchName string) error {
	cmd := exec.CommandContext(ctx, "git", "checkout", "-B", branchName)
	cmd.Dir = g.projectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create/reset branch: %w: %s", err, output)
	}
	return nil
}

// SwitchBranch switches to an existing branch
func (g *GitOperations) SwitchBranch(ctx context.Context, branchName string) error {
	cmd := exec.CommandContext(ctx, "git", "checkout", branchName)
	cmd.Dir = g.projectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to switch branch: %w: %s", err, output)
	}
	return nil
}

// Commit stages filtered changes and commits. Files matching defaultExcludeDirs
// or defaultExcludeGlobs are never auto-staged. Returns ErrNoStageableChanges
// (wrapped) when all dirty paths are excluded.
func (g *GitOperations) Commit(ctx context.Context, message string) (string, error) {
	// Enumerate dirty paths via NUL-delimited porcelain output.
	statusCmd := exec.CommandContext(ctx, "git", "status", "--porcelain", "-z")
	statusCmd.Dir = g.projectPath
	statusOut, err := statusCmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to enumerate dirty paths: %w", err)
	}

	var stage []string
	var skipped []string
	// -z output: NUL-terminated entries, each "XY path" (3-char prefix + path).
	for _, entry := range strings.Split(strings.TrimRight(string(statusOut), "\x00"), "\x00") {
		if len(entry) < 4 {
			continue
		}
		path := entry[3:]
		if isExcluded(path) {
			skipped = append(skipped, path)
			continue
		}
		stage = append(stage, path)
	}

	if len(stage) == 0 {
		return "", fmt.Errorf("%w: skipped paths: %v", ErrNoStageableChanges, skipped)
	}

	// Stage filtered set; -- disambiguates paths from flags.
	addArgs := append([]string{"add", "--"}, stage...)
	addCmd := exec.CommandContext(ctx, "git", addArgs...)
	addCmd.Dir = g.projectPath
	if output, err := addCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to stage changes: %w: %s", err, output)
	}

	// Commit
	commitCmd := exec.CommandContext(ctx, "git", "commit", "-m", message)
	commitCmd.Dir = g.projectPath
	if output, err := commitCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to commit: %w: %s", err, output)
	}

	// Get commit SHA
	shaCmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	shaCmd.Dir = g.projectPath
	output, err := shaCmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get commit SHA: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// memoryDocsRelDir and graphJSONRelPath are the Navigator knowledge-graph
// paths that scripts/check-graph.py's Knowledge Graph Drift Gate validates
// (GH-4286).
const (
	memoryDocsRelDir = ".agent/knowledge/memories"
	graphJSONRelPath = ".agent/knowledge/graph.json"
)

// graphJSONPathFields are the node fields check-graph.py accepts as a memory
// file reference, in the same priority order.
var graphJSONPathFields = []string{"file", "path", "memory_file"}

// addedMemoryDocs returns memory doc paths (relative to the repo root) added
// between baseBranch and HEAD. Mirrors the file selection in
// scripts/check-graph.py's find_unindexed_memory_files: only *.md files,
// skipping the "resolved" archive subtree and README*.
func (g *GitOperations) addedMemoryDocs(ctx context.Context, baseBranch string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", "--diff-filter=A",
		baseBranch+"...HEAD", "--", memoryDocsRelDir)
	cmd.Dir = g.projectPath
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to diff added memory docs: %w", err)
	}

	var docs []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" || !strings.HasSuffix(line, ".md") {
			continue
		}
		rel := strings.TrimPrefix(line, memoryDocsRelDir+"/")
		if parts := strings.SplitN(rel, "/", 2); parts[0] == "resolved" {
			continue
		}
		if strings.HasPrefix(filepath.Base(line), "README") {
			continue
		}
		docs = append(docs, line)
	}
	return docs, nil
}

// indexedMemoryPaths reads .agent/knowledge/graph.json and returns the set of
// repo-relative memory doc paths referenced by a node's file/path/memory_file
// field, resolved the same way scripts/check-graph.py's resolve_path does:
// tried both as root-relative and as relative to .agent/knowledge/, keeping
// whichever candidate exists on disk. Returns (nil, nil) when graph.json is
// absent — a project without the file has no drift gate to protect.
func (g *GitOperations) indexedMemoryPaths() (map[string]bool, error) {
	raw, err := os.ReadFile(filepath.Join(g.projectPath, graphJSONRelPath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read graph.json: %w", err)
	}

	var graph struct {
		Nodes struct {
			Memories map[string]map[string]any `json:"memories"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &graph); err != nil {
		return nil, fmt.Errorf("failed to parse graph.json: %w", err)
	}

	indexed := make(map[string]bool)
	for _, node := range graph.Nodes.Memories {
		for _, field := range graphJSONPathFields {
			rawPath, ok := node[field].(string)
			if !ok {
				continue
			}
			for _, candidate := range []string{
				filepath.Join(g.projectPath, rawPath),
				filepath.Join(g.projectPath, ".agent", "knowledge", rawPath),
			} {
				if _, statErr := os.Stat(candidate); statErr != nil {
					continue
				}
				if rel, relErr := filepath.Rel(g.projectPath, candidate); relErr == nil {
					indexed[filepath.ToSlash(rel)] = true
				}
				break
			}
			break
		}
	}
	return indexed, nil
}

// StripUnindexedMemoryDocs removes newly-added .agent/knowledge/memories/**.md
// files (relative to baseBranch) that have no corresponding node in
// .agent/knowledge/graph.json, committing the removal as a follow-up commit.
// Returns the list of stripped paths (nil if none).
//
// GH-4286: a Pilot execution that commits a memory doc without indexing it in
// graph.json trips the Knowledge Graph Drift Gate CI check
// (scripts/check-graph.py), which the autopilot CI-fix path then treats as a
// real build failure — up to closing the PR via the size guard (PR #4279 was
// lost this way). Memory-doc authoring is Navigator's lane, not an
// execution's (project CLAUDE.md "Memory: Navigator only"), so an execution
// that added one unindexed is out of its lane; stripping it here keeps the
// rest of the diff intact instead of failing the whole task.
func (g *GitOperations) StripUnindexedMemoryDocs(ctx context.Context, baseBranch string) ([]string, error) {
	added, err := g.addedMemoryDocs(ctx, baseBranch)
	if err != nil || len(added) == 0 {
		return nil, err
	}

	indexed, err := g.indexedMemoryPaths()
	if err != nil {
		return nil, err
	}
	if indexed == nil {
		// No graph.json in this project - nothing to reconcile against.
		return nil, nil
	}

	var unindexed []string
	for _, doc := range added {
		if !indexed[doc] {
			unindexed = append(unindexed, doc)
		}
	}
	if len(unindexed) == 0 {
		return nil, nil
	}

	rmArgs := append([]string{"rm", "--"}, unindexed...)
	rmCmd := exec.CommandContext(ctx, "git", rmArgs...)
	rmCmd.Dir = g.projectPath
	if output, err := rmCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to remove unindexed memory doc(s): %w: %s", err, output)
	}

	message := "chore(memory): strip unindexed memory doc(s) added during execution\n\n" +
		"GH-4286: memory docs must be indexed in .agent/knowledge/graph.json in the\n" +
		"same commit or they trip the Knowledge Graph Drift Gate. Removed:\n" +
		strings.Join(unindexed, "\n")
	commitCmd := exec.CommandContext(ctx, "git", "commit", "-m", message)
	commitCmd.Dir = g.projectPath
	if output, err := commitCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to commit removal of unindexed memory doc(s): %w: %s", err, output)
	}

	return unindexed, nil
}

// Push pushes the current branch to remote
func (g *GitOperations) Push(ctx context.Context, branchName string) error {
	cmd := exec.CommandContext(ctx, "git", "push", "-u", "origin", branchName)
	cmd.Dir = g.projectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to push: %w: %s", err, output)
	}
	return nil
}

// CreatePR creates a pull request using gh CLI.
// GH-2325: title is validated against the conventional commit format before
// the remote call so malformed titles cannot reach main (and public release
// notes). The expected shape is "<issue-id>: <type>(<scope>)?: <subject>",
// which matches what the squash-merge path strips back to a conventional
// commit message.
func (g *GitOperations) CreatePR(ctx context.Context, title, body, baseBranch string) (string, error) {
	if err := validatePRTitle(title); err != nil {
		return "", err
	}

	// GH-2177: Detect current branch to pass --head explicitly.
	// In worktree mode, gh may see uncommitted changes and refuse to infer the head branch.
	// Using --head bypasses the dirty working tree check.
	headBranch := ""
	if branchCmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD"); branchCmd != nil {
		branchCmd.Dir = g.projectPath
		if out, err := branchCmd.Output(); err == nil {
			headBranch = strings.TrimSpace(string(out))
		}
	}

	args := []string{"pr", "create",
		"--title", title,
		"--body", body,
		"--base", baseBranch,
	}
	if headBranch != "" {
		args = append(args, "--head", headBranch)
	}

	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = g.projectPath
	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	if err != nil {
		// Check if PR already exists - gh returns exit 1 but includes URL in output
		if strings.Contains(outputStr, "already exists") {
			if url := extractPRURL(outputStr); url != "" {
				return url, nil
			}
		}
		return "", fmt.Errorf("failed to create PR: %w: %s", err, output)
	}

	// Extract PR URL from output
	prURL := strings.TrimSpace(outputStr)
	return prURL, nil
}

// extractPRURL extracts a GitHub PR URL from text
func extractPRURL(text string) string {
	// Look for GitHub PR URL pattern: https://github.com/owner/repo/pull/123
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "github.com") && strings.Contains(line, "/pull/") {
			// Extract just the URL if there's other text
			if idx := strings.Index(line, "https://"); idx >= 0 {
				url := line[idx:]
				// Trim any trailing text after the URL
				if spaceIdx := strings.IndexAny(url, " \t\n"); spaceIdx > 0 {
					url = url[:spaceIdx]
				}
				return url
			}
		}
	}
	return ""
}

// FindMergedPRByBranch returns the URL of a merged PR whose head is branch, or
// "" if none exists. TASK-359 Layer 1 (Shape C): lets the executor skip opening
// a duplicate PR for work that is already merged. It uses the gh CLI — the same
// dependency CreatePR already relies on — so the executor needs no github.Client
// wiring (the merge-detection API lives only on *github.Client).
func (g *GitOperations) FindMergedPRByBranch(ctx context.Context, branch string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "list",
		"--head", branch,
		"--state", "merged",
		"--json", "url",
		"--limit", "1",
	)
	cmd.Dir = g.projectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to list merged PRs for %s: %w: %s", branch, err, output)
	}
	return parseFirstPRURL(output), nil
}

// FindOpenPRByBranch returns the URL of an open PR whose head is branch, or ""
// if none exists. GH-4022: lets the direct (non-epic) executor path adopt an
// already-open PR from a prior/retried dispatch of the same branch instead of
// racing gh CLI into a duplicate PR.
func (g *GitOperations) FindOpenPRByBranch(ctx context.Context, branch string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "list",
		"--head", branch,
		"--state", "open",
		"--json", "url",
		"--limit", "1",
	)
	cmd.Dir = g.projectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to list open PRs for %s: %w: %s", branch, err, output)
	}
	return parseFirstPRURL(output), nil
}

// parseFirstPRURL extracts the first "url" field from `gh pr list --json url`
// output (a JSON array like [{"url":"https://github.com/o/r/pull/1"}]). Returns
// "" for an empty array or unparseable input.
func parseFirstPRURL(jsonOutput []byte) string {
	var prs []struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(jsonOutput, &prs); err != nil {
		return ""
	}
	if len(prs) == 0 {
		return ""
	}
	return prs[0].URL
}

// GetCurrentBranch returns the current branch name
func (g *GitOperations) GetCurrentBranch(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "branch", "--show-current")
	cmd.Dir = g.projectPath
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// GetDefaultBranch returns the default branch (main or master)
func (g *GitOperations) GetDefaultBranch(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "symbolic-ref", "refs/remotes/origin/HEAD")
	cmd.Dir = g.projectPath
	output, err := cmd.Output()
	if err != nil {
		// Fallback to checking for main or master
		if g.branchExists(ctx, "main") {
			return "main", nil
		}
		if g.branchExists(ctx, "master") {
			return "master", nil
		}
		return "", fmt.Errorf("could not determine default branch: %w", err)
	}

	ref := strings.TrimSpace(string(output))
	parts := strings.Split(ref, "/")
	return parts[len(parts)-1], nil
}

// branchExists checks if a branch exists
func (g *GitOperations) branchExists(ctx context.Context, branch string) bool {
	cmd := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = g.projectPath
	return cmd.Run() == nil
}

// GetChangedFiles returns list of changed files
func (g *GitOperations) GetChangedFiles(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", "HEAD")
	cmd.Dir = g.projectPath
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get changed files: %w", err)
	}

	files := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(files) == 1 && files[0] == "" {
		return []string{}, nil
	}
	return files, nil
}

// HasUncommittedChanges checks if there are uncommitted changes
func (g *GitOperations) HasUncommittedChanges(ctx context.Context) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = g.projectPath
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("failed to check status: %w", err)
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

// PushToMain pushes changes directly to the main/default branch
func (g *GitOperations) PushToMain(ctx context.Context) error {
	defaultBranch, err := g.GetDefaultBranch(ctx)
	if err != nil {
		defaultBranch = "main"
	}
	cmd := exec.CommandContext(ctx, "git", "push", "origin", defaultBranch)
	cmd.Dir = g.projectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to push to %s: %w: %s", defaultBranch, err, output)
	}
	return nil
}

// CountNewCommits returns the number of commits on the current branch
// that are not on the base branch. Uses `git rev-list --count base..HEAD`.
// Returns 0 if the base branch doesn't exist or there are no new commits.
func (g *GitOperations) CountNewCommits(ctx context.Context, baseBranch string) (int, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-list", "--count", baseBranch+"..HEAD")
	cmd.Dir = g.projectPath
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("failed to count new commits: %w", err)
	}
	countStr := strings.TrimSpace(string(output))
	var count int
	if _, parseErr := fmt.Sscanf(countStr, "%d", &count); parseErr != nil {
		return 0, fmt.Errorf("failed to parse commit count %q: %w", countStr, parseErr)
	}
	return count, nil
}

// GetCurrentCommitSHA returns the SHA of the current HEAD commit
func (g *GitOperations) GetCurrentCommitSHA(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = g.projectPath
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get current commit SHA: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// GetDiff returns the diff between the base branch and HEAD.
// Uses three-dot notation (base...HEAD) to show changes on the current branch.
func (g *GitOperations) GetDiff(ctx context.Context, baseBranch string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", baseBranch+"...HEAD")
	cmd.Dir = g.projectPath
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff failed: %w", err)
	}
	return string(output), nil
}

// Pull fetches and merges changes from remote for the specified branch
func (g *GitOperations) Pull(ctx context.Context, branch string) error {
	cmd := exec.CommandContext(ctx, "git", "pull", "origin", branch)
	cmd.Dir = g.projectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to pull %s: %w: %s", branch, err, output)
	}
	return nil
}

// SwitchToBranchAndPull switches to the given branch and pulls latest changes.
// Used to honor project.default_branch / branch_from overrides (GH-2290).
// Pull failures are non-fatal so offline / no-upstream scenarios still work.
func (g *GitOperations) SwitchToBranchAndPull(ctx context.Context, branch string) (string, error) {
	if branch == "" {
		return g.SwitchToDefaultBranchAndPull(ctx)
	}
	if err := g.SwitchBranch(ctx, branch); err != nil {
		return branch, fmt.Errorf("failed to switch to %s: %w", branch, err)
	}
	if err := g.Pull(ctx, branch); err != nil {
		return branch, nil
	}
	return branch, nil
}

// SwitchToDefaultBranchAndPull switches to the default branch and pulls latest changes.
// This ensures new branches are created from the latest default branch, not from
// whatever branch was previously checked out (fixes GH-279).
func (g *GitOperations) SwitchToDefaultBranchAndPull(ctx context.Context) (string, error) {
	// Get default branch name
	defaultBranch, err := g.GetDefaultBranch(ctx)
	if err != nil {
		defaultBranch = "main" // fallback
	}

	// Switch to default branch
	if err := g.SwitchBranch(ctx, defaultBranch); err != nil {
		return defaultBranch, fmt.Errorf("failed to switch to %s: %w", defaultBranch, err)
	}

	// Pull latest changes
	if err := g.Pull(ctx, defaultBranch); err != nil {
		// Pull failure is non-fatal - we can still create branch from local state
		// This handles offline scenarios or repos without upstream configured
		return defaultBranch, nil
	}

	return defaultBranch, nil
}

// CommitsBehindMain returns how many commits the given branch is behind origin/main.
// Returns 0 if the branch is up-to-date or ahead.
// GH-912: Used to detect stale branches that need to be recreated.
func (g *GitOperations) CommitsBehindMain(ctx context.Context, branchName string) (int, error) {
	// First fetch to ensure we have latest remote state
	fetchCmd := exec.CommandContext(ctx, "git", "fetch", "origin")
	fetchCmd.Dir = g.projectPath
	_ = fetchCmd.Run() // Ignore fetch errors - might be offline

	// Get default branch
	defaultBranch, err := g.GetDefaultBranch(ctx)
	if err != nil {
		defaultBranch = "main"
	}

	// Count commits that are in origin/main but not in the branch
	// git rev-list --count <branch>..origin/main
	cmd := exec.CommandContext(ctx, "git", "rev-list", "--count", branchName+"..origin/"+defaultBranch)
	cmd.Dir = g.projectPath
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("failed to count commits behind: %w", err)
	}

	countStr := strings.TrimSpace(string(output))
	var count int
	if _, parseErr := fmt.Sscanf(countStr, "%d", &count); parseErr != nil {
		return 0, fmt.Errorf("failed to parse count %q: %w", countStr, parseErr)
	}

	return count, nil
}

// DeleteBranch deletes a local branch.
// GH-912: Used to remove stale branches before recreating them fresh from main.
func (g *GitOperations) DeleteBranch(ctx context.Context, branchName string) error {
	cmd := exec.CommandContext(ctx, "git", "branch", "-D", branchName)
	cmd.Dir = g.projectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to delete branch: %w: %s", err, output)
	}
	return nil
}

// CreateRecoveryRef pins fromRef (typically "HEAD", or "refs/heads/<branch>"
// when called from outside the worktree that made the commits) under a
// dedicated ref namespace (refs/pilot-recovery/<taskID>) so committed work
// survives even if push/PR creation never succeeds and the worktree is
// later removed. Unlike a worktree's own branch, this ref lives in the
// shared repository (not the per-worktree admin area), so `git worktree
// remove` cannot take it with it — the commit stays reachable via `git
// fetch <repo> <returned-ref>` or `git show <sha>` from the main repo.
// GH-3785: prevents child-worker commits from being stranded in the object
// store with no reachable ref once a push/PR failure leads to worktree
// cleanup.
func (g *GitOperations) CreateRecoveryRef(ctx context.Context, taskID, fromRef string) (string, error) {
	if fromRef == "" {
		fromRef = "HEAD"
	}
	refName := "refs/pilot-recovery/" + sanitizeBranchName(taskID)
	cmd := exec.CommandContext(ctx, "git", "update-ref", refName, fromRef)
	cmd.Dir = g.projectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to create recovery ref %s: %w: %s", refName, err, output)
	}
	return refName, nil
}

// GitDiff holds numstat output from `git diff --numstat origin/main...HEAD`.
type GitDiff struct {
	// Files is the list of changed file paths.
	Files []string
	// Added is the total number of inserted lines across all changed files.
	Added int
	// Removed is the total number of deleted lines across all changed files.
	Removed int
}

// GetDiffStats returns line-level diff statistics between origin/baseBranch and HEAD.
// Uses three-dot notation so only commits on the current branch are counted.
func (g *GitOperations) GetDiffStats(ctx context.Context, baseBranch string) (GitDiff, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--numstat", "origin/"+baseBranch+"...HEAD")
	cmd.Dir = g.projectPath
	output, err := cmd.Output()
	if err != nil {
		return GitDiff{}, fmt.Errorf("git diff --numstat failed: %w", err)
	}

	var diff GitDiff
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		var added, removed int
		var path string
		if _, scanErr := fmt.Sscanf(line, "%d %d %s", &added, &removed, &path); scanErr != nil {
			continue
		}
		diff.Files = append(diff.Files, path)
		diff.Added += added
		diff.Removed += removed
	}
	return diff, nil
}

// RemoteBranchExists checks if a branch exists on the remote (origin).
// GH-1389: Used to verify if push actually succeeded despite worktree chdir errors.
func (g *GitOperations) RemoteBranchExists(ctx context.Context, branchName string) bool {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--heads", "origin", branchName)
	cmd.Dir = g.projectPath
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	// ls-remote returns non-empty output if branch exists
	return len(strings.TrimSpace(string(output))) > 0
}
