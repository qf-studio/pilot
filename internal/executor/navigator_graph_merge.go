package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// mergeNavigatorGraphBack merges the worktree's knowledge graph into the
// clone's. A missing clone graph is simply created. It reports whether the
// clone's file changed.
func mergeNavigatorGraphBack(wtPath, clonePath string) (bool, error) {
	wtInfo, err := os.Lstat(wtPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !wtInfo.Mode().IsRegular() {
		return false, nil
	}
	wtData, err := os.ReadFile(wtPath)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", wtPath, err)
	}

	cloneInfo, err := os.Lstat(clonePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, err
		}
		if err := writeFileAtomic(clonePath, wtData, wtInfo.Mode().Perm(), wtInfo.ModTime()); err != nil {
			return false, err
		}
		return true, nil
	}
	if !cloneInfo.Mode().IsRegular() {
		return false, nil
	}
	cloneData, err := os.ReadFile(clonePath)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", clonePath, err)
	}

	merged, err := mergeGraphJSON(cloneData, wtData, wtInfo.ModTime().After(cloneInfo.ModTime()))
	if err != nil {
		return false, fmt.Errorf("merge graph %s: %w", wtPath, err)
	}
	// Compare against the clone re-encoded the same way, so a formatting-only
	// difference does not count as a change.
	if cloneObj, decErr := decodeGraphObject(cloneData); decErr == nil {
		if normalized, encErr := json.MarshalIndent(cloneObj, "", "  "); encErr == nil && bytes.Equal(normalized, merged) {
			return false, nil
		}
	}
	if err := writeFileAtomic(clonePath, append(merged, '\n'), cloneInfo.Mode().Perm(), time.Time{}); err != nil {
		return false, err
	}
	return true, nil
}

// mergeGraphJSON merges two graph documents. Node and edge collections are
// merged by id (edges without an id by from/to/type), so entries present on
// only one side all survive. When both sides hold the same id, the side named
// by preferWorktree wins; the same side wins for every other top-level field.
func mergeGraphJSON(cloneData, wtData []byte, preferWorktree bool) ([]byte, error) {
	clone, err := decodeGraphObject(cloneData)
	if err != nil {
		return nil, fmt.Errorf("clone graph: %w", err)
	}
	wt, err := decodeGraphObject(wtData)
	if err != nil {
		return nil, fmt.Errorf("worktree graph: %w", err)
	}

	// Depth of keyed maps to merge into before the preferred side wins:
	// nodes is category -> id -> node, concept_index is concept -> list.
	depths := map[string]int{"nodes": 2, "concept_index": 1, "edges": 0}

	out := make(map[string]any, len(clone)+len(wt))
	for k, v := range clone {
		out[k] = v
	}
	for k, wv := range wt {
		cv, both := clone[k]
		if !both {
			out[k] = wv
			continue
		}
		if depth, merge := depths[k]; merge {
			out[k] = mergeGraphValue(cv, wv, preferWorktree, depth)
		} else if preferWorktree {
			out[k] = wv
		}
	}

	return json.MarshalIndent(out, "", "  ")
}

func decodeGraphObject(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, fmt.Errorf("not a JSON object")
	}
	return obj, nil
}

// mergeGraphValue merges lists by identity and maps by key (descending depth
// levels into conflicting entries); anything else is decided by preference.
func mergeGraphValue(clone, wt any, preferWorktree bool, depth int) any {
	switch c := clone.(type) {
	case []any:
		if w, ok := wt.([]any); ok {
			return mergeGraphList(c, w, preferWorktree)
		}
	case map[string]any:
		if w, ok := wt.(map[string]any); ok && depth > 0 {
			out := make(map[string]any, len(c)+len(w))
			for k, v := range c {
				out[k] = v
			}
			for k, wv := range w {
				if cv, both := c[k]; both {
					out[k] = mergeGraphValue(cv, wv, preferWorktree, depth-1)
				} else {
					out[k] = wv
				}
			}
			return out
		}
	}
	if preferWorktree {
		return wt
	}
	return clone
}

// mergeGraphList unions two lists by identity: clone order first, then the
// worktree's new entries. On an identity clash the preferred side's entry
// replaces the other in place.
func mergeGraphList(clone, wt []any, preferWorktree bool) []any {
	out := make([]any, 0, len(clone)+len(wt))
	index := make(map[string]int, len(clone)+len(wt))
	add := func(item any, replace bool) {
		id := graphItemIdentity(item)
		if i, seen := index[id]; seen {
			if replace {
				out[i] = item
			}
			return
		}
		index[id] = len(out)
		out = append(out, item)
	}
	for _, item := range clone {
		add(item, false)
	}
	for _, item := range wt {
		add(item, preferWorktree)
	}
	return out
}

func graphItemIdentity(item any) string {
	if m, ok := item.(map[string]any); ok {
		if id, ok := m["id"].(string); ok && id != "" {
			return "id:" + id
		}
		from, fromOK := m["from"].(string)
		to, toOK := m["to"].(string)
		if fromOK && toOK {
			typ, _ := m["type"].(string)
			return "edge:" + strings.Join([]string{from, to, typ}, "\x00")
		}
	}
	b, _ := json.Marshal(item)
	return "json:" + string(b)
}
