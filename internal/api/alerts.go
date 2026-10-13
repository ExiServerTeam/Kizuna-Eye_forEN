package api

import (
	"net/http"
	"strconv"
	"strings"

	"Kizuna-Eye/pkg/alert"
)

// AlertHistoryProvider provides the alert history.
type AlertHistoryProvider interface {
	Alerts() []alert.HistoryEntry
}

// AlertHistoryClearer is optionally implemented by providers that can clear
// the whole alert history.
type AlertHistoryClearer interface {
	ClearAlerts() error
}

// AlertHandler serves the alert history API.
type AlertHandler struct {
	provider AlertHistoryProvider
}

// NewAlertHandler creates an AlertHandler.
func NewAlertHandler(provider AlertHistoryProvider) *AlertHandler {
	return &AlertHandler{provider: provider}
}

// RegisterRoutes registers the alert routes.
func (h *AlertHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/alerts", h.handleList)
	mux.HandleFunc("DELETE /api/alerts", h.handleClear)
}

// handleList returns the alert history as JSON.
// Query params: limit (max count), level (critical/warning/success/info).
func (h *AlertHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if h.provider == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"alerts": []alert.HistoryEntry{}, "count": 0})
		return
	}

	alerts := h.provider.Alerts()
	if alerts == nil {
		alerts = []alert.HistoryEntry{}
	}

	// Filter by level.
	if level := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("level"))); level != "" {
		filtered := make([]alert.HistoryEntry, 0, len(alerts))
		for _, a := range alerts {
			if strings.ToLower(a.Level) == level {
				filtered = append(filtered, a)
			}
		}
		alerts = filtered
	}

	// Limit the result count.
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 && limit < len(alerts) {
			alerts = alerts[:limit]
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"alerts": alerts,
		"count":  len(alerts),
	})
}

// handleClear removes the whole alert history.
// Providers that do not implement AlertHistoryClearer return 501.
func (h *AlertHandler) handleClear(w http.ResponseWriter, r *http.Request) {
	clearer, ok := h.provider.(AlertHistoryClearer)
	if !ok {
		writeJSONError(w, http.StatusNotImplemented, "clearing alert history is not supported")
		return
	}
	if err := clearer.ClearAlerts(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to clear alert history: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
}
