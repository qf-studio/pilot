package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// PatternType defines types of patterns
type PatternType string

const (
	PatternTypeCode       PatternType = "code"
	PatternTypeStructure  PatternType = "structure"
	PatternTypeNaming     PatternType = "naming"
	PatternTypeWorkflow   PatternType = "workflow"
	PatternTypeError      PatternType = "error"
)

// GlobalPattern represents a pattern that applies across projects
type GlobalPattern struct {
	ID          string                 `json:"id"`
	Type        PatternType            `json:"type"`
	Title       string                 `json:"title"`
	Description string                 `json:"description"`
	Examples    []string               `json:"examples,omitempty"`
	Confidence  float64                `json:"confidence"`
	Uses        int                    `json:"uses"`
	Projects    []string               `json:"projects,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
}

// GlobalPatternStore manages cross-project patterns
type GlobalPatternStore struct {
	patterns map[string]*GlobalPattern
	path     string
	mu       sync.RWMutex
}

// NewGlobalPatternStore creates a new global pattern store
func NewGlobalPatternStore(dataPath string) (*GlobalPatternStore, error) {
	store := &GlobalPatternStore{
		patterns: make(map[string]*GlobalPattern),
		path:     filepath.Join(dataPath, "global_patterns.json"),
	}

	if err := store.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	return store, nil
}

// load loads patterns from disk
func (s *GlobalPatternStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}

	var patterns []*GlobalPattern
	if err := json.Unmarshal(data, &patterns); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, pattern := range patterns {
		s.patterns[pattern.ID] = pattern
	}

	return nil
}

// save persists patterns to disk
func (s *GlobalPatternStore) save() error {
	s.mu.RLock()
	patterns := make([]*GlobalPattern, 0, len(s.patterns))
	for _, pattern := range s.patterns {
		patterns = append(patterns, pattern)
	}
	s.mu.RUnlock()

	data, err := json.MarshalIndent(patterns, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(s.path, data, 0644)
}

// Add adds or updates a pattern
func (s *GlobalPatternStore) Add(pattern *GlobalPattern) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if pattern.ID == "" {
		pattern.ID = fmt.Sprintf("%s_%d", pattern.Type, time.Now().UnixNano())
	}

	now := time.Now()
	if existing, ok := s.patterns[pattern.ID]; ok {
		pattern.CreatedAt = existing.CreatedAt
		pattern.Uses = existing.Uses + 1
	} else {
		pattern.CreatedAt = now
		pattern.Uses = 1
	}
	pattern.UpdatedAt = now

	s.patterns[pattern.ID] = pattern
	return s.save()
}

// Get retrieves a pattern by ID
func (s *GlobalPatternStore) Get(id string) (*GlobalPattern, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	pattern, ok := s.patterns[id]
	return pattern, ok
}

// GetByType retrieves patterns by type
func (s *GlobalPatternStore) GetByType(patternType PatternType) []*GlobalPattern {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []*GlobalPattern
	for _, pattern := range s.patterns {
		if pattern.Type == patternType {
			results = append(results, pattern)
		}
	}

	return results
}

// GetForProject retrieves patterns relevant to a project
func (s *GlobalPatternStore) GetForProject(projectPath string) []*GlobalPattern {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []*GlobalPattern
	for _, pattern := range s.patterns {
		// Include global patterns (no specific projects) or project-specific
		if len(pattern.Projects) == 0 {
			results = append(results, pattern)
		} else {
			for _, p := range pattern.Projects {
				if p == projectPath {
					results = append(results, pattern)
					break
				}
			}
		}
	}

	return results
}

// IncrementUse increments the use count for a pattern
func (s *GlobalPatternStore) IncrementUse(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if pattern, ok := s.patterns[id]; ok {
		pattern.Uses++
		pattern.UpdatedAt = time.Now()
		return s.save()
	}

	return fmt.Errorf("pattern not found: %s", id)
}

// Remove removes a pattern
func (s *GlobalPatternStore) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.patterns, id)
	return s.save()
}

// Count returns the number of patterns
func (s *GlobalPatternStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.patterns)
}

// PatternLearner learns patterns from execution history
type PatternLearner struct {
	store    *GlobalPatternStore
	execStore *Store
}

// NewPatternLearner creates a new pattern learner
func NewPatternLearner(patternStore *GlobalPatternStore, execStore *Store) *PatternLearner {
	return &PatternLearner{
		store:     patternStore,
		execStore: execStore,
	}
}

// LearnFromExecution learns patterns from a completed execution
func (l *PatternLearner) LearnFromExecution(ctx context.Context, exec *Execution) error {
	if exec.Status != "completed" && exec.Status != "failed" {
		return nil // Only learn from finished executions
	}

	// Create an extractor to handle pattern extraction
	extractor := NewPatternExtractor(l.store, l.execStore)

	// Extract patterns from the execution
	result, err := extractor.ExtractFromExecution(ctx, exec)
	if err != nil {
		return fmt.Errorf("pattern extraction failed: %w", err)
	}

	// Save extracted patterns
	if len(result.Patterns) > 0 || len(result.AntiPatterns) > 0 {
		if err := extractor.SaveExtractedPatterns(ctx, result); err != nil {
			return fmt.Errorf("failed to save patterns: %w", err)
		}
	}

	return nil
}
