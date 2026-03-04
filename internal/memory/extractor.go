package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// PatternExtractor extracts patterns from execution results
type PatternExtractor struct {
	store          *GlobalPatternStore
	execStore      *Store
	minOccurrences int
	minConfidence  float64
}

// NewPatternExtractor creates a new pattern extractor
func NewPatternExtractor(patternStore *GlobalPatternStore, execStore *Store) *PatternExtractor {
	return &PatternExtractor{
		store:          patternStore,
		execStore:      execStore,
		minOccurrences: 3,
		minConfidence:  0.7,
	}
}

// ExtractedPattern represents a pattern found in execution output
type ExtractedPattern struct {
	Type        PatternType
	Title       string
	Description string
	Examples    []string
	Confidence  float64
	Context     string // e.g., "Go handlers", "React components"
}

// ExtractionResult holds the result of pattern extraction
type ExtractionResult struct {
	ExecutionID  string
	ProjectPath  string
	Patterns     []*ExtractedPattern
	AntiPatterns []*ExtractedPattern
	ExtractedAt  time.Time
}

// ExtractFromExecution extracts patterns from a completed execution
func (e *PatternExtractor) ExtractFromExecution(ctx context.Context, exec *Execution) (*ExtractionResult, error) {
	if exec.Status != "completed" {
		return nil, fmt.Errorf("can only extract patterns from completed executions")
	}

	result := &ExtractionResult{
		ExecutionID:  exec.ID,
		ProjectPath:  exec.ProjectPath,
		Patterns:     make([]*ExtractedPattern, 0),
		AntiPatterns: make([]*ExtractedPattern, 0),
		ExtractedAt:  time.Now(),
	}

	// Extract code patterns from output
	codePatterns := e.extractCodePatterns(exec.Output)
	result.Patterns = append(result.Patterns, codePatterns...)

	// Extract error patterns (anti-patterns)
	if exec.Error != "" {
		errorPatterns := e.extractErrorPatterns(exec.Error)
		result.AntiPatterns = append(result.AntiPatterns, errorPatterns...)
	}

	// Extract workflow patterns
	workflowPatterns := e.extractWorkflowPatterns(exec.Output)
	result.Patterns = append(result.Patterns, workflowPatterns...)

	return result, nil
}

// ExtractFromReviewComments extracts patterns from PR review comments
func (e *PatternExtractor) ExtractFromReviewComments(ctx context.Context,
	comments []string, projectPath string) (*ExtractionResult, error) {
	result := &ExtractionResult{
		ExecutionID:  fmt.Sprintf("review_%d", time.Now().UnixNano()),
		ProjectPath:  projectPath,
		Patterns:     make([]*ExtractedPattern, 0),
		AntiPatterns: make([]*ExtractedPattern, 0),
		ExtractedAt:  time.Now(),
	}

	// Combine all comments for analysis
	combinedComments := strings.Join(comments, " ")

	// Review comment matchers for anti-patterns and positive feedback
	reviewMatchers := []struct {
		patterns    []string // Keywords to match
		pType       PatternType
		title       string
		desc        string
		isAnti      bool
		confidence  float64
	}{
		{
			patterns:   []string{"add test", "test coverage", "missing test"},
			pType:      PatternTypeWorkflow,
			title:      "Add tests",
			desc:       "Ensure comprehensive test coverage for all functions",
			isAnti:     true,
			confidence: 0.75,
		},
		{
			patterns:   []string{"naming", "unclear variable", "rename", "confusing name"},
			pType:      PatternTypeNaming,
			title:      "Improve naming",
			desc:       "Use clear, descriptive names for variables and functions",
			isAnti:     true,
			confidence: 0.7,
		},
		{
			patterns:   []string{"error handling", "unchecked error", "handle error"},
			pType:      PatternTypeError,
			title:      "Add error handling",
			desc:       "Properly handle and propagate errors",
			isAnti:     true,
			confidence: 0.75,
		},
		{
			patterns:   []string{"documentation", "add comment", "unclear", "explain"},
			pType:      PatternTypeCode,
			title:      "Add documentation",
			desc:       "Add comments and documentation for clarity",
			isAnti:     true,
			confidence: 0.65,
		},
		{
			patterns:   []string{"good pattern", "nice approach", "well done", "excellent", "clean implementation"},
			pType:      PatternTypeCode,
			title:      "Well-implemented pattern",
			desc:       "This implementation follows good practices",
			isAnti:     false,
			confidence: 0.8,
		},
	}

	// Check each matcher against comments
	for _, matcher := range reviewMatchers {
		found := false
		lowerComments := strings.ToLower(combinedComments)

		for _, pattern := range matcher.patterns {
			if strings.Contains(lowerComments, strings.ToLower(pattern)) {
				found = true
				break
			}
		}

		if found {
			p := &ExtractedPattern{
				Type:        matcher.pType,
				Title:       matcher.title,
				Description: matcher.desc,
				Examples:    comments,
				Confidence:  matcher.confidence,
				Context:     "PR review",
			}

			if matcher.isAnti {
				result.AntiPatterns = append(result.AntiPatterns, p)
			} else {
				result.Patterns = append(result.Patterns, p)
			}
		}
	}

	return result, nil
}

