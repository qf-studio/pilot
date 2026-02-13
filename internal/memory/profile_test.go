package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewProfileManager(t *testing.T) {
	pm := NewProfileManager("/global/path", "/project/path")

	if pm.globalPath != "/global/path" {
		t.Errorf("globalPath = %q, want %q", pm.globalPath, "/global/path")
	}

	if pm.projectPath != "/project/path" {
		t.Errorf("projectPath = %q, want %q", pm.projectPath, "/project/path")
	}
}

func TestProfileManager_LoadEmpty(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "profile-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	pm := NewProfileManager(
		filepath.Join(tmpDir, "global.json"),
		filepath.Join(tmpDir, "project.json"),
	)

	profile, err := pm.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if profile == nil {
		t.Fatal("Load() returned nil profile")
	}

	if profile.Conventions == nil {
		t.Error("Load() returned profile with nil Conventions map")
	}

	if profile.Verbosity != "" {
		t.Errorf("Verbosity = %q, want empty", profile.Verbosity)
	}
}

func TestProfileManager_SaveAndLoad(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "profile-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	pm := NewProfileManager(
		filepath.Join(tmpDir, "global.json"),
		filepath.Join(tmpDir, "project.json"),
	)

	profile := &UserProfile{
		Verbosity:    "concise",
		Frameworks:   []string{"gin", "gorm"},
		Conventions:  map[string]string{"indent": "tabs", "naming": "camelCase"},
		CodePatterns: []string{"early_returns", "guard_clauses"},
		Corrections: []Correction{
			{Pattern: "use fmt.Printf", Correction: "use slog instead", Count: 3},
		},
	}

	// Save to global
	if err := pm.Save(profile, true); err != nil {
		t.Fatalf("Save(global) error = %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(filepath.Join(tmpDir, "global.json")); os.IsNotExist(err) {
		t.Error("global.json was not created")
	}

	// Load and verify
	loaded, err := pm.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if loaded.Verbosity != "concise" {
		t.Errorf("Verbosity = %q, want %q", loaded.Verbosity, "concise")
	}

	if len(loaded.Frameworks) != 2 {
		t.Errorf("len(Frameworks) = %d, want 2", len(loaded.Frameworks))
	}

	if loaded.Conventions["indent"] != "tabs" {
		t.Errorf("Conventions[indent] = %q, want %q", loaded.Conventions["indent"], "tabs")
	}

	if len(loaded.Corrections) != 1 {
		t.Errorf("len(Corrections) = %d, want 1", len(loaded.Corrections))
	}

	if loaded.Corrections[0].Count != 3 {
		t.Errorf("Corrections[0].Count = %d, want 3", loaded.Corrections[0].Count)
	}
}

func TestProfileManager_MergeProfiles(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "profile-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	globalPath := filepath.Join(tmpDir, "global.json")
	projectPath := filepath.Join(tmpDir, "project.json")
	pm := NewProfileManager(globalPath, projectPath)

	// Create global profile
	globalProfile := &UserProfile{
		Verbosity:    "detailed",
		Frameworks:   []string{"gin"},
		Conventions:  map[string]string{"indent": "spaces"},
		CodePatterns: []string{"early_returns"},
	}
	if err := pm.Save(globalProfile, true); err != nil {
		t.Fatalf("Save(global) error = %v", err)
	}

	// Create project profile (overrides)
	projectProfile := &UserProfile{
		Verbosity:    "concise",
		Frameworks:   []string{"echo"}, // Different framework
		Conventions:  map[string]string{"indent": "tabs", "test": "value"},
		CodePatterns: []string{"guard_clauses"},
	}
	if err := pm.Save(projectProfile, false); err != nil {
		t.Fatalf("Save(project) error = %v", err)
	}

	// Load merged profile
	merged, err := pm.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Verbosity should be overridden by project
	if merged.Verbosity != "concise" {
		t.Errorf("Verbosity = %q, want %q (project override)", merged.Verbosity, "concise")
	}

	// Frameworks should be merged (deduplicated)
	if len(merged.Frameworks) != 2 {
		t.Errorf("len(Frameworks) = %d, want 2 (merged)", len(merged.Frameworks))
	}

	// Conventions should be merged with project taking precedence
	if merged.Conventions["indent"] != "tabs" {
		t.Errorf("Conventions[indent] = %q, want %q (project override)", merged.Conventions["indent"], "tabs")
	}
	if merged.Conventions["test"] != "value" {
		t.Errorf("Conventions[test] = %q, want %q", merged.Conventions["test"], "value")
	}

	// CodePatterns should be merged
	if len(merged.CodePatterns) != 2 {
		t.Errorf("len(CodePatterns) = %d, want 2 (merged)", len(merged.CodePatterns))
	}
}

