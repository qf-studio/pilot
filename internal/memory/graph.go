package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// GraphNode represents a node in the knowledge graph
type GraphNode struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"type"`
	Title     string                 `json:"title"`
	Content   string                 `json:"content,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Relations []string               `json:"relations,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
	UpdatedAt time.Time              `json:"updated_at"`
}

// KnowledgeGraph provides cross-project knowledge management
type KnowledgeGraph struct {
	nodes map[string]*GraphNode
	path  string
	mu    sync.RWMutex
}

// NewKnowledgeGraph creates a new knowledge graph
func NewKnowledgeGraph(dataPath string) (*KnowledgeGraph, error) {
	kg := &KnowledgeGraph{
		nodes: make(map[string]*GraphNode),
		path:  filepath.Join(dataPath, "knowledge.json"),
	}

	if err := kg.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	return kg, nil
}

// load loads the graph from disk
func (kg *KnowledgeGraph) load() error {
	data, err := os.ReadFile(kg.path)
	if err != nil {
		return err
	}

	var nodes []*GraphNode
	if err := json.Unmarshal(data, &nodes); err != nil {
		return err
	}

	kg.mu.Lock()
	defer kg.mu.Unlock()

	for _, node := range nodes {
		kg.nodes[node.ID] = node
	}

	return nil
}

