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
	store         *GlobalPatternStore
	execStore     *Store
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
	ExecutionID string
	ProjectPath string
	Patterns    []*ExtractedPattern
	AntiPatterns []*ExtractedPattern
	ExtractedAt time.Time
}

// ExtractFromExecution extracts patterns from a completed execution
func (e *PatternExtractor) ExtractFromExecution(ctx context.Context, exec *Execution) (*ExtractionResult, error) {
	if exec.Status != "completed" {
		return nil, fmt.Errorf("can only extract patterns from completed executions")
	}

	result := &ExtractionResult{
		ExecutionID: exec.ID,
		ProjectPath: exec.ProjectPath,
		Patterns:    make([]*ExtractedPattern, 0),
		AntiPatterns: make([]*ExtractedPattern, 0),
		ExtractedAt: time.Now(),
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

// extractCodePatterns extracts code-related patterns
func (e *PatternExtractor) extractCodePatterns(output string) []*ExtractedPattern {
	var patterns []*ExtractedPattern

	// Look for common successful patterns in output
	patternMatchers := []struct {
		regex    *regexp.Regexp
		pType    PatternType
		title    string
		desc     string
		context  string
	}{
		{
			regex:   regexp.MustCompile(`(?i)using\s+context\.Context\s+in\s+(\w+)`),
			pType:   PatternTypeCode,
			title:   "Use context.Context for cancellation",
			desc:    "Pass context.Context to functions for proper cancellation and timeout handling",
			context: "Go handlers",
		},
		{
			regex:   regexp.MustCompile(`(?i)added\s+error\s+handling\s+for\s+(\w+)`),
			pType:   PatternTypeCode,
			title:   "Explicit error handling",
			desc:    "Always handle errors explicitly rather than ignoring them",
			context: "Go functions",
		},
		{
			regex:   regexp.MustCompile(`(?i)created?\s+test[s]?\s+for\s+(\w+)`),
			pType:   PatternTypeWorkflow,
			title:   "Test-driven implementation",
			desc:    "Create tests alongside implementation code",
			context: "All code",
		},
		{
			regex:   regexp.MustCompile(`(?i)using\s+(zap|slog|logrus)\s+for\s+logging`),
			pType:   PatternTypeCode,
			title:   "Structured logging",
			desc:    "Use structured logging library instead of fmt.Printf",
			context: "Go services",
		},
		{
			regex:   regexp.MustCompile(`(?i)added?\s+validation\s+for\s+(\w+)`),
			pType:   PatternTypeCode,
			title:   "Input validation",
			desc:    "Validate inputs at system boundaries",
			context: "API handlers",
		},
	}

	for _, matcher := range patternMatchers {
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
				"context":       p.Context,
				"execution_id":  result.ExecutionID,
				"extracted_at":  result.ExtractedAt,
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

		if err := e.store.Add(globalPattern); err != nil {
			return fmt.Errorf("failed to save anti-pattern: %w", err)
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
	existing.Confidence = min(0.95, 0.5 + (projectCount * 0.1))
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
