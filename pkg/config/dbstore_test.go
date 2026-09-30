package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingEncryption(t *testing.T) {
	t.Setenv("PDH_SETTINGS_KEY", strings.Repeat("k", 40))
	enc, err := encryptSetting("geheim!")
	if err != nil || !strings.HasPrefix(enc, encPrefix) || strings.Contains(enc, "geheim") {
		t.Fatalf("%q %v", enc, err)
	}
	if v, err := decryptSetting(enc); err != nil || v != "geheim!" {
		t.Errorf("%q %v", v, err)
	}
	if v, _ := decryptSetting("klartext"); v != "klartext" {
		t.Error("Klartext verändert")
	}
	t.Setenv("PDH_SETTINGS_KEY", strings.Repeat("x", 40))
	if _, err := decryptSetting(enc); err == nil {
		t.Error("mit falschem Schlüssel entschlüsselt")
	}
	if e, _ := encryptSetting(""); e != "" {
		t.Error("leerer Wert verschlüsselt")
	}
}

func TestBootstrapAndSecretKeys(t *testing.T) {
	for _, k := range []string{"PDH_DATABASE_PASSWORD", "PDH_DATABASE_HOST", "PDH_SETTINGS_KEY"} {
		if !BootstrapKey(k) {
			t.Errorf("%s muss in der Datei bleiben", k)
		}
	}
	if BootstrapKey("PDH_SMTP_HOST") || BootstrapKey("PDH_AUTH_JWTSECRET") {
		t.Error("gehört in die Datenbank")
	}
	for _, k := range []string{"PDH_SMTP_PASSWORD", "PDH_AUTH_JWTSECRET", "PDH_UPDATE_AGENT_TOKEN", "PDH_CREDENTIALS_KEY", "PDH_COPILOT_ANTHROPICKEY"} {
		if !SecretKey(k) {
			t.Errorf("%s ist geheim", k)
		}
	}
	if SecretKey("PDH_SMTP_HOST") {
		t.Error("Host ist nicht geheim")
	}
}

func TestCommentOutKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	os.WriteFile(path, []byte("# Kopf\nPDH_DATABASE_HOST=db\nPDH_SMTP_HOST=mail\nPDH_SMTP_PASSWORD=geheim\n"), 0o600)
	if err := commentOutKeys(path, []string{"PDH_SMTP_HOST", "PDH_SMTP_PASSWORD"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	if !strings.Contains(s, "PDH_DATABASE_HOST=db") || strings.Contains(s, "\nPDH_SMTP_HOST=") || strings.Contains(s, "geheim") {
		t.Errorf("Datei:\n%s", s)
	}
	vals, _ := ReadEnvFile(path)
	if len(vals) != 1 || vals["PDH_DATABASE_HOST"] != "db" {
		t.Errorf("aktive Werte: %v", vals)
	}
	baks, _ := filepath.Glob(path + ".bak-*")
	if len(baks) != 1 {
		t.Errorf("Sicherungskopie fehlt: %v", baks)
	}
}
