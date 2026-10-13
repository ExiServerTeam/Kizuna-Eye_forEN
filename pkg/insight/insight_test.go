package insight

import (
	"testing"
	"time"

	"Kizuna-Eye/pkg/alert"
)

func entry(typ, level, msg string, ts time.Time) alert.HistoryEntry {
	return alert.HistoryEntry{Type: typ, Level: level, Message: msg, Timestamp: ts}
}

func TestAnalyzeEmpty(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	res := Analyze(nil, now, 7*24*time.Hour)

	if res.Threat.Total != 0 {
		t.Errorf("Total = %d, want 0", res.Threat.Total)
	}
	if res.Threat.Risk != 0 {
		t.Errorf("Risk = %d, want 0", res.Threat.Risk)
	}
	if res.Threat.Grade != "A" {
		t.Errorf("Grade = %q, want A", res.Threat.Grade)
	}
	if res.Profile.PeakHour != -1 {
		t.Errorf("PeakHour = %d, want -1", res.Profile.PeakHour)
	}
	if res.Threat.Drivers == nil || len(res.Threat.Drivers) != 0 {
		t.Errorf("Drivers = %v, want empty non-nil", res.Threat.Drivers)
	}
	if res.Profile.Sources == nil || len(res.Profile.Sources) != 0 {
		t.Errorf("Sources = %v, want empty non-nil", res.Profile.Sources)
	}
	if res.Period != int64((7 * 24 * time.Hour).Seconds()) {
		t.Errorf("Period = %d", res.Period)
	}
}

func TestAnalyzeDefaultsZeroInputs(t *testing.T) {
	// A zero now / non-positive period must not panic and must use defaults.
	res := Analyze(nil, time.Time{}, 0)
	if res.Period != int64(DefaultPeriod.Seconds()) {
		t.Errorf("Period = %d, want default", res.Period)
	}
	if res.Until.IsZero() {
		t.Error("Until is zero; a default now should have been applied")
	}
}

func TestAnalyzeCountsAndRisk(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	entries := []alert.HistoryEntry{
		entry("security_integrity", "critical", "改ざん", now.Add(-1*time.Hour)),
		entry("security_suid", "critical", "pwned", now.Add(-2*time.Hour)),
		entry("security_ssh_failed", "warning", "接続元: 10.0.0.5", now.Add(-3*time.Hour)),
		entry("agent_recovery", "success", "復帰しました", now.Add(-4*time.Hour)),
	}
	res := Analyze(entries, now, 7*24*time.Hour)

	if res.Threat.Total != 4 {
		t.Errorf("Total = %d, want 4", res.Threat.Total)
	}
	if res.Threat.Critical != 2 {
		t.Errorf("Critical = %d, want 2", res.Threat.Critical)
	}
	if res.Threat.Warning != 1 {
		t.Errorf("Warning = %d, want 1", res.Threat.Warning)
	}
	if res.Threat.Success != 1 {
		t.Errorf("Success = %d, want 1", res.Threat.Success)
	}
	if res.Threat.Info != 0 {
		t.Errorf("Info = %d, want 0", res.Threat.Info)
	}
	// 8 + 8 + 3 + 0
	if res.Threat.Risk != 19 {
		t.Errorf("Risk = %d, want 19", res.Threat.Risk)
	}
	// goodness 81 -> B
	if res.Threat.Grade != "B" {
		t.Errorf("Grade = %q, want B", res.Threat.Grade)
	}
	if res.Threat.Sources != 1 {
		t.Errorf("Sources = %d, want 1", res.Threat.Sources)
	}

	// Drivers exclude the zero-weight success alert.
	if len(res.Threat.Drivers) != 3 {
		t.Fatalf("Drivers = %+v, want 3", res.Threat.Drivers)
	}
	if res.Threat.Drivers[0].Key != "integrity" {
		t.Errorf("Drivers[0].Key = %q, want integrity", res.Threat.Drivers[0].Key)
	}
	if res.Threat.Drivers[0].Weight != 8 {
		t.Errorf("Drivers[0].Weight = %d, want 8", res.Threat.Drivers[0].Weight)
	}
	if res.Threat.Drivers[2].Key != "ssh_failed" {
		t.Errorf("Drivers[2].Key = %q, want ssh_failed", res.Threat.Drivers[2].Key)
	}
}

