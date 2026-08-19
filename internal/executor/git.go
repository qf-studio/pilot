package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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

// CreateOrResetBranchFromOrigin fetches origin/<baseBranch> (best-effort,
// via resolveGuardBaseRef) and force-creates/resets branchName directly from
// the resolved ref via `git checkout -B <branchName> <baseRef>`. Falls back
// to the local <baseBranch> ref when origin isn't reachable (offline, no
// "origin" remote configured — some test fixtures), preserving behavior in
// that environment.
//
// This unconditionally discards any commits an existing branchName already
// carried, so it is only safe to use where a pre-existing branch cannot yet
// hold real work — e.g. the decomposed-parent branch step, which runs before
// any subtask executes. Callers where a task branch may already carry
// legitimate in-progress commits from an earlier attempt (the common
// single-task execution path) must use EnsureBranchFromOrigin instead, which
// only recreates the branch when it is actually stale.
func (g *GitOperations) CreateOrResetBranchFromOrigin(ctx context.Context, branchName, baseBranch string) (string, error) {
	baseRef := g.resolveGuardBaseRef(ctx, baseBranch)

	cmd := exec.CommandContext(ctx, "git", "checkout", "-B", branchName, baseRef)
	cmd.Dir = g.projectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to create/reset branch %s from %s: %w: %s", branchName, baseRef, err, output)
	}
	return baseRef, nil
}

// EnsureBranchFromOrigin fetches origin/<baseBranch> (best-effort, via
// resolveGuardBaseRef) and ensures branchName exists, cut from that resolved
// ref:
//
//   - branchName doesn't exist locally yet: created directly from baseRef.
//   - branchName exists but is behind baseRef (stale — e.g. left over from a
//     prior, now-superseded run, GH-912): deleted and recreated from baseRef.
//   - branchName exists and is NOT behind baseRef (it may carry legitimate
//     work-in-progress commits from an earlier attempt on this same clone):
//     switched to as-is, preserving those commits.
//
// GH-4594: direct-mode (non-worktree) task branches used to be cut via
// SwitchToDefaultBranchAndPull/SwitchToBranchAndPull (checkout the LOCAL base
// branch, then `git pull` — a merge) followed by CreateBranch (checkout -b
// with no explicit start point, i.e. from whatever HEAD the pull left
// behind) — and, on the stale-branch path, CommitsBehindMain's own fresh
// `git fetch` was discarded immediately afterward because the recreate step
// (CreateBranch) still branched off that same ambient HEAD instead of the
// ref CommitsBehindMain had just resolved. Pull failure is also silently
// swallowed (non-fatal, to support offline use), so any fetch hiccup left
// the branch-doesn't-exist-yet path cutting from stale local state too. This
// method fixes both: baseRef is resolved once up front from a real fetch,
// and every branch-creating code path below is given that ref explicitly —
// never ambient HEAD.
//
// Returns the base ref actually used, and whether the branch was freshly
// (re)created from it (false when an existing non-stale branch was merely
// switched to).
func (g *GitOperations) EnsureBranchFromOrigin(ctx context.Context, branchName, baseBranch string) (baseRef string, created bool, err error) {
	baseRef = g.resolveGuardBaseRef(ctx, baseBranch)

	if !g.branchExists(ctx, branchName) {
		if createErr := g.checkoutNewBranchFrom(ctx, branchName, baseRef); createErr != nil {
			return baseRef, false, fmt.Errorf("failed to create branch %s from %s: %w", branchName, baseRef, createErr)
		}
		return baseRef, true, nil
	}

	stale, staleErr := g.branchIsBehind(ctx, branchName, baseRef)
	if staleErr != nil {
		// Fail open on the staleness check itself (mirrors CommitsBehindMain's
		// prior non-fatal logging): fall through to switching to the existing
		// branch as-is rather than risking discarding unverified work.
		stale = false
	}

	if !stale {
		if switchErr := g.SwitchBranch(ctx, branchName); switchErr != nil {
			return baseRef, false, fmt.Errorf("failed to switch to existing branch %s: %w", branchName, switchErr)
		}
		return baseRef, false, nil
	}

	if delErr := g.DeleteBranch(ctx, branchName); delErr != nil {
		return baseRef, false, fmt.Errorf("failed to delete stale branch %s: %w", branchName, delErr)
	}
	if createErr := g.checkoutNewBranchFrom(ctx, branchName, baseRef); createErr != nil {
		return baseRef, false, fmt.Errorf("failed to recreate stale branch %s from %s: %w", branchName, baseRef, createErr)
	}
	return baseRef, true, nil
}

