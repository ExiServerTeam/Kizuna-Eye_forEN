package config

import "testing"

// TestValidateForwardURL: only http/https with a host is accepted; empty is ok.
func TestValidateForwardURL(t *testing.T) {
	cases := []struct {
		raw     string
		wantErr bool
	}{
		{"", false},
		{"   ", false},
		{"http://192.168.0.10:9200/log", false},
		{"https://collector.example.com/ingest", false},
		{"ftp://x/log", true},
		{"file:///etc/passwd", true},
		{"http://", true},
	}
	for _, tc := range cases {
		err := ValidateForwardURL(tc.raw)
		if tc.wantErr && err == nil {
			t.Errorf("ValidateForwardURL(%q) = nil, want error", tc.raw)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("ValidateForwardURL(%q) = %v, want nil", tc.raw, err)
		}
	}
}

// TestLoadDashboardConfigRejectsBadForwardURL: a bad log_forward.url fails load.
func TestLoadDashboardConfigRejectsBadForwardURL(t *testing.T) {
	p := writeTemp(t, `{"config_version":2,"listen_addr":":8080","log_forward":{"url":"ftp://x"}}`)
	if _, err := LoadDashboardConfig(p); err == nil {
		t.Fatal("LoadDashboardConfig must reject an ftp log_forward.url")
	}
}

// TestForwardTimeoutDefault: <=0 uses 5s.
func TestForwardTimeoutDefault(t *testing.T) {
	var c LogForwardConfig
	if c.Enabled() {
		t.Fatal("empty url must be disabled")
	}
	if got := c.Timeout().Seconds(); got != 5 {
		t.Fatalf("default timeout = %v, want 5", got)
	}
	c = LogForwardConfig{URL: " http://x ", TimeoutSec: 30}
	if !c.Enabled() {
		t.Fatal("non-empty url must be enabled")
	}
	if got := c.Timeout().Seconds(); got != 30 {
		t.Fatalf("timeout = %v, want 30", got)
	}
}