// codePatternMatcher maps a compiled regex to the pattern metadata it produces.
type codePatternMatcher struct {
	regex   *regexp.Regexp
	pType   PatternType
	title   string
	desc    string
	context string
}

// codePatternMatchers holds all 11 categories of code pattern matchers used by
// extractCodePatterns. len(codePatternMatchers) == 11.
var codePatternMatchers = []codePatternMatcher{
	// 1. Context / cancellation
	{
		regex:   regexp.MustCompile(`(?i)using\s+context\.Context\s+in\s+(\w+)`),
		pType:   PatternTypeCode,
		title:   "Use context.Context for cancellation",
		desc:    "Pass context.Context to functions for proper cancellation and timeout handling",
		context: "Go handlers",
	},
	// 2. Error handling
	{
		regex:   regexp.MustCompile(`(?i)added\s+error\s+handling\s+for\s+(\w+)`),
		pType:   PatternTypeCode,
		title:   "Explicit error handling",
		desc:    "Always handle errors explicitly rather than ignoring them",
		context: "Go functions",
	},
	// 3. Test-driven implementation
	{
		regex:   regexp.MustCompile(`(?i)created?\s+test[s]?\s+for\s+(\w+)`),
		pType:   PatternTypeWorkflow,
		title:   "Test-driven implementation",
		desc:    "Create tests alongside implementation code",
		context: "All code",
	},
	// 4. Structured logging
	{
		regex:   regexp.MustCompile(`(?i)using\s+(zap|slog|logrus)\s+for\s+logging`),
		pType:   PatternTypeCode,
		title:   "Structured logging",
		desc:    "Use structured logging library instead of fmt.Printf",
		context: "Go services",
	},
	// 5. Input validation
	{
		regex:   regexp.MustCompile(`(?i)added?\s+validation\s+for\s+(\w+)`),
		pType:   PatternTypeCode,
		title:   "Input validation",
		desc:    "Validate inputs at system boundaries",
		context: "API handlers",
	},
	// 6. API Design — endpoint patterns, request/response structure, middleware usage
	{
		regex:   regexp.MustCompile(`(?i)(?:http\.Handler|router\.\w+|\bendpoint\b|\bmiddleware\b|\bREST\b|\bGraphQL\b)`),
		pType:   PatternTypeCode,
		title:   "API design pattern",
		desc:    "Organise HTTP handlers, middleware, and routing following REST or GraphQL conventions",
		context: "API design",
	},
	// 7. Concurrency — goroutine patterns, mutex usage, channel patterns, sync primitives
	{
		regex:   regexp.MustCompile(`(?i)(?:go\s+func|sync\.Mutex|sync\.WaitGroup|chan\s+\w+|select\s*\{|sync\.Once)`),
		pType:   PatternTypeCode,
		title:   "Concurrency pattern",
		desc:    "Use goroutines, channels, and sync primitives for safe concurrent access",
		context: "Concurrency",
	},
	// 8. Config wiring — YAML tags, env vars, Viper, mapstructure
	{
		regex:   regexp.MustCompile("(?i)(?:yaml:\"|env:\"|config\\.\\w+|viper\\.\\w+|\\bmapstructure\\b|os\\.Getenv)"),
		pType:   PatternTypeCode,
		title:   "Config wiring pattern",
		desc:    "Load configuration from YAML, environment variables, or Viper with proper struct tags",
		context: "Config wiring",
	},
	// 9. Test patterns — table-driven tests, httptest, mocks, testify assertions
	{
		regex:   regexp.MustCompile(`(?i)(?:t\.Run\s*\(|httptest\.\w+|\bmock\w*\b|\btestify\b|assert\.\w+|require\.\w+)`),
		pType:   PatternTypeWorkflow,
		title:   "Test pattern",
		desc:    "Use table-driven tests, httptest, mocks, and testify assertions for comprehensive coverage",
		context: "Test patterns",
	},
	// 10. Performance — caching, connection pooling, batch ops, indexing
	{
		regex:   regexp.MustCompile(`(?i)(?:\bcache\b|\bpool\b|\bbatch\b|\bindex\b|\bSetMaxOpenConns\b|sync\.Pool)`),
		pType:   PatternTypeCode,
		title:   "Performance pattern",
		desc:    "Apply caching, connection pooling, batching, and indexing for performance-critical paths",
		context: "Performance",
	},
	// 11. Security — auth, token validation, sanitisation, RBAC, HMAC
	{
		regex:   regexp.MustCompile(`(?i)(?:\bauthentication\b|\bauthorization\b|\btoken\s+\w+|\bsanitize\b|\bescape\b|\bpermission\b|\brbac\b|\bhmac\b)`),
		pType:   PatternTypeCode,
		title:   "Security pattern",
		desc:    "Apply authentication, token validation, input sanitisation, RBAC, and HMAC signing",
		context: "Security",
	},
}