// checkoutNewBranchFrom runs `git checkout -b <branchName> <startPoint>`.
// Assumes branchName does not already exist locally (callers are
// responsible for that precondition — EnsureBranchFromOrigin only calls this
// after confirming absence or deleting a stale branch).
func (g *GitOperations) checkoutNewBranchFrom(ctx context.Context, branchName, startPoint string) error {
	cmd := exec.CommandContext(ctx, "git", "checkout", "-b", branchName, startPoint)
	cmd.Dir = g.projectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, output)
	}
	return nil
}

// branchIsBehind reports whether branchName is missing any commits that
// baseRef has — i.e. `git rev-list --count <branchName>..<baseRef>` > 0.
// This only detects "behind"; a branch that has both unique commits AND is
// missing baseRef commits (diverged) is also reported stale here, matching
// CommitsBehindMain's prior GH-912 semantics.
func (g *GitOperations) branchIsBehind(ctx context.Context, branchName, baseRef string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-list", "--count", branchName+".."+baseRef)
	cmd.Dir = g.projectPath
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("failed to count commits behind: %w", err)
	}
	var count int
	if _, scanErr := fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &count); scanErr != nil {
		return false, fmt.Errorf("failed to parse behind count %q: %w", strings.TrimSpace(string(output)), scanErr)
	}
	return count > 0, nil
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
// file reference. Unlike check-graph.py (which takes the first field present
// and stops), the Go readers below check ALL of these per node — GH-4496
// found the "stop at first field" shortcut can leave a resolvable path
// ignored when a node carries more than one path-shaped field.
var graphJSONPathFields = []string{"file", "path", "memory_file"}

// memoryGraphDoc is the subset of .agent/knowledge/graph.json the executor's
// memory-doc guards read: node groups (only "memories" carries file paths)
// plus concept_index, which GH-4496 strike 3 showed can carry a memory doc's
// slug even when no node's path field resolves cleanly.
type memoryGraphDoc struct {
	Nodes struct {
		Memories map[string]map[string]any `json:"memories"`
	} `json:"nodes"`
	ConceptIndex map[string][]string `json:"concept_index"`
}

// loadMemoryGraph reads and parses .agent/knowledge/graph.json once for all
// of the memory-doc guards below. Returns (nil, nil) when the file is
// absent — a project without one has no drift gate to protect.
func (g *GitOperations) loadMemoryGraph() (*memoryGraphDoc, error) {
	raw, err := os.ReadFile(filepath.Join(g.projectPath, graphJSONRelPath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read graph.json: %w", err)
	}

	var graph memoryGraphDoc
	if err := json.Unmarshal(raw, &graph); err != nil {
		return nil, fmt.Errorf("failed to parse graph.json: %w", err)
	}
	return &graph, nil
}

// loadMemoryGraphAtRef reads and parses graph.json as it existed at ref
// (e.g. baseBranch), independent of whatever the current working tree/HEAD
// holds. This matters for both EnforceMemoryDocDeletionGuard (the veto leg)
// and graphIndexedMemoryNodes (the restore leg, GH-4581): strikes 1-2 of the
// TASK-410 series deleted a memory doc AND its graph.json node in the same
// commit, so checking indexed-status against HEAD's graph.json sees no
// dangling reference and misses it entirely. Checking against baseBranch's
// graph.json instead answers the question that actually matters: was this
// doc part of the curated knowledge graph before this branch touched it?
// Returns (nil, nil) when graph.json didn't exist at ref.
func (g *GitOperations) loadMemoryGraphAtRef(ctx context.Context, ref string) (*memoryGraphDoc, error) {
	cmd := exec.CommandContext(ctx, "git", "show", ref+":"+graphJSONRelPath)
	cmd.Dir = g.projectPath
	raw, err := cmd.Output()
	if err != nil {
		// Most common cause: graph.json didn't exist at ref. Treat the same
		// as "no graph to protect" rather than failing the guard closed.
		return nil, nil
	}

	var graph memoryGraphDoc
	if err := json.Unmarshal(raw, &graph); err != nil {
		return nil, fmt.Errorf("failed to parse graph.json at %s: %w", ref, err)
	}
	return &graph, nil
}

// memorySlug returns a memory doc's graph-identity slug: its basename with
// the .md extension stripped. Current-convention nodes key nodes.memories by
// this slug directly; legacy nodes instead carry a path-shaped field whose
// basename is this slug.
func memorySlug(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".md")
}

