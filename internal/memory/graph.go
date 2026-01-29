package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// save persists the graph to disk (acquires read lock)
func (kg *KnowledgeGraph) save() error {
	kg.mu.RLock()
	defer kg.mu.RUnlock()
	return kg.saveUnlocked()
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

// GetPatterns retrieves all patterns
func (kg *KnowledgeGraph) GetPatterns() []*GraphNode {
	return kg.GetByType("pattern")
}

// GetLearnings retrieves all learnings
func (kg *KnowledgeGraph) GetLearnings() []*GraphNode {
	return kg.GetByType("learning")
}
