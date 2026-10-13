package api

import (
	"net/http"
)

// HistorySample is a compact time-series point.
type HistorySample struct {
	Timestamp int64   `json:"timestamp"`
	CPU       float64 `json:"cpu"`
	Mem       float64 `json:"mem"`
	Disk      float64 `json:"disk"`
}

// HistoryHub provides recent compact samples.
type HistoryHub interface {
	HistorySamples() []HistorySample
}

// HistoryHandler serves the metrics history API.
type HistoryHandler struct {
	hub HistoryHub
}

// NewHistoryHandler creates a HistoryHandler.
func NewHistoryHandler(hub HistoryHub) *HistoryHandler {
	return &HistoryHandler{hub: hub}
}

// RegisterRoutes registers the history route.
func (h *HistoryHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/history", h.handleHistory)
}

// handleHistory returns the recent samples as JSON (oldest first).
func (h *HistoryHandler) handleHistory(w http.ResponseWriter, r *http.Request) {
	if h.hub == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"samples": []interface{}{}, "count": 0})
		return
	}
	samples := h.hub.HistorySamples()
	if samples == nil {
		samples = []HistorySample{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"samples": samples,
		"count":   len(samples),
	})
}