// indexedMemorySlugs returns every memory-doc slug that graph.json
// references ANYWHERE: as a nodes.memories node ID (current convention), as
// the basename of a node's file/path/memory_file field (regardless of
// whether that field resolves to an existing path on disk), or as a value
// inside concept_index. This is intentionally broader and more permissive
// than resolving exact paths — GH-4496 strike 3 (PR #4495, commit 78870958)
// deleted a doc whose node WAS present in nodes.memories with a correct
// "file" field; whatever the strip pass's exact-path check missed, a
// slug appearing anywhere in the graph must still veto deletion.
func indexedMemorySlugs(graph *memoryGraphDoc) map[string]bool {
	slugs := make(map[string]bool)
	if graph == nil {
		return slugs
	}
	for nodeID, node := range graph.Nodes.Memories {
		slugs[nodeID] = true
		for _, field := range graphJSONPathFields {
			if rawPath, ok := node[field].(string); ok {
				slugs[memorySlug(rawPath)] = true
			}
		}
	}
	for _, values := range graph.ConceptIndex {
		for _, v := range values {
			slugs[v] = true
		}
	}
	return slugs
}

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
	graph, err := g.loadMemoryGraph()
	if err != nil || graph == nil {
		return nil, err
	}

	indexed := make(map[string]bool)
	for _, node := range graph.Nodes.Memories {
		// GH-4496: check every path-shaped field on the node, not just the
		// first one present — a node carrying both a resolvable and a stale
		// field must not have the resolvable one shadowed.
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
			}
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
//
// GH-4496: a doc only counts as unindexed when BOTH the exact-path check
// (indexedMemoryPaths) AND the slug-anywhere-in-the-graph check
// (indexedMemorySlugs) come back negative — three strikes in 26 hours showed
// the exact-path check alone can misjudge a genuinely indexed doc (strike 3,
// PR #4495/commit 78870958, deleted a doc whose node had a correct "file"
// field). Every doc considered and the verdict is logged so a future
// misjudgment is diagnosable from the run log instead of only from a later
// graph-vs-disk audit.
func (g *GitOperations) StripUnindexedMemoryDocs(ctx context.Context, baseBranch string) ([]string, error) {
	// GH-4582: resolve baseRef once (origin/<baseBranch> when it resolves,
	// falling back to the raw baseBranch) so this leg agrees with its
	// siblings RestoreDeletedIndexedMemoryDocs and EnforceMemoryDocDeletionGuard
	// on what the base branch contains — a stale local baseBranch made this
	// leg misclassify pre-existing origin docs as branch-added, which the
	// origin-relative veto leg then correctly refused to let it delete.
	baseRef := g.resolveGuardBaseRef(ctx, baseBranch)

	added, err := g.addedMemoryDocs(ctx, baseRef)
	if err != nil || len(added) == 0 {
		return nil, err
	}

	graph, err := g.loadMemoryGraph()
	if err != nil {
		return nil, err
	}
	if graph == nil {
		// No graph.json in this project - nothing to reconcile against.
		return nil, nil
	}

	indexed, err := g.indexedMemoryPaths()
	if err != nil {
		return nil, err
	}
	slugs := indexedMemorySlugs(graph)

	var unindexed []string
	for _, doc := range added {
		slug := memorySlug(doc)
		byPath := indexed[doc]
		bySlug := slugs[slug]
		slog.Default().Debug("strip-unindexed-memory-docs: checked added doc",
			slog.String("doc", doc),
			slog.String("slug", slug),
			slog.Bool("indexed_by_path", byPath),
			slog.Bool("indexed_by_slug", bySlug),
		)
		if byPath || bySlug {
			continue
		}
		unindexed = append(unindexed, doc)
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

// resolveGuardBaseRef resolves the ref the memory-doc guards should diff
// against: origin/<baseBranch> when it resolves locally, falling back to the
// local <baseBranch> ref otherwise. Mirrors CountNewCommitsAgainstOrigin's
// GH-4566 fix for the no-commits guard.
//
// TASK-424: worktrees are cut from a freshly fetched origin/<base>
// (worktree.go), but nothing fast-forwards the shared clone's local <base>
// ref on every path — so a memory doc merged to origin/<base> after the
// local ref last synced was invisible to every guard leg that diffed against
// the stale local baseBranch. The doc was neither seen as deleted (strip
// leg diffs against a base that never had it) nor protected (the veto leg's
// baseBranch graph.json also predates it), so the deletion only materialized
// at squash-merge on GitHub, where no guard runs (strikes 4/5: #4534/#4535,
// #4551).
//
// Fetches origin/<baseBranch> first (best-effort — offline / no-remote
// environments fall through). If origin/<baseBranch> still doesn't resolve
// locally (no "origin" remote configured — bare local repos, some unit
// tests), returns the local <baseBranch> ref unchanged, preserving prior
// behavior in that environment.
func (g *GitOperations) resolveGuardBaseRef(ctx context.Context, baseBranch string) string {
	fetchCmd := exec.CommandContext(ctx, "git", "fetch", "origin", baseBranch)
	fetchCmd.Dir = g.projectPath
	withGitCredentials(ctx, fetchCmd)
	_ = fetchCmd.Run() // best-effort; fall back to whatever origin/<base> already resolves to (or local base)

	verifyCmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "-q", "origin/"+baseBranch)
	verifyCmd.Dir = g.projectPath
	if err := verifyCmd.Run(); err == nil {
		return "origin/" + baseBranch
	}
	return baseBranch
}

// deletedMemoryDocs returns memory doc paths (relative to the repo root)
// deleted between baseRef and HEAD. Mirrors addedMemoryDocs's file
// selection but with --diff-filter=D: GH-4398's restore guard cares about
// removals, not additions, of .agent/knowledge/memories/**.md. Callers pass
// an already-resolved ref (see resolveGuardBaseRef) rather than a raw branch
// name.
func (g *GitOperations) deletedMemoryDocs(ctx context.Context, baseRef string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", "--diff-filter=D",
		baseRef+"...HEAD", "--", memoryDocsRelDir)
	cmd.Dir = g.projectPath
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to diff deleted memory docs: %w", err)
	}

	var docs []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" || !strings.HasSuffix(line, ".md") {
			continue
		}
		docs = append(docs, line)
	}
	return docs, nil
}