// extractCodePatterns extracts code-related patterns
func (e *PatternExtractor) extractCodePatterns(output string) []*ExtractedPattern {
	var patterns []*ExtractedPattern

	for _, matcher := range codePatternMatchers {
		if matches := matcher.regex.FindAllStringSubmatch(output, -1); len(matches) > 0 {
			examples := make([]string, 0, len(matches))
			for _, m := range matches {
				if len(m) > 1 {
					examples = append(examples, m[1])
				}
			}
			patterns = append(patterns, &ExtractedPattern{
				Type:        matcher.pType,
				Title:       matcher.title,
				Description: matcher.desc,
				Examples:    examples,
				Confidence:  0.7, // Base confidence, adjusted by occurrences
				Context:     matcher.context,
			})
		}
	}

	return patterns
}

// extractErrorPatterns extracts patterns from errors (anti-patterns to avoid)
func (e *PatternExtractor) extractErrorPatterns(errorOutput string) []*ExtractedPattern {
	var patterns []*ExtractedPattern

	errorMatchers := []struct {
		regex   *regexp.Regexp
		pType   PatternType
		title   string
		desc    string
		context string
	}{
		{
			regex:   regexp.MustCompile(`(?i)nil\s+pointer\s+dereference`),
			pType:   PatternTypeError,
			title:   "Nil pointer dereference",
			desc:    "Always check for nil before dereferencing pointers",
			context: "Go code",
		},
		{
			regex:   regexp.MustCompile(`(?i)sql:\s+no\s+rows\s+in\s+result\s+set`),
			pType:   PatternTypeError,
			title:   "Handle SQL no rows",
			desc:    "Check for sql.ErrNoRows when querying database",
			context: "Go database code",
		},
		{
			regex:   regexp.MustCompile(`(?i)context\s+deadline\s+exceeded`),
			pType:   PatternTypeError,
			title:   "Context timeout",
			desc:    "Handle context deadline exceeded errors gracefully",
			context: "Async operations",
		},
		{
			regex:   regexp.MustCompile(`(?i)race\s+condition\s+detected`),
			pType:   PatternTypeError,
			title:   "Race condition detected",
			desc:    "Use mutex or channels for concurrent access",
			context: "Concurrent Go code",
		},
		{
			regex:   regexp.MustCompile(`(?i)import\s+cycle\s+not\s+allowed`),
			pType:   PatternTypeStructure,
			title:   "Import cycle",
			desc:    "Restructure packages to avoid import cycles",
			context: "Go package structure",
		},
		// CI compilation errors
		{
			regex:   regexp.MustCompile(`(?i)undefined:\s*\w+`),
			pType:   PatternTypeError,
			title:   "Undefined identifier",
			desc:    "Ensure all identifiers are declared or imported before use",
			context: "Go compilation",
		},
		{
			regex:   regexp.MustCompile(`(?i)\w+\s+declared\s+(?:and\s+)?not\s+used`),
			pType:   PatternTypeError,
			title:   "Unused variable or import",
			desc:    "Remove unused variables and imports to pass compilation",
			context: "Go compilation",
		},
		{
			regex:   regexp.MustCompile(`(?i)cannot\s+use\s+.*\s+as\s+.*\s+in`),
			pType:   PatternTypeError,
			title:   "Type mismatch",
			desc:    "Ensure type compatibility in assignments and function calls",
			context: "Go compilation",
		},
		// CI test failures
		{
			regex:   regexp.MustCompile(`(?m)^---\s+FAIL:\s+\w+`),
			pType:   PatternTypeError,
			title:   "Test failure",
			desc:    "One or more tests failed during CI; investigate and fix failing assertions",
			context: "Go test",
		},
		{
			regex:   regexp.MustCompile(`(?i)panic:\s+test\s+timed\s+out|test\s+.*\s+timed?\s*out`),
			pType:   PatternTypeError,
			title:   "Test timeout",
			desc:    "Test exceeded its deadline; check for blocking operations or infinite loops",
			context: "Go test",
		},
		{
			regex:   regexp.MustCompile(`(?i)panic:\s+runtime\s+error`),
			pType:   PatternTypeError,
			title:   "Runtime panic in test",
			desc:    "A test triggered a runtime panic; add nil checks and bounds validation",
			context: "Go test",
		},
		// CI lint errors
		{
			regex:   regexp.MustCompile(`(?i)(?:golangci-lint|staticcheck|errcheck|govet|gosimple)\s*[:\[]`),
			pType:   PatternTypeWorkflow,
			title:   "Lint violation",
			desc:    "Code does not pass linter checks; fix reported issues before merging",
			context: "Go lint",
		},
		// CI build/module errors
		{
			regex:   regexp.MustCompile(`(?i)missing\s+go\.sum\s+entry|missing\s+module`),
			pType:   PatternTypeError,
			title:   "Missing module",
			desc:    "Run 'go mod tidy' to resolve missing module or go.sum entries",
			context: "Go modules",
		},
		{
			regex:   regexp.MustCompile(`(?i)require\s+.*:\s+version\s+"[^"]*"\s+invalid|go\.mod\s+.*\s+version\s+mismatch`),
			pType:   PatternTypeError,
			title:   "Module version conflict",
			desc:    "Resolve version conflicts in go.mod; ensure compatible dependency versions",
			context: "Go modules",
		},
	}

	for _, matcher := range errorMatchers {
		if matcher.regex.MatchString(errorOutput) {
			patterns = append(patterns, &ExtractedPattern{
				Type:        matcher.pType,
				Title:       matcher.title,
				Description: matcher.desc,
				Examples:    []string{errorOutput[:min(200, len(errorOutput))]},
				Confidence:  0.8, // Higher confidence for errors
				Context:     matcher.context,
			})
		}
	}

	return patterns
}

