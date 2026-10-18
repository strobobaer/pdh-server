package web

import (
	"context"
	"strings"
	"testing"
)

func TestEnvTakeoverRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := ServerConfigData{EnvFile: "/app/config/pdh.env", FileWritable: true, AgentReady: true, TakeOverTotal: 2}
	for _, g := range envGroups {
		gv := envGroupView{Key: g.Key, Label: g.Label, Icon: g.Icon, Intro: g.Intro}
		for _, f := range g.Fields {
			v := envFieldView{envField: f, Source: "default"}
			if f.Key == "PDH_NEXTCLOUD_PASSWORD" {
				v.Source, v.StillInEnv = "db", true
			}
			gv.Fields = append(gv.Fields, v)
		}
		switch g.Key {
		case "microsoft":
			gv.TakeOver = []string{"PDH_MICROSOFT_CLIENT_ID", "PDH_MICROSOFT_CLIENT_SECRET"}
			gv.External, gv.TestWhat, gv.TestRecorded = 2, "microsoft", true
		case "nextcloud":
			gv.Removable, gv.CanRemove, gv.TestWhat, gv.TestRecorded = []string{"PDH_NEXTCLOUD_PASSWORD"}, true, "nextcloud", true
			gv.Test = &envTestResult{OK: true, At: "18.10.2026 10:00", Msg: "Angemeldet bei Nextcloud als pdh."}
		case "translate":
			gv.TestWhat, gv.TestRecorded = "deepl", true
			gv.Removable = []string{"PDH_DEEPL_KEY"}
			gv.Test = &envTestResult{OK: false, At: "18.10.2026 09:00", Msg: "Schlüssel ungültig"}
		}
		d.Groups = append(d.Groups, gv)
	}
	out := renderPage(t, tmpl, "server_config", d)
	mustContain(t, "Server-Einstellungen", out,
		"2 sicherheitsrelevante Wert(e)", `name="group" value="all"`, "Alle in die Datenbank übernehmen",
		`<input type="hidden" name="group" value="microsoft"><button class="btn btn-primary" type="submit"><i class="ti ti-database-import"></i> In die Datenbank übernehmen`,
		`action="/admin/server-config/env-remove"`, `<input type="hidden" name="group" value="nextcloud"><button class="btn" type="submit"><i class="ti ti-eraser"></i> Aus .env.docker entfernen`,
		"noch in .env.docker", `hx-post="/admin/server-config/test?what=microsoft"`, `hx-post="/admin/server-config/test?what=deepl"`, `hx-include="#sc-translate"`,
		`sc-dot ok`, `sc-dot bad`, "Zuerst unten testen", "Angemeldet bei Nextcloud als pdh.")
	if strings.Count(out, `action="/admin/server-config/env-remove"`) != 1 {
		t.Error("Entfernen darf nur bei grünem Test angeboten werden")
	}
}

func TestEnvTestsNeedValues(t *testing.T) {
	for _, k := range []string{"PDH_MICROSOFT_TENANT_ID", "PDH_MICROSOFT_CLIENT_ID", "PDH_MICROSOFT_CLIENT_SECRET", "PDH_NEXTCLOUD_BASEURL", "PDH_NEXTCLOUD_USERNAME", "PDH_NEXTCLOUD_PASSWORD", "PDH_DEEPL_KEY"} {
		t.Setenv(k, "")
	}
	ctx := context.Background()
	if _, err := testMicrosoft(ctx, map[string]string{"PDH_MICROSOFT_TENANT_ID": "t"}); err == nil || !strings.Contains(err.Error(), "Client-Secret") {
		t.Errorf("microsoft: %v", err)
	}
	if _, err := testNextcloud(ctx, nil); err == nil {
		t.Error("nextcloud ohne Werte")
	}
	if _, err := testDeepL(ctx, nil); err == nil {
		t.Error("deepl ohne Schlüssel")
	}
	t.Setenv("PDH_DEEPL_KEY", "aus-env")
	if effectiveValue(map[string]string{"PDH_DEEPL_KEY": ""}, "PDH_DEEPL_KEY") != "aus-env" || effectiveValue(map[string]string{"PDH_DEEPL_KEY": "neu"}, "PDH_DEEPL_KEY") != "neu" {
		t.Error("effectiveValue")
	}
	if envTestGroup("deepl") != "translate" || envTestGroup("microsoft") != "microsoft" || envTestGroup("x") != "" {
		t.Error("envTestGroup")
	}
}
