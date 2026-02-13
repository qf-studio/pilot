package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// UserProfile stores learned user preferences across sessions.
// It captures communication style, code preferences, and corrections
// to improve AI responses over time.
type UserProfile struct {
	mu sync.RWMutex

	// Communication style
	Verbosity string `json:"verbosity,omitempty"` // "concise" or "detailed"

	// Code preferences
	Frameworks   []string          `json:"frameworks,omitempty"`    // e.g., ["gin", "gorm"]
	Conventions  map[string]string `json:"conventions,omitempty"`   // e.g., {"indent": "tabs"}
	CodePatterns []string          `json:"code_patterns,omitempty"` // e.g., ["early_returns"]

	// Corrections learned from user feedback
	Corrections []Correction `json:"corrections,omitempty"`
}

// Correction represents a learned correction from user feedback.
// When a user corrects the AI, the pattern and correction are stored
// to avoid repeating the same mistake.
type Correction struct {
	Pattern    string `json:"pattern"`    // What triggered the correction
	Correction string `json:"correction"` // What user said/preferred
	Count      int    `json:"count"`      // Times this correction came up
}

// ProfileManager handles loading and saving user profiles.
// It supports both global profiles (~/.pilot/profile.json) and
// project-specific profiles (.agent/.user-profile.json).
type ProfileManager struct {
	globalPath  string // e.g., ~/.pilot/profile.json
	projectPath string // e.g., .agent/.user-profile.json
}

// NewProfileManager creates a profile manager with the given paths.
// globalPath is typically ~/.pilot/profile.json
// projectPath is typically .agent/.user-profile.json in the project root
func NewProfileManager(globalPath, projectPath string) *ProfileManager {
	return &ProfileManager{
		globalPath:  globalPath,
		projectPath: projectPath,
	}
}

// Load loads the user profile, merging global defaults with project overrides.
// Global profile is loaded first, then project-specific settings are applied on top.
func (pm *ProfileManager) Load() (*UserProfile, error) {
	profile := &UserProfile{
		Conventions: make(map[string]string),
	}

	// Load global defaults
	if data, err := os.ReadFile(pm.globalPath); err == nil {
		_ = json.Unmarshal(data, profile)
	}

	// Apply project overrides
	if data, err := os.ReadFile(pm.projectPath); err == nil {
		var projectProfile UserProfile
		if json.Unmarshal(data, &projectProfile) == nil {
			pm.mergeProfiles(profile, &projectProfile)
		}
	}

	// Ensure conventions map is initialized
	if profile.Conventions == nil {
		profile.Conventions = make(map[string]string)
	}

	return profile, nil
}

// Save saves the profile to the appropriate path.
// If global is true, saves to globalPath; otherwise saves to projectPath.
func (pm *ProfileManager) Save(profile *UserProfile, global bool) error {
	profile.mu.RLock()
	defer profile.mu.RUnlock()

	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return err
	}

	path := pm.projectPath
	if global {
		path = pm.globalPath
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// RecordCorrection learns from a user correction.
// If the same pattern was corrected before, increments the count;
// otherwise adds a new correction entry.
func (profile *UserProfile) RecordCorrection(pattern, correction string) {
	profile.mu.Lock()
	defer profile.mu.Unlock()

	// Check if we've seen this pattern before
	for i := range profile.Corrections {
		if profile.Corrections[i].Pattern == pattern {
			profile.Corrections[i].Count++
			profile.Corrections[i].Correction = correction
			return
		}
	}

	// New correction
	profile.Corrections = append(profile.Corrections, Correction{
		Pattern:    pattern,
		Correction: correction,
		Count:      1,
	})
}

// SetPreference sets a convention preference.
func (profile *UserProfile) SetPreference(key, value string) {
	profile.mu.Lock()
	defer profile.mu.Unlock()

	if profile.Conventions == nil {
		profile.Conventions = make(map[string]string)
	}
	profile.Conventions[key] = value
}

// GetPreference gets a convention preference.
// Returns empty string if the key is not set.
func (profile *UserProfile) GetPreference(key string) string {
	profile.mu.RLock()
	defer profile.mu.RUnlock()

	if profile.Conventions == nil {
		return ""
	}
	return profile.Conventions[key]
}

// AddFramework adds a framework to the user's preferences if not already present.
func (profile *UserProfile) AddFramework(framework string) {
	profile.mu.Lock()
	defer profile.mu.Unlock()

	for _, f := range profile.Frameworks {
		if f == framework {
			return // Already present
		}
	}
	profile.Frameworks = append(profile.Frameworks, framework)
}

// AddCodePattern adds a code pattern preference if not already present.
func (profile *UserProfile) AddCodePattern(pattern string) {
	profile.mu.Lock()
	defer profile.mu.Unlock()

	for _, p := range profile.CodePatterns {
		if p == pattern {
			return // Already present
		}
	}
	profile.CodePatterns = append(profile.CodePatterns, pattern)
}

// GetCorrection returns the learned correction for a pattern, if any.
// Returns the correction string and true if found, empty string and false otherwise.
func (profile *UserProfile) GetCorrection(pattern string) (string, bool) {
	profile.mu.RLock()
	defer profile.mu.RUnlock()

	for _, c := range profile.Corrections {
		if c.Pattern == pattern {
			return c.Correction, true
		}
	}
	return "", false
}

// mergeProfiles applies project overrides to the base profile.
// Non-empty fields from override replace or extend the base.
func (pm *ProfileManager) mergeProfiles(base, override *UserProfile) {
	if override.Verbosity != "" {
		base.Verbosity = override.Verbosity
	}

	// Append frameworks (deduplicate)
	for _, f := range override.Frameworks {
		found := false
		for _, bf := range base.Frameworks {
			if bf == f {
				found = true
				break
			}
		}
		if !found {
			base.Frameworks = append(base.Frameworks, f)
		}
	}

	// Override conventions
	if base.Conventions == nil {
		base.Conventions = make(map[string]string)
	}
	for k, v := range override.Conventions {
		base.Conventions[k] = v
	}

	// Append code patterns (deduplicate)
	for _, p := range override.CodePatterns {
		found := false
		for _, bp := range base.CodePatterns {
			if bp == p {
				found = true
				break
			}
		}
		if !found {
			base.CodePatterns = append(base.CodePatterns, p)
		}
	}

	// Merge corrections (project corrections take precedence)
	for _, oc := range override.Corrections {
		found := false
		for i, bc := range base.Corrections {
			if bc.Pattern == oc.Pattern {
				base.Corrections[i] = oc
				found = true
				break
			}
		}
		if !found {
			base.Corrections = append(base.Corrections, oc)
		}
	}
}
