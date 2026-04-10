package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// mockKnowledgeGraphRecorder implements KnowledgeGraphRecorder for testing.
type mockKnowledgeGraphRecorder struct {
	mu             sync.Mutex
	learningCalls  []graphLearningCall
	keywordResults []*memory.GraphNode
	returnErr      error
}

type graphLearningCall struct {
	title        string
	content      string
	filesChanged []string
	patterns     []string
	outcome      string
}

func (m *mockKnowledgeGraphRecorder) AddExecutionLearning(title, content string, filesChanged []string, patterns []string, outcome string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.learningCalls = append(m.learningCalls, graphLearningCall{
		title:        title,
		content:      content,
		filesChanged: filesChanged,
		patterns:     patterns,
		outcome:      outcome,
	})
	return m.returnErr
}

func (m *mockKnowledgeGraphRecorder) GetRelatedByKeywords(_ []string) []*memory.GraphNode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.keywordResults
}

// --- Runner integration tests ---

func TestRecordGraphLearning_Success(t *testing.T) {
	runner := NewRunner()
	mock := &mockKnowledgeGraphRecorder{}
	runner.SetKnowledgeGraph(mock)

	task := &Task{
		ID:          "GH-100",
		Title:       "Add webhook auth",
		Description: "Add authentication to webhook endpoints",
	}

	result := &ExecutionResult{
		Success:  true,
		Duration: 3 * time.Second,
	}

	runner.recordGraphLearning(task, result)

	mock.mu.Lock()
	defer mock.mu.Unlock()

	if len(mock.learningCalls) != 1 {
		t.Fatalf("expected 1 AddExecutionLearning call, got %d", len(mock.learningCalls))
	}

	call := mock.learningCalls[0]
	if call.title != "Add webhook auth" {
		t.Errorf("title = %q, want %q", call.title, "Add webhook auth")
	}
	if call.outcome != "success" {
		t.Errorf("outcome = %q, want %q", call.outcome, "success")
	}
	// Should extract "auth" and "webhook" patterns
	hasAuth := false
	hasWebhook := false
	for _, p := range call.patterns {
		if p == "auth" {
			hasAuth = true
		}
		if p == "webhook" {
			hasWebhook = true
		}
	}
	if !hasAuth {
		t.Error("expected 'auth' in patterns")
	}
	if !hasWebhook {
		t.Error("expected 'webhook' in patterns")
	}
}

func TestRecordGraphLearning_Failure(t *testing.T) {
	runner := NewRunner()
	mock := &mockKnowledgeGraphRecorder{}
	runner.SetKnowledgeGraph(mock)

	task := &Task{
		ID:          "GH-101",
		Title:       "Fix database migration",
		Description: "Fix broken migration script",
	}

	result := &ExecutionResult{
		Success: false,
		Error:   "compilation failed",
	}

	runner.recordGraphLearning(task, result)

	mock.mu.Lock()
	defer mock.mu.Unlock()

	if len(mock.learningCalls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(mock.learningCalls))
	}
	if mock.learningCalls[0].outcome != "failure" {
		t.Errorf("outcome = %q, want %q", mock.learningCalls[0].outcome, "failure")
	}
}

func TestRecordGraphLearning_NilGraph(t *testing.T) {
	runner := NewRunner()
	// knowledgeGraph is nil by default
	task := &Task{ID: "GH-102", Title: "Test nil"}
	result := &ExecutionResult{Success: true}

	// Should not panic
	runner.recordGraphLearning(task, result)
}

func TestRecordGraphLearning_ErrorDoesNotPanic(t *testing.T) {
	runner := NewRunner()
	mock := &mockKnowledgeGraphRecorder{returnErr: fmt.Errorf("disk full")}
	runner.SetKnowledgeGraph(mock)

	task := &Task{ID: "GH-103", Title: "Test error"}
	result := &ExecutionResult{Success: true}

	// Should not panic - error is logged
	runner.recordGraphLearning(task, result)

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.learningCalls) != 1 {
		t.Fatalf("expected call even on error, got %d", len(mock.learningCalls))
	}
}

func TestRecordGraphLearning_TruncatesLongDescription(t *testing.T) {
	runner := NewRunner()
	mock := &mockKnowledgeGraphRecorder{}
	runner.SetKnowledgeGraph(mock)

	longDesc := strings.Repeat("x", 600)
	task := &Task{
		ID:          "GH-104",
		Title:       "Long task",
		Description: longDesc,
	}
	result := &ExecutionResult{Success: true}

	runner.recordGraphLearning(task, result)

	mock.mu.Lock()
	defer mock.mu.Unlock()

	if len(mock.learningCalls[0].content) > 500 {
		t.Errorf("content length = %d, want <= 500", len(mock.learningCalls[0].content))
	}
}

// --- Setter/Has tests ---

func TestSetKnowledgeGraph(t *testing.T) {
	runner := NewRunner()
	if runner.HasKnowledgeGraph() {
		t.Error("expected HasKnowledgeGraph=false before set")
	}
	runner.SetKnowledgeGraph(&mockKnowledgeGraphRecorder{})
	if !runner.HasKnowledgeGraph() {
		t.Error("expected HasKnowledgeGraph=true after set")
	}
}

// --- extractLearningPatterns tests ---

