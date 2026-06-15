package transcription

import (
	"strings"
	"testing"
)

func TestCheckSetup(t *testing.T) {
	tests := []struct {
		name           string
		config         *Config
		wantKeySet     bool
		wantBackend    string
		wantMissingKey bool
	}{
		{"key set", &Config{OpenAIAPIKey: "test-openai-key"}, true, "whisper-api", false},
		{"empty key", &Config{}, false, "none", true},
		{"nil config", nil, false, "none", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := CheckSetup(tt.config)
			if status.OpenAIKeySet != tt.wantKeySet {
				t.Errorf("OpenAIKeySet = %v, want %v", status.OpenAIKeySet, tt.wantKeySet)
			}
			if status.RecommendedBackend != tt.wantBackend {
				t.Errorf("RecommendedBackend = %q, want %q", status.RecommendedBackend, tt.wantBackend)
			}
			hasMissingKey := false
			for _, dep := range status.Missing {
				if dep.Name == "OPENAI_API_KEY" {
					hasMissingKey = true
				}
			}
			if hasMissingKey != tt.wantMissingKey {
				t.Errorf("missing OPENAI_API_KEY = %v, want %v", hasMissingKey, tt.wantMissingKey)
			}
		})
	}
}

func TestGetInstallInstructions(t *testing.T) {
	got := GetInstallInstructions()
	for _, want := range []string{"OpenAI API key", "config.yaml", "Restart"} {
		if !strings.Contains(got, want) {
			t.Errorf("instructions missing %q:\n%s", want, got)
		}
	}
}

func TestFormatStatusMessage(t *testing.T) {
	ready := FormatStatusMessage(&SetupStatus{OpenAIKeySet: true})
	if !strings.Contains(ready, "ready") || !strings.Contains(ready, "Whisper API") {
		t.Errorf("ready message unexpected: %q", ready)
	}

	notReady := FormatStatusMessage(&SetupStatus{OpenAIKeySet: false})
	if !strings.Contains(notReady, "not set") {
		t.Errorf("not-ready message should mention missing key: %q", notReady)
	}
}