func TestUserProfile_RecordCorrection(t *testing.T) {
	profile := &UserProfile{
		Conventions: make(map[string]string),
	}

	// First correction
	profile.RecordCorrection("use println", "use slog.Info")

	if len(profile.Corrections) != 1 {
		t.Fatalf("len(Corrections) = %d, want 1", len(profile.Corrections))
	}

	if profile.Corrections[0].Count != 1 {
		t.Errorf("Count = %d, want 1", profile.Corrections[0].Count)
	}

	// Same pattern again - should increment count
	profile.RecordCorrection("use println", "use structured logging")

	if len(profile.Corrections) != 1 {
		t.Errorf("len(Corrections) = %d, want 1 (same pattern)", len(profile.Corrections))
	}

	if profile.Corrections[0].Count != 2 {
		t.Errorf("Count = %d, want 2", profile.Corrections[0].Count)
	}

	if profile.Corrections[0].Correction != "use structured logging" {
		t.Errorf("Correction = %q, want updated value", profile.Corrections[0].Correction)
	}

	// Different pattern - should add new correction
	profile.RecordCorrection("use fmt.Errorf", "use errors.New")

	if len(profile.Corrections) != 2 {
		t.Errorf("len(Corrections) = %d, want 2", len(profile.Corrections))
	}
}

func TestUserProfile_SetGetPreference(t *testing.T) {
	profile := &UserProfile{
		Conventions: make(map[string]string),
	}

	// Get non-existent preference
	if got := profile.GetPreference("nonexistent"); got != "" {
		t.Errorf("GetPreference(nonexistent) = %q, want empty", got)
	}

	// Set and get preference
	profile.SetPreference("indent", "tabs")

	if got := profile.GetPreference("indent"); got != "tabs" {
		t.Errorf("GetPreference(indent) = %q, want %q", got, "tabs")
	}

	// Override preference
	profile.SetPreference("indent", "spaces")

	if got := profile.GetPreference("indent"); got != "spaces" {
		t.Errorf("GetPreference(indent) after override = %q, want %q", got, "spaces")
	}
}

func TestUserProfile_SetPreferenceNilMap(t *testing.T) {
	profile := &UserProfile{}

	// Should handle nil Conventions map
	profile.SetPreference("key", "value")

	if profile.Conventions == nil {
		t.Error("SetPreference should initialize Conventions map")
	}

	if got := profile.GetPreference("key"); got != "value" {
		t.Errorf("GetPreference(key) = %q, want %q", got, "value")
	}
}

func TestUserProfile_GetPreferenceNilMap(t *testing.T) {
	profile := &UserProfile{}

	// Should handle nil Conventions map gracefully
	if got := profile.GetPreference("key"); got != "" {
		t.Errorf("GetPreference with nil map = %q, want empty", got)
	}
}

func TestUserProfile_AddFramework(t *testing.T) {
	profile := &UserProfile{}

	profile.AddFramework("gin")
	if len(profile.Frameworks) != 1 {
		t.Errorf("len(Frameworks) = %d, want 1", len(profile.Frameworks))
	}

	// Add same framework again - should not duplicate
	profile.AddFramework("gin")
	if len(profile.Frameworks) != 1 {
		t.Errorf("len(Frameworks) = %d, want 1 (no duplicate)", len(profile.Frameworks))
	}

	// Add different framework
	profile.AddFramework("gorm")
	if len(profile.Frameworks) != 2 {
		t.Errorf("len(Frameworks) = %d, want 2", len(profile.Frameworks))
	}
}