func TestExtractLearningPatterns(t *testing.T) {
	tests := []struct {
		name     string
		task     *Task
		wantAny  []string
		wantNone []string
	}{
		{
			name:    "auth and api task",
			task:    &Task{Title: "Add OAuth to API", Description: "authentication endpoint"},
			wantAny: []string{"auth", "api"},
		},
		{
			name:     "no matching patterns",
			task:     &Task{Title: "Update readme", Description: "improve docs"},
			wantNone: []string{"auth", "api", "test"},
		},
		{
			name:    "test and fix",
			task:    &Task{Title: "Fix broken tests", Description: "unit test failing"},
			wantAny: []string{"fix", "test"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractLearningPatterns(tt.task)
			gotSet := make(map[string]bool)
			for _, p := range got {
				gotSet[p] = true
			}
			for _, want := range tt.wantAny {
				if !gotSet[want] {
					t.Errorf("expected pattern %q in result %v", want, got)
				}
			}
			for _, notWant := range tt.wantNone {
				if gotSet[notWant] {
					t.Errorf("unexpected pattern %q in result %v", notWant, got)
				}
			}
		})
	}
}

// --- Prompt builder integration tests ---

func TestBuildPrompt_InjectsRelatedLearnings(t *testing.T) {
	// Create temp dir with .agent/ so Navigator path is triggered
	tempDir, err := os.MkdirTemp("", "pilot-test-graph")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	agentDir := filepath.Join(tempDir, ".agent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatalf("create .agent dir: %v", err)
	}

	now := time.Now()
	mock := &mockKnowledgeGraphRecorder{
		keywordResults: []*memory.GraphNode{
			{ID: "1", Type: "execution_learning", Title: "Add auth middleware", Content: "Added JWT validation", UpdatedAt: now},
			{ID: "2", Type: "pattern", Title: "Error handling", Content: "Always wrap errors", UpdatedAt: now},
		},
	}

	runner := NewRunner()
	runner.SetKnowledgeGraph(mock)

	task := &Task{
		ID:          "GH-200",
		Title:       "Add auth to webhook",
		Description: "Implement authentication for webhook endpoints",
		ProjectPath: tempDir,
	}

	prompt := runner.BuildPrompt(task, tempDir)

	if !strings.Contains(prompt, "## Related Learnings") {
		t.Error("expected '## Related Learnings' section in prompt")
	}
	if !strings.Contains(prompt, "Add auth middleware") {
		t.Error("expected learning title in prompt")
	}
	if !strings.Contains(prompt, "Added JWT validation") {
		t.Error("expected learning content in prompt")
	}
	if !strings.Contains(prompt, "Always wrap errors") {
		t.Error("expected second learning content in prompt")
	}
}

func TestBuildPrompt_NoLearningsWhenGraphNil(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pilot-test-graph-nil")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	agentDir := filepath.Join(tempDir, ".agent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatalf("create .agent dir: %v", err)
	}

	runner := NewRunner()
	// knowledgeGraph is nil

	task := &Task{
		ID:          "GH-201",
		Title:       "Simple fix",
		Description: "Fix a typo in config",
		ProjectPath: tempDir,
	}

	prompt := runner.BuildPrompt(task, tempDir)

	if strings.Contains(prompt, "## Related Learnings") {
		t.Error("should not inject learnings when graph is nil")
	}
}

func TestBuildPrompt_NoLearningsWhenEmptyResults(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pilot-test-graph-empty")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	agentDir := filepath.Join(tempDir, ".agent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatalf("create .agent dir: %v", err)
	}

	mock := &mockKnowledgeGraphRecorder{
		keywordResults: nil, // no results
	}

	runner := NewRunner()
	runner.SetKnowledgeGraph(mock)

	task := &Task{
		ID:          "GH-202",
		Title:       "Add webhook handler",
		Description: "Create a new webhook handler",
		ProjectPath: tempDir,
	}

	prompt := runner.BuildPrompt(task, tempDir)

	if strings.Contains(prompt, "## Related Learnings") {
		t.Error("should not inject learnings section when no results")
	}
}

func TestBuildPrompt_CapsLearningsAtFive(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pilot-test-graph-cap")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	agentDir := filepath.Join(tempDir, ".agent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatalf("create .agent dir: %v", err)
	}

	now := time.Now()
	nodes := make([]*memory.GraphNode, 8)
	for i := range nodes {
		nodes[i] = &memory.GraphNode{
			ID:        fmt.Sprintf("node-%d", i),
			Type:      "execution_learning",
			Title:     fmt.Sprintf("Learning %d", i),
			Content:   fmt.Sprintf("Content for learning %d", i),
			UpdatedAt: now,
		}
	}

	mock := &mockKnowledgeGraphRecorder{keywordResults: nodes}

	runner := NewRunner()
	runner.SetKnowledgeGraph(mock)

	task := &Task{
		ID:          "GH-203",
		Title:       "Add API auth",
		Description: "Implement api authentication",
		ProjectPath: tempDir,
	}

	prompt := runner.BuildPrompt(task, tempDir)

	// Should contain learnings 0-4 but NOT 5-7
	if !strings.Contains(prompt, "Learning 4") {
		t.Error("expected Learning 4 in prompt (5th item)")
	}
	if strings.Contains(prompt, "Learning 5") {
		t.Error("Learning 5 should be excluded (cap at 5)")
	}
}

// --- Compile-time interface check ---

func TestKnowledgeGraphRecorder_Interface(t *testing.T) {
	// Verify that *memory.KnowledgeGraph satisfies the KnowledgeGraphRecorder interface
	var _ KnowledgeGraphRecorder = (*memory.KnowledgeGraph)(nil)
}