func TestAnalyzeRiskIsCappedAt100(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	var entries []alert.HistoryEntry
	for i := 0; i < 20; i++ {
		entries = append(entries, entry("security_integrity", "critical", "x", now.Add(-time.Duration(i)*time.Minute)))
	}
	res := Analyze(entries, now, 24*time.Hour)
	if res.Threat.Risk != 100 {
		t.Errorf("Risk = %d, want capped at 100", res.Threat.Risk)
	}
	if res.Threat.Grade != "E" {
		t.Errorf("Grade = %q, want E", res.Threat.Grade)
	}
}

func TestAnalyzeFiltersOutsideWindow(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	entries := []alert.HistoryEntry{
		entry("security_integrity", "critical", "old", now.Add(-30*24*time.Hour)),
		entry("security_integrity", "critical", "new", now.Add(-1*time.Hour)),
	}
	res := Analyze(entries, now, 7*24*time.Hour)
	if res.Threat.Total != 1 {
		t.Errorf("Total = %d, want 1 (old entry must be excluded)", res.Threat.Total)
	}
}

func TestAnalyzeSourceProfile(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	entries := []alert.HistoryEntry{
		entry("security_ssh_failed", "warning", "接続元: 10.0.0.5", now.Add(-2*time.Hour)),
		entry("security_suid", "critical", "10.0.0.5 に SUID ビット付きのファイルが出現しました。", now.Add(-1*time.Hour)),
		entry("security_ssh_failed", "warning", "接続元: 10.0.0.9", now.Add(-30*time.Minute)),
	}
	res := Analyze(entries, now, 24*time.Hour)

	if res.Threat.Sources != 2 {
		t.Fatalf("Sources = %d, want 2", res.Threat.Sources)
	}
	// The address with the critical alert must be first.
	if res.Profile.Sources[0].IP != "10.0.0.5" {
		t.Fatalf("Sources[0].IP = %q, want 10.0.0.5", res.Profile.Sources[0].IP)
	}
	s := res.Profile.Sources[0]
	if s.Count != 2 || s.Critical != 1 || s.Warning != 1 {
		t.Errorf("counts = %d/%d/%d, want 2/1/1", s.Count, s.Critical, s.Warning)
	}
	if !s.MultiTechnique {
		t.Error("MultiTechnique = false, want true for two distinct categories")
	}
	if len(s.Categories) != 2 || s.Categories[0] != "ssh_failed" || s.Categories[1] != "suid" {
		t.Errorf("Categories = %v, want [ssh_failed suid]", s.Categories)
	}
	if !s.LastSeen.After(s.FirstSeen) {
		t.Errorf("LastSeen %v not after FirstSeen %v", s.LastSeen, s.FirstSeen)
	}
	if res.Profile.Sources[1].MultiTechnique {
		t.Error("single-technique source marked MultiTechnique")
	}
}

func TestAnalyzeHourlyHistogram(t *testing.T) {
	day := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	now := day.Add(12 * time.Hour)
	entries := []alert.HistoryEntry{
		entry("security_ssh_failed", "warning", "a", day.Add(3*time.Hour)),
		entry("security_ssh_failed", "warning", "b", day.Add(3*time.Hour+10*time.Minute)),
		entry("security_ssh_failed", "warning", "c", day.Add(5*time.Hour)),
	}
	res := Analyze(entries, now, 24*time.Hour)

	if res.Profile.Hourly[3] != 2 {
		t.Errorf("Hourly[3] = %d, want 2", res.Profile.Hourly[3])
	}
	if res.Profile.Hourly[5] != 1 {
		t.Errorf("Hourly[5] = %d, want 1", res.Profile.Hourly[5])
	}
	if res.Profile.PeakHour != 3 {
		t.Errorf("PeakHour = %d, want 3", res.Profile.PeakHour)
	}
}

func TestAnalyzeCapsLists(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	var entries []alert.HistoryEntry
	for i := 0; i < 15; i++ {
		entries = append(entries, entry("security_cat"+string(rune('a'+i)), "warning", "x", now.Add(-time.Minute)))
	}
	res := Analyze(entries, now, 24*time.Hour)
	if len(res.Profile.Categories) != maxList {
		t.Errorf("Categories = %d, want capped at %d", len(res.Profile.Categories), maxList)
	}
	if len(res.Threat.Drivers) != maxList {
		t.Errorf("Drivers = %d, want capped at %d", len(res.Threat.Drivers), maxList)
	}
}

