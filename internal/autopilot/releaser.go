package autopilot

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// Releaser handles automatic release creation after PR merge.
type Releaser struct {
	ghClient *github.Client
	owner    string
	repo     string
	config   *ReleaseConfig
}

// NewReleaser creates a new releaser.
func NewReleaser(ghClient *github.Client, owner, repo string, config *ReleaseConfig) *Releaser {
	return &Releaser{
		ghClient: ghClient,
		owner:    owner,
		repo:     repo,
		config:   config,
	}
}

// SemVer represents a semantic version.
type SemVer struct {
	Major int
	Minor int
	Patch int
}

// String returns the version string with prefix.
func (v SemVer) String(prefix string) string {
	return fmt.Sprintf("%s%d.%d.%d", prefix, v.Major, v.Minor, v.Patch)
}

// Bump increments the version based on bump type.
func (v SemVer) Bump(bumpType BumpType) SemVer {
	switch bumpType {
	case BumpMajor:
		return SemVer{Major: v.Major + 1, Minor: 0, Patch: 0}
	case BumpMinor:
		return SemVer{Major: v.Major, Minor: v.Minor + 1, Patch: 0}
	case BumpPatch:
		return SemVer{Major: v.Major, Minor: v.Minor, Patch: v.Patch + 1}
	default:
		return v
	}
}

// Compare returns -1, 0, or 1 depending on whether v is less than, equal to,
// or greater than other.
func (v SemVer) Compare(other SemVer) int {
	if v.Major != other.Major {
		if v.Major > other.Major {
			return 1
		}
		return -1
	}
	if v.Minor != other.Minor {
		if v.Minor > other.Minor {
			return 1
		}
		return -1
	}
	if v.Patch != other.Patch {
		if v.Patch > other.Patch {
			return 1
		}
		return -1
	}
	return 0
}

// ParseSemVer parses a version string like "v1.2.3" or "1.2.3".
func ParseSemVer(s string) (SemVer, error) {
	// Remove common prefixes
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")

	// Strip build metadata (everything after +)
	if idx := strings.Index(s, "+"); idx > 0 {
		s = s[:idx]
	}

	// Strip pre-release suffix (everything after first -)
	if idx := strings.Index(s, "-"); idx > 0 {
		s = s[:idx]
	}

	// Split by dots
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return SemVer{}, fmt.Errorf("invalid semver: %s", s)
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return SemVer{}, fmt.Errorf("invalid major version: %s", parts[0])
	}

	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return SemVer{}, fmt.Errorf("invalid minor version: %s", parts[1])
	}

	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return SemVer{}, fmt.Errorf("invalid patch version: %s", parts[2])
	}

	return SemVer{Major: major, Minor: minor, Patch: patch}, nil
}

// conventionalCommitRegex matches conventional commit format.
var conventionalCommitRegex = regexp.MustCompile(`^(\w+)(\(.+\))?(!)?:\s*(.+)`)

// DetectBumpType analyzes commit messages and returns the highest bump type needed.
func DetectBumpType(commits []*github.Commit) BumpType {
	maxBump := BumpNone

	for _, commit := range commits {
		msg := commit.Commit.Message
		// Get first line only
		if idx := strings.Index(msg, "\n"); idx > 0 {
			msg = msg[:idx]
		}

		bump := parseBumpFromMessage(msg)
		if bumpPriority(bump) > bumpPriority(maxBump) {
			maxBump = bump
		}
	}

	return maxBump
}

// parseBumpFromMessage parses a single commit message for bump type.
func parseBumpFromMessage(msg string) BumpType {
	matches := conventionalCommitRegex.FindStringSubmatch(msg)
	if matches == nil {
		return BumpNone
	}

	commitType := strings.ToLower(matches[1])
	breaking := matches[3] == "!"

	// Check for BREAKING CHANGE in type or marker
	if breaking || strings.HasPrefix(strings.ToUpper(commitType), "BREAKING") {
		return BumpMajor
	}

	// Check commit type
	switch commitType {
	case "feat", "feature":
		return BumpMinor
	case "fix", "bugfix", "perf":
		return BumpPatch
	case "docs", "doc", "style", "refactor", "test", "tests", "chore", "ci", "build":
		// These don't trigger releases by default
		return BumpNone
	default:
		return BumpNone
	}
}

