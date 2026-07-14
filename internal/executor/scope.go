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

// bareFilePattern matches a bare top-level filename with a common
// source-code extension (no directory prefix). Root-level source files like
// version.go or CHANGELOG.md have no directory component, so filePathPattern
// (which requires at least one "/") never matches them and they were
// entirely invisible to scope detection — two subtasks that each touched a
// different bare top-level file looked scope-disjoint (0 directories found)
// and fell through to the unreliable title-word heuristic instead of being
// recognized as 2 distinct files (GH-4302: canary epic-lifecycle scenario
// touching version.go + CHANGELOG-CANARY.md collapsed to a single task).
var bareFilePattern = regexp.MustCompile(`\b([\w\-]+\.(?:go|py|ts|tsx|js|jsx|rs|java|rb|css|scss|html|yaml|yml|json|toml|sql|sh|md))\b`)

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

	// Extract directories from file paths. Also track each path's basename so
	// a bare mention of the same file elsewhere (e.g. a title saying "update
	// epic.go" alongside a description citing "internal/executor/epic.go")
	// isn't double-counted as its own separate scope key below.
	pathBasenames := make(map[string]bool)
	matches := filePathPattern.FindAllStringSubmatch(text, -1)
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		filePath := m[1]
		lastSlash := strings.LastIndex(filePath, "/")
		if lastSlash > 0 {
			dirs[filePath[:lastSlash]] = true
			pathBasenames[filePath[lastSlash+1:]] = true
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
	rootConfigMentions := RootConfigFileMentions(text)
	if len(rootConfigMentions) > 0 {
		dirs[RootScopeKey] = true
	}

	// Extract bare top-level filenames (no directory prefix, not already
	// folded into RootScopeKey above). Each distinct bare filename is its
	// own scope key so two different root-level source files are NOT
	// mistaken for the same package (GH-4302).
	for _, m := range bareFilePattern.FindAllStringSubmatchIndex(text, -1) {
		start, end := m[2], m[3]
		if start > 0 && text[start-1] == '/' {
			continue // tail of a longer path already handled above
		}
		filename := text[start:end]
		if rootConfigMentions[filename] {
			continue // already folded into RootScopeKey
		}
		if pathBasenames[filename] {
			continue // same file already accounted for via a full path elsewhere
		}
		dirs[filename] = true
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
