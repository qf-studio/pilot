package main

import (
	"testing"

	"github.com/qf-studio/pilot/internal/autopilot"
)

func TestInheritedRequiredChecksWarning(t *testing.T) {
	tests := []struct {
		name     string
		global   *autopilot.Config
		override *autopilot.ProjectCIChecksOverride
		want     bool
	}{
		{"nil override + global required_checks", &autopilot.Config{RequiredChecks: []string{"test", "lint"}}, nil, true},
		{"nil override + global ci_checks.required", &autopilot.Config{CIChecks: &autopilot.CIChecksConfig{Required: []string{"test"}}}, nil, true},
		{"nil override + empty global", &autopilot.Config{}, nil, false},
		{"nil override + nil global", nil, nil, false},
		{"override with list", &autopilot.Config{RequiredChecks: []string{"test"}}, &autopilot.ProjectCIChecksOverride{RequiredChecks: []string{"ci"}}, false},
		{"override with empty required_checks", &autopilot.Config{RequiredChecks: []string{"test"}}, &autopilot.ProjectCIChecksOverride{RequiredChecks: []string{}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := warnInheritedRequiredChecks(tt.global, tt.override, "proj", "org/repo"); got != tt.want {
				t.Errorf("warnInheritedRequiredChecks() = %v, want %v", got, tt.want)
			}
		})
	}
}
