package executor

import (
	"bytes"
	"sync"
	"text/template"
	"time"

	"github.com/alekspetrov/pilot/internal/memory"
)

// DriftIndicator represents a sign of collaboration drift
type DriftIndicator struct {
	Type     string    // "repeated_correction", "context_confusion", "quality_drop"
	Count    int       // How many times this indicator has been seen
	LastSeen time.Time // When this indicator was last seen
	Pattern  string    // The pattern that triggered this indicator
}

// DriftDetector monitors for collaboration drift and triggers re-anchoring
type DriftDetector struct {
	mu           sync.Mutex
	indicators   []DriftIndicator
	threshold    int                    // corrections before triggering
	window       time.Duration          // time window for counting corrections
	profileStore *memory.ProfileManager // for persisting corrections
}

// NewDriftDetector creates a drift detector with the given threshold
func NewDriftDetector(threshold int, profile *memory.ProfileManager) *DriftDetector {
	return &DriftDetector{
		threshold:    threshold,
		window:       30 * time.Minute, // count corrections within 30 min window
		profileStore: profile,
	}
}

// RecordCorrection logs a user correction for drift tracking
func (d *DriftDetector) RecordCorrection(pattern, correction string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()

	// Check if pattern seen before
	for i := range d.indicators {
		if d.indicators[i].Pattern == pattern {
			d.indicators[i].Count++
			d.indicators[i].LastSeen = now
			return
		}
	}

	// New indicator
	d.indicators = append(d.indicators, DriftIndicator{
		Type:     "repeated_correction",
		Count:    1,
		LastSeen: now,
		Pattern:  pattern,
	})

	// Also record in profile for persistence
	if d.profileStore != nil {
		if profile, err := d.profileStore.Load(); err == nil {
			profile.RecordCorrection(pattern, correction)
			_ = d.profileStore.Save(profile, false)
		}
	}
}

// RecordContextConfusion logs when the AI shows signs of context confusion
func (d *DriftDetector) RecordContextConfusion(pattern string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()

	d.indicators = append(d.indicators, DriftIndicator{
		Type:     "context_confusion",
		Count:    1,
		LastSeen: now,
		Pattern:  pattern,
	})
}

// ShouldReanchor returns true if drift indicators exceed threshold
func (d *DriftDetector) ShouldReanchor() bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	cutoff := time.Now().Add(-d.window)
	recentCount := 0

	for _, ind := range d.indicators {
		if ind.LastSeen.After(cutoff) {
			recentCount += ind.Count
		}
	}

	return recentCount >= d.threshold
}

// GetRecentIndicators returns indicators within the time window
func (d *DriftDetector) GetRecentIndicators() []DriftIndicator {
	d.mu.Lock()
	defer d.mu.Unlock()

	cutoff := time.Now().Add(-d.window)
	var recent []DriftIndicator

	for _, ind := range d.indicators {
		if ind.LastSeen.After(cutoff) {
			recent = append(recent, ind)
		}
	}

	return recent
}

// reanchorTemplate is the template for generating re-anchor prompts
var reanchorTemplate = template.Must(template.New("reanchor").Parse(`
## Re-Anchoring Required

Recent corrections indicate misalignment. Before proceeding:

1. Review recent corrections:
{{range .Indicators}}   - {{.Type}}: "{{.Pattern}}" ({{.Count}}x)
{{end}}
2. Confirm understanding of user preferences
3. Apply corrections to current work
4. Proceed with adjusted approach
`))

// GetReanchorPrompt returns a prompt to restore alignment
func (d *DriftDetector) GetReanchorPrompt() string {
	indicators := d.GetRecentIndicators()
	if len(indicators) == 0 {
		return ""
	}

	data := struct {
		Indicators []DriftIndicator
	}{
		Indicators: indicators,
	}

	var buf bytes.Buffer
	if err := reanchorTemplate.Execute(&buf, data); err != nil {
		// Fallback to simple message
		return "\n## Re-Anchoring Required\n\nRecent corrections indicate misalignment. Review user preferences before proceeding.\n"
	}

	return buf.String()
}

// Reset clears drift indicators after successful re-anchor
func (d *DriftDetector) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.indicators = nil
}

// SetWindow sets the time window for counting corrections
func (d *DriftDetector) SetWindow(window time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.window = window
}

// SetThreshold sets the correction count threshold
func (d *DriftDetector) SetThreshold(threshold int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.threshold = threshold
}
