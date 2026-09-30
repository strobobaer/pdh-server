package web

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"pdh/pkg/config"
)

func TestEnvCatalogueUniqueAndValid(t *testing.T) {
	seen := map[string]bool{}
	for _, g := range envGroups {
		for _, f := range g.Fields {
			if seen[f.Key] {
				t.Errorf("%s doppelt", f.Key)
			}
			seen[f.Key] = true
			if !strings.HasPrefix(f.Key, "PDH_") || f.Label == "" {
				t.Errorf("%+v", f)
			}
			if f.Type == "select" && len(f.Options) == 0 {
				t.Errorf("%s ohne Optionen", f.Key)
			}
		}
	}
	// alle Werte aus .env.example muessen pflegbar sein
	for _, k := range []string{"PDH_DATABASE_PASSWORD", "PDH_AUTH_JWTSECRET", "PDH_SMTP_HOST", "PDH_PUBLIC_URL", "PDH_CREDENTIALS_KEY", "PDH_BACKUP_DIR", "PDH_COPILOT_BACKEND"} {
		if !seen[k] {
			t.Errorf("%s fehlt im Katalog", k)
		}
	}
}

func TestValidateEnvValue(t *testing.T) {
	port, _ := envFieldByKey("PDH_SMTP_PORT")
	pub, _ := envFieldByKey("PDH_PUBLIC_URL")
	tls, _ := envFieldByKey("PDH_SMTP_TLS")
	jwt, _ := envFieldByKey("PDH_AUTH_JWTSECRET")
	for _, c := range []struct {
		f  envField
		v  string
		ok bool
	}{
		{port, "587", true}, {port, "70000", false}, {port, "abc", false},
		{pub, "https://pdh.firma.de", true}, {pub, "pdh.firma.de", false}, {pub, "javascript:alert(1)", false},
		{tls, "tls", true}, {tls, "ssl3", false},
		{jwt, "kurz", false}, {jwt, strings.Repeat("a", 32), true},
		{pub, "a\nb", false},
	} {
		if err := validateEnvValue(c.f, c.v); (err == nil) != c.ok {
			t.Errorf("%s=%q: %v", c.f.Key, c.v, err)
		}
	}
}

func TestCollectEnvUpdatesSecretsKept(t *testing.T) {
	config.FromProcessEnv("PDH_SMTP_HOST") // Schnappschuss der echten Umgebung vor t.Setenv
	t.Setenv("PDH_SMTP_PASSWORD", "alt")
	t.Setenv("PDH_SMTP_HOST", "alt.example.com")
	var mail envGroup
	for _, g := range envGroups {
		if g.Key == "mail" {
			mail = g
		}
	}
	form := url.Values{"PDH_SMTP_HOST": {"neu.example.com"}, "PDH_SMTP_PASSWORD": {""}, "PDH_SMTP_PORT": {"587"}, "PDH_SMTP_TLS": {"starttls"}, "PDH_SMTP_USER": {""}, "PDH_SMTP_FROM": {""}}
	r := httptest.NewRequest("POST", "/admin/server-config/save", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = r.ParseForm()
	up, err := collectEnvUpdates(r, mail)
	if err != nil {
		t.Fatal(err)
	}
	if up["PDH_SMTP_HOST"] != "neu.example.com" {
		t.Errorf("Host nicht übernommen: %v", up)
	}
	if _, ok := up["PDH_SMTP_PASSWORD"]; ok {
		t.Error("leeres Passwortfeld hat das Passwort überschrieben")
	}
}

func TestServerConfigPageRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ServerConfigData{EnvFile: "/app/config/pdh.env", FileWritable: true, CanRestart: true, RestartPending: []string{"Port"},
		PendingImport: []string{"PDH_SMTP_FROM"}, Undecryptable: []string{"PDH_NEXTCLOUD_PASSWORD"}, ImportedAt: "30.09.2026 10:00", ImportedCount: 5}
	for _, g := range envGroups {
		gv := envGroupView{Key: g.Key, Label: g.Label, Icon: g.Icon, Intro: g.Intro}
		for _, f := range g.Fields {
			v := envFieldView{envField: f, Source: "default", Value: f.Default}
			if f.Key == "PDH_DATABASE_HOST" {
				v.Source, v.ReadOnly = "env", true
				gv.External = 1
			}
			if f.Key == "PDH_SMTP_PASSWORD" {
				v.Source, v.IsSet = "db", true
			}
			gv.Fields = append(gv.Fields, v)
		}
		d.Groups = append(d.Groups, gv)
	}
	out := renderPage(t, tmpl, "server_config", d)
	for _, want := range []string{"Server-Einstellungen", "/app/config/pdh.env", "Server neu starten", "Neustart", "PDH_SMTP_PASSWORD",
		"gesetzt – leer lassen", "clear_PDH_SMTP_PASSWORD", "Umgebung", "Verbindung testen", "Testmail an mich", "Neuen Anmelde-Schlüssel", "Datenbank", "/admin/server-config/import", "PDH_SMTP_FROM", "Nicht entschlüsselbar", "am 30.09.2026 10:00"} {
		if !strings.Contains(out, want) {
			t.Errorf("Seite enthält %q nicht", want)
		}
	}
	if strings.Contains(out, `value="alt"`) {
		t.Error("Geheimnis im Klartext ausgegeben")
	}
}

func TestRedactQuery(t *testing.T) {
	q := url.Values{"token": {"abc"}, "tab": {"stock"}, "api_key": {"x"}}
	got := redactQuery(q)
	if strings.Contains(got, "abc") || strings.Contains(got, "=x") || !strings.Contains(got, "tab=stock") {
		t.Errorf("%s", got)
	}
	if maskUID("04A1B2C3") != "…B2C3" || maskUID("12") != "****" {
		t.Error("maskUID")
	}
	if csvSafe([]string{"=SUM(A1)"})[0] != "'=SUM(A1)" {
		t.Error("csvSafe")
	}
}

func TestServerLogsPageRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ServerLogsData{
		Filter: logFilterView{Range: "1h", Limit: 300, Live: true}, Components: []string{"auth", "http"},
		Stats: map[string]int{"error": 2}, Dir: "logs", RowsURL: "/admin/server-config/logs/rows?live=1", CSVURL: "/x.csv", JSONURL: "/x.json", Now: "12:00:00",
		Days: []logDayView{{"2026-09-30", "1.2 MB"}}, CanConfig: true,
		Rows: []logRowView{{Time: "10:00:01", TimeFull: "30.09.2026 10:00:01.000", Level: "error", LevelClass: "lg-error", Component: "http",
			Msg: "POST /inventory/1/book", User: "Max", Status: "500", Duration: "12.5 ms", RequestID: "abc/1",
			ComponentURL: "/admin/server-config/logs?component=http", UserURL: "/admin/server-config/logs?user=Max", RIDURL: "/admin/server-config/logs?rid=abc%2F1",
			Details: []logDetail{{"ip", "10.0.0.5"}}}},
	}
	out := renderPage(t, tmpl, "server_logs", d)
	for _, want := range []string{"Server-Protokoll", "POST /inventory/1/book", "10.0.0.5", "every 5s", "live, aktualisiert 12:00:00",
		"2026-09-30", "Alle Einträge dieser Anfrage", `name="rid"`, "Fehler (24 h)", "/x.csv", "?component=http"} {
		if !strings.Contains(out, want) {
			t.Errorf("Protokollseite enthält %q nicht", want)
		}
	}
}
