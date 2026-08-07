package autopilot

import (
	"io"
	"net/http"
	"testing"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// fakeAdmissionPauser records PauseAdmissionFor/ResumeAdmissionFor calls so
// tests can assert whether alertPlatformBreakerTransition's admission-pause
// block fired, without needing a real *executor.Dispatcher (which would
// require importing internal/executor — the whole reason AdmissionPauser is
// a narrow interface owned by internal/autopilot, see controller.go).
type fakeAdmissionPauser struct {
	paused  []string
	resumed []string
}

func (f *fakeAdmissionPauser) PauseAdmissionFor(owner string)  { f.paused = append(f.paused, owner) }
func (f *fakeAdmissionPauser) ResumeAdmissionFor(owner string) { f.resumed = append(f.resumed, owner) }

// TestAlertPlatformBreakerTransition_NoTransition_AdmissionPauserNotCalled
// covers the "disabled-by-config is byte-identical no-op" criterion for the
// GH-4792 admission-pause seam: a zero-value PlatformBreakerResult (neither
// JustOpened nor JustClosed — the shape produced by a nil/disabled breaker's
// nil-safe Observe/EvaluateClose) must return before ever touching the
// wired AdmissionPauser, so a controller with the breaker disabled behaves
// identically whether or not SetAdmissionPauser was ever called.
func TestAlertPlatformBreakerTransition_NoTransition_AdmissionPauserNotCalled(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig() // PlatformBreaker left nil: breaker disabled by config
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	pauser := &fakeAdmissionPauser{}
	c.SetAdmissionPauser(pauser)
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	c.alertPlatformBreakerTransition(PlatformBreakerResult{})

	if len(pauser.paused) != 0 || len(pauser.resumed) != 0 {
		t.Errorf("admission pauser called with breaker disabled: paused=%v resumed=%v", pauser.paused, pauser.resumed)
	}
	if len(sink.events) != 0 {
		t.Errorf("alert fired for a non-transition result: %+v", sink.events)
	}
}

// TestAlertPlatformBreakerTransition_PauseAdmissionDisabled_SkipsPauseButStillAlerts
// covers the pause_admission: false config opt-out (default true, see
// PlatformBreakerConfig.PauseAdmissionEnabled): even on a genuine open
// transition, the admission pauser must not be invoked when the operator
// has explicitly disabled admission-pause — while the alert itself (an
// unrelated, always-on part-1 behavior) still fires.
func TestAlertPlatformBreakerTransition_PauseAdmissionDisabled_SkipsPauseButStillAlerts(t *testing.T) {
	stubPlatformStatusHTTPGet(t, func(url string) (*http.Response, error) {
		return nil, io.ErrClosedPipe
	})

	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.PlatformBreaker = &PlatformBreakerConfig{
		Enabled:        true,
		PauseAdmission: boolPtr(false),
	}
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	pauser := &fakeAdmissionPauser{}
	c.SetAdmissionPauser(pauser)
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	c.alertPlatformBreakerTransition(PlatformBreakerResult{JustOpened: true, Open: true, CorrelatedPRs: []string{"owner/repo#1"}})

	if len(pauser.paused) != 0 {
		t.Errorf("PauseAdmissionFor called with pause_admission=false: %v", pauser.paused)
	}
	if len(sink.events) != 1 || sink.events[0].Type != alerts.EventType("platform_breaker_open") {
		t.Fatalf("expected exactly 1 platform_breaker_open alert regardless of pause_admission setting, got %+v", sink.events)
	}
}

// TestAlertPlatformBreakerTransition_OpenAndClose_PausesAndResumesAdmission
// is the positive control: with an AdmissionPauser wired and pause_admission
// left at its default (true), a JustOpened result pauses admission under
// the platform-breaker owner key and a JustClosed result resumes it under
// the same key — the pairing PauseAdmissionFor/ResumeAdmissionFor must use
// to stay owner-scoped alongside GH-4683's self-upgrade drain.
func TestAlertPlatformBreakerTransition_OpenAndClose_PausesAndResumesAdmission(t *testing.T) {
	stubPlatformStatusHTTPGet(t, func(url string) (*http.Response, error) {
		return nil, io.ErrClosedPipe
	})

	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.PlatformBreaker = &PlatformBreakerConfig{Enabled: true}
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	pauser := &fakeAdmissionPauser{}
	c.SetAdmissionPauser(pauser)
	c.SetAlertsEngine(&fakeAlertSink{})

	c.alertPlatformBreakerTransition(PlatformBreakerResult{JustOpened: true, Open: true, CorrelatedPRs: []string{"owner/repo#1"}})
	if len(pauser.paused) != 1 || pauser.paused[0] != PlatformBreakerAdmissionPauseOwner {
		t.Errorf("paused = %v, want exactly [%q]", pauser.paused, PlatformBreakerAdmissionPauseOwner)
	}

	c.alertPlatformBreakerTransition(PlatformBreakerResult{JustClosed: true, Open: false, CorrelatedPRs: []string{"owner/repo#1"}})
	if len(pauser.resumed) != 1 || pauser.resumed[0] != PlatformBreakerAdmissionPauseOwner {
		t.Errorf("resumed = %v, want exactly [%q]", pauser.resumed, PlatformBreakerAdmissionPauseOwner)
	}
}
