package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// ParallelConfig configures parallel execution behavior
type ParallelConfig struct {
	// MaxSubagents is the maximum number of concurrent subagents
	MaxSubagents int
	// EnableResearch enables parallel research phase for complex tasks
	EnableResearch bool
	// ResearchTimeout is the timeout for research phase subagents
	ResearchTimeout time.Duration
}

// DefaultParallelConfig returns default parallel execution settings
func DefaultParallelConfig() *ParallelConfig {
	return &ParallelConfig{
		MaxSubagents:    3,
		EnableResearch:  true,
		ResearchTimeout: 60 * time.Second,
	}
}

// SubagentType represents different types of parallel subagents
type SubagentType string

const (
	// SubagentResearch explores the codebase
	SubagentResearch SubagentType = "research"
	// SubagentAnalysis analyzes patterns and architecture
	SubagentAnalysis SubagentType = "analysis"
	// SubagentImplementation implements code changes
	SubagentImplementation SubagentType = "implementation"
	// SubagentTest runs tests and validation
	SubagentTest SubagentType = "test"
)

// Subagent represents a parallel execution unit
type Subagent struct {
	ID       string
	Type     SubagentType
	Model    string
	Prompt   string
	Started  time.Time
	Finished time.Time
	Result   *SubagentResult
}

// SubagentResult holds the output from a subagent execution
type SubagentResult struct {
	Output       string
	Error        string
	Success      bool
	TokensInput  int64
	TokensOutput int64
	Duration     time.Duration
}

// ParallelRunner coordinates multiple subagent executions
type ParallelRunner struct {
	config     *ParallelConfig
	modelRoute *ModelRouter
	log        *slog.Logger
	mu         sync.Mutex
	running    map[string]*exec.Cmd
}

// NewParallelRunner creates a new parallel execution coordinator
func NewParallelRunner(config *ParallelConfig, router *ModelRouter) *ParallelRunner {
	if config == nil {
		config = DefaultParallelConfig()
	}
	return &ParallelRunner{
		config:     config,
		modelRoute: router,
		log:        logging.WithComponent("parallel"),
		running:    make(map[string]*exec.Cmd),
	}
}

// ExecuteResearchPhase runs parallel research subagents for complex tasks
// Returns combined research findings to inform the main implementation
func (p *ParallelRunner) ExecuteResearchPhase(ctx context.Context, task *Task) (*ResearchResult, error) {
	if !p.config.EnableResearch {
		return nil, nil
	}

	p.log.Info("Starting parallel research phase",
		slog.String("task_id", task.ID),
		slog.Int("max_subagents", p.config.MaxSubagents),
	)

	// Define research tasks based on task type
	researchTasks := p.planResearchTasks(task)
	if len(researchTasks) == 0 {
		return nil, nil
	}

	// Create context with timeout
	researchCtx, cancel := context.WithTimeout(ctx, p.config.ResearchTimeout)
	defer cancel()

	// Run research subagents in parallel
	results := make(chan *SubagentResult, len(researchTasks))
	var wg sync.WaitGroup

	for i, rt := range researchTasks {
		if i >= p.config.MaxSubagents {
			break
		}

		wg.Add(1)
		go func(subTask researchTask) {
			defer wg.Done()
			result := p.executeSubagent(researchCtx, task.ProjectPath, subTask)
			results <- result
		}(rt)
	}

	// Wait for all research to complete
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	research := &ResearchResult{
		TaskID:    task.ID,
		Findings:  make([]string, 0),
		StartTime: time.Now(),
	}

	for result := range results {
		if result.Success && result.Output != "" {
			research.Findings = append(research.Findings, result.Output)
			research.TotalTokens += result.TokensInput + result.TokensOutput
		}
		if result.Error != "" {
			p.log.Warn("Research subagent error", slog.String("error", result.Error))
		}
	}

	research.EndTime = time.Now()
	research.Duration = research.EndTime.Sub(research.StartTime)

	p.log.Info("Research phase completed",
		slog.String("task_id", task.ID),
		slog.Int("findings", len(research.Findings)),
		slog.Duration("duration", research.Duration),
	)

	return research, nil
}

// researchTask defines a single research subagent task
type researchTask struct {
	Type   SubagentType
	Prompt string
	Model  string
}

