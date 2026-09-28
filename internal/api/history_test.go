package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubHistoryHub struct{ list []HistorySample }

func (s stubHistoryHub) HistorySamples() []HistorySample { return s.list }

func TestHistoryHandler(t *testing.T) {
	h := NewHistoryHandler(stubHistoryHub{list: []HistorySample{
		{Timestamp: 1, CPU: 10, Mem: 20, Disk: 30},
		{Timestamp: 2, CPU: 15, Mem: 25, Disk: 35},
	}})
	rr := httptest.NewRecorder()
	h.handleHistory(rr, httptest.NewRequest(http.MethodGet, "/api/history", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp struct {
		Samples []HistorySample `json:"samples"`
		Count   int             `json:"count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 2 || len(resp.Samples) != 2 {
		t.Errorf("count = %d / len = %d, want 2", resp.Count, len(resp.Samples))
	}
}

func TestHistoryHandlerNilHub(t *testing.T) {
	h := NewHistoryHandler(nil)
	rr := httptest.NewRecorder()
	h.handleHistory(rr, httptest.NewRequest(http.MethodGet, "/api/history", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
}
