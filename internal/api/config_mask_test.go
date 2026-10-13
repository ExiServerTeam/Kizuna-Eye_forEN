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

// Regression: when channels are reordered, a masked value must not borrow a
// different channel's secret. The old channel order was [discord, telegram];
// the new order is [telegram, discord]. Without the type check, the masked
// telegram token would be filled with the discord webhook and vice versa.
func TestRestoreSecretsArrayReorderDoesNotMisassign(t *testing.T) {
	old := map[string]interface{}{
		"notifications": map[string]interface{}{
			"channels": []interface{}{
				map[string]interface{}{"type": "discord", "webhook_url": "https://discord-real"},
				map[string]interface{}{"type": "telegram", "bot_token": "telegram-real"},
			},
		},
	}
	incoming := map[string]interface{}{
		"notifications": map[string]interface{}{
			"channels": []interface{}{
				map[string]interface{}{"type": "telegram", "bot_token": maskedValue},
				map[string]interface{}{"type": "discord", "webhook_url": maskedValue},
			},
		},
	}

	out := restoreSecrets(incoming, old).(map[string]interface{})
	chans := out["notifications"].(map[string]interface{})["channels"].([]interface{})

	telegram := chans[0].(map[string]interface{})
	if telegram["bot_token"] != "telegram-real" {
		t.Errorf("telegram bot_token must restore its own secret, got %v", telegram["bot_token"])
	}
	discord := chans[1].(map[string]interface{})
	if discord["webhook_url"] != "https://discord-real" {
		t.Errorf("discord webhook_url must restore its own secret, got %v", discord["webhook_url"])
	}
}

// When the element at a position changes type, the masked placeholder must be
// dropped rather than filled with the wrong type's secret.
func TestRestoreSecretsTypeChangeDropsMask(t *testing.T) {
	old := []interface{}{
		map[string]interface{}{"type": "discord", "webhook_url": "https://discord-real"},
	}
	incoming := []interface{}{
		map[string]interface{}{"type": "telegram", "bot_token": maskedValue},
	}
	out := restoreSecrets(incoming, old).([]interface{})
	tg := out[0].(map[string]interface{})
	if tg["bot_token"] != "" {
		t.Errorf("a masked value with no matching old entry must become empty, got %v", tg["bot_token"])
	}
}

// Regression: two channels of the SAME type must each restore their own
// secret. Matching only by type sent every masked discord channel to the
// first old discord channel, losing the second channel's webhook_url.
func TestRestoreSecretsTwoSameTypeChannels(t *testing.T) {
	old := map[string]interface{}{
		"channels": []interface{}{
			map[string]interface{}{"type": "discord", "webhook_url": "https://one"},
			map[string]interface{}{"type": "discord", "webhook_url": "https://two"},
		},
	}
	incoming := map[string]interface{}{
		"channels": []interface{}{
			map[string]interface{}{"type": "discord", "webhook_url": maskedValue},
			map[string]interface{}{"type": "discord", "webhook_url": maskedValue},
		},
	}
	out := restoreSecrets(incoming, old).(map[string]interface{})
	chans := out["channels"].([]interface{})
	if chans[0].(map[string]interface{})["webhook_url"] != "https://one" {
		t.Errorf("first channel = %v, want https://one", chans[0])
	}
	if chans[1].(map[string]interface{})["webhook_url"] != "https://two" {
		t.Errorf("second channel = %v, want https://two (secret must not be overwritten by the first)", chans[1])
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
