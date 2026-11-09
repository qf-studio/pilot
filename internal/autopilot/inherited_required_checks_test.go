package autopilot

import (
	"reflect"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

func TestInheritedRequiredChecks(t *testing.T) {
	tests := []struct {
		name     string
		global   *Config
		override *ProjectCIChecksOverride
		want     []string
	}{
		{"nil override + global list", &Config{RequiredChecks: []string{"test", "lint"}}, nil, []string{"test", "lint"}},
		{"nil override + ci_checks.required", &Config{CIChecks: &CIChecksConfig{Required: []string{"build"}}}, nil, []string{"build"}},
		{"nil override + empty global", &Config{}, nil, nil},
		{"nil override + nil global", nil, nil, nil},
		{"override set with list", &Config{RequiredChecks: []string{"test"}}, &ProjectCIChecksOverride{RequiredChecks: []string{"ci"}}, nil},
		{"override set with empty required_checks", &Config{RequiredChecks: []string{"test"}}, &ProjectCIChecksOverride{RequiredChecks: []string{}}, nil},
		{"override set, nil fields", &Config{RequiredChecks: []string{"test"}}, &ProjectCIChecksOverride{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InheritedRequiredChecks(tt.global, tt.override)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("InheritedRequiredChecks() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyCIOutcome_ConfigMismatchNamesHint(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RequiredChecks = []string{"test", "lint"}
	c := NewController(cfg, github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://127.0.0.1:0"), nil, "owner", "repo")
	prState := &PRState{PRNumber: 7}
	if !c.applyCIOutcome(prState, "abcdef1234567", CIConfigMismatch) {
		t.Fatal("applyCIOutcome(CIConfigMismatch) = false, want true")
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
	if !strings.Contains(prState.Error, CIChecksInheritHint) {
		t.Errorf("Error = %q, want it to contain the ci_checks hint", prState.Error)
	}
}