// bumpPriority returns priority for comparison (higher = more significant).
func bumpPriority(b BumpType) int {
	switch b {
	case BumpMajor:
		return 3
	case BumpMinor:
		return 2
	case BumpPatch:
		return 1
	default:
		return 0
	}
}

// VersionBaseline is the resolved release baseline for a repo: the highest
// semver found across the latest published GitHub Release (if any) and
// every git tag, plus a human-readable Source describing which ref produced
// it (e.g. "tag:v0.35.0" or "release:v1.2.3"). GH-4953: a tag pushed without
// ever going through the Releases API (e.g. a manual base-guard tag) has no
// backing Release object, so a baseline sourced from GetLatestRelease alone
// is blind to it — the sdk train cut v0.34.2 as the "next" version while
// v0.35.0 already existed as a tag-only ref, shipping a version Go module
// resolution ranks BELOW the newer commit.
type VersionBaseline struct {
	Version SemVer
	Source  string
}

// currentVersionBaseline computes the release baseline for owner/repo as the
// max semver across the latest GitHub Release (if any) and ALL git tags —
// regardless of who created a tag or whether it has a backing Release
// object (mem-093 / GH-4953). Every release also has a backing tag, so this
// is normally a superset check; the release lookup is kept alongside the
// tag scan only as a defensive fallback in case ListTags is ever
// unreachable while the Releases API is not.
func (r *Releaser) currentVersionBaseline(ctx context.Context, owner, repo string) (VersionBaseline, error) {
	var candidates []VersionBaseline

	release, err := r.ghClient.GetLatestRelease(ctx, owner, repo)
	if err != nil {
		return VersionBaseline{}, fmt.Errorf("failed to get latest release: %w", err)
	}
	if release != nil {
		if v, perr := ParseSemVer(release.TagName); perr == nil {
			candidates = append(candidates, VersionBaseline{Version: v, Source: "release:" + release.TagName})
		}
	}

	// ListTags paginates exhaustively regardless of the perPage page-size
	// argument (see studio-sdk client.go), so this covers every tag in the
	// repo's history, not just the most recent page.
	tags, err := r.ghClient.ListTags(ctx, owner, repo, 100)
	if err != nil {
		return VersionBaseline{}, fmt.Errorf("failed to list tags: %w", err)
	}
	for _, tag := range tags {
		v, perr := ParseSemVer(tag.Name)
		if perr != nil {
			continue
		}
		candidates = append(candidates, VersionBaseline{Version: v, Source: "tag:" + tag.Name})
	}

	if len(candidates) == 0 {
		return VersionBaseline{Version: SemVer{}, Source: "none"}, nil
	}

	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.Version.Compare(best.Version) > 0 {
			best = c
		}
	}
	return best, nil
}

// CurrentVersionBaselineForRepo exposes the full VersionBaseline (version +
// source) for the specified repo, for callers that need to log how the
// baseline was determined (GH-4953 acceptance: "Release decision log line
// includes baseline version + how it was found").
func (r *Releaser) CurrentVersionBaselineForRepo(ctx context.Context, owner, repo string) (VersionBaseline, error) {
	return r.currentVersionBaseline(ctx, owner, repo)
}

// GetCurrentVersion returns the current version baseline (see
// currentVersionBaseline) for the releaser's configured repo.
func (r *Releaser) GetCurrentVersion(ctx context.Context) (SemVer, error) {
	baseline, err := r.currentVersionBaseline(ctx, r.owner, r.repo)
	if err != nil {
		return SemVer{}, err
	}
	return baseline.Version, nil
}

