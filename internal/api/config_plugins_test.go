package api

import "testing"

// TestValidatePluginsBlock pins the fail-closed check: a signature policy that
// cannot be enforced (require_signature=true without a key) must be rejected at
// save time. Saving it was accepted before, and the agent then refused to start
// on the next restart.
func TestValidatePluginsBlock(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"nil block is allowed", nil, false},
		{"empty block is allowed", map[string]interface{}{}, false},
		{"signature off is allowed", map[string]interface{}{
			"require_signature": false,
		}, false},
		{"signature on with key is allowed", map[string]interface{}{
			"require_signature": true,
			"public_key_file":   "/etc/kizuna-eye/plugin_signing.pub",
		}, false},
		{"signature on without key is rejected", map[string]interface{}{
			"require_signature": true,
		}, true},
		{"signature on with blank key is rejected", map[string]interface{}{
			"require_signature": true,
			"public_key_file":   "   ",
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePluginsBlock(tc.value)
			if tc.wantErr && err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
		})
	}
}
