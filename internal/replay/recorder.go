package replay

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
	"log/slog"
)

// Recorder captures execution events for later replay
type Recorder struct {
	id           string
	taskID       string
	projectPath  string
	basePath     string // ~/.pilot/recordings
	recording    *Recording
	streamFile   *os.File
	diffFiles    map[string]*FileDiff // Track file changes
	sequence     int
	currentPhase string
	phaseStart   time.Time
	mu           sync.Mutex
	log          *slog.Logger
}

// NewRecorder creates a recorder for a task execution
func NewRecorder(taskID, projectPath, basePath string) (*Recorder, error) {
	// Generate unique recording ID
	id := fmt.Sprintf("TG-%d", time.Now().UnixNano()/int64(time.Millisecond))

	// Create recording directory
	recordingDir := filepath.Join(basePath, id)
	if err := os.MkdirAll(recordingDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create recording directory: %w", err)
	}

	// Create diffs subdirectory
	diffsDir := filepath.Join(recordingDir, "diffs")
	if err := os.MkdirAll(diffsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create diffs directory: %w", err)
	}

	// Create stream file
	streamPath := filepath.Join(recordingDir, "stream.jsonl")
	streamFile, err := os.Create(streamPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create stream file: %w", err)
	}

	r := &Recorder{
		id:          id,
		taskID:      taskID,
		projectPath: projectPath,
		basePath:    basePath,
		streamFile:  streamFile,
		diffFiles:   make(map[string]*FileDiff),
		log:         logging.WithComponent("recorder"),
		recording: &Recording{
			ID:          id,
			TaskID:      taskID,
			ProjectPath: projectPath,
			StartTime:   time.Now(),
			StreamPath:  streamPath,
			DiffsPath:   diffsDir,
			SummaryPath: filepath.Join(recordingDir, "summary.md"),
			Metadata: &Metadata{
				Tags: make(map[string]string),
			},
			TokenUsage:   &TokenUsage{},
			PhaseTimings: make([]PhaseTiming, 0),
		},
	}

	r.log.Info("Recording started",
		slog.String("id", id),
		slog.String("task_id", taskID),
		slog.String("path", recordingDir),
	)

	return r, nil
}

// RecordEvent records a raw stream event
func (r *Recorder) RecordEvent(rawJSON string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.sequence++
	event := StreamEvent{
		Timestamp: time.Now(),
		Sequence:  r.sequence,
		Raw:       rawJSON,
	}

	// Parse the event type and extract useful info
	parsed := r.parseEvent(rawJSON)
	if parsed != nil {
		event.Type = parsed.Type
		event.Parsed = parsed

		// Track token usage
		if parsed.InputTokens > 0 || parsed.OutputTokens > 0 {
			r.recording.TokenUsage.InputTokens += parsed.InputTokens
			r.recording.TokenUsage.OutputTokens += parsed.OutputTokens
			r.recording.TokenUsage.TotalTokens = r.recording.TokenUsage.InputTokens + r.recording.TokenUsage.OutputTokens
		}

		// Track phase changes
		if phase := r.detectPhase(parsed); phase != "" && phase != r.currentPhase {
			r.recordPhaseEnd()
			r.currentPhase = phase
			r.phaseStart = time.Now()
		}

		// Track file operations
		if parsed.FilePath != "" && parsed.FileOperation != "" {
			r.trackFileChange(parsed)
		}
	}

	// Write to stream file
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	if _, err := r.streamFile.WriteString(string(eventJSON) + "\n"); err != nil {
		return fmt.Errorf("failed to write event: %w", err)
	}

	r.recording.EventCount = r.sequence
	return nil
}

