package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// PatternQuery holds parameters for querying patterns
type PatternQuery struct {
	ProjectPath   string
	Types         []string      // Filter by pattern types
	IncludeGlobal bool          // Include global scope patterns
	MinConfidence float64       // Minimum confidence threshold
	MaxResults    int           // Maximum number of results
	SearchTerm    string        // Optional text search
	IncludeAnti   bool          // Include anti-patterns
	Scope         string        // "project", "org", "global" or empty for all
}

// QueryResult holds the result of a pattern query
type QueryResult struct {
	Patterns     []*CrossPattern
	TotalMatches int
	QueryTime    time.Duration
}

// PatternQueryService provides pattern querying capabilities
type PatternQueryService struct {
	store *Store
}

// NewPatternQueryService creates a new pattern query service
func NewPatternQueryService(store *Store) *PatternQueryService {
	return &PatternQueryService{store: store}
}

// Query executes a pattern query with the given parameters
func (s *PatternQueryService) Query(ctx context.Context, q *PatternQuery) (*QueryResult, error) {
	start := time.Now()

	// Set defaults
	if q.MaxResults <= 0 {
		q.MaxResults = 20
	}
	if q.MinConfidence <= 0 {
		q.MinConfidence = 0.5
	}

	var patterns []*CrossPattern
	var err error

	// Use search if term provided
	if q.SearchTerm != "" {
		patterns, err = s.store.SearchCrossPatterns(q.SearchTerm, q.MaxResults*2) // Over-fetch for filtering
		if err != nil {
			return nil, fmt.Errorf("search failed: %w", err)
		}
	} else if q.ProjectPath != "" {
		patterns, err = s.store.GetCrossPatternsForProject(q.ProjectPath, q.IncludeGlobal)
		if err != nil {
			return nil, fmt.Errorf("project query failed: %w", err)
		}
	} else {
		patterns, err = s.store.GetTopCrossPatterns(q.MaxResults*2, q.MinConfidence)
		if err != nil {
			return nil, fmt.Errorf("top patterns query failed: %w", err)
		}
	}

	// Apply filters
	filtered := make([]*CrossPattern, 0, len(patterns))
	for _, p := range patterns {
		// Filter by confidence
		if p.Confidence < q.MinConfidence {
			continue
		}

		// Filter by type
		if len(q.Types) > 0 && !containsString(q.Types, p.Type) {
			continue
		}

		// Filter anti-patterns
		if !q.IncludeAnti && p.IsAntiPattern {
			continue
		}

		// Filter by scope
		if q.Scope != "" && p.Scope != q.Scope {
			continue
		}

		filtered = append(filtered, p)
	}

	// Sort by confidence and occurrences
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].Confidence != filtered[j].Confidence {
			return filtered[i].Confidence > filtered[j].Confidence
		}
		return filtered[i].Occurrences > filtered[j].Occurrences
	})

	// Limit results
	totalMatches := len(filtered)
	if len(filtered) > q.MaxResults {
		filtered = filtered[:q.MaxResults]
	}

	return &QueryResult{
		Patterns:     filtered,
		TotalMatches: totalMatches,
		QueryTime:    time.Since(start),
	}, nil
}

// GetRelevantPatterns retrieves patterns relevant to a task context
func (s *PatternQueryService) GetRelevantPatterns(ctx context.Context, projectPath string, taskContext string) ([]*CrossPattern, error) {
	// Get all project patterns
	patterns, err := s.store.GetCrossPatternsForProject(projectPath, true)
	if err != nil {
		return nil, err
	}

	// Score patterns based on context relevance
	type scoredPattern struct {
		pattern *CrossPattern
		score   float64
	}

	scored := make([]scoredPattern, 0, len(patterns))
	contextLower := strings.ToLower(taskContext)

	for _, p := range patterns {
		score := p.Confidence // Base score from confidence

		// Boost score if context matches
		if strings.Contains(contextLower, strings.ToLower(p.Context)) {
			score += 0.2
		}

		// Boost score if title keywords match task context
		titleWords := strings.Fields(strings.ToLower(p.Title))
		for _, word := range titleWords {
			if len(word) > 3 && strings.Contains(contextLower, word) {
				score += 0.1
			}
		}

		// Boost for high-occurrence patterns
		if p.Occurrences > 5 {
			score += 0.1
		}

		scored = append(scored, scoredPattern{pattern: p, score: score})
	}

	// Sort by score
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	// Return top patterns
	maxResults := 10
	if len(scored) < maxResults {
		maxResults = len(scored)
	}

	result := make([]*CrossPattern, maxResults)
	for i := 0; i < maxResults; i++ {
		result[i] = scored[i].pattern
	}

	return result, nil
}

