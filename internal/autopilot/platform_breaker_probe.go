package autopilot

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// GH-4792 (TASK-458 part 2): advisory corroboration probe against GitHub's
// own official Statuspage API. This NEVER gates PlatformBreaker's
// open/close decision — that is driven exclusively by cross-PR CI-failure
// correlation (platform_breaker.go). The probe only enriches the
// operator-facing open/close alert with independent evidence, and a green
// result must never veto an already-correlated signal: status pages lag
// reality, confirmed during the 2026-08-06 outage this whole feature exists
// to catch.
//
// Endpoint choice: component-scoped, not the coarse page-level
// status.json. componentsURL finds the "Actions" component's own status;
// incidentsURL checks for an unresolved incident whose components include
// Actions. Both are official githubstatus.com endpoints — no third party.
// (An issue-thread comment on #4792 pitched a third-party status
// aggregator; evaluated and explicitly rejected in favor of this
// component-scoped official API, which needs no third-party trust/lag
// tradeoff. See #4792 discussion / TASK-458 refs.)
const (
	githubStatusComponentsURL  = "https://www.githubstatus.com/api/v2/components.json"
	githubStatusIncidentsURL   = "https://www.githubstatus.com/api/v2/incidents/unresolved.json"
	githubStatusProbeTimeout   = 5 * time.Second
	githubStatusProbeUserAgent = "pilot-platform-breaker/1.0"
	githubActionsComponentName = "Actions"
)

// PlatformProbeVerdict is the advisory outcome of a githubstatus.com
// corroboration probe.
type PlatformProbeVerdict string

const (
	// PlatformProbeCorroborating: the Actions component is reported
	// degraded/partial/major outage, or an unresolved incident lists Actions
	// among its affected components.
	PlatformProbeCorroborating PlatformProbeVerdict = "corroborating"
	// PlatformProbeGreen: both endpoints answered and neither reports any
	// Actions trouble. Per the 2026-08-06 incident, this must NEVER veto an
	// already-correlated breaker signal — status pages lag.
	PlatformProbeGreen PlatformProbeVerdict = "green"
	// PlatformProbeUnknown: a probe failed, timed out, returned unparseable
	// JSON, or the components response didn't contain an Actions component.
	// Treated the same as green for gating purposes (i.e. never blocks
	// anything) but logged/alerted distinctly since it carries no signal
	// either way.
	PlatformProbeUnknown PlatformProbeVerdict = "unknown"
)

// platformStatusHTTPGetter is an injectable HTTP GET function, mirroring
// internal/health's httpGetter convention for testability.
type platformStatusHTTPGetter func(url string) (*http.Response, error)

// platformStatusHTTPGet is the default platformStatusHTTPGetter: 5s
// timeout, explicit User-Agent (internal/health/health.go:130-142
// convention). Overridden in tests.
var platformStatusHTTPGet platformStatusHTTPGetter = func(url string) (*http.Response, error) {
	client := &http.Client{Timeout: githubStatusProbeTimeout}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", githubStatusProbeUserAgent)
	return client.Do(req)
}

type githubStatusComponentsResponse struct {
	Components []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"components"`
}

type githubStatusIncidentsResponse struct {
	Incidents []struct {
		Name       string `json:"name"`
		Impact     string `json:"impact"`
		Components []struct {
			Name string `json:"name"`
		} `json:"components"`
	} `json:"incidents"`
}

// ProbeGitHubStatus performs the advisory githubstatus.com corroboration
// check: is the Actions component itself degraded, or is there an
// unresolved incident affecting it. Every failure mode (network error,
// non-200, bad JSON, missing Actions component) is swallowed into
// PlatformProbeUnknown — callers never need to handle an error, and the
// result must never be used to block or veto the breaker's own
// correlation-driven open/close decision. A nil log uses slog.Default().
func ProbeGitHubStatus(log *slog.Logger) PlatformProbeVerdict {
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "platform_breaker_probe")

	if v := probeGitHubStatusComponents(log); v == PlatformProbeCorroborating {
		return v
	} else if v == PlatformProbeUnknown {
		// Still check incidents — an unresolved incident is independent
		// corroborating evidence even if the components probe was unknown.
		if iv := probeGitHubStatusIncidents(log); iv == PlatformProbeCorroborating {
			return iv
		}
		return PlatformProbeUnknown
	}

	// Components probe came back green — incidents probe decides the
	// final verdict.
	return probeGitHubStatusIncidents(log)
}

func probeGitHubStatusComponents(log *slog.Logger) PlatformProbeVerdict {
	resp, err := platformStatusHTTPGet(githubStatusComponentsURL)
	if err != nil {
		log.Warn("githubstatus.com components probe failed — treating as unknown, never vetoes breaker", "error", err)
		return PlatformProbeUnknown
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		log.Warn("githubstatus.com components probe returned non-200", "status", resp.StatusCode)
		return PlatformProbeUnknown
	}
	var parsed githubStatusComponentsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		log.Warn("githubstatus.com components probe returned unparseable JSON", "error", err)
		return PlatformProbeUnknown
	}
	for _, c := range parsed.Components {
		if c.Name != githubActionsComponentName {
			continue
		}
		switch c.Status {
		case "degraded_performance", "partial_outage", "major_outage":
			log.Warn("githubstatus.com reports Actions component degraded", "status", c.Status)
			return PlatformProbeCorroborating
		default:
			return PlatformProbeGreen
		}
	}
	log.Warn("githubstatus.com components response did not contain an Actions component")
	return PlatformProbeUnknown
}

func probeGitHubStatusIncidents(log *slog.Logger) PlatformProbeVerdict {
	resp, err := platformStatusHTTPGet(githubStatusIncidentsURL)
	if err != nil {
		log.Warn("githubstatus.com incidents probe failed — treating as unknown, never vetoes breaker", "error", err)
		return PlatformProbeUnknown
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		log.Warn("githubstatus.com incidents probe returned non-200", "status", resp.StatusCode)
		return PlatformProbeUnknown
	}
	var parsed githubStatusIncidentsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		log.Warn("githubstatus.com incidents probe returned unparseable JSON", "error", err)
		return PlatformProbeUnknown
	}
	for _, inc := range parsed.Incidents {
		for _, c := range inc.Components {
			if c.Name == githubActionsComponentName {
				log.Warn("githubstatus.com reports an unresolved incident affecting Actions",
					"incident", inc.Name, "impact", inc.Impact)
				return PlatformProbeCorroborating
			}
		}
	}
	return PlatformProbeGreen
}
