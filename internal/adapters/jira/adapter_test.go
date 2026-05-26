package jira

import (
	"testing"
)

func TestNewAdapter(t *testing.T) {
	cfg := &Config{
		Enabled:  true,
		BaseURL:  "https://jira.example.com",
		Username: "user",
		APIToken: "fake-token",
		Platform: "cloud",
		Polling: &PollingConfig{
			Enabled: true,
		},
	}
	a := NewAdapter(cfg)
	if a.Name() != "jira" {
		t.Errorf("Name() = %q, want %q", a.Name(), "jira")
	}
	if a.Client() == nil {
		t.Error("Client() returned nil")
	}
	if !a.PollingEnabled() {
		t.Error("PollingEnabled() should return true")
	}
}