// ciCategoryMatcher defines a CI error pattern with its category classification.
type ciCategoryMatcher struct {
	regex    *regexp.Regexp
	pType    PatternType
	title    string
	desc     string
	category string // compilation, test, lint, build
}

// ciMatchers are pre-compiled matchers organized by CI error category.
var ciMatchers = []ciCategoryMatcher{
	// --- Compilation errors ---
	{
		regex:    regexp.MustCompile(`(?i)undefined:\s*\w+`),
		pType:    PatternTypeError,
		title:    "Undefined identifier",
		desc:     "Ensure all identifiers are declared or imported before use",
		category: "compilation",
	},
	{
		regex:    regexp.MustCompile(`(?i)cannot\s+use\s+.*\s+as\s+.*\s+in`),
		pType:    PatternTypeError,
		title:    "Type mismatch",
		desc:     "Ensure type compatibility in assignments and function calls",
		category: "compilation",
	},
	{
		regex:    regexp.MustCompile(`(?i)\w+\s+declared\s+(?:and\s+)?not\s+used`),
		pType:    PatternTypeError,
		title:    "Unused variable or import",
		desc:     "Remove unused variables and imports to pass compilation",
		category: "compilation",
	},
	{
		regex:    regexp.MustCompile(`(?i)missing\s+return\s+at\s+end\s+of\s+function`),
		pType:    PatternTypeError,
		title:    "Missing return",
		desc:     "All code paths in a function must end with a return statement",
		category: "compilation",
	},
	// --- Test failures ---
	{
		regex:    regexp.MustCompile(`(?m)^---\s+FAIL:\s+\w+`),
		pType:    PatternTypeError,
		title:    "Test failure",
		desc:     "One or more tests failed during CI; investigate and fix failing assertions",
		category: "test",
	},
	{
		regex:    regexp.MustCompile(`(?i)panic:\s+test\s+timed\s+out|test\s+.*\s+timed?\s*out`),
		pType:    PatternTypeError,
		title:    "Test timeout",
		desc:     "Test exceeded its deadline; check for blocking operations or infinite loops",
		category: "test",
	},
	{
		regex:    regexp.MustCompile(`(?i)(?:expected\s+.*\s+(?:but\s+)?got|assert(?:ion)?\s+fail|Expected\s+.*\s+to\s+(?:equal|be|match))`),
		pType:    PatternTypeError,
		title:    "Assertion failure",
		desc:     "Test assertion did not match expected value; verify logic and test data",
		category: "test",
	},
	{
		regex:    regexp.MustCompile(`(?i)panic:\s+runtime\s+error`),
		pType:    PatternTypeError,
		title:    "Runtime panic in test",
		desc:     "A test triggered a runtime panic; add nil checks and bounds validation",
		category: "test",
	},
	// --- Lint errors ---
	{
		regex:    regexp.MustCompile(`(?i)golangci-lint\s*[:\[]`),
		pType:    PatternTypeWorkflow,
		title:    "golangci-lint violation",
		desc:     "Code does not pass golangci-lint checks; fix reported issues before merging",
		category: "lint",
	},
	{
		regex:    regexp.MustCompile(`(?i)staticcheck\s*[:\[]`),
		pType:    PatternTypeWorkflow,
		title:    "staticcheck finding",
		desc:     "staticcheck found issues; address reported diagnostics",
		category: "lint",
	},
	{
		regex:    regexp.MustCompile(`(?i)errcheck\s*[:\[]`),
		pType:    PatternTypeWorkflow,
		title:    "errcheck violation",
		desc:     "Unchecked error return values detected; handle all returned errors",
		category: "lint",
	},
	// --- Build/module errors ---
	{
		regex:    regexp.MustCompile(`(?i)missing\s+go\.sum\s+entry|missing\s+module`),
		pType:    PatternTypeError,
		title:    "Missing module",
		desc:     "Run 'go mod tidy' to resolve missing module or go.sum entries",
		category: "build",
	},
	{
		regex:    regexp.MustCompile(`(?i)require\s+.*:\s+version\s+"[^"]*"\s+invalid|go\.mod\s+.*\s+version\s+mismatch`),
		pType:    PatternTypeError,
		title:    "Module version conflict",
		desc:     "Resolve version conflicts in go.mod; ensure compatible dependency versions",
		category: "build",
	},
	{
		regex:    regexp.MustCompile(`(?i)import\s+cycle\s+not\s+allowed`),
		pType:    PatternTypeStructure,
		title:    "Import cycle",
		desc:     "Restructure packages to avoid import cycles",
		category: "build",
	},
	{
		regex:    regexp.MustCompile(`(?i)cannot\s+find\s+package|could\s+not\s+import`),
		pType:    PatternTypeError,
		title:    "Missing dependency",
		desc:     "Required package not found; check import paths and run 'go mod tidy'",
		category: "build",
	},
}