// planResearchTasks creates research subtasks based on task analysis
func (p *ParallelRunner) planResearchTasks(task *Task) []researchTask {
	tasks := make([]researchTask, 0, 3)

	// Determine research needs based on task description
	desc := task.Description

	// Always include codebase exploration for context
	tasks = append(tasks, researchTask{
		Type: SubagentResearch,
		Prompt: fmt.Sprintf(`Explore the codebase to understand the context for this task:
Task: %s

Focus on:
1. Existing patterns and conventions used in this project
2. Related files and modules that might be affected
3. Any existing similar implementations

Output a brief summary of relevant findings (max 500 words).
DO NOT make any changes. Research only.`, desc),
		Model: "haiku", // Fast model for research
	})

	// Check if task mentions tests or testing
	if containsAny(desc, []string{"test", "spec", "coverage", "validation"}) {
		tasks = append(tasks, researchTask{
			Type: SubagentAnalysis,
			Prompt: fmt.Sprintf(`Analyze the testing patterns in this codebase for task:
Task: %s

Focus on:
1. Test file locations and naming conventions
2. Testing frameworks and utilities used
3. Existing test patterns to follow

Output a brief summary (max 300 words).
DO NOT make any changes. Research only.`, desc),
			Model: "haiku",
		})
	}

	// Check if task involves API or integration
	if containsAny(desc, []string{"api", "endpoint", "integration", "webhook", "http"}) {
		tasks = append(tasks, researchTask{
			Type: SubagentAnalysis,
			Prompt: fmt.Sprintf(`Analyze API patterns in this codebase for task:
Task: %s

Focus on:
1. API structure and routing patterns
2. Request/response handling conventions
3. Error handling patterns

Output a brief summary (max 300 words).
DO NOT make any changes. Research only.`, desc),
			Model: "haiku",
		})
	}

	return tasks
}

// executeSubagent runs a single subagent and returns results
func (p *ParallelRunner) executeSubagent(ctx context.Context, projectPath string, task researchTask) *SubagentResult {
	start := time.Now()

	// Determine model flag
	modelFlag := ""
	switch task.Model {
	case "haiku":
		modelFlag = "--model haiku"
	case "sonnet":
		modelFlag = "--model sonnet"
	case "opus":
		modelFlag = "--model opus"
	}

	// Build command - use haiku for fast research
	args := []string{"-p", task.Prompt, "--output-format", "text"}
	if modelFlag != "" {
		args = append([]string{modelFlag}, args...)
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = projectPath

	// Track running command
	cmdID := fmt.Sprintf("%s-%d", task.Type, time.Now().UnixNano())
	p.mu.Lock()
	p.running[cmdID] = cmd
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		delete(p.running, cmdID)
		p.mu.Unlock()
	}()

	// Execute
	output, err := cmd.Output()
	duration := time.Since(start)

	result := &SubagentResult{
		Duration: duration,
	}

	if err != nil {
		result.Success = false
		result.Error = err.Error()
		return result
	}

	result.Success = true
	result.Output = string(output)
	// Note: Token counts not available from text output mode
	// Would need stream-json parsing for accurate counts

	return result
}

// Cancel stops all running subagents
func (p *ParallelRunner) Cancel() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for id, cmd := range p.running {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			p.log.Debug("Cancelled subagent", slog.String("id", id))
		}
	}
	p.running = make(map[string]*exec.Cmd)
}

// ResearchResult holds combined findings from parallel research
type ResearchResult struct {
	TaskID      string
	Findings    []string
	TotalTokens int64
	StartTime   time.Time
	EndTime     time.Time
	Duration    time.Duration
}

// Summarize returns a combined summary of research findings
func (r *ResearchResult) Summarize() string {
	if len(r.Findings) == 0 {
		return ""
	}

	summary := "## Research Findings\n\n"
	for i, finding := range r.Findings {
		summary += fmt.Sprintf("### Finding %d\n%s\n\n", i+1, finding)
	}
	return summary
}

// containsAny checks if text contains any of the keywords (case-insensitive)
func containsAny(text string, keywords []string) bool {
	textLower := toLowerASCII(text)
	for _, kw := range keywords {
		if containsSubstr(textLower, toLowerASCII(kw)) {
			return true
		}
	}
	return false
}

// toLowerASCII converts string to lowercase (simple ASCII implementation)
func toLowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// containsSubstr checks if s contains substr
func containsSubstr(s, substr string) bool {
	return len(s) >= len(substr) && findSubstr(s, substr) >= 0
}

// findSubstr finds substr in s, returns -1 if not found
func findSubstr(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
