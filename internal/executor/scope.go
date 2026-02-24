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

// ExtractDirectoriesFromText finds file paths and directory references in text
// and returns their unique parent directories. Handles both file paths
// (e.g., "internal/executor/runner.go" → "internal/executor") and bare directory
// references (e.g., "internal/comms/" → "internal/comms").
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