// graphIndexedMemoryNodes reads .agent/knowledge/graph.json AT baseRef (not
// the working tree's current/HEAD copy) and returns a map from repo-relative
// memory doc path to the graph node id that references it, covering both the
// "file"/"memory_file" (root-relative) and legacy "path" (relative to
// .agent/knowledge/) key shapes. Unlike indexedMemoryPaths, this does NOT
// require the file to exist on disk: GH-4398's restore guard runs precisely
// when the file is missing (deleted), so an on-disk existence check would
// always fail the case it exists to catch. Returns (nil, nil) when
// graph.json is absent at baseRef — no drift gate to protect.
//
// GH-4581 (8th occurrence of the TASK-410 phantom-deletion class): this used
// to read graph.json via loadMemoryGraph() — the working tree's current/HEAD
// copy — exactly the shape loadMemoryGraphAtRef's doc comment already warned
// against: strikes 1-2 (GH-4484, GH-4489) deleted a memory doc AND its
// graph.json node in the same commit, so a HEAD-relative read sees no
// dangling reference and the restore leg silently found nothing to restore.
// EnforceMemoryDocDeletionGuard (the veto leg) was fixed to read baseRef's
// graph.json for exactly this reason, but this restore leg — which runs
// BEFORE the veto leg and would have made the veto moot by restoring the
// file — was never given the same fix, so it kept missing precisely the
// deletions the veto leg still (correctly) hard-blocks pointer dispatch on.
// Reading baseRef here closes that gap: the restore leg now sees the same
// graph the veto leg does.
func (g *GitOperations) graphIndexedMemoryNodes(ctx context.Context, baseRef string) (map[string]string, error) {
	graph, err := g.loadMemoryGraphAtRef(ctx, baseRef)
	if err != nil || graph == nil {
		return nil, err
	}

	nodes := make(map[string]string)
	for nodeID, node := range graph.Nodes.Memories {
		// GH-4496: check every path-shaped field, not just the first present.
		for _, field := range graphJSONPathFields {
			rawPath, ok := node[field].(string)
			if !ok {
				continue
			}
			// Register both the root-relative ("file"/"memory_file") and the
			// legacy .agent/knowledge/-relative ("path") candidate forms —
			// whichever matches the diff's path wins, the other is simply
			// never looked up. Mirrors indexedMemoryPaths' candidate order.
			nodes[filepath.ToSlash(rawPath)] = nodeID
			nodes[filepath.ToSlash(filepath.Join(".agent", "knowledge", rawPath))] = nodeID
		}
	}
	return nodes, nil
}

