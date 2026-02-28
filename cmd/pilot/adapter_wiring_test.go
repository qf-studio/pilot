package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/alekspetrov/pilot/internal/config"
)

// TestAdapterPollerRegistrations_CoverAllAdapterTypes verifies that every adapter
// type defined in config.AdaptersConfig has a corresponding PollerRegistration
// returned by adapterPollerRegistrations(). Adapters that are not polled (GitHub
// has unique multi-repo handling; Slack, Telegram, GitLab are notification/bot
// channels) are explicitly excluded.
func TestAdapterPollerRegistrations_CoverAllAdapterTypes(t *testing.T) {
	// Adapters that intentionally have no PollerRegistration.
	excluded := map[string]bool{
		"GitHub":   true, // multi-repo, rate-limit, execution mode — separate startup path
		"GitLab":   true, // webhook-based, no poller
		"Slack":    true, // notification channel + socket mode, not a task source poller
		"Telegram": true, // bot adapter, not a task source poller
	}

	// Build set of registered poller names (lowercased).
	regs := adapterPollerRegistrations()
	registered := make(map[string]bool, len(regs))
	for _, r := range regs {
		registered[strings.ToLower(r.Name)] = true
	}

	// Reflect over AdaptersConfig fields to discover all adapter types.
	adapterType := reflect.TypeOf(config.AdaptersConfig{})
	for i := 0; i < adapterType.NumField(); i++ {
		field := adapterType.Field(i)
		if excluded[field.Name] {
			continue
		}

		name := strings.ToLower(field.Name)
		if !registered[name] {
			t.Errorf("adapter %q (config.AdaptersConfig.%s) has no PollerRegistration — "+
				"add one in adapterPollerRegistrations() or exclude it in this test", name, field.Name)
		}
	}
}
