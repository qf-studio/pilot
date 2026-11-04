package memory

import (
	"testing"
	"time"
)

func TestRuntimeOverrides_StoreRoundTrip(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, _, _, _, found, err := store.GetRuntimeOverride(RuntimeOverrideBillingMode); err != nil || found {
		t.Fatalf("empty store: found=%v err=%v, want not found", found, err)
	}

	until := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	before := time.Now().UTC().Add(-time.Second)
	if err := store.SetRuntimeOverride(RuntimeOverrideBillingMode, "api_key", until, "cli"); err != nil {
		t.Fatalf("SetRuntimeOverride: %v", err)
	}
	value, gotUntil, setBy, setAt, found, err := store.GetRuntimeOverride(RuntimeOverrideBillingMode)
	if err != nil || !found {
		t.Fatalf("Get after Set: found=%v err=%v", found, err)
	}
	if value != "api_key" || setBy != "cli" || !gotUntil.Equal(until) {
		t.Errorf("got value=%q setBy=%q until=%v, want api_key cli %v", value, setBy, gotUntil, until)
	}
	if setAt.Before(before) || setAt.After(time.Now().UTC().Add(time.Second)) {
		t.Errorf("set_at = %v, want about now", setAt)
	}

	// Replace: new value, no expiry (NULL until reads back as the zero time).
	if err := store.SetRuntimeOverride(RuntimeOverrideBillingMode, "subscription", time.Time{}, "gateway"); err != nil {
		t.Fatalf("SetRuntimeOverride replace: %v", err)
	}
	value, gotUntil, setBy, _, found, err = store.GetRuntimeOverride(RuntimeOverrideBillingMode)
	if err != nil || !found {
		t.Fatalf("Get after replace: found=%v err=%v", found, err)
	}
	if value != "subscription" || setBy != "gateway" || !gotUntil.IsZero() {
		t.Errorf("got value=%q setBy=%q until=%v, want subscription gateway zero", value, setBy, gotUntil)
	}

	if err := store.DeleteRuntimeOverride(RuntimeOverrideBillingMode); err != nil {
		t.Fatalf("DeleteRuntimeOverride: %v", err)
	}
	if _, _, _, _, found, err := store.GetRuntimeOverride(RuntimeOverrideBillingMode); err != nil || found {
		t.Errorf("after delete: found=%v err=%v, want not found", found, err)
	}
	if err := store.DeleteRuntimeOverride(RuntimeOverrideBillingMode); err != nil {
		t.Errorf("deleting a missing row: %v", err)
	}
}