// extractCIErrorPatterns extracts error patterns from CI logs using CI-specific
// category matchers. Each pattern is tagged with source:ci, its category
// (compilation/test/lint/build), and optional check names. Confidence starts at
// 0.5 for CI-sourced patterns (boosted on recurrence via SaveExtractedPatterns).
// Returns empty result for blank ciLogs.
func (e *PatternExtractor) extractCIErrorPatterns(ciLogs string, checkNames ...string) []*ExtractedPattern {
	if strings.TrimSpace(ciLogs) == "" {
		return nil
	}

	var patterns []*ExtractedPattern

	for _, m := range ciMatchers {
		if !m.regex.MatchString(ciLogs) {
			continue
		}

		ctx := fmt.Sprintf("source:ci category:%s", m.category)
		if len(checkNames) > 0 {
			ctx += " check:" + strings.Join(checkNames, ",")
		}

		patterns = append(patterns, &ExtractedPattern{
			Type:        m.pType,
			Title:       m.title,
			Description: m.desc,
			Examples:    []string{ciLogs[:min(200, len(ciLogs))]},
			Confidence:  0.5,
			Context:     ctx,
		})
	}

	return patterns
}

// extractWorkflowPatterns extracts workflow-related patterns
func (e *PatternExtractor) extractWorkflowPatterns(output string) []*ExtractedPattern {
	var patterns []*ExtractedPattern

	workflowIndicators := []struct {
		indicator string
		pType     PatternType
		title     string
		desc      string
	}{
		{
			indicator: "make test",
			pType:     PatternTypeWorkflow,
			title:     "Run tests via make",
			desc:      "Use make test for consistent test execution",
		},
		{
			indicator: "make lint",
			pType:     PatternTypeWorkflow,
			title:     "Run linter via make",
			desc:      "Use make lint for code quality checks",
		},
		{
			indicator: "git commit",
			pType:     PatternTypeWorkflow,
			title:     "Commit changes",
			desc:      "Commit changes after implementation",
		},
	}

	for _, ind := range workflowIndicators {
		if strings.Contains(strings.ToLower(output), ind.indicator) {
			patterns = append(patterns, &ExtractedPattern{
				Type:        ind.pType,
				Title:       ind.title,
				Description: ind.desc,
				Confidence:  0.6,
			})
		}
	}

	return patterns
}