func TestCategoryOfNormalizes(t *testing.T) {
	cases := map[string]string{
		"security_ssh_failed":   "ssh_failed",
		"security_SSH failed!!": "ssh_failed",
		"agent_disconnect":      "agent_disconnect",
		"":                      "other",
		"!!!":                   "other",
		"security_":             "other",
	}
	for in, want := range cases {
		if got := CategoryOf(in); got != want {
			t.Errorf("CategoryOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCategoryOfCapsLength(t *testing.T) {
	long := "security_" + string(make([]byte, 0))
	for i := 0; i < 200; i++ {
		long += "a"
	}
	if got := CategoryOf(long); len(got) > 48 {
		t.Errorf("CategoryOf returned %d chars, want <= 48", len(got))
	}
}

func TestExtractIPs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"plain", "接続元: 127.0.0.1 です。", []string{"127.0.0.1"}},
		{"with port", "from 127.0.0.1:59456 ssh2", []string{"127.0.0.1"}},
		{"bind address dropped", "TCP ポート 44444 が新たに待ち受けを開始しました（0.0.0.0）。", nil},
		{"port alone is not an ip", "ポート 44444 が開きました", nil},
		{"time is not an ip", "12:34 に検知", nil},
		{"ipv6 loopback", "接続元: ::1", []string{"::1"}},
		{"deduplicated", "10.0.0.5 と 10.0.0.5", []string{"10.0.0.5"}},
		{"no ip", "改ざんを検知しました", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractIPs(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("ExtractIPs(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("ExtractIPs(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestGradeBoundaries(t *testing.T) {
	cases := map[int]string{
		100: "A", 90: "A", 89: "B", 75: "B", 74: "C",
		60: "C", 59: "D", 40: "D", 39: "E", 0: "E",
	}
	for in, want := range cases {
		if got := Grade(in); got != want {
			t.Errorf("Grade(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestEvaluatePostureAllOn(t *testing.T) {
	p := EvaluatePosture(PostureInput{
		AuthEnabled:          true,
		PublicViewerEnabled:  false,
		SecureCookies:        true,
		AgentTokenSet:        true,
		RequireSignature:     true,
		PublicKeySet:         true,
		AlertHistorySigned:   true,
		NotificationsEnabled: true,
		ListenRestricted:     true,
	})
	if p.Max != 100 {
		t.Errorf("Max = %d, want 100", p.Max)
	}
	if p.Score != 100 {
		t.Errorf("Score = %d, want 100", p.Score)
	}
	if p.Grade != "A" {
		t.Errorf("Grade = %q, want A", p.Grade)
	}
}

func TestEvaluatePostureAllOff(t *testing.T) {
	// PublicViewerEnabled defaults to true in the shipped config, so "all off"
	// means the score is 0 and the grade is E.
	p := EvaluatePosture(PostureInput{PublicViewerEnabled: true})
	if p.Score != 0 {
		t.Errorf("Score = %d, want 0", p.Score)
	}
	if p.Grade != "E" {
		t.Errorf("Grade = %q, want E", p.Grade)
	}
}

func TestEvaluatePostureSignatureNeedsKey(t *testing.T) {
	p := EvaluatePosture(PostureInput{RequireSignature: true, PublicKeySet: false})
	found := false
	for _, it := range p.Items {
		if it.Key == "require_signature" {
			found = true
			if it.OK {
				t.Error("require_signature marked OK without a public key")
			}
		}
	}
	if !found {
		t.Fatal("require_signature item missing")
	}
}

func TestEvaluatePostureItemsAreStable(t *testing.T) {
	// The UI translates posture.<key>; a renamed key would silently show the
	// raw key. Pin the set and the weights.
	p := EvaluatePosture(PostureInput{})
	want := map[string]int{
		"auth_enabled":          25,
		"require_signature":     20,
		"public_viewer_off":     10,
		"agent_token":           10,
		"alert_history_signed":  10,
		"notifications_enabled": 10,
		"listen_restricted":     10,
		"secure_cookies":        5,
	}
	if len(p.Items) != len(want) {
		t.Fatalf("got %d items, want %d", len(p.Items), len(want))
	}
	seen := map[string]bool{}
	for _, it := range p.Items {
		w, ok := want[it.Key]
		if !ok {
			t.Errorf("unexpected posture key %q", it.Key)
			continue
		}
		seen[it.Key] = true
		if it.Weight != w {
			t.Errorf("weight(%s) = %d, want %d", it.Key, it.Weight, w)
		}
	}
	for k := range want {
		if !seen[k] {
			t.Errorf("posture key %q missing", k)
		}
	}
}
