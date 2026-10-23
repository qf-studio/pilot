package gateway

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/qf-studio/pilot/internal/briefs"
	"github.com/qf-studio/pilot/internal/memory"
)

// BriefPayloadStore is the optional structured-brief read surface (GH-5791).
// It is asserted on the dashboard store, like ChatUsageStore, so stores
// without brief storage keep satisfying DashboardStore. *memory.Store
// implements it.
type BriefPayloadStore interface {
	ListBriefPayloads(limit int, kind string) ([]*memory.BriefPayload, error)
}

const (
	defaultBriefsLimit = 10
	maxBriefsLimit     = 100
)

// parseBriefKind reads the optional ?kind= filter. Empty means no filter;
// anything outside the allowed set writes a 400 and reports ok=false.
func parseBriefKind(w http.ResponseWriter, r *http.Request) (kind string, ok bool) {
	kind = r.URL.Query().Get("kind")
	switch kind {
	case "", string(briefs.KindDaily), string(briefs.KindWeekly):
		return kind, true
	}
	http.Error(w, "kind must be one of: daily, weekly", http.StatusBadRequest)
	return "", false
}

func (s *Server) briefPayloadStore(w http.ResponseWriter) BriefPayloadStore {
	s.mu.RLock()
	store := s.dashboardStore
	s.mu.RUnlock()

	if store == nil {
		http.Error(w, "dashboard store not configured", http.StatusServiceUnavailable)
		return nil
	}
	bs, ok := store.(BriefPayloadStore)
	if !ok {
		http.Error(w, "brief storage not available", http.StatusServiceUnavailable)
		return nil
	}
	return bs
}

// handleBriefsLatest serves GET /api/v1/briefs/latest: the newest structured
// brief payload verbatim, or 404 when none has been generated yet. An
// optional ?kind=daily|weekly restricts it to that cadence.
func (s *Server) handleBriefsLatest(w http.ResponseWriter, r *http.Request) {
	kind, ok := parseBriefKind(w, r)
	if !ok {
		return
	}
	bs := s.briefPayloadStore(w)
	if bs == nil {
		return
	}
	rows, err := bs.ListBriefPayloads(1, kind)
	if err != nil {
		http.Error(w, "failed to fetch briefs", http.StatusInternalServerError)
		return
	}
	if len(rows) == 0 {
		http.Error(w, "no briefs yet", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(rows[0].Payload)
}

// handleBriefs serves GET /api/v1/briefs?limit=N: up to N stored briefs,
// newest first, as {"briefs": [...]}. limit defaults to 10, max 100. An
// optional ?kind=daily|weekly restricts the list to that cadence.
func (s *Server) handleBriefs(w http.ResponseWriter, r *http.Request) {
	limit := defaultBriefsLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
			return
		}
		limit = min(n, maxBriefsLimit)
	}

	kind, ok := parseBriefKind(w, r)
	if !ok {
		return
	}

	bs := s.briefPayloadStore(w)
	if bs == nil {
		return
	}
	rows, err := bs.ListBriefPayloads(limit, kind)
	if err != nil {
		http.Error(w, "failed to fetch briefs", http.StatusInternalServerError)
		return
	}
	out := make([]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Payload)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"briefs": out})
}
