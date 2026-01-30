package azuredevops

import (
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if config.Enabled {
		t.Error("expected Enabled to be false by default")
	}

	if config.BaseURL != "https://dev.azure.com" {
		t.Errorf("expected BaseURL 'https://dev.azure.com', got '%s'", config.BaseURL)
	}

	if config.PilotTag != "pilot" {
		t.Errorf("expected PilotTag 'pilot', got '%s'", config.PilotTag)
	}

	if len(config.WorkItemTypes) != 3 {
		t.Errorf("expected 3 work item types, got %d", len(config.WorkItemTypes))
	}

	if config.Polling == nil {
		t.Fatal("expected Polling to be non-nil")
	}
	if config.Polling.Enabled {
		t.Error("expected Polling.Enabled to be false by default")
	}
	if config.Polling.Interval != 30*time.Second {
		t.Errorf("expected Polling.Interval 30s, got %v", config.Polling.Interval)
	}

	if config.StaleLabelCleanup == nil {
		t.Fatal("expected StaleLabelCleanup to be non-nil")
	}
	if !config.StaleLabelCleanup.Enabled {
		t.Error("expected StaleLabelCleanup.Enabled to be true by default")
	}
}

func TestPriorityFromValue(t *testing.T) {
	tests := []struct {
		value    int
		expected Priority
	}{
		{1, PriorityUrgent},
		{2, PriorityHigh},
		{3, PriorityMedium},
		{4, PriorityLow},
		{0, PriorityNone},
		{5, PriorityNone},
		{-1, PriorityNone},
	}

	for _, tt := range tests {
		result := PriorityFromValue(tt.value)
		if result != tt.expected {
			t.Errorf("PriorityFromValue(%d) = %d, expected %d", tt.value, result, tt.expected)
		}
	}
}

func TestPriorityName(t *testing.T) {
	tests := []struct {
		priority Priority
		expected string
	}{
		{PriorityUrgent, "Urgent"},
		{PriorityHigh, "High"},
		{PriorityMedium, "Medium"},
		{PriorityLow, "Low"},
		{PriorityNone, "No Priority"},
	}

	for _, tt := range tests {
		result := PriorityName(tt.priority)
		if result != tt.expected {
			t.Errorf("PriorityName(%d) = '%s', expected '%s'", tt.priority, result, tt.expected)
		}
	}
}

func TestWorkItemMethods(t *testing.T) {
	wi := &WorkItem{
		ID:  42,
		Rev: 3,
		Fields: map[string]interface{}{
			"System.Title":                  "Test Work Item",
			"System.Description":            "<p>HTML Description</p>",
			"System.State":                  StateActive,
			"System.WorkItemType":           "Bug",
			"System.Tags":                   "tag1; tag2",
			"Microsoft.VSTS.Common.Priority": float64(1),
			"System.CreatedDate":            "2024-06-15T10:00:00Z",
			"System.ChangedDate":            "2024-06-16T12:00:00Z",
		},
	}

	if wi.GetTitle() != "Test Work Item" {
		t.Errorf("GetTitle() = '%s', expected 'Test Work Item'", wi.GetTitle())
	}

	if wi.GetState() != StateActive {
		t.Errorf("GetState() = '%s', expected '%s'", wi.GetState(), StateActive)
	}

	if wi.GetWorkItemType() != "Bug" {
		t.Errorf("GetWorkItemType() = '%s', expected 'Bug'", wi.GetWorkItemType())
	}

	tags := wi.GetTags()
	if len(tags) != 2 {
		t.Errorf("GetTags() returned %d tags, expected 2", len(tags))
	}

	if !wi.HasTag("tag1") {
		t.Error("HasTag('tag1') = false, expected true")
	}
	if wi.HasTag("nonexistent") {
		t.Error("HasTag('nonexistent') = true, expected false")
	}

	if wi.GetPriority() != PriorityUrgent {
		t.Errorf("GetPriority() = %d, expected %d", wi.GetPriority(), PriorityUrgent)
	}

	created := wi.GetCreatedDate()
	if created.Year() != 2024 || created.Month() != 6 || created.Day() != 15 {
		t.Errorf("GetCreatedDate() = %v, unexpected value", created)
	}

	changed := wi.GetChangedDate()
	if changed.Year() != 2024 || changed.Month() != 6 || changed.Day() != 16 {
		t.Errorf("GetChangedDate() = %v, unexpected value", changed)
	}
}

func TestWorkItemEmptyFields(t *testing.T) {
	wi := &WorkItem{
		ID:     1,
		Fields: map[string]interface{}{},
	}

	if wi.GetTitle() != "" {
		t.Errorf("GetTitle() on empty fields = '%s', expected empty", wi.GetTitle())
	}

	if wi.GetDescription() != "" {
		t.Errorf("GetDescription() on empty fields = '%s', expected empty", wi.GetDescription())
	}

	if wi.GetState() != "" {
		t.Errorf("GetState() on empty fields = '%s', expected empty", wi.GetState())
	}

	if wi.GetWorkItemType() != "" {
		t.Errorf("GetWorkItemType() on empty fields = '%s', expected empty", wi.GetWorkItemType())
	}

	if wi.GetTags() != nil {
		t.Errorf("GetTags() on empty fields = %v, expected nil", wi.GetTags())
	}

	if wi.GetPriority() != PriorityNone {
		t.Errorf("GetPriority() on empty fields = %d, expected %d", wi.GetPriority(), PriorityNone)
	}

	if !wi.GetCreatedDate().IsZero() {
		t.Error("GetCreatedDate() on empty fields should be zero")
	}
}

func TestWorkItemReproStepsDescription(t *testing.T) {
	// Some work item types use Microsoft.VSTS.TCM.ReproSteps instead of System.Description
	wi := &WorkItem{
		ID: 1,
		Fields: map[string]interface{}{
			"Microsoft.VSTS.TCM.ReproSteps": "Repro steps description",
		},
	}

	desc := wi.GetDescription()
	if desc != "Repro steps description" {
		t.Errorf("GetDescription() from ReproSteps = '%s', expected 'Repro steps description'", desc)
	}
}
