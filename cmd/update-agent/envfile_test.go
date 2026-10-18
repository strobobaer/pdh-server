package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommentOutEnvKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env.docker")
	in := "PDH_DATABASE_PASSWORD=geheim\nPDH_MICROSOFT_CLIENT_SECRET=abc123\n# PDH_DEEPL_KEY=alt\nexport PDH_DEEPL_KEY=xyz:fx\nPDH_PUBLIC_URL=https://pdh\n"
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	done, err := commentOutEnvKeys(path, []string{"PDH_MICROSOFT_CLIENT_SECRET", "PDH_DEEPL_KEY", "PDH_NICHT_DA"}, time.Date(2026, 10, 18, 0, 0, 0, 0, time.Local))
	if err != nil || strings.Join(done, ",") != "PDH_MICROSOFT_CLIENT_SECRET,PDH_DEEPL_KEY" {
		t.Fatalf("%v %v", done, err)
	}
	b, _ := os.ReadFile(path)
	out := string(b)
	for _, gone := range []string{"abc123", "xyz:fx"} {
		if strings.Contains(out, gone) {
			t.Errorf("Wert %q steht noch in der Datei:\n%s", gone, out)
		}
	}
	for _, want := range []string{"PDH_DATABASE_PASSWORD=geheim", "PDH_PUBLIC_URL=https://pdh", "# PDH_DEEPL_KEY=alt", "# PDH_MICROSOFT_CLIENT_SECRET=…  → in die PDH-Datenbank übernommen (18.10.2026"} {
		if !strings.Contains(out, want) {
			t.Errorf("fehlt %q:\n%s", want, out)
		}
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Errorf("Rechte geändert: %v", info.Mode().Perm())
	}
	if matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*bak*")); len(matches) > 0 {
		t.Errorf("Klartext-Sicherungskopie angelegt: %v", matches)
	}
	for k, want := range map[string]bool{"PDH_DATABASE_PASSWORD": true, "PDH_UPDATE_AGENT_TOKEN": true, "PDH_GRAFANA_DB_PASSWORD": true, "PDH_SETTINGS_KEY": true,
		"PDH_MICROSOFT_CLIENT_SECRET": false, "PDH_AUTH_JWTSECRET": false} {
		if envRemoveBlocked(k) != want {
			t.Errorf("%s: blockiert=%v", k, !want)
		}
	}
}
