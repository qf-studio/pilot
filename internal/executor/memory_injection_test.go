package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMemoryGraphFixture writes a .agent/knowledge/graph.json under dir
// containing an "epic-decomposition" concept and a matching memory, mirroring
// the fixture shape used by internal/memory/graphrecall's own tests.
func writeMemoryGraphFixture(t *testing.T, dir string) {
	t.Helper()
	graphDir := filepath.Join(dir, ".agent", "knowledge")
	if err := os.MkdirAll(graphDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const fixture = `{
  "version": 1,
  "nodes": {
    "concepts": {
      "epic-decomposition": {
        "label": "Epic Decomposition",
        "description": "splitting large tasks into subtasks"
      }
    },
    "memories": {
      "mem-epic-1": {
        "type": "pitfall",
        "summary": "subtasks must declare explicit dependency order",
        "concepts": ["epic-decomposition"],
        "confidence": 0.9
      }
    }
  },
  "edges": []
}`
	if err := os.WriteFile(filepath.Join(graphDir, "graph.json"), []byte(fixture), 0644); err != nil {
		t.Fatalf("write graph.json: %v", err)
	}
}

func enabledMemoryInjectionConfig() *BackendConfig {
	return &BackendConfig{
		MemoryInjection: &MemoryInjectionConfig{Enabled: true, MaxMemories: 5},
	}
}

func TestBuildPromptMemoryInjection(t *testing.T) {
	t.Run("injection appears for task mentioning epic decomposition against fixture graph", func(t *testing.T) {
		dir := t.TempDir()
		writeMemoryGraphFixture(t, dir)

		runner := NewRunner()
		runner.config = enabledMemoryInjectionConfig()
		task := &Task{
			ID:          "GH-3909",
			Title:       "Epic decomposition follow-up",
			Description: "Fix a bug in epic decomposition subtask ordering",
			ProjectPath: dir,
		}

		prompt := runner.BuildPrompt(task, dir)

		if !strings.Contains(prompt, "## Known pitfalls from project memory") {
			t.Fatal("prompt should contain the memory injection header")
		}
		if !strings.Contains(prompt, "pitfall") {
			t.Error("prompt should include the memory's type")
		}
		if !strings.Contains(prompt, "subtasks must declare explicit dependency order") {
			t.Error("prompt should include the memory's summary")
		}
		if !strings.Contains(prompt, "0.90") {
			t.Error("prompt should include the memory's confidence")
		}
		if !strings.Contains(prompt, "Heed these before implementing.") {
			t.Error("prompt should end the block with the heed directive")
		}
	})

	t.Run("LocalMode skips memory injection", func(t *testing.T) {
		dir := t.TempDir()
		writeMemoryGraphFixture(t, dir)

		runner := NewRunner()
		runner.config = enabledMemoryInjectionConfig()
		task := &Task{
			ID:          "GH-3909",
			Title:       "Epic decomposition follow-up",
			Description: "Fix a bug in epic decomposition subtask ordering",
			ProjectPath: dir,
			LocalMode:   true,
		}

		withMemory := runner.BuildPrompt(task, dir)

		baselineRunner := NewRunner()
		baselineTask := *task
		withoutMemory := baselineRunner.BuildPrompt(&baselineTask, dir)

		if withMemory != withoutMemory {
			t.Errorf("LocalMode prompt must be byte-identical regardless of memory injection config\nwith:    %q\nwithout: %q", withMemory, withoutMemory)
		}
		if strings.Contains(withMemory, "Known pitfalls from project memory") {
			t.Error("LocalMode prompt should never contain the memory injection block")
		}
	})

	t.Run("disabled config skips memory injection", func(t *testing.T) {
		dir := t.TempDir()
		writeMemoryGraphFixture(t, dir)
		if err := os.MkdirAll(filepath.Join(dir, ".agent"), 0755); err != nil {
			t.Fatalf("mkdir .agent: %v", err)
		}

		task := &Task{
			ID:          "GH-3909",
			Title:       "Epic decomposition follow-up",
			Description: "Fix a bug in epic decomposition subtask ordering",
			ProjectPath: dir,
		}

		disabledRunner := NewRunner()
		disabledRunner.config = &BackendConfig{
			MemoryInjection: &MemoryInjectionConfig{Enabled: false, MaxMemories: 5},
		}
		withDisabledConfig := disabledRunner.BuildPrompt(task, dir)

		nilConfigRunner := NewRunner()
		baselineTask := *task
		withNilConfig := nilConfigRunner.BuildPrompt(&baselineTask, dir)

		if withDisabledConfig != withNilConfig {
			t.Errorf("disabled config prompt must be byte-identical to no-config prompt\ndisabled: %q\nnil:      %q", withDisabledConfig, withNilConfig)
		}
		if strings.Contains(withDisabledConfig, "Known pitfalls from project memory") {
			t.Error("prompt should not contain memory injection block when config is disabled")
		}
	})

	t.Run("no graph produces byte-identical output", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".agent"), 0755); err != nil {
			t.Fatalf("mkdir .agent: %v", err)
		}
		// Deliberately no .agent/knowledge/graph.json written.

		task := &Task{
			ID:          "GH-3909",
			Title:       "Epic decomposition follow-up",
			Description: "Fix a bug in epic decomposition subtask ordering",
			ProjectPath: dir,
		}

		enabledRunner := NewRunner()
		enabledRunner.config = enabledMemoryInjectionConfig()
		withEnabledConfig := enabledRunner.BuildPrompt(task, dir)

		nilConfigRunner := NewRunner()
		baselineTask := *task
		withNilConfig := nilConfigRunner.BuildPrompt(&baselineTask, dir)

		if withEnabledConfig != withNilConfig {
			t.Errorf("missing-graph prompt must be byte-identical to no-config prompt\nenabled: %q\nnil:     %q", withEnabledConfig, withNilConfig)
		}
	})

	t.Run("no matches produces byte-identical output", func(t *testing.T) {
		dir := t.TempDir()
		writeMemoryGraphFixture(t, dir) // fixture only has "epic-decomposition" concept

		task := &Task{
			ID:          "GH-3909",
			Title:       "Unrelated task",
			Description: "Fix a totally unrelated dashboard rendering glitch",
			ProjectPath: dir,
		}

		enabledRunner := NewRunner()
		enabledRunner.config = enabledMemoryInjectionConfig()
		withEnabledConfig := enabledRunner.BuildPrompt(task, dir)

		nilConfigRunner := NewRunner()
		baselineTask := *task
		withNilConfig := nilConfigRunner.BuildPrompt(&baselineTask, dir)

		if withEnabledConfig != withNilConfig {
			t.Errorf("no-match prompt must be byte-identical to no-config prompt\nenabled: %q\nnil:     %q", withEnabledConfig, withNilConfig)
		}
		if strings.Contains(withEnabledConfig, "Known pitfalls from project memory") {
			t.Error("prompt should not contain memory injection block when nothing was recalled")
		}
	})

	t.Run("char cap enforcement truncates the list, never mid-entry", func(t *testing.T) {
		dir := t.TempDir()
		graphDir := filepath.Join(dir, ".agent", "knowledge")
		if err := os.MkdirAll(graphDir, 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		// Build a fixture with 5 memories, each with a long, uniquely-tagged
		// summary so the ~1500 char cap forces the list to truncate before
		// all 5 fit. Confidence descends per entry so ranking (and thus
		// which entries survive truncation) is deterministic.
		summaries := make([]string, 5)
		var sb strings.Builder
		sb.WriteString(`{"version":1,"nodes":{"concepts":{"epic-decomposition":{"label":"Epic Decomposition"}},"memories":{`)
		for i := 0; i < 5; i++ {
			if i > 0 {
				sb.WriteString(",")
			}
			summaries[i] = fmt.Sprintf("UNIQTAG%d ", i) + strings.Repeat("this pitfall summary is deliberately verbose ", 8) // ~380 chars
			confidence := 0.9 - float64(i)*0.1
			sb.WriteString(fmt.Sprintf(`"mem-%c":{"type":"pitfall","summary":%q,"concepts":["epic-decomposition"],"confidence":%.2f}`,
				'a'+i, summaries[i], confidence))
		}
		sb.WriteString(`}},"edges":[]}`)
		if err := os.WriteFile(filepath.Join(graphDir, "graph.json"), []byte(sb.String()), 0644); err != nil {
			t.Fatalf("write graph.json: %v", err)
		}

		runner := NewRunner()
		runner.config = enabledMemoryInjectionConfig()
		task := &Task{
			ID:          "GH-3909",
			Title:       "Epic decomposition follow-up",
			Description: "Fix a bug in epic decomposition subtask ordering",
			ProjectPath: dir,
		}

		prompt := runner.BuildPrompt(task, dir)

		start := strings.Index(prompt, "## Known pitfalls from project memory")
		if start == -1 {
			t.Fatal("prompt should contain the memory injection block")
		}
		end := strings.Index(prompt[start:], "Heed these before implementing.")
		if end == -1 {
			t.Fatal("prompt should contain the closing directive")
		}
		block := prompt[start : start+end+len("Heed these before implementing.")]

		if len(block) > memoryInjectionCharCap {
			t.Errorf("memory injection block is %d chars, want <= %d", len(block), memoryInjectionCharCap)
		}

		entryCount := 0
		for i, s := range summaries {
			tag := fmt.Sprintf("UNIQTAG%d ", i)
			if !strings.Contains(block, tag) {
				continue
			}
			entryCount++
			// The list must be truncated whole-entry, never mid-summary:
			// every included entry's full (long) summary must appear intact.
			if !strings.Contains(block, s) {
				t.Errorf("entry %d present but its summary was truncated mid-entry", i)
			}
		}
		if entryCount == 0 {
			t.Fatal("expected at least one memory entry in the block")
		}
		if entryCount >= 5 {
			t.Errorf("expected the char cap to drop some entries, got all %d", entryCount)
		}
	})

	t.Run("Navigator prefix untouched (regression guard for mem-004)", func(t *testing.T) {
		dir := t.TempDir()
		writeMemoryGraphFixture(t, dir)

		runner := NewRunner()
		runner.config = enabledMemoryInjectionConfig()
		task := &Task{
			ID:          "GH-3909",
			Title:       "Epic decomposition follow-up",
			Description: "Fix a bug in epic decomposition subtask ordering",
			ProjectPath: dir,
			Branch:      "pilot/GH-3909",
		}

		prompt := runner.BuildPrompt(task, dir)

		if !strings.HasPrefix(prompt, ExecutorPromptHeader) {
			t.Error("[PILOT-EXEC] header must remain the first content in the prompt")
		}
		if !strings.Contains(prompt, "## PILOT EXECUTION MODE") {
			t.Error("Navigator PILOT EXECUTION MODE section must be present unmodified")
		}
		if !strings.Contains(prompt, GetAutonomousWorkflowInstructions()) {
			t.Error("Navigator autonomous workflow instructions must be present unmodified")
		}

		// The memory injection block must appear strictly after the
		// Navigator prefix and task sections, never interleaved within them.
		navPos := strings.Index(prompt, "## PILOT EXECUTION MODE")
		memPos := strings.Index(prompt, "## Known pitfalls from project memory")
		if memPos == -1 {
			t.Fatal("expected memory injection block to be present")
		}
		if memPos < navPos {
			t.Error("memory injection block must come after the Navigator prefix, not before")
		}
	})
}
