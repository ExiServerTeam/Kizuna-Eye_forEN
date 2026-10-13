package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"Kizuna-Eye/pkg/alert"
)

type stubAlertProvider struct{ list []alert.HistoryEntry }

func (s stubAlertProvider) Alerts() []alert.HistoryEntry { return s.list }

func TestAlertsFilterAndLimit(t *testing.T) {
	prov := stubAlertProvider{list: []alert.HistoryEntry{
		{Type: "memory", Level: "critical", Title: "m1", Timestamp: time.Now()},
		{Type: "disk", Level: "warning", Title: "d1", Timestamp: time.Now()},
		{Type: "cpu_temp", Level: "critical", Title: "c1", Timestamp: time.Now()},
	}}
	h := NewAlertHandler(prov)

	type respT struct {
		Alerts []alert.HistoryEntry `json:"alerts"`
		Count  int                  `json:"count"`
	}

	rr := httptest.NewRecorder()
	h.handleList(rr, httptest.NewRequest(http.MethodGet, "/api/alerts?level=critical", nil))
	var resp respT
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 2 {
		t.Errorf("critical count = %d, want 2", resp.Count)
	}

	rr = httptest.NewRecorder()
	h.handleList(rr, httptest.NewRequest(http.MethodGet, "/api/alerts?limit=1", nil))
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Count != 1 {
		t.Errorf("limit count = %d, want 1", resp.Count)
	}
}

func TestAlertsNilProvider(t *testing.T) {
	h := NewAlertHandler(nil)
	rr := httptest.NewRecorder()
	h.handleList(rr, httptest.NewRequest(http.MethodGet, "/api/alerts", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
}
