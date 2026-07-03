package executor

import (
	"regexp"
	"strings"
)

// filePathPattern matches file paths with common source-code extensions.
// Examples: internal/executor/runner.go, src/app/main.ts, lib/utils.py
var filePathPattern = regexp.MustCompile(`\b((?:[\w\-]+/)+[\w\-]+\.(?:go|py|ts|tsx|js|jsx|rs|java|rb|css|scss|html|yaml|yml|json|toml|sql|sh|md))\b`)

// dirOnlyPattern matches directory-only references ending with a slash.
// Examples: internal/comms/, src/utils/, cmd/pilot/
var dirOnlyPattern = regexp.MustCompile(`\b((?:[\w\-]+/)+[\w\-]+/)(?:\s|$|[),;:"'])`)

// rootConfigFilePattern matches root-level scaffold/config files. These live at
// the repo root, so they share no directory and filePathPattern's slash
// requirement never matches them — two issues that both bootstrap package.json
// look scope-disjoint even though dispatching them in parallel guarantees a
// merge conflict (GH-3714).
var rootConfigFilePattern = regexp.MustCompile(`\b(package(?:-lock)?\.json|tsconfig\.json|go\.(?:mod|sum)|Cargo\.(?:toml|lock)|Makefile|pnpm-lock\.yaml|yarn\.lock|vitest\.config\.\w+|eslint\.config\.\w+)\b`)

// RootScopeKey is the pseudo-directory used to represent a shared reference to
// a root-level config/scaffold file (see rootConfigFilePattern).
const RootScopeKey = "<root>"

// RootConfigFileMentions returns the set of distinct root-level config/scaffold
// files (package.json, go.mod, Makefile, lockfiles, etc.) referenced in text.
func RootConfigFileMentions(text string) map[string]bool {
	mentions := make(map[string]bool)
	for _, m := range rootConfigFilePattern.FindAllString(text, -1) {
		mentions[m] = true
	}
	return mentions
}

// ExtractDirectoriesFromText finds file paths and directory references in text
// and returns their unique parent directories. Handles both file paths
// (e.g., "internal/executor/runner.go" → "internal/executor") and bare directory
// references (e.g., "internal/comms/" → "internal/comms"). Mentions of
// root-level config/scaffold files (package.json, go.mod, Makefile, ...) are
// folded into the RootScopeKey pseudo-directory since they have no real parent.
func ExtractDirectoriesFromText(text string) map[string]bool {
	dirs := make(map[string]bool)

	// Extract directories from file paths
	matches := filePathPattern.FindAllStringSubmatch(text, -1)
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		filePath := m[1]
		lastSlash := strings.LastIndex(filePath, "/")
		if lastSlash > 0 {
			dirs[filePath[:lastSlash]] = true
		}
	}

	// Extract bare directory references (e.g., "internal/comms/")
	dirMatches := dirOnlyPattern.FindAllStringSubmatch(text, -1)
	for _, m := range dirMatches {
		if len(m) < 2 {
			continue
		}
		// Trim trailing slash
		dir := strings.TrimRight(m[1], "/")
		if dir != "" {
			dirs[dir] = true
		}
	}

	// Fold root-level config/scaffold file mentions into a shared pseudo-path
	// so two issues that both invent package.json/tsconfig.json/etc. collide
	// even though neither reference contains a directory separator.
	if len(RootConfigFileMentions(text)) > 0 {
		dirs[RootScopeKey] = true
	}

	return dirs
}

// IssuesOverlap returns true if two issue bodies reference at least one common
// directory, indicating they may cause merge conflicts when executed in parallel.
func IssuesOverlap(bodyA, bodyB string) bool {
	dirsA := ExtractDirectoriesFromText(bodyA)
	dirsB := ExtractDirectoriesFromText(bodyB)

	for dir := range dirsA {
		if dirsB[dir] {
			return true
		}
	}
	return false
}
