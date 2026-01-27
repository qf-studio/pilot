package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// PatternScope defines the visibility scope of a pattern
type PatternScope string

const (
	ScopeProject PatternScope = "project" // Visible only within the project
	ScopeOrg     PatternScope = "org"     // Visible to all org projects
	ScopeGlobal  PatternScope = "global"  // Visible to all users (future)
)

// PatternSync handles cross-project pattern synchronization
type PatternSync struct {
	store       *Store
	orgPatterns *OrgPatternStore
}

// NewPatternSync creates a new pattern sync service
func NewPatternSync(store *Store, dataPath string) (*PatternSync, error) {
	orgStore, err := NewOrgPatternStore(dataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create org pattern store: %w", err)
	}

	return &PatternSync{
		store:       store,
		orgPatterns: orgStore,
	}, nil
}

// OrgPatternStore manages organization-level pattern aggregation
type OrgPatternStore struct {
	patterns map[string]*AggregatedPattern
	path     string
}

// AggregatedPattern represents a pattern aggregated across projects
type AggregatedPattern struct {
	ID            string           `json:"id"`
	Type          string           `json:"type"`
	Title         string           `json:"title"`
	Description   string           `json:"description"`
	Context       string           `json:"context"`
	Examples      []string         `json:"examples"`
	Confidence    float64          `json:"confidence"`
	Occurrences   int              `json:"occurrences"`
	ProjectCount  int              `json:"project_count"`
	Projects      []ProjectMention `json:"projects"`
	IsAntiPattern bool             `json:"is_anti_pattern"`
	LastSynced    time.Time        `json:"last_synced"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
}

// ProjectMention tracks a pattern's presence in a specific project
type ProjectMention struct {
	ProjectPath string    `json:"project_path"`
	Uses        int       `json:"uses"`
	SuccessRate float64   `json:"success_rate"`
	LastUsed    time.Time `json:"last_used"`
}

// NewOrgPatternStore creates a new organization pattern store
func NewOrgPatternStore(dataPath string) (*OrgPatternStore, error) {
	store := &OrgPatternStore{
		patterns: make(map[string]*AggregatedPattern),
		path:     filepath.Join(dataPath, "org_patterns.json"),
	}

	if err := store.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	return store, nil
}

// load loads patterns from disk
func (s *OrgPatternStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}

	var patterns []*AggregatedPattern
	if err := json.Unmarshal(data, &patterns); err != nil {
		return err
	}

	for _, p := range patterns {
		s.patterns[p.ID] = p
	}

	return nil
}

// save persists patterns to disk
func (s *OrgPatternStore) save() error {
	patterns := make([]*AggregatedPattern, 0, len(s.patterns))
	for _, p := range s.patterns {
		patterns = append(patterns, p)
	}

	// Sort by confidence for predictable output
	sort.Slice(patterns, func(i, j int) bool {
		return patterns[i].Confidence > patterns[j].Confidence
	})

	data, err := json.MarshalIndent(patterns, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(s.path, data, 0644)
}

// Get retrieves an aggregated pattern by ID
func (s *OrgPatternStore) Get(id string) (*AggregatedPattern, bool) {
	p, ok := s.patterns[id]
	return p, ok
}

// GetAll retrieves all aggregated patterns
func (s *OrgPatternStore) GetAll() []*AggregatedPattern {
	patterns := make([]*AggregatedPattern, 0, len(s.patterns))
	for _, p := range s.patterns {
		patterns = append(patterns, p)
	}
	return patterns
}

// Update updates an aggregated pattern
func (s *OrgPatternStore) Update(pattern *AggregatedPattern) error {
	pattern.UpdatedAt = time.Now()
	pattern.LastSynced = time.Now()
	s.patterns[pattern.ID] = pattern
	return s.save()
}

// SyncFromProject synchronizes patterns from a project to org level
func (ps *PatternSync) SyncFromProject(ctx context.Context, projectPath string) error {
	// Get all cross-project patterns from the database
	patterns, err := ps.store.GetCrossPatternsForProject(projectPath, false)
	if err != nil {
		return fmt.Errorf("failed to get project patterns: %w", err)
	}

	// Get project links for each pattern
	for _, p := range patterns {
		links, err := ps.store.GetProjectsForPattern(p.ID)
		if err != nil {
			continue
		}

		// Aggregate into org pattern
		if err := ps.aggregatePattern(p, links); err != nil {
			return fmt.Errorf("failed to aggregate pattern %s: %w", p.ID, err)
		}
	}

	return nil
}

// aggregatePattern aggregates a pattern across all its project mentions
func (ps *PatternSync) aggregatePattern(p *CrossPattern, links []*PatternProjectLink) error {
	existing, _ := ps.orgPatterns.Get(p.ID)

	if existing == nil {
		// Create new aggregated pattern
		existing = &AggregatedPattern{
			ID:            p.ID,
			Type:          p.Type,
			Title:         p.Title,
			Description:   p.Description,
			Context:       p.Context,
			Examples:      p.Examples,
			IsAntiPattern: p.IsAntiPattern,
			CreatedAt:     p.CreatedAt,
		}
	}

	// Update with latest data
	existing.Title = p.Title
	existing.Description = p.Description
	existing.Context = p.Context

	// Merge examples (deduplicate)
	exampleSet := make(map[string]bool)
	for _, ex := range existing.Examples {
		exampleSet[ex] = true
	}
	for _, ex := range p.Examples {
		if !exampleSet[ex] && len(existing.Examples) < 10 {
			existing.Examples = append(existing.Examples, ex)
			exampleSet[ex] = true
		}
	}

	// Calculate aggregated metrics
	existing.Occurrences = 0
	existing.Projects = make([]ProjectMention, 0, len(links))
	totalSuccessRate := 0.0

	for _, link := range links {
		existing.Occurrences += link.Uses
		successRate := 0.0
		total := link.SuccessCount + link.FailureCount
		if total > 0 {
			successRate = float64(link.SuccessCount) / float64(total)
		}
		totalSuccessRate += successRate

		existing.Projects = append(existing.Projects, ProjectMention{
			ProjectPath: link.ProjectPath,
			Uses:        link.Uses,
			SuccessRate: successRate,
			LastUsed:    link.LastUsed,
		})
	}

	existing.ProjectCount = len(links)

	// Calculate confidence based on occurrences and success rate
	if existing.ProjectCount > 0 {
		avgSuccessRate := totalSuccessRate / float64(existing.ProjectCount)
		// Confidence grows with more projects and higher success rate
		baseConfidence := 0.5 + (float64(existing.ProjectCount) * 0.05)      // +5% per project
		existing.Confidence = min(0.95, baseConfidence+(avgSuccessRate*0.3)) // +30% max from success rate
	}

	return ps.orgPatterns.Update(existing)
}

// GetOrgPatterns retrieves all org-level patterns
func (ps *PatternSync) GetOrgPatterns() []*AggregatedPattern {
	return ps.orgPatterns.GetAll()
}

// GetTopOrgPatterns retrieves top org patterns by confidence
func (ps *PatternSync) GetTopOrgPatterns(limit int) []*AggregatedPattern {
	all := ps.orgPatterns.GetAll()

	// Sort by confidence
	sort.Slice(all, func(i, j int) bool {
		return all[i].Confidence > all[j].Confidence
	})

	if len(all) > limit {
		return all[:limit]
	}
	return all
}

// GetOrgPatternsForContext retrieves org patterns relevant to a context
func (ps *PatternSync) GetOrgPatternsForContext(ctx context.Context, taskContext string) ([]*AggregatedPattern, error) {
	all := ps.orgPatterns.GetAll()

	// Score patterns by relevance
	type scored struct {
		pattern *AggregatedPattern
		score   float64
	}

	var results []scored
	for _, p := range all {
		score := p.Confidence

		// Boost if pattern type matches context hints
		if matchesContext(p.Type, taskContext) {
			score += 0.1
		}

		// Boost for high occurrence patterns
		if p.Occurrences > 10 {
			score += 0.1
		}

		// Boost for patterns used across many projects
		if p.ProjectCount > 3 {
			score += 0.1
		}

		results = append(results, scored{pattern: p, score: score})
	}

	// Sort by score
	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	// Return top results
	patterns := make([]*AggregatedPattern, 0, min(10, len(results)))
	for i := 0; i < len(results) && i < 10; i++ {
		patterns = append(patterns, results[i].pattern)
	}

	return patterns, nil
}

// matchesContext checks if a pattern type is relevant to a context
func matchesContext(patternType, context string) bool {
	// Simple keyword matching - could be enhanced with embeddings
	typeContextMap := map[string][]string{
		"code":      {"function", "method", "implement", "write", "create"},
		"structure": {"package", "organize", "refactor", "architecture"},
		"workflow":  {"test", "build", "deploy", "commit", "ci"},
		"error":     {"error", "bug", "fix", "debug"},
		"naming":    {"rename", "name", "convention"},
	}

	keywords, ok := typeContextMap[patternType]
	if !ok {
		return false
	}

	for _, kw := range keywords {
		if contains(context, kw) {
			return true
		}
	}

	return false
}

// contains checks if text contains a keyword (case-insensitive)
func contains(text, keyword string) bool {
	return len(text) >= len(keyword) && (text == keyword ||
		len(text) > len(keyword) && (text[:len(keyword)] == keyword || text[len(text)-len(keyword):] == keyword))
}

// ExportPatterns exports patterns to a file for sharing
func (ps *PatternSync) ExportPatterns(ctx context.Context, outputPath string, minConfidence float64) error {
	patterns := ps.GetTopOrgPatterns(100)

	var exported []*AggregatedPattern
	for _, p := range patterns {
		if p.Confidence >= minConfidence {
			// Anonymize project paths
			for i := range p.Projects {
				p.Projects[i].ProjectPath = filepath.Base(p.Projects[i].ProjectPath)
			}
			exported = append(exported, p)
		}
	}

	data, err := json.MarshalIndent(exported, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal patterns: %w", err)
	}

	return os.WriteFile(outputPath, data, 0644)
}

// ImportPatterns imports patterns from a file
func (ps *PatternSync) ImportPatterns(ctx context.Context, inputPath string, scope PatternScope) error {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("failed to read input file: %w", err)
	}

	var patterns []*AggregatedPattern
	if err := json.Unmarshal(data, &patterns); err != nil {
		return fmt.Errorf("failed to parse patterns: %w", err)
	}

	for _, p := range patterns {
		// Convert to cross pattern
		crossPattern := &CrossPattern{
			ID:            p.ID,
			Type:          p.Type,
			Title:         p.Title,
			Description:   p.Description,
			Context:       p.Context,
			Examples:      p.Examples,
			Confidence:    p.Confidence * 0.8, // Reduce confidence for imported patterns
			Occurrences:   1,
			IsAntiPattern: p.IsAntiPattern,
			Scope:         string(scope),
		}

		if err := ps.store.SaveCrossPattern(crossPattern); err != nil {
			return fmt.Errorf("failed to save pattern %s: %w", p.ID, err)
		}
	}

	return nil
}

// PromoteToOrg promotes a project pattern to org scope
func (ps *PatternSync) PromoteToOrg(ctx context.Context, patternID string) error {
	pattern, err := ps.store.GetCrossPattern(patternID)
	if err != nil {
		return fmt.Errorf("pattern not found: %w", err)
	}

	pattern.Scope = "org"
	return ps.store.SaveCrossPattern(pattern)
}

// DemoteToProject demotes an org pattern to project scope
func (ps *PatternSync) DemoteToProject(ctx context.Context, patternID, projectPath string) error {
	pattern, err := ps.store.GetCrossPattern(patternID)
	if err != nil {
		return fmt.Errorf("pattern not found: %w", err)
	}

	pattern.Scope = "project"
	if err := ps.store.SaveCrossPattern(pattern); err != nil {
		return err
	}

	// Ensure link to project
	return ps.store.LinkPatternToProject(patternID, projectPath)
}