// RestoredMemoryDoc describes one graph-indexed memory file that
// RestoreDeletedIndexedMemoryDocs restored after finding it deleted relative
// to baseBranch, plus the graph node id that still references it.
type RestoredMemoryDoc struct {
	Path   string
	NodeID string
}

// RestoreDeletedIndexedMemoryDocs is the restore leg of the GH-4387 protected-
// memory guard (GH-4398): it finds any .agent/knowledge/memories/**.md file
// deleted between baseBranch and HEAD that is still referenced by a node in
// .agent/knowledge/graph.json, restores it via `git checkout <baseBranch> --
// <path>` as a fail-safe, and stages the restoration as a follow-up commit so
// the file rides the same PR the guard is protecting.
//
// Deleting a genuinely unindexed memory doc remains allowed: only paths with
// a surviving graph node are restored. Returns (nil, nil) when there is
// nothing to restore (no deletions under memories/, no graph.json, or none of
// the deletions are graph-indexed).
//
// Callers own logging + ledger visibility for each restored doc (see
// Runner.recordMemoryGuardRestoreEvents) and deciding when in the push/PR
// path this runs (GH-4397); this method only performs the git-level restore.
func (g *GitOperations) RestoreDeletedIndexedMemoryDocs(ctx context.Context, baseBranch string) ([]RestoredMemoryDoc, error) {
	baseRef := g.resolveGuardBaseRef(ctx, baseBranch)

	deleted, err := g.deletedMemoryDocs(ctx, baseRef)
	if err != nil || len(deleted) == 0 {
		return nil, err
	}

	nodes, err := g.graphIndexedMemoryNodes(ctx, baseRef)
	if err != nil || len(nodes) == 0 {
		return nil, err
	}

	var restored []RestoredMemoryDoc
	for _, doc := range deleted {
		if nodeID, ok := nodes[doc]; ok {
			restored = append(restored, RestoredMemoryDoc{Path: doc, NodeID: nodeID})
		}
	}
	if len(restored) == 0 {
		return nil, nil
	}

	// TASK-424: restore from baseRef (origin/<baseBranch> when it resolves),
	// not the raw baseBranch — a doc merged to origin after the local branch
	// last synced exists there even though the stale local ref never saw it.
	checkoutArgs := append([]string{"checkout", baseRef, "--"}, restoredPaths(restored)...)
	checkoutCmd := exec.CommandContext(ctx, "git", checkoutArgs...)
	checkoutCmd.Dir = g.projectPath
	if output, err := checkoutCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to restore protected memory doc(s): %w: %s", err, output)
	}

	addArgs := append([]string{"add", "--"}, restoredPaths(restored)...)
	addCmd := exec.CommandContext(ctx, "git", addArgs...)
	addCmd.Dir = g.projectPath
	if output, err := addCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to stage restored memory doc(s): %w: %s", err, output)
	}

	message := "fix(memory): restore graph-indexed memory doc(s) deleted during execution\n\n" +
		"GH-4387/GH-4398: the protected-memory guard detected deletion of file(s)\n" +
		"still referenced by .agent/knowledge/graph.json and restored them as a\n" +
		"fail-safe so the Knowledge Graph Drift Gate does not wedge this PR.\n" +
		"Restored:\n" + strings.Join(restoredPaths(restored), "\n")
	commitCmd := exec.CommandContext(ctx, "git", "commit", "-m", message)
	commitCmd.Dir = g.projectPath
	if output, err := commitCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failed to commit restored memory doc(s): %w: %s", err, output)
	}

	return restored, nil
}

