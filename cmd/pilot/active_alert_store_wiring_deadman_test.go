package main

import (
	"strings"
	"testing"
)

// TestRunPollingMode_WiresActiveAlertStore is a source-level deadman guard
// (mirrors label_lifecycle_deadman_test.go's established pattern for this
// otherwise-unexercisable startup path): runPollingMode — the production
// polling-mode entrypoint's alert engine construction site — must pass
// alerts.WithActiveAlertStore(store) to alerts.NewEngine, alongside the
// existing alerts.WithExecutionLifecycle(executor.NewExecutionLifecycle(store))
// option, inside the same `if store != nil` block (GH-5095).
//
// PR#5090 (GH-4890) merged the active-alert persistence layer complete and
// fully tested, but WithActiveAlertStore had zero production call sites — a
// real daemon restart still lost all active-alert state (GH-4716 incident
// class: alert plumbing dead in production despite FEATURE-MATRIX marking
// the row done). This test fails if the wiring line is ever removed or
// reverted.
func TestRunPollingMode_WiresActiveAlertStore(t *testing.T) {
	body := githubFuncBody(t, "main.go", "func runPollingMode(")

	if !strings.Contains(body, "alerts.WithActiveAlertStore(store)") {
		t.Error("runPollingMode must wire alerts.WithActiveAlertStore(store) into the polling-mode alert engine's " +
			"engineOpts (GH-5095) — otherwise active-alert persistence (GH-4890/PR#5090) never actually engages in " +
			"production and a daemon restart loses all currently-firing alert state (GH-4716 class)")
	}

	// Must live inside the same `if store != nil` guard as the sibling
	// WithExecutionLifecycle wiring, not gated separately or unconditionally
	// (store is *memory.Store and nil is a valid, expected value here).
	ifStoreIdx := strings.Index(body, "if store != nil {")
	lifecycleIdx := strings.Index(body, "alerts.WithExecutionLifecycle(executor.NewExecutionLifecycle(store))")
	activeAlertIdx := strings.Index(body, "alerts.WithActiveAlertStore(store)")
	newEngineIdx := strings.Index(body, "alertsEngine = alerts.NewEngine(alertsCfg, engineOpts...)")
	if ifStoreIdx < 0 || lifecycleIdx < 0 || activeAlertIdx < 0 || newEngineIdx < 0 {
		t.Fatal("expected the `if store != nil` guard, WithExecutionLifecycle, WithActiveAlertStore, and the " +
			"alerts.NewEngine(alertsCfg, engineOpts...) call all present in runPollingMode")
	}
	if ifStoreIdx >= lifecycleIdx || lifecycleIdx >= activeAlertIdx || activeAlertIdx >= newEngineIdx {
		t.Error("expected WithActiveAlertStore(store) to be appended inside the `if store != nil` block, after " +
			"WithExecutionLifecycle and before the engineOpts are consumed by alerts.NewEngine")
	}
}