// parseEvent extracts structured data from a raw stream event
func (r *Recorder) parseEvent(rawJSON string) *ParsedEvent {
	var raw map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &raw); err != nil {
		return nil
	}

	parsed := &ParsedEvent{}

	// Get event type
	if t, ok := raw["type"].(string); ok {
		parsed.Type = t
	}
	if st, ok := raw["subtype"].(string); ok {
		parsed.Subtype = st
	}

	// Handle result events
	if parsed.Type == "result" {
		if result, ok := raw["result"].(string); ok {
			parsed.Result = result
		}
		if isErr, ok := raw["is_error"].(bool); ok {
			parsed.IsError = isErr
		}
		// Extract usage from result
		if usage, ok := raw["usage"].(map[string]any); ok {
			if in, ok := usage["input_tokens"].(float64); ok {
				parsed.InputTokens = int64(in)
			}
			if out, ok := usage["output_tokens"].(float64); ok {
				parsed.OutputTokens = int64(out)
			}
		}
	}

	// Handle assistant events (tool calls, text)
	if parsed.Type == "assistant" {
		if msg, ok := raw["message"].(map[string]any); ok {
			if content, ok := msg["content"].([]any); ok {
				for _, block := range content {
					if b, ok := block.(map[string]any); ok {
						blockType, _ := b["type"].(string)
						switch blockType {
						case "tool_use":
							parsed.ToolName, _ = b["name"].(string)
							if input, ok := b["input"].(map[string]any); ok {
								parsed.ToolInput = input
								// Extract file path for file operations
								if fp, ok := input["file_path"].(string); ok {
									parsed.FilePath = fp
									parsed.FileOperation = r.detectFileOp(parsed.ToolName)
								}
							}
						case "text":
							if text, ok := b["text"].(string); ok {
								parsed.Text = text
							}
						}
					}
				}
			}
		}
	}

	return parsed
}

// detectFileOp determines the file operation from tool name
func (r *Recorder) detectFileOp(toolName string) string {
	switch toolName {
	case "Read":
		return "read"
	case "Write":
		return "create"
	case "Edit":
		return "modify"
	default:
		return ""
	}
}

// detectPhase determines execution phase from event
func (r *Recorder) detectPhase(parsed *ParsedEvent) string {
	// From tool usage
	switch parsed.ToolName {
	case "Read", "Glob", "Grep":
		return "Exploring"
	case "Write", "Edit":
		return "Implementing"
	case "Bash":
		if cmd, ok := parsed.ToolInput["command"].(string); ok {
			cmdLower := strings.ToLower(cmd)
			if strings.Contains(cmdLower, "git commit") {
				return "Committing"
			}
			if strings.Contains(cmdLower, "test") {
				return "Testing"
			}
		}
		return ""
	}

	// From text patterns (Navigator phases)
	if parsed.Text != "" {
		text := parsed.Text
		if strings.Contains(text, "PHASE:") || strings.Contains(text, "Phase:") {
			if strings.Contains(text, "RESEARCH") || strings.Contains(text, "Research") {
				return "Research"
			}
			if strings.Contains(text, "IMPL") || strings.Contains(text, "Implement") {
				return "Implementing"
			}
			if strings.Contains(text, "VERIFY") || strings.Contains(text, "Verify") {
				return "Verifying"
			}
			if strings.Contains(text, "COMPLETE") || strings.Contains(text, "Complete") {
				return "Completing"
			}
		}
	}

	return ""
}

// recordPhaseEnd records the end of a phase timing
func (r *Recorder) recordPhaseEnd() {
	if r.currentPhase != "" && !r.phaseStart.IsZero() {
		timing := PhaseTiming{
			Phase:    r.currentPhase,
			Start:    r.phaseStart,
			End:      time.Now(),
			Duration: time.Since(r.phaseStart),
		}
		r.recording.PhaseTimings = append(r.recording.PhaseTimings, timing)
	}
}

// trackFileChange tracks file modifications
func (r *Recorder) trackFileChange(parsed *ParsedEvent) {
	// For now, just track that the file was touched
	// Full diff tracking would require before/after content
	if _, exists := r.diffFiles[parsed.FilePath]; !exists {
		r.diffFiles[parsed.FilePath] = &FileDiff{
			Timestamp: time.Now(),
			FilePath:  parsed.FilePath,
			Operation: parsed.FileOperation,
		}
	}
}

// SetMetadata sets recording metadata
func (r *Recorder) SetMetadata(key, value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recording.Metadata.Tags[key] = value
}

// SetBranch sets the branch metadata
func (r *Recorder) SetBranch(branch string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recording.Metadata.Branch = branch
}

// SetCommitSHA sets the commit SHA metadata
func (r *Recorder) SetCommitSHA(sha string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recording.Metadata.CommitSHA = sha
}

// SetPRUrl sets the PR URL metadata
func (r *Recorder) SetPRUrl(url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recording.Metadata.PRUrl = url
}

// SetNavigator sets the Navigator flag
func (r *Recorder) SetNavigator(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recording.Metadata.HasNavigator = enabled
}

// SetModel sets the model name
func (r *Recorder) SetModel(model string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recording.Metadata.ModelName = model
}

