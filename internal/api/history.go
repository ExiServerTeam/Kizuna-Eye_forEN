package api

import (
	"net/http"
)

// HistorySample はコンパクトな時系列の 1 点だ。
// HistorySample is a compact time-series point.
type HistorySample struct {
	Timestamp int64   `json:"timestamp"`
	CPU       float64 `json:"cpu"`
	Mem       float64 `json:"mem"`
	Disk      float64 `json:"disk"`
}

// HistoryHub は最近のコンパクトなサンプルを提供する。
// HistoryHub provides recent compact samples.
type HistoryHub interface {
	HistorySamples() []HistorySample
}

// HistoryHandler はメトリクス履歴 API を提供する。
// HistoryHandler serves the metrics history API.
type HistoryHandler struct {
	hub HistoryHub
}

// NewHistoryHandler は HistoryHandler を作る。
// NewHistoryHandler creates a HistoryHandler.
func NewHistoryHandler(hub HistoryHub) *HistoryHandler {
	return &HistoryHandler{hub: hub}
}

// RegisterRoutes は履歴のルートを登録する。
// RegisterRoutes registers the history route.
func (h *HistoryHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/history", h.handleHistory)
}

// handleHistory は最近のサンプルを JSON で返す（古い順）。
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
