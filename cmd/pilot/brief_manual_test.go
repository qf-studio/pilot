package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/briefs"
	"github.com/qf-studio/pilot/internal/memory"
)

func manualBriefGenerator(t *testing.T) (*briefs.Generator, *memory.Store) {
	t.Helper()
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := &briefs.BriefConfig{Timezone: "UTC", Content: briefs.ContentConfig{MaxItemsPerSection: 10}}
	return briefs.NewGenerator(store, cfg), store
}

// pilot brief --weekly ends the window now and stores a kind=weekly payload so
// the console sees manual runs (GH-5838).
func TestGenerateManualBrief_WeeklyStoresWeeklyPayload(t *testing.T) {
	generator, store := manualBriefGenerator(t)

	brief, err := generateManualBrief(generator, true)
	if err != nil {
		t.Fatalf("generateManualBrief: %v", err)
	}
	if d := time.Since(brief.Period.End); d < 0 || d > time.Minute {
		t.Errorf("window ends %v ago, want now", d)
	}
	if got := brief.Period.End.Sub(brief.Period.Start); got != 7*24*time.Hour {
		t.Errorf("window = %v, want 168h", got)
	}

	stored, err := store.ListBriefPayloads(10, "weekly")
	if err != nil {
		t.Fatalf("ListBriefPayloads: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored weekly payloads = %d, want 1", len(stored))
	}
	if stored[0].Kind != "weekly" {
		t.Errorf("stored kind = %q, want weekly", stored[0].Kind)
	}
	var payload struct {
		Kind   string          `json:"kind"`
		Weekly json.RawMessage `json:"weekly"`
	}
	if err := json.Unmarshal(stored[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Kind != "weekly" || len(payload.Weekly) == 0 {
		t.Errorf("payload kind = %q weekly = %s, want weekly with proof", payload.Kind, payload.Weekly)
	}
}