// restoredPaths extracts the Path field from a []RestoredMemoryDoc, for use
// as git command arguments.
func restoredPaths(docs []RestoredMemoryDoc) []string {
	paths := make([]string, len(docs))
	for i, d := range docs {
		paths[i] = d.Path
	}
	return paths
}

// ErrMemoryDocDeletionVetoed is returned by EnforceMemoryDocDeletionGuard when
// the branch still carries a net deletion of a pre-existing
// .agent/knowledge/memories/**.md file after the strip/restore passes have
// already run, and the task never said it was allowed to touch memory docs.
var ErrMemoryDocDeletionVetoed = fmt.Errorf("execution deleted memory doc(s) outside its lane")

// EnforceMemoryDocDeletionGuard is the GH-4496 hard-veto leg of the TASK-410
// memory-loss series: it must run AFTER StripUnindexedMemoryDocs and
// RestoreDeletedIndexedMemoryDocs, and reports any file under
// .agent/knowledge/memories/ that is STILL net-deleted relative to
// baseBranch AND was indexed in baseBranch's graph.json before this branch
// touched it.
//
// The baseBranch check (not HEAD's current graph.json) is deliberate: in
// strikes 1-2 of the TASK-410 series (GH-4484, GH-4489) the offending commit
// deleted the memory doc AND its graph.json node together, so by the time
// any HEAD-relative check runs there is no dangling reference left to
// notice — the tree looks internally consistent even though it destroyed
// curated knowledge. Checking against the graph as it stood on baseBranch
// catches exactly that case. Deleting a doc that was never indexed on
// baseBranch either (see TestFinalizeDecomposedParentPR_AllowsDeletingUnindexedMemoryDoc)
// remains allowed — this guard is about protecting the graph's existing
// knowledge, not blocking all memory-doc deletions outright.
//
// Unless allowMemoryChanges is true — the task itself explicitly targets
// memory files — this is a hard veto (the caller must block push and fail
// the run) rather than advisory-only, converting a silent-data-loss class
// into a loud, reviewable failure.
func (g *GitOperations) EnforceMemoryDocDeletionGuard(ctx context.Context, baseBranch string, allowMemoryChanges bool) ([]string, error) {
	if allowMemoryChanges {
		return nil, nil
	}
	baseRef := g.resolveGuardBaseRef(ctx, baseBranch)

	deleted, err := g.deletedMemoryDocs(ctx, baseRef)
	if err != nil || len(deleted) == 0 {
		return nil, err
	}

	// TASK-424: read baseRef's graph.json (origin/<baseBranch> when it
	// resolves), not the raw baseBranch's — the stale local base's graph.json
	// can predate a doc's indexing (or deletion) that already landed on
	// origin, letting a genuinely-protected doc slip past the veto.
	baseGraph, err := g.loadMemoryGraphAtRef(ctx, baseRef)
	if err != nil {
		return nil, err
	}
	if baseGraph == nil {
		return nil, nil
	}
	baseSlugs := indexedMemorySlugs(baseGraph)

	var vetoed []string
	for _, doc := range deleted {
		if baseSlugs[memorySlug(doc)] {
			vetoed = append(vetoed, doc)
		}
	}
	if len(vetoed) == 0 {
		return nil, nil
	}
	return vetoed, fmt.Errorf("%w: %v", ErrMemoryDocDeletionVetoed, vetoed)
}

// memoryFileIntentKeywords are lower-cased substrings in a task's title or
// description that indicate the task itself is expected to touch
// .agent/knowledge/memories/** — e.g. a Navigator memory-authoring or
// memory-tooling task — so EnforceMemoryDocDeletionGuard should not veto its
// deletions.
var memoryFileIntentKeywords = []string{
	".agent/knowledge/memories",
	"memory doc",
	"memory-doc",
	"knowledge graph",
	"graph.json",
}

