package api

import (
	"net/http"
	"strconv"
	"strings"

	"Kizuna-Eye/pkg/alert"
)

// AlertHistoryProvider はアラート履歴を提供する。
// AlertHistoryProvider provides the alert history.
type AlertHistoryProvider interface {
	Alerts() []alert.HistoryEntry
}

// AlertHistoryClearer はアラート履歴全体をクリアできるプロバイダが任意で実装する。
// AlertHistoryClearer is optionally implemented by providers that can clear
// the whole alert history.
type AlertHistoryClearer interface {
	ClearAlerts() error
}

// AlertHandler はアラート履歴 API を提供する。
// AlertHandler serves the alert history API.
type AlertHandler struct {
	provider AlertHistoryProvider
}

// NewAlertHandler は AlertHandler を作る。
// NewAlertHandler creates an AlertHandler.
func NewAlertHandler(provider AlertHistoryProvider) *AlertHandler {
	return &AlertHandler{provider: provider}
}

// RegisterRoutes はアラートのルートを登録する。
// RegisterRoutes registers the alert routes.
func (h *AlertHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/alerts/{id}", h.handleGet)
	mux.HandleFunc("GET /api/alerts", h.handleList)
	mux.HandleFunc("DELETE /api/alerts", h.handleClear)
}

// handleGet は ID で 1 件のアラートを返す（タスク1: 詳細ビュー）。
// ID は timestamp/type/title から導出したロケータなので、最初の一致エントリを返す。
// 一致が無ければ 404。
// handleGet returns one alert by its ID (task 1: detail view).
// The ID is a locator derived from timestamp/type/title, so the first
// matching entry is returned. 404 when nothing matches.
func (h *AlertHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	if h.provider == nil {
		writeJSONError(w, http.StatusNotFound, "alert not found")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "missing alert id")
		return
	}
	for _, a := range h.provider.Alerts() {
		if a.ID == id {
			writeJSON(w, http.StatusOK, a)
			return
		}
	}
	writeJSONError(w, http.StatusNotFound, "alert not found")
}

// handleList はアラート履歴を JSON で返す。
// クエリパラメータ: limit（最大件数）、level（critical/warning/success/info）。
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

	// レベルで絞り込む。
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

	// 結果件数を制限する。
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

// handleClear はアラート履歴全体を削除する。
// AlertHistoryClearer を実装しないプロバイダは 501 を返す。
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
