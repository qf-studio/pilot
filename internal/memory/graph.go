package memory

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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
	nodes     map[string]*GraphNode
	path      string
	mu        sync.RWMutex
	saveCount int64 // incremented by saveUnlocked on each successful disk write
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

// load loads the graph from disk, falling back to knowledge.json.bak on parse failure.
func (kg *KnowledgeGraph) load() error {
	data, err := os.ReadFile(kg.path)
	if err != nil {
		return err
	}

	var nodes []*GraphNode
	if parseErr := json.Unmarshal(data, &nodes); parseErr != nil {
		bakPath := kg.path + ".bak"
		bakData, bakErr := os.ReadFile(bakPath)
		if bakErr != nil {
			return parseErr
		}
		var bakNodes []*GraphNode
		if bakErr = json.Unmarshal(bakData, &bakNodes); bakErr != nil {
			return parseErr
		}
		slog.Warn("knowledge.json corrupt; recovering from backup", "err", parseErr)
		nodes = bakNodes
		if renameErr := os.Rename(bakPath, kg.path); renameErr != nil {
			slog.Warn("failed to promote knowledge.json.bak to primary", "err", renameErr)
		}
	}

	kg.mu.Lock()
	defer kg.mu.Unlock()

	for _, node := range nodes {
		kg.nodes[node.ID] = node
	}

	return nil
}

// saveUnlocked persists the graph atomically (caller must hold lock).
// It writes to a temp file in the same directory, fsyncs, backs up the current
// knowledge.json to knowledge.json.bak, then renames the temp file over knowledge.json.
func (kg *KnowledgeGraph) saveUnlocked() error {
	nodes := make([]*GraphNode, 0, len(kg.nodes))
	for _, node := range kg.nodes {
		nodes = append(nodes, node)
	}

	data, err := json.MarshalIndent(nodes, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(kg.path)
	tmp, err := os.CreateTemp(dir, "knowledge-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp file: %w", err)
	}

	// Back up current file before replacing (best-effort).
	if existing, readErr := os.ReadFile(kg.path); readErr == nil {
		_ = os.WriteFile(kg.path+".bak", existing, 0644)
	}

	if err := os.Rename(tmpName, kg.path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename temp to knowledge.json: %w", err)
	}

	atomic.AddInt64(&kg.saveCount, 1)
	return nil
}

// addUnlocked mutates the in-memory node map without persisting (caller must hold write lock).
func (kg *KnowledgeGraph) addUnlocked(node *GraphNode) {
	now := time.Now()
	if existing, ok := kg.nodes[node.ID]; ok {
		node.CreatedAt = existing.CreatedAt
	} else {
		node.CreatedAt = now
	}
	node.UpdatedAt = now
	kg.nodes[node.ID] = node
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
// All nodes are staged via addUnlocked, then flushed with a single saveUnlocked call.
func (kg *KnowledgeGraph) AddExecutionLearning(title, content string, filesChanged []string, patterns []string, outcome string) error {
	kg.mu.Lock()
	defer kg.mu.Unlock()

	now := time.Now()
	nano := now.UnixNano()

	learningID := fmt.Sprintf("exec_learning_%d", nano)
	var relationIDs []string

	for i, file := range filesChanged {
		fileID := fmt.Sprintf("file_%d_%d", nano, i)
		kg.addUnlocked(&GraphNode{
			ID:    fileID,
			Type:  "file",
			Title: file,
			Metadata: map[string]interface{}{
				"learning_id": learningID,
			},
		})
		relationIDs = append(relationIDs, fileID)
	}

	for i, pattern := range patterns {
		patternID := fmt.Sprintf("exec_pattern_%d_%d", nano, i)
		kg.addUnlocked(&GraphNode{
			ID:    patternID,
			Type:  "pattern",
			Title: pattern,
			Metadata: map[string]interface{}{
				"learning_id": learningID,
			},
		})
		relationIDs = append(relationIDs, patternID)
	}

	outcomeID := fmt.Sprintf("outcome_%d", nano)
	kg.addUnlocked(&GraphNode{
		ID:    outcomeID,
		Type:  "outcome",
		Title: outcome,
		Metadata: map[string]interface{}{
			"learning_id": learningID,
		},
	})
	relationIDs = append(relationIDs, outcomeID)

	kg.addUnlocked(&GraphNode{
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
	})

	return kg.saveUnlocked()
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