// selfReviewFinding defines a mapping from self-review markers to pattern types
type selfReviewFinding struct {
	regex   *regexp.Regexp
	pType   PatternType
	title   string
	desc    string
	context string
}

// selfReviewFindings are the matchers for self-review output markers
var selfReviewFindings = []selfReviewFinding{
	{
		regex:   regexp.MustCompile(`(?i)(?:missing|no|lack(?:ing)?)\s+error\s+handl`),
		pType:   PatternTypeError,
		title:   "Missing error handling",
		desc:    "Errors must be checked and propagated, not silently ignored",
		context: "Self-review",
	},
	{
		regex:   regexp.MustCompile(`(?i)(?:unused\s+import|dead\s+code|unreachable\s+code)`),
		pType:   PatternTypeCode,
		title:   "Dead code detected",
		desc:    "Remove unused imports, unreachable code, and dead code paths",
		context: "Self-review",
	},
	{
		regex:   regexp.MustCompile(`(?i)(?:config\s+field\s+not\s+wired|struct\s+field\s+unused|field\s+.*not\s+(?:used|wired|connected))`),
		pType:   PatternTypeStructure,
		title:   "Unwired config or struct field",
		desc:    "All config and struct fields must be wired through to their consumers",
		context: "Self-review",
	},
	{
		regex:   regexp.MustCompile(`(?i)(?:test\s+coverage\s+gap|missing\s+test|no\s+test|lint\s+violation|linter\s+error)`),
		pType:   PatternTypeWorkflow,
		title:   "Test or lint gap",
		desc:    "All new code paths must have test coverage; lint violations must be resolved",
		context: "Self-review",
	},
	{
		regex:   regexp.MustCompile(`(?i)(?:build\s+(?:fail|verification\s+fail)|compilation?\s+(?:error|fail))`),
		pType:   PatternTypeError,
		title:   "Build verification failure",
		desc:    "Code must compile and pass build verification before submission",
		context: "Self-review",
	},
	{
		regex:   regexp.MustCompile(`(?i)(?:cross.file\s+parity|PARITY_GAP|parity\s+(?:issue|mismatch))`),
		pType:   PatternTypeStructure,
		title:   "Cross-file parity issue",
		desc:    "Related changes across files must stay in sync (interfaces, implementations, tests)",
		context: "Self-review",
	},
	{
		regex:   regexp.MustCompile(`(?i)REVIEW_FIXED:`),
		pType:   PatternTypeCode,
		title:   "Self-review fix applied",
		desc:    "Issue caught and fixed during self-review pass",
		context: "Self-review",
	},
	{
		regex:   regexp.MustCompile(`(?i)SUSPICIOUS_VALUE`),
		pType:   PatternTypeCode,
		title:   "Suspicious value detected",
		desc:    "Hardcoded or suspicious values should be verified against requirements",
		context: "Self-review",
	},
	{
		regex:   regexp.MustCompile(`(?i)INCOMPLETE`),
		pType:   PatternTypeWorkflow,
		title:   "Incomplete implementation",
		desc:    "Implementation is missing required functionality or TODO items remain",
		context: "Self-review",
	},
}

