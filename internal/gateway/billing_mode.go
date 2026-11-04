package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/qf-studio/pilot/internal/executor"
)

// POST modes of /api/v1/billing-mode; "config" clears the override.
const (
	billingModeSubscription = "subscription"
	billingModeAPIKey       = "api_key"
	billingModeConfig       = "config"
)

// billingModeAccepted names the POST modes in a 400 body.
const billingModeAccepted = "subscription, api_key, config"

// billingSetByAccepted names the POST set_by values in a 400 body.
const billingSetByAccepted = "cli, gateway"

// billingModeResponse is the body of GET and a successful POST
// /api/v1/billing-mode. until is empty when there is none.
type billingModeResponse struct {
	Mode       string `json:"mode"`
	Source     string `json:"source"`
	Until      string `json:"until"`
	ConfigMode string `json:"config_mode"`
}

type billingModeRequest struct {
	Mode  string `json:"mode"`
	Until string `json:"until,omitempty"`
	SetBy string `json:"set_by,omitempty"`
}

// billingModeSetBy resolves the posted set_by: empty means gateway, and only the
// callers of this endpoint (cli, gateway) are accepted. fallback and config are
// written by the daemon itself, never over HTTP.
func billingModeSetBy(setBy string) (string, bool) {
	switch setBy {
	case "":
		return executor.BillingModeSourceGateway, true
	case executor.BillingModeSourceCLI, executor.BillingModeSourceGateway:
		return setBy, true
	}
	return "", false
}

func formatBillingUntil(until time.Time) string {
	if until.IsZero() {
		return ""
	}
	return until.UTC().Format(time.RFC3339)
}

func newBillingModeResponse(st executor.BillingOverrideStatus) billingModeResponse {
	return billingModeResponse{
		Mode:       st.Mode,
		Source:     st.Source,
		Until:      formatBillingUntil(st.Until),
		ConfigMode: st.ConfigMode,
	}
}

// handleBillingMode serves the runtime billing-mode override (GH-6052): GET
// reports the mode in force; POST {"mode": "subscription"|"api_key"|"config",
// "until": RFC3339, "set_by": "cli"|"gateway"} sets it or, for "config", clears it. The name keeps clear of
// the hosted console's /api/v1/billing routes, which are customer billing; this
// endpoint never touches them. Same auth as the other apiMux routes.
func (s *Server) handleBillingMode(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	src := s.billingOverrideSource
	s.mu.RUnlock()
	if src == nil {
		http.Error(w, "billing mode override is not available", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		if status, msg := applyBillingModeRequest(w, r, src); status != 0 {
			http.Error(w, msg, status)
			return
		}
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(newBillingModeResponse(src.BillingOverride()))
}

// applyBillingModeRequest decodes and applies a POST. A non-zero status is the
// HTTP error to answer with.
func applyBillingModeRequest(w http.ResponseWriter, r *http.Request, src BillingOverrideSource) (int, string) {
	var req billingModeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		return http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %v", err)
	}
	switch req.Mode {
	case billingModeSubscription, billingModeAPIKey, billingModeConfig:
	default:
		return http.StatusBadRequest, fmt.Sprintf("invalid mode %q: accepted values are %s", req.Mode, billingModeAccepted)
	}

	setBy, ok := billingModeSetBy(req.SetBy)
	if !ok {
		return http.StatusBadRequest, fmt.Sprintf("invalid set_by %q: accepted values are %s", req.SetBy, billingSetByAccepted)
	}

	if req.Mode == billingModeConfig {
		if req.Until != "" {
			return http.StatusBadRequest, `"until" does not apply to mode "config"`
		}
		if err := src.ClearBillingOverride(setBy); err != nil {
			return http.StatusInternalServerError, err.Error()
		}
		return 0, ""
	}

	var until time.Time
	if req.Until != "" {
		t, err := time.Parse(time.RFC3339, req.Until)
		if err != nil {
			return http.StatusBadRequest, fmt.Sprintf("invalid until %q: want RFC 3339", req.Until)
		}
		until = t
	}
	if err := src.SetBillingOverride(req.Mode, until, setBy); err != nil {
		if errors.Is(err, executor.ErrBillingOverrideInvalid) {
			return http.StatusBadRequest, err.Error()
		}
		return http.StatusInternalServerError, err.Error()
	}
	return 0, ""
}
