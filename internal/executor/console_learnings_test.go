package executor

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"

	_ "modernc.org/sqlite"
)

func newLearningsKnowledgeStore(t *testing.T) *memory.KnowledgeStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ks := memory.NewKnowledgeStore(db)
	if err := ks.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	return ks
}

func addConsoleLearning(t *testing.T, ks *memory.KnowledgeStore, project, header, reason string) {
	t.Helper()
	m := &memory.Memory{
		Type:      memory.MemoryTypeLearning,
		Content:   header + "\n" + reason,
		ProjectID: project,
		Source:    memory.SourceConsoleReview,
	}
	if err := ks.AddMemory(m); err != nil {
		t.Fatalf("AddMemory: %v", err)
	}
}

func TestPromptMemoryBlock_IncludesConsoleLearnings(t *testing.T) {
	dir := t.TempDir() // project id = filepath.Base(dir)
	ks := newLearningsKnowledgeStore(t)
	addConsoleLearning(t, ks, projectIDOf(dir), "review_reject — task GH-7", "never swallow the error from the store")

	runner := NewRunner()
	runner.config = enabledMemoryInjectionConfig()
	runner.SetKnowledgeStore(ks)
	task := &Task{ID: "GH-8", Title: "Unrelated", Description: "Something else", ProjectPath: dir}

	block := runner.buildMemoryInjectionBlock(task, dir, false)
	if !strings.Contains(block, "never swallow the error from the store") {
		t.Fatalf("memory block missing the console learning reason:\n%s", block)
	}
	if !strings.Contains(block, "review_reject — task GH-7") {
		t.Errorf("memory block missing the subject header:\n%s", block)
	}

	if prompt := runner.BuildPrompt(task, dir); !strings.Contains(prompt, "never swallow the error from the store") {
		t.Errorf("task prompt missing the console learning reason")
	}
}

func TestPromptMemoryBlock_ConsoleLearningsScopedToProject(t *testing.T) {
	dir := t.TempDir()
	ks := newLearningsKnowledgeStore(t)
	addConsoleLearning(t, ks, "some-other-project", "review_reject", "belongs elsewhere")
	if err := ks.AddMemory(&memory.Memory{Type: memory.MemoryTypeLearning, Content: "h\nfrom a different source", ProjectID: projectIDOf(dir)}); err != nil {
		t.Fatal(err)
	}

	runner := NewRunner()
	runner.config = enabledMemoryInjectionConfig()
	runner.SetKnowledgeStore(ks)
	task := &Task{ID: "GH-8", Title: "t", Description: "d", ProjectPath: dir}

	if block := runner.buildMemoryInjectionBlock(task, dir, false); block != "" {
		t.Errorf("block = %q, want empty (no console-review learnings for this project)", block)
	}
}

func TestPromptMemoryBlock_ConsoleLearningsRespectGating(t *testing.T) {
	dir := t.TempDir()
	ks := newLearningsKnowledgeStore(t)
	addConsoleLearning(t, ks, projectIDOf(dir), "review_reject", "reason text")

	t.Run("injection disabled", func(t *testing.T) {
		runner := NewRunner()
		runner.config = &BackendConfig{MemoryInjection: &MemoryInjectionConfig{Enabled: false}}
		runner.SetKnowledgeStore(ks)
		if block := runner.buildMemoryInjectionBlock(&Task{ProjectPath: dir}, dir, true); block != "" {
			t.Errorf("block = %q, want empty when memory injection is off", block)
		}
	})
	t.Run("local mode", func(t *testing.T) {
		runner := NewRunner()
		runner.config = enabledMemoryInjectionConfig()
		runner.SetKnowledgeStore(ks)
		if block := runner.buildMemoryInjectionBlock(&Task{ProjectPath: dir, LocalMode: true}, dir, true); block != "" {
			t.Errorf("block = %q, want empty in local mode", block)
		}
	})
	t.Run("no knowledge store", func(t *testing.T) {
		runner := NewRunner()
		runner.config = enabledMemoryInjectionConfig()
		if block := runner.buildMemoryInjectionBlock(&Task{ProjectPath: dir}, dir, false); block != "" {
			t.Errorf("block = %q, want empty without a knowledge store", block)
		}
	})
}