// ExtractFromSelfReview extracts patterns from self-review output by parsing
// finding markers (REVIEW_FIXED:, SUSPICIOUS_VALUE, PARITY_GAP, INCOMPLETE, etc.)
// and mapping them to pattern types. Patterns are stored as anti-patterns with
// confidence 0.5 (boosted on recurrence via existing SaveCrossPattern upsert logic).
func (e *PatternExtractor) ExtractFromSelfReview(ctx context.Context, selfReviewOutput string, projectPath string) (*ExtractionResult, error) {
	result := &ExtractionResult{
		ExecutionID:  fmt.Sprintf("self_review_%d", time.Now().UnixNano()),
		ProjectPath:  projectPath,
		Patterns:     make([]*ExtractedPattern, 0),
		AntiPatterns: make([]*ExtractedPattern, 0),
		ExtractedAt:  time.Now(),
	}

	if strings.TrimSpace(selfReviewOutput) == "" {
		return result, nil
	}

	for _, finding := range selfReviewFindings {
		matches := finding.regex.FindAllString(selfReviewOutput, -1)
		if len(matches) == 0 {
			continue
		}

		examples := append([]string{}, matches...)

		result.AntiPatterns = append(result.AntiPatterns, &ExtractedPattern{
			Type:        finding.pType,
			Title:       finding.title,
			Description: finding.desc,
			Examples:    examples,
			Confidence:  0.5,
			Context:     finding.context,
		})
	}

	return result, nil
}

// SaveExtractedPatterns saves extracted patterns to the store
func (e *PatternExtractor) SaveExtractedPatterns(ctx context.Context, result *ExtractionResult) error {
	for _, p := range result.Patterns {
		globalPattern := &GlobalPattern{
			Type:        p.Type,
			Title:       p.Title,
			Description: p.Description,
			Examples:    p.Examples,
			Confidence:  p.Confidence,
			Projects:    []string{result.ProjectPath},
			Metadata: map[string]interface{}{
				"context":         p.Context,
				"execution_id":    result.ExecutionID,
				"extracted_at":    result.ExtractedAt,
				"is_anti_pattern": false,
			},
		}

		// Check if similar pattern exists
		if existing := e.findSimilarPattern(globalPattern); existing != nil {
			// Merge with existing pattern
			e.mergePattern(existing, globalPattern, result.ProjectPath)
			if err := e.store.Add(existing); err != nil {
				return fmt.Errorf("failed to update pattern: %w", err)
			}
		} else {
			if err := e.store.Add(globalPattern); err != nil {
				return fmt.Errorf("failed to save pattern: %w", err)
			}
		}
	}

	// Save anti-patterns with special marker
	for _, p := range result.AntiPatterns {
		globalPattern := &GlobalPattern{
			Type:        p.Type,
			Title:       "[ANTI] " + p.Title,
			Description: "AVOID: " + p.Description,
			Examples:    p.Examples,
			Confidence:  p.Confidence,
			Projects:    []string{result.ProjectPath},
			Metadata: map[string]interface{}{
				"context":         p.Context,
				"execution_id":    result.ExecutionID,
				"extracted_at":    result.ExtractedAt,
				"is_anti_pattern": true,
			},
		}

		// Recurrence detection: if a similar anti-pattern already exists,
		// boost confidence instead of creating a duplicate.
		effectiveConfidence := p.Confidence
		if existing := e.findSimilarPattern(globalPattern); existing != nil {
			isCISourced := strings.HasPrefix(fmt.Sprintf("%v", existing.Metadata["context"]), "source:ci") ||
				strings.HasPrefix(p.Context, "source:ci")
			if isCISourced {
				// CI recurrence: boost by 1.5x, capped at 0.95
				existing.Confidence = min(0.95, existing.Confidence*1.5)
			} else {
				e.mergePattern(existing, globalPattern, result.ProjectPath)
			}
			effectiveConfidence = existing.Confidence
			if err := e.store.Add(existing); err != nil {
				return fmt.Errorf("failed to update anti-pattern: %w", err)
			}
		} else {
			if err := e.store.Add(globalPattern); err != nil {
				return fmt.Errorf("failed to save anti-pattern: %w", err)
			}
		}

		// Also persist CI-sourced anti-patterns to SQLite CrossPattern store
		// so they are visible to PatternContext.InjectPatterns().
		if strings.HasPrefix(p.Context, "source:ci") && e.execStore != nil {
			crossPattern := &CrossPattern{
				ID:            fmt.Sprintf("ci_%s_%s", p.Type, strings.ReplaceAll(strings.ToLower(p.Title), " ", "_")),
				Type:          string(p.Type),
				Title:         "[ANTI] " + p.Title,
				Description:   "AVOID: " + p.Description,
				Context:       p.Context,
				Examples:      p.Examples,
				Confidence:    effectiveConfidence,
				Occurrences:   1,
				IsAntiPattern: true,
				Scope:         "project",
			}
			_ = e.execStore.SaveCrossPattern(crossPattern)
			_ = e.execStore.LinkPatternToProject(crossPattern.ID, result.ProjectPath)
		}
	}

	return nil
}