func TestUserProfile_AddCodePattern(t *testing.T) {
	profile := &UserProfile{}

	profile.AddCodePattern("early_returns")
	if len(profile.CodePatterns) != 1 {
		t.Errorf("len(CodePatterns) = %d, want 1", len(profile.CodePatterns))
	}

	// Add same pattern again - should not duplicate
	profile.AddCodePattern("early_returns")
	if len(profile.CodePatterns) != 1 {
		t.Errorf("len(CodePatterns) = %d, want 1 (no duplicate)", len(profile.CodePatterns))
	}

	// Add different pattern
	profile.AddCodePattern("guard_clauses")
	if len(profile.CodePatterns) != 2 {
		t.Errorf("len(CodePatterns) = %d, want 2", len(profile.CodePatterns))
	}
}

func TestUserProfile_GetCorrection(t *testing.T) {
	profile := &UserProfile{
		Corrections: []Correction{
			{Pattern: "use println", Correction: "use slog", Count: 3},
		},
	}

	// Get existing correction
	correction, found := profile.GetCorrection("use println")
	if !found {
		t.Error("GetCorrection should find existing pattern")
	}
	if correction != "use slog" {
		t.Errorf("correction = %q, want %q", correction, "use slog")
	}

	// Get non-existent correction
	_, found = profile.GetCorrection("nonexistent")
	if found {
		t.Error("GetCorrection should return false for non-existent pattern")
	}
}

func TestUserProfile_ThreadSafety(t *testing.T) {
	profile := &UserProfile{
		Conventions: make(map[string]string),
	}

	done := make(chan bool)

	// Concurrent writes
	go func() {
		for i := 0; i < 100; i++ {
			profile.SetPreference("key", "value1")
		}
		done <- true
	}()

	go func() {
		for i := 0; i < 100; i++ {
			profile.SetPreference("key", "value2")
		}
		done <- true
	}()

	// Concurrent reads
	go func() {
		for i := 0; i < 100; i++ {
			_ = profile.GetPreference("key")
		}
		done <- true
	}()

	go func() {
		for i := 0; i < 100; i++ {
			profile.RecordCorrection("pattern", "correction")
		}
		done <- true
	}()

	// Wait for all goroutines
	for i := 0; i < 4; i++ {
		<-done
	}

	// If we get here without a race detector error, the test passes
}

func TestProfileManager_MergeCorrectionsPrecedence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "profile-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	globalPath := filepath.Join(tmpDir, "global.json")
	projectPath := filepath.Join(tmpDir, "project.json")
	pm := NewProfileManager(globalPath, projectPath)

	// Create global profile with correction
	globalProfile := &UserProfile{
		Conventions: make(map[string]string),
		Corrections: []Correction{
			{Pattern: "shared", Correction: "global correction", Count: 5},
			{Pattern: "global only", Correction: "only in global", Count: 1},
		},
	}
	if err := pm.Save(globalProfile, true); err != nil {
		t.Fatalf("Save(global) error = %v", err)
	}

	// Create project profile with override
	projectProfile := &UserProfile{
		Conventions: make(map[string]string),
		Corrections: []Correction{
			{Pattern: "shared", Correction: "project correction", Count: 10},
			{Pattern: "project only", Correction: "only in project", Count: 2},
		},
	}
	if err := pm.Save(projectProfile, false); err != nil {
		t.Fatalf("Save(project) error = %v", err)
	}

	// Load merged
	merged, err := pm.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Should have 3 corrections
	if len(merged.Corrections) != 3 {
		t.Errorf("len(Corrections) = %d, want 3", len(merged.Corrections))
	}

	// Verify project takes precedence for shared pattern
	for _, c := range merged.Corrections {
		if c.Pattern == "shared" {
			if c.Correction != "project correction" {
				t.Errorf("shared correction = %q, want project override", c.Correction)
			}
			if c.Count != 10 {
				t.Errorf("shared count = %d, want 10", c.Count)
			}
		}
	}
}
