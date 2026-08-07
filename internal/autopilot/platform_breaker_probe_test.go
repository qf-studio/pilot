package autopilot

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// stubPlatformStatusHTTPGet installs an override for platformStatusHTTPGet
// for the duration of the test, restoring the original on cleanup — mirrors
// the injectable-getter test convention already used for internal/health.
func stubPlatformStatusHTTPGet(t *testing.T, fn platformStatusHTTPGetter) {
	t.Helper()
	orig := platformStatusHTTPGet
	platformStatusHTTPGet = fn
	t.Cleanup(func() { platformStatusHTTPGet = orig })
}

func jsonResponse(status int, body string) (*http.Response, error) {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

// TestProbeGitHubStatus_ComponentDegraded_IsCorroborating verifies the
// Actions component itself reporting degraded/partial/major outage status is
// classified corroborating, regardless of the incidents endpoint (which
// should not even need to be reached — the components check short-circuits).
func TestProbeGitHubStatus_ComponentDegraded_IsCorroborating(t *testing.T) {
	stubPlatformStatusHTTPGet(t, func(url string) (*http.Response, error) {
		switch url {
		case githubStatusComponentsURL:
			return jsonResponse(http.StatusOK, `{"components":[{"name":"Actions","status":"major_outage"},{"name":"API Requests","status":"operational"}]}`)
		case githubStatusIncidentsURL:
			t.Error("incidents endpoint should not be reached once components already reports corroborating")
			return jsonResponse(http.StatusOK, `{"incidents":[]}`)
		default:
			t.Fatalf("unexpected probe URL: %s", url)
			return nil, nil
		}
	})

	if got := ProbeGitHubStatus(slog.Default()); got != PlatformProbeCorroborating {
		t.Errorf("ProbeGitHubStatus = %s, want %s", got, PlatformProbeCorroborating)
	}
}

// TestProbeGitHubStatus_UnresolvedIncidentAffectingActions_IsCorroborating
// verifies an unresolved incident naming Actions among its components is
// corroborating even when the Actions component itself reports operational
// (status pages sometimes lag their own incident feed).
func TestProbeGitHubStatus_UnresolvedIncidentAffectingActions_IsCorroborating(t *testing.T) {
	stubPlatformStatusHTTPGet(t, func(url string) (*http.Response, error) {
		switch url {
		case githubStatusComponentsURL:
			return jsonResponse(http.StatusOK, `{"components":[{"name":"Actions","status":"operational"}]}`)
		case githubStatusIncidentsURL:
			return jsonResponse(http.StatusOK, `{"incidents":[{"name":"Actions degraded","impact":"major","components":[{"name":"Actions"}]}]}`)
		default:
			t.Fatalf("unexpected probe URL: %s", url)
			return nil, nil
		}
	})

	if got := ProbeGitHubStatus(slog.Default()); got != PlatformProbeCorroborating {
		t.Errorf("ProbeGitHubStatus = %s, want %s", got, PlatformProbeCorroborating)
	}
}

// TestProbeGitHubStatus_BothGreen_IsGreen verifies the fully-healthy case:
// Actions operational and no unresolved incident naming it.
func TestProbeGitHubStatus_BothGreen_IsGreen(t *testing.T) {
	stubPlatformStatusHTTPGet(t, func(url string) (*http.Response, error) {
		switch url {
		case githubStatusComponentsURL:
			return jsonResponse(http.StatusOK, `{"components":[{"name":"Actions","status":"operational"}]}`)
		case githubStatusIncidentsURL:
			return jsonResponse(http.StatusOK, `{"incidents":[{"name":"Unrelated incident","impact":"minor","components":[{"name":"Pages"}]}]}`)
		default:
			t.Fatalf("unexpected probe URL: %s", url)
			return nil, nil
		}
	})

	if got := ProbeGitHubStatus(slog.Default()); got != PlatformProbeGreen {
		t.Errorf("ProbeGitHubStatus = %s, want %s", got, PlatformProbeGreen)
	}
}

// TestProbeGitHubStatus_NetworkFailure_IsUnknown verifies a transport-level
// failure on both endpoints degrades to Unknown rather than propagating an
// error — ProbeGitHubStatus's contract is that callers never need to handle
// an error, and a failed probe must never be mistaken for a green one.
func TestProbeGitHubStatus_NetworkFailure_IsUnknown(t *testing.T) {
	stubPlatformStatusHTTPGet(t, func(url string) (*http.Response, error) {
		return nil, io.ErrClosedPipe
	})

	if got := ProbeGitHubStatus(slog.Default()); got != PlatformProbeUnknown {
		t.Errorf("ProbeGitHubStatus = %s, want %s", got, PlatformProbeUnknown)
	}
}

// TestProbeGitHubStatus_NonOKStatus_IsUnknown verifies a non-200 response
// (rate limited, 5xx, etc.) also degrades to Unknown.
func TestProbeGitHubStatus_NonOKStatus_IsUnknown(t *testing.T) {
	stubPlatformStatusHTTPGet(t, func(url string) (*http.Response, error) {
		return jsonResponse(http.StatusServiceUnavailable, `{}`)
	})

	if got := ProbeGitHubStatus(slog.Default()); got != PlatformProbeUnknown {
		t.Errorf("ProbeGitHubStatus = %s, want %s", got, PlatformProbeUnknown)
	}
}

// TestProbeGitHubStatus_UnparseableJSON_IsUnknown verifies malformed JSON
// degrades to Unknown instead of panicking or propagating a decode error.
func TestProbeGitHubStatus_UnparseableJSON_IsUnknown(t *testing.T) {
	stubPlatformStatusHTTPGet(t, func(url string) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `not json`)
	})

	if got := ProbeGitHubStatus(slog.Default()); got != PlatformProbeUnknown {
		t.Errorf("ProbeGitHubStatus = %s, want %s", got, PlatformProbeUnknown)
	}
}

// TestProbeGitHubStatus_MissingActionsComponent_IsUnknown verifies a
// components response that doesn't even list an Actions component (schema
// drift on githubstatus.com's side) degrades to Unknown rather than being
// silently treated as green.
func TestProbeGitHubStatus_MissingActionsComponent_IsUnknown(t *testing.T) {
	stubPlatformStatusHTTPGet(t, func(url string) (*http.Response, error) {
		switch url {
		case githubStatusComponentsURL:
			return jsonResponse(http.StatusOK, `{"components":[{"name":"Pages","status":"operational"}]}`)
		case githubStatusIncidentsURL:
			return jsonResponse(http.StatusOK, `{"incidents":[]}`)
		default:
			t.Fatalf("unexpected probe URL: %s", url)
			return nil, nil
		}
	})

	if got := ProbeGitHubStatus(slog.Default()); got != PlatformProbeUnknown {
		t.Errorf("ProbeGitHubStatus = %s, want %s", got, PlatformProbeUnknown)
	}
}

// TestProbeGitHubStatus_NilLoggerUsesDefault verifies a nil *slog.Logger
// doesn't panic — ProbeGitHubStatus falls back to slog.Default().
func TestProbeGitHubStatus_NilLoggerUsesDefault(t *testing.T) {
	stubPlatformStatusHTTPGet(t, func(url string) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"components":[{"name":"Actions","status":"operational"}],"incidents":[]}`)
	})

	if got := ProbeGitHubStatus(nil); got != PlatformProbeGreen {
		t.Errorf("ProbeGitHubStatus(nil) = %s, want %s", got, PlatformProbeGreen)
	}
}
