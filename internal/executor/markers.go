package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ContextMarker represents a saved session checkpoint
type ContextMarker struct {
	Name        string
	Description string
	CreatedAt   time.Time
	FilePath    string

	// Session state
	TaskID        string
	FilesModified []string
	Commits       []string
	CurrentFocus  string
}

// CreateMarker saves a context marker to .agent/.context-markers/
func CreateMarker(agentPath string, marker *ContextMarker) error {
	markersDir := filepath.Join(agentPath, ".context-markers")
	if err := os.MkdirAll(markersDir, 0755); err != nil {
		return err
	}

	// Generate filename: YYYY-MM-DD-HHMM_description.md
	timestamp := time.Now().Format("2006-01-02-1504")
	safeName := sanitizeFilename(marker.Description)
	filename := fmt.Sprintf("%s_%s.md", timestamp, safeName)

	marker.FilePath = filepath.Join(markersDir, filename)
	marker.CreatedAt = time.Now()

	content := formatMarker(marker)
	if err := os.WriteFile(marker.FilePath, []byte(content), 0644); err != nil {
		return err
	}

	// Update .active file
	return SetActiveMarker(agentPath, marker.FilePath)
}

// SetActiveMarker updates the .active file to point to current marker
func SetActiveMarker(agentPath, markerPath string) error {
	activePath := filepath.Join(agentPath, ".context-markers", ".active")
	return os.WriteFile(activePath, []byte(markerPath), 0644)
}

// GetActiveMarker returns the currently active marker path
func GetActiveMarker(agentPath string) (string, error) {
	activePath := filepath.Join(agentPath, ".context-markers", ".active")
	data, err := os.ReadFile(activePath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// LoadMarker reads and parses a marker file
func LoadMarker(path string) (*ContextMarker, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseMarker(string(data), path)
}

// ListMarkers returns all markers sorted by date (newest first)
func ListMarkers(agentPath string) ([]*ContextMarker, error) {
	markersDir := filepath.Join(agentPath, ".context-markers")
	entries, err := os.ReadDir(markersDir)
	if err != nil {
		return nil, err
	}

	var markers []*ContextMarker
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(markersDir, entry.Name())
		marker, err := LoadMarker(path)
		if err != nil {
			continue // skip invalid markers
		}
		markers = append(markers, marker)
	}

	sort.Slice(markers, func(i, j int) bool {
		return markers[i].CreatedAt.After(markers[j].CreatedAt)
	})

	return markers, nil
}

// CleanupOldMarkers removes markers older than retention period
func CleanupOldMarkers(agentPath string, retentionDays int) error {
	markers, err := ListMarkers(agentPath)
	if err != nil {
		return err
	}

	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	for _, m := range markers {
		if m.CreatedAt.Before(cutoff) {
			os.Remove(m.FilePath)
		}
	}
	return nil
}

func formatMarker(m *ContextMarker) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Context Marker: %s\n\n", m.Description))
	sb.WriteString(fmt.Sprintf("**Created:** %s\n\n", m.CreatedAt.Format("2006-01-02 15:04")))

	if m.TaskID != "" {
		sb.WriteString(fmt.Sprintf("**Task:** %s\n\n", m.TaskID))
	}

	sb.WriteString("## Current Focus\n\n")
	sb.WriteString(m.CurrentFocus)
	sb.WriteString("\n\n")

	if len(m.FilesModified) > 0 {
		sb.WriteString("## Files Modified\n\n")
		for _, f := range m.FilesModified {
			sb.WriteString(fmt.Sprintf("- %s\n", f))
		}
		sb.WriteString("\n")
	}

	if len(m.Commits) > 0 {
		sb.WriteString("## Commits\n\n")
		for _, c := range m.Commits {
			sb.WriteString(fmt.Sprintf("- %s\n", c))
		}
	}

	return sb.String()
}

func parseMarker(content, path string) (*ContextMarker, error) {
	marker := &ContextMarker{FilePath: path}

	lines := strings.Split(content, "\n")
	for i, line := range lines {
		// Parse title: # Context Marker: <description>
		if strings.HasPrefix(line, "# Context Marker:") {
			marker.Description = strings.TrimSpace(strings.TrimPrefix(line, "# Context Marker:"))
			continue
		}

		// Parse created date: **Created:** YYYY-MM-DD HH:MM
		if strings.HasPrefix(line, "**Created:**") {
			dateStr := strings.TrimSpace(strings.TrimPrefix(line, "**Created:**"))
			if t, err := time.Parse("2006-01-02 15:04", dateStr); err == nil {
				marker.CreatedAt = t
			}
			continue
		}

		// Parse task: **Task:** <task-id>
		if strings.HasPrefix(line, "**Task:**") {
			marker.TaskID = strings.TrimSpace(strings.TrimPrefix(line, "**Task:**"))
			continue
		}

		// Parse current focus section
		if strings.TrimSpace(line) == "## Current Focus" {
			// Read until next section or end
			var focusLines []string
			for j := i + 1; j < len(lines); j++ {
				if strings.HasPrefix(lines[j], "##") {
					break
				}
				if strings.TrimSpace(lines[j]) != "" {
					focusLines = append(focusLines, lines[j])
				}
			}
			marker.CurrentFocus = strings.Join(focusLines, "\n")
			continue
		}

		// Parse files modified section
		if strings.TrimSpace(line) == "## Files Modified" {
			for j := i + 1; j < len(lines); j++ {
				if strings.HasPrefix(lines[j], "##") {
					break
				}
				if strings.HasPrefix(lines[j], "- ") {
					marker.FilesModified = append(marker.FilesModified, strings.TrimPrefix(lines[j], "- "))
				}
			}
			continue
		}

		// Parse commits section
		if strings.TrimSpace(line) == "## Commits" {
			for j := i + 1; j < len(lines); j++ {
				if strings.HasPrefix(lines[j], "##") {
					break
				}
				if strings.HasPrefix(lines[j], "- ") {
					marker.Commits = append(marker.Commits, strings.TrimPrefix(lines[j], "- "))
				}
			}
			continue
		}
	}

	// Extract name from filename if not set
	if marker.Name == "" {
		marker.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	}

	return marker, nil
}

// Note: sanitizeFilename is defined in docs.go and shared across the package
