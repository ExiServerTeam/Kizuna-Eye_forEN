package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"Kizuna-Eye/pkg/alert"
	"Kizuna-Eye/pkg/insight"
)

type fakeInsightProvider struct{ entries []alert.HistoryEntry }

func (f *fakeInsightProvider) Alerts() []alert.HistoryEntry { return f.entries }

func TestInsightEmpty(t *testing.T) {
	h := NewInsightHandler(&fakeInsightProvider{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/insight", nil)
	rec := httptest.NewRecorder()
	h.handleGet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		PeriodHours int             `json:"period_hours"`
		Threat      insight.Threat  `json:"threat"`
		Profile     insight.Profile `json:"profile"`
		Posture     insight.Posture `json:"posture"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.PeriodHours != defaultPeriodHours {
		t.Errorf("period_hours = %d, want %d", body.PeriodHours, defaultPeriodHours)
	}
	if body.Threat.Total != 0 {
		t.Errorf("threat.total = %d, want 0", body.Threat.Total)
	}
	// Empty lists must be [] in JSON, never null: the frontend iterates them
	// without a null check.
	if body.Threat.Drivers == nil {
		t.Error("threat.drivers is null; want []")
	}
	if body.Profile.Sources == nil {
		t.Error("profile.sources is null; want []")
	}
	if body.Posture.Items == nil {
		t.Error("posture.items is null; want []")
	}
}

func TestInsightNilProviderAndPosture(t *testing.T) {
	// Neither the provider nor the posture callback may be required; a
	// half-wired dashboard must still answer instead of panicking.
	h := NewInsightHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/insight", nil)
	rec := httptest.NewRecorder()
	h.handleGet(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestInsightPopulated(t *testing.T) {
	now := time.Now()
	p := &fakeInsightProvider{entries: []alert.HistoryEntry{
		{Type: "security_ssh_failed", Level: "warning", Message: "接続元: 203.0.113.7", Timestamp: now.Add(-time.Hour)},
		{Type: "security_integrity", Level: "critical", Message: "/etc/passwd が変更されました", Timestamp: now.Add(-2 * time.Hour)},
	}}
	posture := insight.PostureInput{AuthEnabled: true, SecureCookies: true}
	h := NewInsightHandler(p, func() insight.PostureInput { return posture })

	req := httptest.NewRequest(http.MethodGet, "/api/insight", nil)
	rec := httptest.NewRecorder()
	h.handleGet(rec, req)

	var body struct {
		Threat  insight.Threat  `json:"threat"`
		Profile insight.Profile `json:"profile"`
		Posture insight.Posture `json:"posture"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Threat.Total != 2 {
		t.Errorf("threat.total = %d, want 2", body.Threat.Total)
	}
	if body.Threat.Critical != 1 {
		t.Errorf("threat.critical = %d, want 1", body.Threat.Critical)
	}
	if len(body.Profile.Sources) != 1 || body.Profile.Sources[0].IP != "203.0.113.7" {
		t.Errorf("profile.sources = %+v, want one source 203.0.113.7", body.Profile.Sources)
	}
	// auth 25 + public_viewer_off 10 + secure_cookies 5 = 40.
	// (posture scoring is asserted exhaustively in pkg/insight; here we only
	// check the handler wired the callback through.)
	if body.Posture.Score != 40 {
		t.Errorf("posture.score = %d, want 40", body.Posture.Score)
	}
}

func TestInsightPeriodClamped(t *testing.T) {
	h := NewInsightHandler(&fakeInsightProvider{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/insight?period_hours=999999", nil)
	rec := httptest.NewRecorder()
	h.handleGet(rec, req)

	var body struct {
		PeriodHours int `json:"period_hours"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.PeriodHours != maxPeriodHours {
		t.Errorf("period_hours = %d, want clamped to %d", body.PeriodHours, maxPeriodHours)
	}
}

func TestInsightPeriodIgnoresGarbage(t *testing.T) {
	for _, q := range []string{"?period_hours=abc", "?period_hours=-5", "?period_hours=0"} {
		h := NewInsightHandler(&fakeInsightProvider{}, nil)
		req := httptest.NewRequest(http.MethodGet, "/api/insight"+q, nil)
		rec := httptest.NewRecorder()
		h.handleGet(rec, req)

		var body struct {
			PeriodHours int `json:"period_hours"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode (%s): %v", q, err)
		}
		if body.PeriodHours != defaultPeriodHours {
			t.Errorf("period_hours(%s) = %d, want default %d", q, body.PeriodHours, defaultPeriodHours)
		}
	}
}

func TestInsightSetsNoStore(t *testing.T) {
	h := NewInsightHandler(&fakeInsightProvider{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/insight", nil)
	rec := httptest.NewRecorder()
	h.handleGet(rec, req)
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}