// findSimilarPattern finds an existing similar pattern
func (e *PatternExtractor) findSimilarPattern(pattern *GlobalPattern) *GlobalPattern {
	existing := e.store.GetByType(pattern.Type)
	for _, p := range existing {
		// Simple title matching - could be enhanced with embedding similarity
		if strings.EqualFold(p.Title, pattern.Title) {
			return p
		}
	}
	return nil
}

// mergePattern merges a new pattern into an existing one
func (e *PatternExtractor) mergePattern(existing, new *GlobalPattern, projectPath string) {
	// Add project if not already present
	projectExists := false
	for _, p := range existing.Projects {
		if p == projectPath {
			projectExists = true
			break
		}
	}
	if !projectExists {
		existing.Projects = append(existing.Projects, projectPath)
	}

	// Merge examples (deduplicate)
	exampleSet := make(map[string]bool)
	for _, ex := range existing.Examples {
		exampleSet[ex] = true
	}
	for _, ex := range new.Examples {
		if !exampleSet[ex] {
			existing.Examples = append(existing.Examples, ex)
			exampleSet[ex] = true
		}
	}

	// Increase confidence based on multiple occurrences
	// More projects seeing same pattern = higher confidence
	projectCount := float64(len(existing.Projects))
	existing.Confidence = min(0.95, 0.5+(projectCount*0.1))
}

// ExtractAndSave is a convenience method that extracts and saves patterns
func (e *PatternExtractor) ExtractAndSave(ctx context.Context, exec *Execution) error {
	result, err := e.ExtractFromExecution(ctx, exec)
	if err != nil {
		return err
	}

	if len(result.Patterns) == 0 && len(result.AntiPatterns) == 0 {
		return nil // Nothing to save
	}

	return e.SaveExtractedPatterns(ctx, result)
}

// PatternAnalysisRequest is sent to the Python LLM analyzer
type PatternAnalysisRequest struct {
	ExecutionID   string `json:"execution_id"`
	ProjectPath   string `json:"project_path"`
	Output        string `json:"output"`
	Error         string `json:"error,omitempty"`
	DiffContent   string `json:"diff_content,omitempty"`
	CommitMessage string `json:"commit_message,omitempty"`
}

// PatternAnalysisResponse is returned from the Python LLM analyzer
type PatternAnalysisResponse struct {
	Patterns     []LLMPattern `json:"patterns"`
	AntiPatterns []LLMPattern `json:"anti_patterns"`
}

// LLMPattern is a pattern identified by the LLM
type LLMPattern struct {
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Context     string   `json:"context"`
	Examples    []string `json:"examples,omitempty"`
	Confidence  float64  `json:"confidence"`
}

// ToJSON converts the request to JSON for the Python bridge
func (r *PatternAnalysisRequest) ToJSON() (string, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ParseAnalysisResponse parses the Python LLM response
func ParseAnalysisResponse(jsonData string) (*PatternAnalysisResponse, error) {
	var resp PatternAnalysisResponse
	if err := json.Unmarshal([]byte(jsonData), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