func TestPromptMemoryBlock_ConsoleLearningsAlongsideGraphMemories(t *testing.T) {
	dir := t.TempDir()
	writeMemoryGraphFixture(t, dir)
	ks := newLearningsKnowledgeStore(t)
	addConsoleLearning(t, ks, projectIDOf(dir), "review_reject", "console reason")

	runner := NewRunner()
	runner.config = enabledMemoryInjectionConfig()
	runner.SetKnowledgeStore(ks)
	task := &Task{ID: "GH-9", Title: "Epic decomposition", Description: "epic decomposition subtask ordering", ProjectPath: dir}

	block := runner.buildMemoryInjectionBlock(task, dir, true)
	graphAt := strings.Index(block, "subtasks must declare explicit dependency order")
	learnAt := strings.Index(block, "console reason")
	if graphAt < 0 || learnAt < 0 || graphAt > learnAt {
		t.Errorf("want graph memories then console learnings in one block:\n%s", block)
	}
}

func TestRenderConsoleLearnings_BoundedAndNewestFirst(t *testing.T) {
	var learnings []*memory.Memory
	for i := 0; i < consoleLearningsLimit; i++ {
		learnings = append(learnings, &memory.Memory{
			Content: fmt.Sprintf("review_reject — task GH-%d\n%s", i, strings.Repeat(fmt.Sprintf("r%d ", i), 100)),
		})
	}
	block := renderConsoleLearnings(learnings)
	body := strings.TrimSuffix(strings.TrimPrefix(block, consoleLearningsHeader), consoleLearningsFooter)
	if len(body) > consoleLearningsCharCap {
		t.Errorf("learnings text = %d bytes, want <= %d", len(body), consoleLearningsCharCap)
	}
	if !strings.Contains(body, "GH-0") {
		t.Error("newest learning (first in input) was dropped")
	}
	if strings.Contains(body, "GH-9") {
		t.Error("oldest learning should have been dropped to stay within the cap")
	}

	huge := renderConsoleLearnings([]*memory.Memory{{Content: "h\n" + strings.Repeat("é", 5000)}})
	if len(huge) == 0 || len(strings.TrimSuffix(strings.TrimPrefix(huge, consoleLearningsHeader), consoleLearningsFooter)) > consoleLearningsCharCap {
		t.Errorf("single oversized learning not truncated within the cap (len %d)", len(huge))
	}
	if renderConsoleLearnings(nil) != "" {
		t.Error("no learnings must render nothing")
	}
}

func TestBuildChatPrompt_ConsoleLearnings(t *testing.T) {
	if got, want := buildChatPromptWithLearnings("/p", "hi", ""), BuildChatPrompt("/p", "hi"); got != want {
		t.Error("empty learnings block must leave the chat prompt unchanged")
	}

	dir := t.TempDir()
	ks := newLearningsKnowledgeStore(t)
	addConsoleLearning(t, ks, projectIDOf(dir), "draft_reject — draft d1", "tickets must name a test")
	runner := NewRunner()
	runner.SetKnowledgeStore(ks)

	prompt := buildChatPromptWithLearnings(dir, "draft a ticket", runner.buildConsoleLearningsBlock(dir))
	reasonAt := strings.Index(prompt, "tickets must name a test")
	userAt := strings.Index(prompt, chatUserSeparator)
	if reasonAt < 0 || userAt < 0 || reasonAt > userAt {
		t.Errorf("learning should appear before the user message:\n%s", prompt)
	}
}

func projectIDOf(path string) string { return consoleLearningsProjectID(path) }
