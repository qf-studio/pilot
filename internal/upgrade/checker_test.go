package upgrade

import (
	"testing"
)

// newTestChecker builds a VersionChecker without running NewVersionChecker's
// NewUpgrader probe (which touches os.Executable/symlinks) — evaluate() only
// needs the callback/threshold fields these tests exercise.
func newTestChecker() *VersionChecker {
	return &VersionChecker{currentVersion: "2.201.2"}
}

func TestVersionChecker_OnUpdate_FiresOnUpdateAvail(t *testing.T) {
	c := newTestChecker()
	var got *VersionInfo
	c.OnUpdate(func(info *VersionInfo) { got = info })

	c.evaluate(&VersionInfo{Current: "2.201.2", Latest: "v2.201.3", UpdateAvail: true})

	if got == nil {
		t.Fatal("OnUpdate callback was not invoked")
	}
	if got.Latest != "v2.201.3" {
		t.Errorf("Latest = %q, want v2.201.3", got.Latest)
	}
}

func TestVersionChecker_OnUpdate_SkippedWhenNoUpdate(t *testing.T) {
	c := newTestChecker()
	called := false
	c.OnUpdate(func(info *VersionInfo) { called = true })

	c.evaluate(&VersionInfo{Current: "2.201.2", Latest: "v2.201.2", UpdateAvail: false})

	if called {
		t.Error("OnUpdate callback should not fire when UpdateAvail is false")
	}
}

func TestVersionChecker_OnStale_DisabledByDefault(t *testing.T) {
	c := newTestChecker()
	called := false
	c.OnStale(func(info *VersionInfo) { called = true })

	// staleThreshold defaults to 0 (disabled) — even 8 releases behind must
	// not fire until a threshold is explicitly set (GH-3790).
	c.evaluate(&VersionInfo{Current: "2.201.2", Latest: "v2.207.1", ReleasesBehind: 8})

	if called {
		t.Error("OnStale should not fire when staleThreshold is 0 (disabled)")
	}
}

func TestVersionChecker_OnStale_FiresAtOrAboveThreshold(t *testing.T) {
	c := newTestChecker()
	c.SetStaleThreshold(3)

	var calls int
	var lastInfo *VersionInfo
	c.OnStale(func(info *VersionInfo) {
		calls++
		lastInfo = info
	})

	// Below threshold: no callback.
	c.evaluate(&VersionInfo{Current: "2.201.2", Latest: "v2.202.0", ReleasesBehind: 2})
	if calls != 0 {
		t.Fatalf("calls = %d, want 0 for releases_behind below threshold", calls)
	}

	// At threshold: callback fires.
	c.evaluate(&VersionInfo{Current: "2.201.2", Latest: "v2.204.0", ReleasesBehind: 3})
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 at threshold", calls)
	}
	if lastInfo.ReleasesBehind != 3 {
		t.Errorf("ReleasesBehind = %d, want 3", lastInfo.ReleasesBehind)
	}

	// Above threshold: callback fires again (GH-3790 shape: 8 releases behind).
	c.evaluate(&VersionInfo{Current: "2.201.2", Latest: "v2.207.1", ReleasesBehind: 8})
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 above threshold", calls)
	}
}