// PromptInjection formats patterns for injection into task prompts
type PromptInjection struct {
	Patterns     []*CrossPattern
	AntiPatterns []*CrossPattern
}

// FormatForPrompt formats patterns for injection into a task prompt
func (s *PatternQueryService) FormatForPrompt(ctx context.Context, projectPath string, taskContext string) (string, error) {
	// Get relevant patterns
	patterns, err := s.GetRelevantPatterns(ctx, projectPath, taskContext)
	if err != nil {
		return "", err
	}

	// Get anti-patterns
	q := &PatternQuery{
		ProjectPath:   projectPath,
		IncludeAnti:   true,
		MinConfidence: 0.6,
		MaxResults:    5,
	}
	antiResult, err := s.Query(ctx, q)
	if err != nil {
		return "", err
	}

	var antiPatterns []*CrossPattern
	for _, p := range antiResult.Patterns {
		if p.IsAntiPattern {
			antiPatterns = append(antiPatterns, p)
		}
	}

	// Separate patterns and anti-patterns
	var normalPatterns []*CrossPattern
	for _, p := range patterns {
		if !p.IsAntiPattern {
			normalPatterns = append(normalPatterns, p)
		}
	}

	return s.formatPatternBlock(normalPatterns, antiPatterns), nil
}

// formatPatternBlock formats patterns into a prompt-ready block
func (s *PatternQueryService) formatPatternBlock(patterns, antiPatterns []*CrossPattern) string {
	if len(patterns) == 0 && len(antiPatterns) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n## Learned Patterns\n\n")
	sb.WriteString("The following patterns have been learned from previous executions across projects:\n\n")

	if len(patterns) > 0 {
		sb.WriteString("### Recommended Patterns\n\n")
		for _, p := range patterns {
			sb.WriteString(fmt.Sprintf("- **%s** (confidence: %.0f%%)\n", p.Title, p.Confidence*100))
			sb.WriteString(fmt.Sprintf("  %s\n", p.Description))
			if p.Context != "" {
				sb.WriteString(fmt.Sprintf("  _Context: %s_\n", p.Context))
			}
			sb.WriteString("\n")
		}
	}

	if len(antiPatterns) > 0 {
		sb.WriteString("### Anti-Patterns to Avoid\n\n")
		for _, p := range antiPatterns {
			title := strings.TrimPrefix(p.Title, "[ANTI] ")
			sb.WriteString(fmt.Sprintf("- **%s** (confidence: %.0f%%)\n", title, p.Confidence*100))
			desc := strings.TrimPrefix(p.Description, "AVOID: ")
			sb.WriteString(fmt.Sprintf("  %s\n", desc))
			if p.Context != "" {
				sb.WriteString(fmt.Sprintf("  _Context: %s_\n", p.Context))
			}
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// GetPatternSuggestions returns pattern suggestions for an incomplete task
func (s *PatternQueryService) GetPatternSuggestions(ctx context.Context, projectPath, partialOutput string) ([]*CrossPattern, error) {
	// Analyze partial output to determine what patterns might be helpful
	outputLower := strings.ToLower(partialOutput)

	// Keywords that suggest specific pattern types
	typeHints := map[string][]string{
		"code":      {"function", "method", "class", "struct", "implement"},
		"structure": {"package", "module", "import", "organize"},
		"workflow":  {"test", "build", "deploy", "commit"},
		"error":     {"error", "exception", "fail", "panic"},
	}

	relevantTypes := make([]string, 0)
	for pType, keywords := range typeHints {
		for _, kw := range keywords {
			if strings.Contains(outputLower, kw) {
				relevantTypes = append(relevantTypes, pType)
				break
			}
		}
	}

	// Query patterns with type filter
	q := &PatternQuery{
		ProjectPath:   projectPath,
		Types:         relevantTypes,
		MinConfidence: 0.6,
		MaxResults:    5,
		IncludeGlobal: true,
	}

	result, err := s.Query(ctx, q)
	if err != nil {
		return nil, err
	}

	return result.Patterns, nil
}

// containsString checks if a slice contains a string
func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
