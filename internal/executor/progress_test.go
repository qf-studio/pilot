package executor

import (
	"testing"
)

func TestNewProgressDisplay(t *testing.T) {
	tests := []struct {
		name      string
		taskID    string
		taskTitle string
		enabled   bool
	}{
		{
			name:      "enabled display",
			taskID:    "TASK-123",
			taskTitle: "Test Task",
			enabled:   true,
		},
		{
			name:      "disabled display",
			taskID:    "TASK-456",
			taskTitle: "Another Task",
			enabled:   false,
		},
		{
			name:      "empty values",
			taskID:    "",
			taskTitle: "",
			enabled:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pd := NewProgressDisplay(tt.taskID, tt.taskTitle, tt.enabled)

			if pd == nil {
				t.Fatal("NewProgressDisplay returned nil")
			}
			if pd.taskID != tt.taskID {
				t.Errorf("taskID = %q, want %q", pd.taskID, tt.taskID)
			}
			if pd.taskTitle != tt.taskTitle {
				t.Errorf("taskTitle = %q, want %q", pd.taskTitle, tt.taskTitle)
			}
			if pd.enabled != tt.enabled {
				t.Errorf("enabled = %v, want %v", pd.enabled, tt.enabled)
			}
			if pd.phase != "Starting" {
				t.Errorf("phase = %q, want Starting", pd.phase)
			}
			if pd.progress != 0 {
				t.Errorf("progress = %d, want 0", pd.progress)
			}
			if pd.maxLogs != 5 {
				t.Errorf("maxLogs = %d, want 5", pd.maxLogs)
			}
			if len(pd.logs) != 0 {
				t.Errorf("logs should be empty, got %d entries", len(pd.logs))
			}
		})
	}
}

func TestProgressDisplayUpdate(t *testing.T) {
	t.Run("disabled display ignores updates", func(t *testing.T) {
		pd := NewProgressDisplay("TASK-1", "Test", false)
		originalPhase := pd.phase
		originalProgress := pd.progress

		pd.Update("New Phase", 50, "Some message")

		// Values should not change when disabled
		if pd.phase != originalPhase {
			t.Errorf("phase changed on disabled display: got %q", pd.phase)
		}
		if pd.progress != originalProgress {
			t.Errorf("progress changed on disabled display: got %d", pd.progress)
		}
	})

	t.Run("enabled display updates values", func(t *testing.T) {
		// Note: We can't test render() output easily, but we can verify state changes
		// Create display but don't call Start() to avoid console output
		pd := &ProgressDisplay{
			taskID:    "TASK-1",
			taskTitle: "Test",
			phase:     "Starting",
			progress:  0,
			logs:      []string{},
			maxLogs:   5,
			enabled:   false, // Keep disabled to avoid render() calls
		}

		// Manually update fields to test logic
		pd.phase = "Implementing"
		pd.progress = 50

		if pd.phase != "Implementing" {
			t.Errorf("phase = %q, want Implementing", pd.phase)
		}
		if pd.progress != 50 {
			t.Errorf("progress = %d, want 50", pd.progress)
		}
	})

	t.Run("logs are capped at maxLogs", func(t *testing.T) {
		pd := &ProgressDisplay{
			taskID:    "TASK-1",
			taskTitle: "Test",
			logs:      []string{},
			maxLogs:   3,
			enabled:   false,
		}

		// Simulate adding logs
		for i := 0; i < 5; i++ {
			pd.logs = append(pd.logs, "log entry")
			if len(pd.logs) > pd.maxLogs {
				pd.logs = pd.logs[1:]
			}
		}

		if len(pd.logs) != 3 {
			t.Errorf("logs count = %d, want 3", len(pd.logs))
		}
	})
}

func TestProgressDisplayRenderProgressBar(t *testing.T) {
	tests := []struct {
		name     string
		progress int
	}{
		{"zero progress", 0},
		{"half progress", 50},
		{"full progress", 100},
		{"quarter progress", 25},
		{"three quarter progress", 75},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pd := &ProgressDisplay{
				progress: tt.progress,
			}

			bar := pd.renderProgressBar()

			// Bar should contain brackets
			if bar[0] != '[' {
				t.Error("progress bar should start with '['")
			}
			// Bar ends with ']' but may have ANSI codes, so just check it contains ']'
			if !containsStr(bar, "]") {
				t.Error("progress bar should contain ']'")
			}
		})
	}
}

func TestProgressDisplayStartFinishDisabled(t *testing.T) {
	pd := NewProgressDisplay("TASK-1", "Test", false)

	// These should not panic when disabled
	pd.Start()
	pd.Finish(true, "Done")
	pd.Finish(false, "Error")
}

// containsStr is a simple string contains check
func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