// saveUnlocked persists the graph to disk (caller must hold lock)
func (kg *KnowledgeGraph) saveUnlocked() error {
	nodes := make([]*GraphNode, 0, len(kg.nodes))
	for _, node := range kg.nodes {
		nodes = append(nodes, node)
	}

	data, err := json.MarshalIndent(nodes, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(kg.path, data, 0644)
}

// Add adds or updates a node
func (kg *KnowledgeGraph) Add(node *GraphNode) error {
	kg.mu.Lock()
	defer kg.mu.Unlock()

	if node.ID == "" {
		return fmt.Errorf("node ID is required")
	}

	now := time.Now()
	if existing, ok := kg.nodes[node.ID]; ok {
		node.CreatedAt = existing.CreatedAt
	} else {
		node.CreatedAt = now
	}
	node.UpdatedAt = now

	kg.nodes[node.ID] = node
	return kg.saveUnlocked()
}

// Get retrieves a node by ID
func (kg *KnowledgeGraph) Get(id string) (*GraphNode, bool) {
	kg.mu.RLock()
	defer kg.mu.RUnlock()

	node, ok := kg.nodes[id]
	return node, ok
}

// Search searches nodes by query
func (kg *KnowledgeGraph) Search(query string) []*GraphNode {
	kg.mu.RLock()
	defer kg.mu.RUnlock()

	query = strings.ToLower(query)
	var results []*GraphNode

	for _, node := range kg.nodes {
		if strings.Contains(strings.ToLower(node.Title), query) ||
			strings.Contains(strings.ToLower(node.Content), query) ||
			strings.Contains(strings.ToLower(node.Type), query) {
			results = append(results, node)
		}
	}

	return results
}

// GetByType retrieves nodes by type
func (kg *KnowledgeGraph) GetByType(nodeType string) []*GraphNode {
	kg.mu.RLock()
	defer kg.mu.RUnlock()

	var results []*GraphNode
	for _, node := range kg.nodes {
		if node.Type == nodeType {
			results = append(results, node)
		}
	}

	return results
}

// GetRelated retrieves related nodes
func (kg *KnowledgeGraph) GetRelated(id string) []*GraphNode {
	kg.mu.RLock()
	defer kg.mu.RUnlock()

	node, ok := kg.nodes[id]
	if !ok {
		return nil
	}

	var results []*GraphNode
	for _, relID := range node.Relations {
		if related, ok := kg.nodes[relID]; ok {
			results = append(results, related)
		}
	}

	return results
}

// Remove removes a node
func (kg *KnowledgeGraph) Remove(id string) error {
	kg.mu.Lock()
	defer kg.mu.Unlock()

	delete(kg.nodes, id)
	return kg.saveUnlocked()
}

// Count returns the number of nodes
func (kg *KnowledgeGraph) Count() int {
	kg.mu.RLock()
	defer kg.mu.RUnlock()
	return len(kg.nodes)
}

// AddPattern adds a pattern to the knowledge graph
func (kg *KnowledgeGraph) AddPattern(patternType, content string, metadata map[string]interface{}) error {
	id := fmt.Sprintf("pattern_%s_%d", patternType, time.Now().UnixNano())
	node := &GraphNode{
		ID:       id,
		Type:     "pattern",
		Title:    patternType,
		Content:  content,
		Metadata: metadata,
	}
	return kg.Add(node)
}

// AddLearning adds a learning to the knowledge graph
func (kg *KnowledgeGraph) AddLearning(title, content string, metadata map[string]interface{}) error {
	id := fmt.Sprintf("learning_%d", time.Now().UnixNano())
	node := &GraphNode{
		ID:       id,
		Type:     "learning",
		Title:    title,
		Content:  content,
		Metadata: metadata,
	}
	return kg.Add(node)
}

// AddExecutionLearning adds a learning node with relations linking task→files,
// files→patterns, patterns→outcome. Unlike AddLearning which creates flat nodes,
// this populates the Relations field to connect related concept nodes.
func (kg *KnowledgeGraph) AddExecutionLearning(title, content string, filesChanged []string, patterns []string, outcome string) error {
	now := time.Now()
	nano := now.UnixNano()

	learningID := fmt.Sprintf("exec_learning_%d", nano)
	var relationIDs []string

	// Create file nodes and collect their IDs
	for i, file := range filesChanged {
		fileID := fmt.Sprintf("file_%d_%d", nano, i)
		fileNode := &GraphNode{
			ID:    fileID,
			Type:  "file",
			Title: file,
			Metadata: map[string]interface{}{
				"learning_id": learningID,
			},
		}
		if err := kg.Add(fileNode); err != nil {
			return fmt.Errorf("add file node %q: %w", file, err)
		}
		relationIDs = append(relationIDs, fileID)
	}

	// Create pattern nodes and collect their IDs
	for i, pattern := range patterns {
		patternID := fmt.Sprintf("exec_pattern_%d_%d", nano, i)
		patternNode := &GraphNode{
			ID:    patternID,
			Type:  "pattern",
			Title: pattern,
			Metadata: map[string]interface{}{
				"learning_id": learningID,
			},
		}
		if err := kg.Add(patternNode); err != nil {
			return fmt.Errorf("add pattern node %q: %w", pattern, err)
		}
		relationIDs = append(relationIDs, patternID)
	}

	// Create outcome node
	outcomeID := fmt.Sprintf("outcome_%d", nano)
	outcomeNode := &GraphNode{
		ID:    outcomeID,
		Type:  "outcome",
		Title: outcome,
		Metadata: map[string]interface{}{
			"learning_id": learningID,
		},
	}
	if err := kg.Add(outcomeNode); err != nil {
		return fmt.Errorf("add outcome node: %w", err)
	}
	relationIDs = append(relationIDs, outcomeID)

	// Create the learning node with all relations
	learningNode := &GraphNode{
		ID:      learningID,
		Type:    "execution_learning",
		Title:   title,
		Content: content,
		Metadata: map[string]interface{}{
			"files_changed": filesChanged,
			"patterns":      patterns,
			"outcome":       outcome,
		},
		Relations: relationIDs,
	}
	return kg.Add(learningNode)
}

// GetRelatedByKeywords searches nodes by keywords across title, content, and
// metadata values. Returns matching nodes sorted by recency (newest first).
func (kg *KnowledgeGraph) GetRelatedByKeywords(keywords []string) []*GraphNode {
	if len(keywords) == 0 {
		return nil
	}

	kg.mu.RLock()
	defer kg.mu.RUnlock()

	var results []*GraphNode
	for _, node := range kg.nodes {
		if kg.nodeMatchesKeywords(node, keywords) {
			results = append(results, node)
		}
	}

	// Sort by UpdatedAt descending (newest first)
	sort.Slice(results, func(i, j int) bool {
		return results[i].UpdatedAt.After(results[j].UpdatedAt)
	})

	return results
}

// nodeMatchesKeywords checks if any keyword matches the node's title, content,
// or metadata string values. All comparisons are case-insensitive.
func (kg *KnowledgeGraph) nodeMatchesKeywords(node *GraphNode, keywords []string) bool {
	titleLower := strings.ToLower(node.Title)
	contentLower := strings.ToLower(node.Content)

	for _, kw := range keywords {
		kwLower := strings.ToLower(kw)
		if strings.Contains(titleLower, kwLower) || strings.Contains(contentLower, kwLower) {
			return true
		}
		// Search metadata values
		for _, v := range node.Metadata {
			switch val := v.(type) {
			case string:
				if strings.Contains(strings.ToLower(val), kwLower) {
					return true
				}
			case []interface{}:
				for _, item := range val {
					if s, ok := item.(string); ok && strings.Contains(strings.ToLower(s), kwLower) {
						return true
					}
				}
			}
		}
	}
	return false
}

// GetPatterns retrieves all patterns
func (kg *KnowledgeGraph) GetPatterns() []*GraphNode {
	return kg.GetByType("pattern")
}

// GetLearnings retrieves all learnings
func (kg *KnowledgeGraph) GetLearnings() []*GraphNode {
	return kg.GetByType("learning")
}
