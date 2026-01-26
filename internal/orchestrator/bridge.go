package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// Bridge handles communication between Go and Python orchestrator
type Bridge struct {
	pythonPath string
	scriptDir  string
}

// NewBridge creates a new Python bridge
func NewBridge() (*Bridge, error) {
	// Find Python executable
	pythonPath, err := findPython()
	if err != nil {
		return nil, fmt.Errorf("failed to find Python: %w", err)
	}

	// Get script directory (relative to this package)
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return nil, fmt.Errorf("failed to get caller info")
	}

	// Navigate from internal/orchestrator to orchestrator/
	scriptDir := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(filename))), "orchestrator")

	return &Bridge{
		pythonPath: pythonPath,
		scriptDir:  scriptDir,
	}, nil
}

// findPython finds the Python executable
func findPython() (string, error) {
	// Try python3 first, then python
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("python not found in PATH")
}

// TicketData represents ticket data for planning
type TicketData struct {
	ID          string   `json:"id"`
	Identifier  string   `json:"identifier"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Priority    int      `json:"priority"`
	Labels      []string `json:"labels"`
	Project     string   `json:"project,omitempty"`
	Assignee    string   `json:"assignee,omitempty"`
}

// TaskDocument represents the generated task document
type TaskDocument struct {
	ID       string
	Title    string
	Markdown string
}

// PlanTicket converts a ticket to a task document
func (b *Bridge) PlanTicket(ctx context.Context, ticket *TicketData) (*TaskDocument, error) {
	input, err := json.Marshal(ticket)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ticket: %w", err)
	}

	script := fmt.Sprintf(`
import sys
import json
sys.path.insert(0, %q)
from planner import plan_ticket

ticket = json.loads(%q)
result = plan_ticket(ticket)
print(result)
`, b.scriptDir, string(input))

	output, err := b.runPython(ctx, script)
	if err != nil {
		return nil, err
	}

	return &TaskDocument{
		ID:       fmt.Sprintf("TASK-%s", ticket.Identifier),
		Title:    ticket.Title,
		Markdown: output,
	}, nil
}

// ScoredTask represents a scored task from priority scoring
type ScoredTask struct {
	TaskID      string             `json:"task_id"`
	Title       string             `json:"title"`
	RawPriority int                `json:"raw_priority"`
	Score       float64            `json:"score"`
	Factors     map[string]float64 `json:"factors"`
}

// ScoreTasks scores and prioritizes tasks
func (b *Bridge) ScoreTasks(ctx context.Context, tasks []map[string]interface{}) ([]ScoredTask, error) {
	input, err := json.Marshal(tasks)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal tasks: %w", err)
	}

	script := fmt.Sprintf(`
import sys
import json
sys.path.insert(0, %q)
from priority import score_tasks

tasks = json.loads(%q)
result = score_tasks(tasks)
print(json.dumps(result))
`, b.scriptDir, string(input))

	output, err := b.runPython(ctx, script)
	if err != nil {
		return nil, err
	}

	var scored []ScoredTask
	if err := json.Unmarshal([]byte(output), &scored); err != nil {
		return nil, fmt.Errorf("failed to parse scored tasks: %w", err)
	}

	return scored, nil
}

// GenerateBrief generates a daily brief
func (b *Bridge) GenerateBrief(ctx context.Context, tasks []map[string]interface{}, format string) (string, error) {
	input, err := json.Marshal(tasks)
	if err != nil {
		return "", fmt.Errorf("failed to marshal tasks: %w", err)
	}

	script := fmt.Sprintf(`
import sys
import json
sys.path.insert(0, %q)
from briefing import generate_brief

tasks = json.loads(%q)
result = generate_brief(tasks, %q)
print(result)
`, b.scriptDir, string(input), format)

	return b.runPython(ctx, script)
}

// runPython executes a Python script and returns output
func (b *Bridge) runPython(ctx context.Context, script string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, b.pythonPath, "-c", script)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("python error: %v: %s", err, stderr.String())
	}

	return stdout.String(), nil
}