// Finish completes the recording
func (r *Recorder) Finish(status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Record final phase
	r.recordPhaseEnd()

	// Update recording metadata
	r.recording.EndTime = time.Now()
	r.recording.Duration = r.recording.EndTime.Sub(r.recording.StartTime)
	r.recording.Status = status

	// Calculate estimated cost
	r.recording.TokenUsage.EstimatedCostUSD = r.estimateCost()

	// Close stream file
	if err := r.streamFile.Close(); err != nil {
		r.log.Error("Failed to close stream file", slog.Any("error", err))
	}

	// Save metadata
	if err := r.saveMetadata(); err != nil {
		return fmt.Errorf("failed to save metadata: %w", err)
	}

	// Save diffs
	if err := r.saveDiffs(); err != nil {
		r.log.Warn("Failed to save diffs", slog.Any("error", err))
	}

	// Generate summary
	if err := r.generateSummary(); err != nil {
		r.log.Warn("Failed to generate summary", slog.Any("error", err))
	}

	r.log.Info("Recording finished",
		slog.String("id", r.id),
		slog.String("status", status),
		slog.Duration("duration", r.recording.Duration),
		slog.Int("events", r.recording.EventCount),
	)

	return nil
}

// saveMetadata saves the recording metadata to JSON
func (r *Recorder) saveMetadata() error {
	metadataPath := filepath.Join(r.basePath, r.id, "metadata.json")
	data, err := json.MarshalIndent(r.recording, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(metadataPath, data, 0644)
}

// saveDiffs saves file diffs
func (r *Recorder) saveDiffs() error {
	if len(r.diffFiles) == 0 {
		return nil
	}

	diffsPath := filepath.Join(r.recording.DiffsPath, "changes.json")
	diffs := make([]*FileDiff, 0, len(r.diffFiles))
	for _, d := range r.diffFiles {
		diffs = append(diffs, d)
	}

	data, err := json.MarshalIndent(diffs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(diffsPath, data, 0644)
}

// generateSummary creates a human-readable summary
func (r *Recorder) generateSummary() error {
	var sb strings.Builder

	sb.WriteString("# Execution Recording Summary\n\n")
	sb.WriteString(fmt.Sprintf("**Recording ID**: %s\n", r.recording.ID))
	sb.WriteString(fmt.Sprintf("**Task ID**: %s\n", r.recording.TaskID))
	sb.WriteString(fmt.Sprintf("**Status**: %s\n", r.recording.Status))
	sb.WriteString(fmt.Sprintf("**Duration**: %s\n", r.recording.Duration.Round(time.Second)))
	sb.WriteString(fmt.Sprintf("**Events**: %d\n", r.recording.EventCount))
	sb.WriteString("\n")

	// Metadata
	if r.recording.Metadata != nil {
		sb.WriteString("## Metadata\n\n")
		if r.recording.Metadata.Branch != "" {
			sb.WriteString(fmt.Sprintf("- **Branch**: %s\n", r.recording.Metadata.Branch))
		}
		if r.recording.Metadata.CommitSHA != "" {
			sb.WriteString(fmt.Sprintf("- **Commit**: %s\n", r.recording.Metadata.CommitSHA))
		}
		if r.recording.Metadata.PRUrl != "" {
			sb.WriteString(fmt.Sprintf("- **PR**: %s\n", r.recording.Metadata.PRUrl))
		}
		if r.recording.Metadata.ModelName != "" {
			sb.WriteString(fmt.Sprintf("- **Model**: %s\n", r.recording.Metadata.ModelName))
		}
		sb.WriteString(fmt.Sprintf("- **Navigator**: %v\n", r.recording.Metadata.HasNavigator))
		sb.WriteString("\n")
	}

	// Token usage
	if r.recording.TokenUsage != nil {
		sb.WriteString("## Token Usage\n\n")
		sb.WriteString(fmt.Sprintf("- **Input**: %d tokens\n", r.recording.TokenUsage.InputTokens))
		sb.WriteString(fmt.Sprintf("- **Output**: %d tokens\n", r.recording.TokenUsage.OutputTokens))
		sb.WriteString(fmt.Sprintf("- **Total**: %d tokens\n", r.recording.TokenUsage.TotalTokens))
		sb.WriteString(fmt.Sprintf("- **Estimated Cost**: $%.4f\n", r.recording.TokenUsage.EstimatedCostUSD))
		sb.WriteString("\n")
	}

	// Phase timings
	if len(r.recording.PhaseTimings) > 0 {
		sb.WriteString("## Phase Timings\n\n")
		for _, pt := range r.recording.PhaseTimings {
			pct := float64(pt.Duration) / float64(r.recording.Duration) * 100
			sb.WriteString(fmt.Sprintf("- **%s**: %s (%.1f%%)\n", pt.Phase, pt.Duration.Round(time.Second), pct))
		}
		sb.WriteString("\n")
	}

	// Files changed
	if len(r.diffFiles) > 0 {
		sb.WriteString("## Files Changed\n\n")
		for fp, diff := range r.diffFiles {
			sb.WriteString(fmt.Sprintf("- `%s` (%s)\n", fp, diff.Operation))
		}
		sb.WriteString("\n")
	}

	return os.WriteFile(r.recording.SummaryPath, []byte(sb.String()), 0644)
}

// estimateCost calculates estimated cost from token usage
func (r *Recorder) estimateCost() float64 {
	// Pricing per 1M tokens (Sonnet defaults)
	const (
		inputPrice  = 3.00
		outputPrice = 15.00
	)

	model := r.recording.Metadata.ModelName
	var inPrice, outPrice float64

	if strings.Contains(strings.ToLower(model), "opus") {
		inPrice = 15.00
		outPrice = 75.00
	} else {
		inPrice = inputPrice
		outPrice = outputPrice
	}

	inputCost := float64(r.recording.TokenUsage.InputTokens) * inPrice / 1_000_000
	outputCost := float64(r.recording.TokenUsage.OutputTokens) * outPrice / 1_000_000
	return inputCost + outputCost
}

// GetRecordingID returns the recording ID
func (r *Recorder) GetRecordingID() string {
	return r.id
}

// GetRecording returns the recording metadata
func (r *Recorder) GetRecording() *Recording {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recording
}

// DefaultRecordingsPath returns the default recordings directory
func DefaultRecordingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pilot", "recordings")
}

// ListRecordings lists all recordings matching the filter
func ListRecordings(basePath string, filter *RecordingFilter) ([]*RecordingSummary, error) {
	entries, err := os.ReadDir(basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []*RecordingSummary{}, nil
		}
		return nil, fmt.Errorf("failed to read recordings directory: %w", err)
	}

	var summaries []*RecordingSummary

	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "TG-") {
			continue
		}

		// Load metadata
		metadataPath := filepath.Join(basePath, entry.Name(), "metadata.json")
		data, err := os.ReadFile(metadataPath)
		if err != nil {
			continue // Skip invalid recordings
		}

		var recording Recording
		if err := json.Unmarshal(data, &recording); err != nil {
			continue
		}

		// Apply filters
		if filter != nil {
			if filter.ProjectPath != "" && recording.ProjectPath != filter.ProjectPath {
				continue
			}
			if filter.Status != "" && recording.Status != filter.Status {
				continue
			}
			if !filter.Since.IsZero() && recording.StartTime.Before(filter.Since) {
				continue
			}
		}

		summaries = append(summaries, &RecordingSummary{
			ID:          recording.ID,
			TaskID:      recording.TaskID,
			ProjectPath: recording.ProjectPath,
			Status:      recording.Status,
			StartTime:   recording.StartTime,
			Duration:    recording.Duration,
			EventCount:  recording.EventCount,
		})
	}

	// Apply limit
	if filter != nil && filter.Limit > 0 && len(summaries) > filter.Limit {
		summaries = summaries[:filter.Limit]
	}

	return summaries, nil
}

// LoadRecording loads a recording by ID
func LoadRecording(basePath, id string) (*Recording, error) {
	metadataPath := filepath.Join(basePath, id, "metadata.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read recording: %w", err)
	}

	var recording Recording
	if err := json.Unmarshal(data, &recording); err != nil {
		return nil, fmt.Errorf("failed to parse recording: %w", err)
	}

	return &recording, nil
}

// LoadStreamEvents loads stream events from a recording
func LoadStreamEvents(recording *Recording) ([]*StreamEvent, error) {
	file, err := os.Open(recording.StreamPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open stream file: %w", err)
	}
	defer func() { _ = file.Close() }()

	var events []*StreamEvent
	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		var event StreamEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue // Skip malformed events
		}
		events = append(events, &event)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read stream: %w", err)
	}

	return events, nil
}

// DeleteRecording deletes a recording
func DeleteRecording(basePath, id string) error {
	recordingDir := filepath.Join(basePath, id)
	return os.RemoveAll(recordingDir)
}
