package gateway

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/qf-studio/pilot/internal/memory"
)

// BriefPayloadStore is the optional structured-brief read surface (GH-5791).
// It is asserted on the dashboard store, like ChatUsageStore, so stores
// without brief storage keep satisfying DashboardStore. *memory.Store
// implements it.
type BriefPayloadStore interface {
	ListBriefPayloads(limit int) ([]*memory.BriefPayload, error)
}

const (
	defaultBriefsLimit = 10
	maxBriefsLimit     = 100
)

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
// brief payload verbatim, or 404 when none has been generated yet.
func (s *Server) handleBriefsLatest(w http.ResponseWriter, r *http.Request) {
	bs := s.briefPayloadStore(w)
	if bs == nil {
		return
	}
	rows, err := bs.ListBriefPayloads(1)
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
// newest first, as {"briefs": [...]}. limit defaults to 10, max 100.
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

	bs := s.briefPayloadStore(w)
	if bs == nil {
		return
	}
	rows, err := bs.ListBriefPayloads(limit)
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
