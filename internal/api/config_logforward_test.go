package api

import "testing"

// M-1 regression: the API save path must reject a log_forward.url that is not
// http/https or has no host (it previously accepted file://, gopher://, etc.).
func TestValidateLogForward(t *testing.T) {
	cases := []struct {
		name    string
		raw     interface{}
		wantErr bool
	}{
		{"empty block is ok", map[string]interface{}{}, false},
		{"empty url is ok", map[string]interface{}{"url": ""}, false},
		{"http ok", map[string]interface{}{"url": "http://192.168.0.10:9200"}, false},
		{"https ok", map[string]interface{}{"url": "https://collector.example/logs"}, false},
		{"file scheme rejected", map[string]interface{}{"url": "file:///etc/passwd"}, true},
		{"gopher scheme rejected", map[string]interface{}{"url": "gopher://127.0.0.1:11211/"}, true},
		{"host-less rejected", map[string]interface{}{"url": "http://"}, true},
		{"non-map is ignored", "not-a-map", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateLogForward(c.raw)
			if (err != nil) != c.wantErr {
				t.Fatalf("validateLogForward(%v) err=%v, wantErr=%v", c.raw, err, c.wantErr)
			}
		})
	}
}
