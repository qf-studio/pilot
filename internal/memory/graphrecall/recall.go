// Package graphrecall ranks Navigator knowledge-graph memories against a
// task description so an executor prompt can be seeded with relevant
// history without loading the whole .agent/knowledge tree.
package graphrecall

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MemorySummary is a lightweight, ranked view of a knowledge-graph memory
// node relevant to a given task.
type MemorySummary struct {
	ID         string
	Type       string
	Summary    string
	Concepts   []string
	Confidence float64
	Path       string
}

type graphDoc struct {
	Nodes struct {
		Concepts map[string]conceptNode `json:"concepts"`
		Memories map[string]memoryNode  `json:"memories"`
	} `json:"nodes"`
}

type conceptNode struct {
	Label string `json:"label"`
}

type memoryNode struct {
	Type         string      `json:"type"`
	Summary      string      `json:"summary"`
	Concepts     []string    `json:"concepts"`
	Confidence   float64     `json:"confidence"`
	Resolved     interface{} `json:"resolved,omitempty"`
	SupersededBy string      `json:"superseded_by,omitempty"`
	File         string      `json:"file,omitempty"`
	Path         string      `json:"path,omitempty"`
	MemoryFile   string      `json:"memory_file,omitempty"`
}

// resolvedPath tolerates the file/path/memory_file node-path variants seen
// across graph.json revisions, checked in that order — the same precedence
// scripts/check-graph.py uses. concept_index is a stale derived index and
// is never consulted.
func (m memoryNode) resolvedPath() string {
	if m.File != "" {
		return m.File
	}
	if m.Path != "" {
		return m.Path
	}
	return m.MemoryFile
}

// isExcluded reports whether a memory is retired: resolved may be recorded
// as a bare bool (true) or as a date string (e.g. "2026-05-20 (v2.146.7)"),
// both of which mark the memory closed.
func (m memoryNode) isExcluded() bool {
	if m.SupersededBy != "" {
		return true
	}
	switch v := m.Resolved.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	default:
		return v != nil
	}
}

// RecallRelevant loads the Navigator knowledge graph at
// <projectPath>/.agent/knowledge/graph.json and returns up to limit
// memories relevant to taskText, ranked by concept overlap (desc), then
// confidence (desc), then id (asc). It fails open: a missing or malformed
// graph, or a limit <= 0, yields an empty slice, never an error.
func RecallRelevant(projectPath, taskText string, limit int) []MemorySummary {
	if limit <= 0 {
		return []MemorySummary{}
	}

	graphPath := filepath.Join(projectPath, ".agent", "knowledge", "graph.json")
	data, err := os.ReadFile(graphPath)
	if err != nil {
		return []MemorySummary{}
	}

	var doc graphDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return []MemorySummary{}
	}

	targets := targetConcepts(doc.Nodes.Concepts, taskText)
	if len(targets) == 0 {
		return []MemorySummary{}
	}

	type ranked struct {
		id      string
		mem     memoryNode
		overlap int
	}

	candidates := make([]ranked, 0, len(doc.Nodes.Memories))
	for id, mem := range doc.Nodes.Memories {
		if mem.isExcluded() {
			continue
		}
		overlap := countOverlap(mem.Concepts, targets)
		if overlap == 0 {
			continue
		}
		candidates = append(candidates, ranked{id: id, mem: mem, overlap: overlap})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].overlap != candidates[j].overlap {
			return candidates[i].overlap > candidates[j].overlap
		}
		if candidates[i].mem.Confidence != candidates[j].mem.Confidence {
			return candidates[i].mem.Confidence > candidates[j].mem.Confidence
		}
		return candidates[i].id < candidates[j].id
	})

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	out := make([]MemorySummary, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, MemorySummary{
			ID:         c.id,
			Type:       c.mem.Type,
			Summary:    c.mem.Summary,
			Concepts:   c.mem.Concepts,
			Confidence: c.mem.Confidence,
			Path:       c.mem.resolvedPath(),
		})
	}
	return out
}

func countOverlap(concepts []string, targets map[string]struct{}) int {
	n := 0
	for _, c := range concepts {
		if _, ok := targets[c]; ok {
			n++
		}
	}
	return n
}

// targetConcepts derives the set of graph concept ids relevant to taskText
// by case-insensitive substring matching of each concept's id and label
// (its human-readable alias) against taskText. Hyphens/underscores in both
// the concept id and taskText are normalized to spaces first, so phrase
// wording matters rather than punctuation style.
func targetConcepts(concepts map[string]conceptNode, taskText string) map[string]struct{} {
	targets := map[string]struct{}{}
	normText := normalize(taskText)
	if normText == "" {
		return targets
	}
	for id, c := range concepts {
		if matches(normText, id) || matches(normText, c.Label) {
			targets[id] = struct{}{}
		}
	}
	return targets
}

func matches(normText, candidate string) bool {
	norm := normalize(candidate)
	if norm == "" {
		return false
	}
	return strings.Contains(normText, norm)
}

func normalize(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("-", " ", "_", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}
