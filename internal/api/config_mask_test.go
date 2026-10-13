package api

import (
	"testing"
)

func TestMaskSecrets(t *testing.T) {
	in := map[string]interface{}{
		"agent_token": "real-token",
		"auth": map[string]interface{}{
			"enabled": true,
		},
		"notifications": map[string]interface{}{
			"channels": []interface{}{
				map[string]interface{}{
					"type":        "discord",
					"webhook_url": "https://secret",
				},
			},
		},
		"listen_addr": ":8080",
	}

	out := maskSecrets(in).(map[string]interface{})
	if out["agent_token"] != maskedValue {
		t.Errorf("agent_token not masked: %v", out["agent_token"])
	}
	if out["listen_addr"] != ":8080" {
		t.Errorf("listen_addr should be untouched: %v", out["listen_addr"])
	}
	chans := out["notifications"].(map[string]interface{})["channels"].([]interface{})
	ch := chans[0].(map[string]interface{})
	if ch["webhook_url"] != maskedValue {
		t.Errorf("webhook_url not masked: %v", ch["webhook_url"])
	}
	if ch["type"] != "discord" {
		t.Errorf("type should be untouched: %v", ch["type"])
	}
}

func TestRestoreSecrets(t *testing.T) {
	old := map[string]interface{}{
		"agent_token": "real-token",
	}
	incoming := map[string]interface{}{
		"agent_token": maskedValue,
		"listen_addr": ":9090",
	}

	out := restoreSecrets(incoming, old).(map[string]interface{})
	if out["agent_token"] != "real-token" {
		t.Errorf("masked secret should be restored from old: %v", out["agent_token"])
	}
	if out["listen_addr"] != ":9090" {
		t.Errorf("non-secret should stay: %v", out["listen_addr"])
	}
}

func TestRestoreSecretsKeepsRealValue(t *testing.T) {
	old := map[string]interface{}{
		"agent_token": "old-token",
	}
	incoming := map[string]interface{}{
		"agent_token": "new-real-token",
	}
	out := restoreSecrets(incoming, old).(map[string]interface{})
	if out["agent_token"] != "new-real-token" {
		t.Errorf("a real value must overwrite the old secret: %v", out["agent_token"])
	}
}

func TestRestoreSecretsNested(t *testing.T) {
	old := map[string]interface{}{
		"notifications": map[string]interface{}{
			"channels": []interface{}{
				map[string]interface{}{"webhook_url": "https://real"},
			},
		},
	}
	incoming := map[string]interface{}{
		"notifications": map[string]interface{}{
			"channels": []interface{}{
				map[string]interface{}{"webhook_url": maskedValue},
			},
		},
	}
	out := restoreSecrets(incoming, old).(map[string]interface{})
	chans := out["notifications"].(map[string]interface{})["channels"].([]interface{})
	ch := chans[0].(map[string]interface{})
	if ch["webhook_url"] != "https://real" {
		t.Errorf("nested masked secret should be restored: %v", ch["webhook_url"])
	}
}
