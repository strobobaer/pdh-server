package config

import "testing"

func TestTakeOverPrecedence(t *testing.T) {
	snapshotEnv()
	envMu.Lock()
	origEnv["PDH_TEST_TAKEOVER_X"] = true
	envMu.Unlock()
	t.Cleanup(func() {
		envMu.Lock()
		delete(origEnv, "PDH_TEST_TAKEOVER_X")
		delete(overrideKeys, "PDH_TEST_TAKEOVER_X")
		envMu.Unlock()
	})
	if !FromProcessEnv("PDH_TEST_TAKEOVER_X") || OverridesEnv("PDH_TEST_TAKEOVER_X") {
		t.Fatal("vor der Übernahme: Umgebung hat Vorrang")
	}
	envMu.Lock()
	overrideKeys["PDH_TEST_TAKEOVER_X"] = true
	envMu.Unlock()
	if FromProcessEnv("PDH_TEST_TAKEOVER_X") || !InProcessEnv("PDH_TEST_TAKEOVER_X") || !OverridesEnv("PDH_TEST_TAKEOVER_X") {
		t.Error("nach der Übernahme: Datenbank hat Vorrang, Eintrag steht aber noch in der Umgebung")
	}
	for k, want := range map[string]bool{"PDH_DATABASE_PASSWORD": true, "PDH_SETTINGS_KEY": true, "PDH_UPDATE_AGENT_TOKEN": true,
		"PDH_SERVER_PORT": true, "PDH_GRAFANA_ADMIN_PASSWORD": true, "HOME": true,
		"PDH_MICROSOFT_CLIENT_SECRET": false, "PDH_NEXTCLOUD_PASSWORD": false, "PDH_AUTH_JWTSECRET": false, "PDH_COPILOT_ANTHROPICKEY": false} {
		if TakeOverBlocked(k) != want {
			t.Errorf("%s: blockiert=%v", k, !want)
		}
	}
}