// taskExplicitlyTargetsMemoryFiles reports whether task's title or
// description mentions the memory-doc/knowledge-graph system explicitly,
// per memoryFileIntentKeywords. Used to scope EnforceMemoryDocDeletionGuard's
// veto to executions that never said they needed to touch memory docs.
func taskExplicitlyTargetsMemoryFiles(task *Task) bool {
	if task == nil {
		return false
	}
	haystack := strings.ToLower(task.Title + "\n" + task.Description)
	for _, kw := range memoryFileIntentKeywords {
		if strings.Contains(haystack, kw) {
			return true
		}
	}
	return false
}

// Push pushes the current branch to remote
func (g *GitOperations) Push(ctx context.Context, branchName string) error {
	cmd := exec.CommandContext(ctx, "git", "push", "-u", "origin", branchName)
	cmd.Dir = g.projectPath
	withGitCredentials(ctx, cmd)
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

	cmd := withGhCredentials(ctx, exec.CommandContext(ctx, "gh", args...))
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
	cmd := withGhCredentials(ctx, exec.CommandContext(ctx, "gh", "pr", "list",
		"--head", branch,
		"--state", "merged",
		"--json", "url",
		"--limit", "1",
	))
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
	cmd := withGhCredentials(ctx, exec.CommandContext(ctx, "gh", "pr", "list",
		"--head", branch,
		"--state", "open",
		"--json", "url",
		"--limit", "1",
	))
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
	withGitCredentials(ctx, cmd)
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

// CountNewCommitsAgainstOrigin is CountNewCommits compared against the
// origin-tracked base branch instead of the local base branch ref. GH-4566:
// every worktree (parent epic's and children's alike) is cut from a freshly
// fetched origin/<base> (worktree.go), but nothing fast-forwards the shared
// clone's local <base> ref during a sequential epic run — so on a stale local
// <base>, CountNewCommits(ctx, "main") counts commits that landed on origin
// via other sessions, not commits this branch actually added, and the
// no-commits guard fires open on an empty umbrella branch.
//
// Fetches origin/<baseBranch> first so the comparison reflects the ref the
// branch was actually cut from. The fetch is best-effort: on failure this
// still compares against whatever origin/<baseBranch> already resolves to
// locally, which is strictly fresher than the local <baseBranch> branch in
// this codepath (worktree creation already fetched it before cutting the
// branch). If origin/<baseBranch> doesn't resolve at all (no "origin" remote
// configured — bare local repos, some unit tests), falls back to comparing
// against the local <baseBranch> ref, preserving CountNewCommits' original
// behavior in that environment.
func (g *GitOperations) CountNewCommitsAgainstOrigin(ctx context.Context, baseBranch string) (int, error) {
	fetchCmd := exec.CommandContext(ctx, "git", "fetch", "origin", baseBranch)
	fetchCmd.Dir = g.projectPath
	withGitCredentials(ctx, fetchCmd)
	_ = fetchCmd.Run() // best-effort; fall back to the existing tracking ref on failure

	if count, err := g.CountNewCommits(ctx, "origin/"+baseBranch); err == nil {
		return count, nil
	}
	return g.CountNewCommits(ctx, baseBranch)
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

// ResetHardToCommit discards every change made since sha — committed or
// uncommitted, tracked or untracked — restoring the working tree to exactly
// that commit's state.
//
// GH-4594: direct-mode (non-worktree) quality-gate retries re-invoked Claude
// Code in place, on top of whatever the previous (rejected) attempt left
// behind. Since that attempt's edits were never undone, the retry's new
// edits landed on top of the old ones instead of a clean slate — the
// "leftover ` M version.go` observed x3" symptom from the incident this
// fixes: three retries, three stacked partial edits to the same file, none
// of them ever fully applied or cleanly discarded. Worktree mode doesn't
// need this: each execution already gets an isolated copy created fresh
// from a commit.
//
// Untracked paths matching defaultExcludeDirs/defaultExcludeGlobs (Navigator
// scaffold, lock files, build artifacts) are preserved via `git clean`'s -e
// excludes — the same allowlist Commit() and checkGitClean() already use to
// decide what isn't really a project change.
func (g *GitOperations) ResetHardToCommit(ctx context.Context, sha string) error {
	resetCmd := exec.CommandContext(ctx, "git", "reset", "--hard", sha)
	resetCmd.Dir = g.projectPath
	if output, err := resetCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to reset to %s: %w: %s", sha, err, output)
	}

	cleanArgs := []string{"clean", "-fd"}
	for _, dir := range defaultExcludeDirs {
		cleanArgs = append(cleanArgs, "-e", dir)
	}
	for _, glob := range defaultExcludeGlobs {
		cleanArgs = append(cleanArgs, "-e", glob)
	}
	cleanCmd := exec.CommandContext(ctx, "git", cleanArgs...)
	cleanCmd.Dir = g.projectPath
	if output, err := cleanCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to clean untracked files after reset to %s: %w: %s", sha, err, output)
	}
	return nil
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

// GetDiffAgainstOrigin is GetDiff compared against a freshly fetched
// origin/<baseBranch> instead of the possibly-stale local <baseBranch> ref.
// GH-4988: the intent judge calls GetDiff with a local branch name (task's
// BaseBranch, or GetDefaultBranch's local resolution), which — exactly like
// the GH-4566 CountNewCommits class this mirrors — can be behind
// origin/<baseBranch> in a long-lived worktree or shared clone. Other
// issues merging into origin/main mid-task then show up inside the judge's
// diff as if this branch produced them, and the judge correctly (but
// wrongly, from the operator's perspective) vetoes them as scope creep.
//
// Fetches origin/<baseBranch> (best-effort — falls back to whatever
// tracking ref already resolves locally on failure, same as
// CountNewCommitsAgainstOrigin), resolves the merge-base of that ref and
// HEAD, and diffs from there. Diffing from the merge-base — rather than
// origin/<baseBranch>'s tip — guarantees the result never contains commits
// reachable from current origin/<baseBranch> even when HEAD is also behind
// tip for unrelated reasons.
//
// Returns the diff and the base SHA it was computed against, so callers can
// log which commit the judge actually evaluated from.
func (g *GitOperations) GetDiffAgainstOrigin(ctx context.Context, baseBranch string) (diff string, baseSHA string, err error) {
	fetchCmd := exec.CommandContext(ctx, "git", "fetch", "origin", baseBranch)
	fetchCmd.Dir = g.projectPath
	withGitCredentials(ctx, fetchCmd)
	_ = fetchCmd.Run() // best-effort; fall back to whatever origin/<baseBranch> already resolves to locally

	baseSHA, err = g.resolveMergeBaseSHA(ctx, baseBranch)
	if err != nil {
		return "", "", err
	}

	cmd := exec.CommandContext(ctx, "git", "diff", baseSHA+"...HEAD")
	cmd.Dir = g.projectPath
	output, err := cmd.Output()
	if err != nil {
		return "", baseSHA, fmt.Errorf("git diff failed: %w", err)
	}
	return string(output), baseSHA, nil
}

// resolveMergeBaseSHA resolves the merge-base commit SHA between HEAD and
// origin/<baseBranch>, falling back to the local <baseBranch> ref when
// origin/<baseBranch> doesn't resolve at all (no "origin" remote — bare
// local repos, some unit tests). Mirrors the fallback order
// CountNewCommitsAgainstOrigin uses.
func (g *GitOperations) resolveMergeBaseSHA(ctx context.Context, baseBranch string) (string, error) {
	for _, ref := range []string{"origin/" + baseBranch, baseBranch} {
		cmd := exec.CommandContext(ctx, "git", "merge-base", ref, "HEAD")
		cmd.Dir = g.projectPath
		output, err := cmd.Output()
		if err == nil {
			return strings.TrimSpace(string(output)), nil
		}
	}
	return "", fmt.Errorf("could not resolve merge-base for %q against origin/%s or local %s", baseBranch, baseBranch, baseBranch)
}

// Pull fetches and merges changes from remote for the specified branch
func (g *GitOperations) Pull(ctx context.Context, branch string) error {
	cmd := exec.CommandContext(ctx, "git", "pull", "origin", branch)
	cmd.Dir = g.projectPath
	withGitCredentials(ctx, cmd)
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
	withGitCredentials(ctx, fetchCmd)
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
	withGitCredentials(ctx, cmd)
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	// ls-remote returns non-empty output if branch exists
	return len(strings.TrimSpace(string(output))) > 0
}
