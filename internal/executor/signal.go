package executor

import (
	"encoding/json"
	"log/slog"
	"regexp"
)

// Signal types for v2 protocol
const (
	SignalTypeStatus     = "status"
	SignalTypeExit       = "exit"
	SignalTypePhase      = "phase"
	SignalTypeStagnation = "stagnation"
)

// PilotSignal represents a v2 structured signal from Navigator
type PilotSignal struct {
	Version    int             `json:"v"`
	Type       string          `json:"type"`
	Phase      string          `json:"phase,omitempty"`
	Progress   int             `json:"progress,omitempty"`
	Iteration  int             `json:"iteration,omitempty"`
	MaxIter    int             `json:"max_iterations,omitempty"`
	Indicators map[string]bool `json:"indicators,omitempty"`
	ExitSignal bool            `json:"exit_signal,omitempty"`
	Success    bool            `json:"success,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Message    string          `json:"message,omitempty"`
}

// signalBlockRegex matches ```pilot-signal\n{...}\n```
var signalBlockRegex = regexp.MustCompile("(?s)```pilot-signal\\s*\\n(.+?)\\n```")

// SignalParser extracts and validates pilot signals from text
type SignalParser struct {
	log *slog.Logger
}

// NewSignalParser creates a new SignalParser
func NewSignalParser(log *slog.Logger) *SignalParser {
	if log == nil {
		log = slog.Default()
	}
	return &SignalParser{log: log}
}

// ParseSignals extracts pilot-signal code blocks and parses JSON
// Returns a slice of parsed signals (may have multiple per text block)
func (p *SignalParser) ParseSignals(text string) []PilotSignal {
	var signals []PilotSignal

	matches := signalBlockRegex.FindAllStringSubmatch(text, -1)
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}

		jsonStr := match[1]
		var signal PilotSignal

		if err := json.Unmarshal([]byte(jsonStr), &signal); err != nil {
			p.log.Warn("failed to parse pilot-signal JSON",
				"error", err,
				"json", truncateSignalForLog(jsonStr, 100),
			)
			continue
		}

		// Validate and clamp progress to 0-100
		signal = p.validateSignal(signal)
		signals = append(signals, signal)
	}

	return signals
}

// validateSignal validates and normalizes a PilotSignal
func (p *SignalParser) validateSignal(s PilotSignal) PilotSignal {
	// Clamp progress to 0-100
	if s.Progress < 0 {
		p.log.Warn("clamping negative progress to 0",
			"original", s.Progress,
		)
		s.Progress = 0
	} else if s.Progress > 100 {
		p.log.Warn("clamping progress > 100 to 100",
			"original", s.Progress,
		)
		s.Progress = 100
	}

	// Default version to 2 if not set
	if s.Version == 0 {
		s.Version = 2
	}

	// Default type to status if not set
	if s.Type == "" {
		s.Type = SignalTypeStatus
	}

	return s
}

// HasExitSignal checks if any signal contains exit_signal: true
func (p *SignalParser) HasExitSignal(signals []PilotSignal) bool {
	for _, s := range signals {
		if s.ExitSignal || s.Type == SignalTypeExit {
			return true
		}
	}
	return false
}

// GetLatestProgress returns the progress from the last status signal
// Returns -1 if no status signals found
func (p *SignalParser) GetLatestProgress(signals []PilotSignal) int {
	for i := len(signals) - 1; i >= 0; i-- {
		if signals[i].Type == SignalTypeStatus {
			return signals[i].Progress
		}
	}
	return -1
}

// GetLatestPhase returns the phase from the last signal with a phase
// Returns empty string if no phase found
func (p *SignalParser) GetLatestPhase(signals []PilotSignal) string {
	for i := len(signals) - 1; i >= 0; i-- {
		if signals[i].Phase != "" {
			return signals[i].Phase
		}
	}
	return ""
}

// truncateSignalForLog truncates a string for signal logging
func truncateSignalForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
