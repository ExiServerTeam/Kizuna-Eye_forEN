package api

import (
	"net/http"
	"strconv"
	"time"

	"Kizuna-Eye/pkg/alert"
	"Kizuna-Eye/pkg/insight"
)

// InsightProvider supplies the alert history the insight is derived from.
// The dashboard's alert engine satisfies it.
type InsightProvider interface {
	Alerts() []alert.HistoryEntry
}

// InsightHandler serves GET /api/insight.
//
// 設計方針 (内部設計書 4.1): コアは観測と可視化に徹する。ここは既存の
// アラート履歴を集計して返すだけで、新しい永続データを持たない。検知もしない。
type InsightHandler struct {
	provider InsightProvider
	// postureFn returns the configuration state used for the posture score.
	// It is a callback because the config is re-read by the config editor
	// while the dashboard runs, so a startup snapshot would go stale.
	// A nil function yields a zero-value PostureInput.
	postureFn func() insight.PostureInput
}

// NewInsightHandler creates an InsightHandler. postureFn may be nil.
func NewInsightHandler(provider InsightProvider, postureFn func() insight.PostureInput) *InsightHandler {
	return &InsightHandler{provider: provider, postureFn: postureFn}
}

// RegisterRoutes registers the insight route.
func (h *InsightHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/insight", h.handleGet)
}

// defaultPeriodHours is the reporting window when the client sends none (7 days).
const defaultPeriodHours = 7 * 24

// maxPeriodHours caps the window so a caller cannot ask for a multi-year scan
// on every poll (the history itself is bounded, but the bound should be
// explicit here too).
const maxPeriodHours = 90 * 24

// handleGet returns the aggregated insight. Query param:
//
//	period_hours (int, default 168, clamped to [1, 2160])
func (h *InsightHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	period := defaultPeriodHours
	if v := r.URL.Query().Get("period_hours"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			period = n
		}
	}
	if period > maxPeriodHours {
		period = maxPeriodHours
	}

	var entries []alert.HistoryEntry
	if h.provider != nil {
		entries = h.provider.Alerts()
	}

	res := insight.Analyze(entries, time.Now(), time.Duration(period)*time.Hour)

	var posture insight.Posture
	if h.postureFn != nil {
		posture = insight.EvaluatePosture(h.postureFn())
	} else {
		posture = insight.EvaluatePosture(insight.PostureInput{})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"period_hours": period,
		"since":        res.Since,
		"until":        res.Until,
		"threat":       res.Threat,
		"profile":      res.Profile,
		"posture":      posture,
	})
}