// GenerateChangelog generates a changelog from commits.
func GenerateChangelog(commits []*github.Commit, prNumber int) string {
	var features, fixes, others []string

	for _, commit := range commits {
		msg := commit.Commit.Message
		// Get first line
		if idx := strings.Index(msg, "\n"); idx > 0 {
			msg = msg[:idx]
		}

		matches := conventionalCommitRegex.FindStringSubmatch(msg)
		if matches == nil {
			others = append(others, fmt.Sprintf("- %s", msg))
			continue
		}

		commitType := strings.ToLower(matches[1])
		description := matches[4]

		switch commitType {
		case "feat", "feature":
			features = append(features, fmt.Sprintf("- %s", description))
		case "fix", "bugfix":
			fixes = append(fixes, fmt.Sprintf("- %s", description))
		default:
			others = append(others, fmt.Sprintf("- %s", description))
		}
	}

	var sections []string

	if len(features) > 0 {
		sections = append(sections, "## Features\n"+strings.Join(features, "\n"))
	}
	if len(fixes) > 0 {
		sections = append(sections, "## Bug Fixes\n"+strings.Join(fixes, "\n"))
	}
	if len(others) > 0 {
		sections = append(sections, "## Other Changes\n"+strings.Join(others, "\n"))
	}

	if len(sections) == 0 {
		return fmt.Sprintf("Release from PR #%d", prNumber)
	}

	return strings.Join(sections, "\n\n")
}

// CreateTag creates a lightweight git tag for the new version.
// The actual GitHub Release (with binary assets) is created by GoReleaser CI
// which triggers on tag push. This avoids the conflict where both Pilot and
// GoReleaser try to create the same release.
func (r *Releaser) CreateTag(ctx context.Context, prState *PRState, newVersion SemVer) (string, error) {
	tagName := newVersion.String(r.config.TagPrefix)
	sha := prState.HeadSHA

	if err := r.ghClient.CreateGitTag(ctx, r.owner, r.repo, tagName, sha); err != nil {
		return "", fmt.Errorf("failed to create tag %s: %w", tagName, err)
	}

	return tagName, nil
}

// CreateTagForRepo creates a lightweight git tag in the specified repository.
// Used for cross-repo PRs where the tag should be created in the source repo,
// not the default repo configured in the Releaser.
func (r *Releaser) CreateTagForRepo(ctx context.Context, owner, repo string, prState *PRState, newVersion SemVer) (string, error) {
	tagName := newVersion.String(r.config.TagPrefix)
	sha := prState.HeadSHA

	if err := r.ghClient.CreateGitTag(ctx, owner, repo, tagName, sha); err != nil {
		return "", fmt.Errorf("failed to create tag %s: %w", tagName, err)
	}

	return tagName, nil
}

// CreateReleaseForRepo publishes a GitHub Release for tagName in the specified
// repository via the REST API (publish mode "api", GH-3926). When body is
// empty, GenerateNotes is requested so GitHub compiles release notes from the
// commits since the previous release.
func (r *Releaser) CreateReleaseForRepo(ctx context.Context, owner, repo, tagName, body string) (*github.Release, error) {
	input := &github.ReleaseInput{
		TagName:       tagName,
		Name:          tagName,
		Body:          body,
		GenerateNotes: body == "",
	}
	release, err := r.ghClient.CreateRelease(ctx, owner, repo, input)
	if err != nil {
		return nil, fmt.Errorf("failed to create release %s: %w", tagName, err)
	}
	return release, nil
}

// isDuplicateReleaseError reports whether err indicates a release already
// exists for the requested tag. GitHub returns HTTP 422 with an
// `"already_exists"` error code when POSTing /releases for a tag that already
// has a release — treated as success so a retry after a transient failure
// doesn't loop forever trying to recreate it.
func isDuplicateReleaseError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "already_exists")
}

// GetCurrentVersionForRepo returns the current version baseline (see
// currentVersionBaseline) for the specified repository.
func (r *Releaser) GetCurrentVersionForRepo(ctx context.Context, owner, repo string) (SemVer, error) {
	baseline, err := r.currentVersionBaseline(ctx, owner, repo)
	if err != nil {
		return SemVer{}, err
	}
	return baseline.Version, nil
}

// ShouldRelease determines if a release should be created based on config and bump type.
// Accepts all three auto-release triggers (on_merge, on_scope_close,
// on_schedule) — "manual" and unset never auto-release. Hold semantics for
// on_scope_close/on_schedule (deciding whether THIS merge should release now
// vs. wait) are enforced upstream by Controller.releaseActionFor; by the time
// a PR reaches handleReleasing/DetectBumpType it has already cleared any hold
// (GH-3989).
func (r *Releaser) ShouldRelease(bumpType BumpType) bool {
	if !r.config.Enabled {
		return false
	}
	switch r.config.Trigger {
	case "on_merge", "on_scope_close", "on_schedule":
	default:
		return false
	}
	return bumpType != BumpNone
}
