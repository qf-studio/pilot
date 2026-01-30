package executor

import (
	"strings"
)

// Complexity represents the detected complexity level of a task.
// Used for routing decisions: which model to use, whether to skip Navigator, etc.
type Complexity string

const (
	// ComplexityTrivial is for minimal changes: typos, logs, renames, comment updates.
	// These skip Navigator overhead and use the fastest model.
	ComplexityTrivial Complexity = "trivial"

	// ComplexitySimple is for small, focused changes: add field, small fix, single function.
	// May skip Navigator for known patterns.
	ComplexitySimple Complexity = "simple"

	// ComplexityMedium is for standard feature work: new endpoint, component, integration.
	// Uses full Navigator workflow with default model.
	ComplexityMedium Complexity = "medium"

	// ComplexityComplex is for architectural changes: refactors, migrations, system redesigns.
	// Uses full Navigator workflow with the most capable model.
	ComplexityComplex Complexity = "complex"
)

// trivialPatterns are exact or partial matches indicating trivial tasks.
// Order matters: more specific patterns first.
var trivialPatterns = []string{
	"fix typo",
	"typo",
	"add log",
	"add logging",
	"update comment",
	"fix comment",
	"rename variable",
	"rename function",
	"rename",
	"remove unused",
	"delete unused",
	"bump version",
	"update version",
	"fix import",
	"add import",
	"fix whitespace",
	"formatting",
	"lint fix",
}

// simplePatterns indicate tasks that are small but require some thought.
var simplePatterns = []string{
	"add field",
	"add property",
	"add parameter",
	"add argument",
	"small fix",
	"minor fix",
	"quick fix",
	"update config",
	"change config",
	"update constant",
	"add constant",
	"add test case",
	"fix test",
}

// complexPatterns indicate tasks requiring significant architectural consideration.
var complexPatterns = []string{
	"refactor",
	"rewrite",
	"redesign",
	"migrate",
	"migration",
	"architecture",
	"restructure",
	"overhaul",
	"system",
	"database schema",
	"api design",
	"multi-file",
	"cross-cutting",
}

// DetectComplexity analyzes a task and returns its estimated complexity.
// The detection uses pattern matching on the description and heuristics
// like word count to estimate task complexity.
func DetectComplexity(task *Task) Complexity {
	if task == nil {
		return ComplexityMedium
	}

	desc := strings.ToLower(task.Description)
	title := strings.ToLower(task.Title)
	combined := desc + " " + title

	// Check trivial patterns first (fastest path)
	for _, pattern := range trivialPatterns {
		if strings.Contains(combined, pattern) {
			return ComplexityTrivial
		}
	}

	// Check complex patterns next (prevents false simple classification)
	for _, pattern := range complexPatterns {
		if strings.Contains(combined, pattern) {
			return ComplexityComplex
		}
	}

	// Check simple patterns
	for _, pattern := range simplePatterns {
		if strings.Contains(combined, pattern) {
			return ComplexitySimple
		}
	}

	// Heuristics based on description length
	wordCount := len(strings.Fields(desc))

	// Very short descriptions are likely simple tasks
	if wordCount < 10 {
		return ComplexitySimple
	}

	// Medium-length descriptions are standard feature work
	if wordCount < 50 {
		return ComplexityMedium
	}

	// Long descriptions suggest complex requirements
	return ComplexityComplex
}

// IsTrivial returns true if the task complexity is trivial.
func (c Complexity) IsTrivial() bool {
	return c == ComplexityTrivial
}

// IsSimple returns true if the task complexity is simple or trivial.
func (c Complexity) IsSimple() bool {
	return c == ComplexityTrivial || c == ComplexitySimple
}

// ShouldSkipNavigator returns true if Navigator overhead should be skipped.
// Only trivial tasks skip Navigator to avoid workflow overhead.
func (c Complexity) ShouldSkipNavigator() bool {
	return c == ComplexityTrivial
}

// String returns the string representation of the complexity level.
func (c Complexity) String() string {
	return string(c)
}

// ShouldRunResearch returns true if parallel research phase should run.
// Medium and complex tasks benefit from pre-execution research.
func (c Complexity) ShouldRunResearch() bool {
	return c == ComplexityMedium || c == ComplexityComplex
}
