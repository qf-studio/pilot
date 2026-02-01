package autopilot

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/alekspetrov/pilot/internal/adapters/github"
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

// GetCurrentVersion returns the current version from latest release or tags.
func (r *Releaser) GetCurrentVersion(ctx context.Context) (SemVer, error) {
	// Try latest release first
	release, err := r.ghClient.GetLatestRelease(ctx, r.owner, r.repo)
	if err != nil {
		return SemVer{}, fmt.Errorf("failed to get latest release: %w", err)
	}
	if release != nil {
		return ParseSemVer(release.TagName)
	}

	// Fall back to tags
	tags, err := r.ghClient.ListTags(ctx, r.owner, r.repo, 10)
	if err != nil {
		return SemVer{}, fmt.Errorf("failed to list tags: %w", err)
	}

	// Find highest semver tag
	var versions []SemVer
	for _, tag := range tags {
		if v, err := ParseSemVer(tag.Name); err == nil {
			versions = append(versions, v)
		}
	}

	if len(versions) == 0 {
		// No versions found, start at 0.0.0
		return SemVer{}, nil
	}

	// Sort descending
	sort.Slice(versions, func(i, j int) bool {
		if versions[i].Major != versions[j].Major {
			return versions[i].Major > versions[j].Major
		}
		if versions[i].Minor != versions[j].Minor {
			return versions[i].Minor > versions[j].Minor
		}
		return versions[i].Patch > versions[j].Patch
	})

	return versions[0], nil
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

// CreateRelease creates a new GitHub release.
func (r *Releaser) CreateRelease(ctx context.Context, prState *PRState, newVersion SemVer, changelog string) (*github.Release, error) {
	tagName := newVersion.String(r.config.TagPrefix)

	input := &github.ReleaseInput{
		TagName:         tagName,
		TargetCommitish: "main", // Release from main after merge
		Name:            tagName,
		Body:            changelog,
		Draft:           false,
		Prerelease:      false,
	}

	release, err := r.ghClient.CreateRelease(ctx, r.owner, r.repo, input)
	if err != nil {
		return nil, fmt.Errorf("failed to create release: %w", err)
	}

	return release, nil
}

// ShouldRelease determines if a release should be created based on config and bump type.
func (r *Releaser) ShouldRelease(bumpType BumpType) bool {
	if !r.config.Enabled {
		return false
	}
	if r.config.Trigger != "on_merge" {
		return false
	}
	return bumpType != BumpNone
}
