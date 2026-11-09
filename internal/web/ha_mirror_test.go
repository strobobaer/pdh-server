package web

import (
	"context"
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// mirrorStatusJSON baut /v1/status mit Datei-Spiegelung. set überschreibt Felder der
// Einstellungen, mir den Block „mirror“ (nil = Feld fehlt, wie am Zeugen).
func mirrorStatusJSON(t *testing.T, role string, set, mir map[string]any) string {
	t.Helper()
	settings := map[string]any{"tick_s": 1, "fence_s": 10, "takeover_trio_s": 30, "takeover_duo_s": 60, "cooldown_min": 15,
		"max_per_day": 3, "async_after_s": 10, "lag_limit_mb": 16, "slot_keep_gb": 20, "auto_mode": "observe",
		"mirror_on": false, "mirror_nextcloud": false, "mirror_every_s": 60, "mirror_trash_days": 14, "mirror_alarm_min": 5,
		"mirror_final_s": 120, "mirror_bwlimit_kb": 0, "nc_data_dir": "", "nc_app_dir": "/var/www/nextcloud"}
	for k, v := range set {
		settings[k] = v
	}
	st := map[string]any{"ok": true, "configured": true, "mode": "duo", "ca_holder": true, "observe": true, "auto_mode": "observe",
		"self": map[string]any{"id": "pdh1", "kind": "node", "role": role, "term": 3, "version": "0.77.8", "seen": true, "paths": []any{}},
		"peers": []any{map[string]any{"id": "pdh2", "kind": "node", "role": map[string]string{"aktiv": "passiv", "passiv": "aktiv"}[role], "term": 3, "version": "0.77.8", "seen": true,
			"paths": []any{map[string]any{"addr": "10.0.0.2:7480", "alive": true, "last_recv_ago": 0.4}}}},
		"settings": settings, "gateway_targets": []string{"192.168.1.1"}, "warnings": []string{}, "error": "",
		"access": map[string]any{"vip": "10.77.1.50/24", "vip_on": true}}
	if mir != nil {
		m := map[string]any{"on": true, "active": role == "aktiv", "running": false, "first_done": true, "first_percent": 100, "last_ok_ago": 12.0,
			"last_run_s": 3.2, "env_same": true, "nc_ver_same": true, "nc_missing": []string{}, "sources": []any{}, "unproven": []string{}}
		for k, v := range mir {
			m[k] = v
		}
		st["mirror"] = m
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// mirrorSavedSettings: Einstellungen aus dem letzten PUT /v1/settings der Attrappe.
func mirrorSavedSettings(t *testing.T, f *fakeHA) (map[string]any, []string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i] == "PUT /v1/settings" {
			var body struct {
				Settings       map[string]any `json:"settings"`
				GatewayTargets []string       `json:"gateway_targets"`
			}
			if err := json.Unmarshal([]byte(f.bodies[i]), &body); err != nil {
				t.Fatal(err)
			}
			return body.Settings, body.GatewayTargets
		}
	}
	return nil, nil
}

func (f *fakeHA) count(call string) int {
	n := 0
	for _, c := range f.callList() {
		if c == call {
			n++
		}
	}
	return n
}

// Review Focus 1: Das Formular „Zeiten“ darf die Datei-Spiegelung nie zurücksetzen.
func TestHASettingsKeepMirror(t *testing.T) {
	dir := "/srv/nextcloud/data"
	set := map[string]any{"mirror_on": true, "mirror_nextcloud": true, "mirror_every_s": 90, "mirror_trash_days": 30, "mirror_alarm_min": 7,
		"mirror_final_s": 100, "mirror_bwlimit_kb": 500, "nc_data_dir": dir, "nc_app_dir": dir}
	f := &fakeHA{status: mirrorStatusJSON(t, "aktiv", set, map[string]any{}), events: `[]`}
	startFakeHA(t, f)
	form := url.Values{"tick_s": {"1"}, "fence_s": {"10"}, "takeover_trio_s": {"30"}, "takeover_duo_s": {"60"}, "cooldown_min": {"20"}, "max_per_day": {"3"},
		"async_after_s": {"10"}, "lag_limit_mb": {"16"}, "slot_keep_gb": {"20"}, "gateway_targets": {"192.168.1.1"}}
	if _, err := (&Handler{}).haDoSettings(context.Background(), form); err != nil {
		t.Fatal(err)
	}
	got, _ := mirrorSavedSettings(t, f)
	if got == nil {
		t.Fatalf("kein PUT: %v", f.callList())
	}
	if got["cooldown_min"] != float64(20) {
		t.Errorf("geänderte Zeit nicht gespeichert: %v", got["cooldown_min"])
	}
	for k, want := range set {
		if toJSON(got[k]) != toJSON(want) {
			t.Errorf("%s: %v statt %v (Spiegelung zurückgesetzt)", k, got[k], want)
		}
	}
	// ungültige Zeiten: nichts gelesen, nichts gespeichert (wie bisher)
	n := len(f.callList())
	form.Set("fence_s", "30")
	if _, err := (&Handler{}).haDoSettings(context.Background(), form); err == nil {
		t.Fatal("ungültige Zeiten angenommen")
	}
	if len(f.callList()) != n {
		t.Errorf("Socket trotz ungültiger Zeiten: %v", f.callList()[n:])
	}
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestHAMirrorSettingsSaved(t *testing.T) {
	data := "/srv/nextcloud/data"
	f := &fakeHA{status: mirrorStatusJSON(t, "aktiv", nil, map[string]any{}), events: `[]`}
	startFakeHA(t, f)
	h := &Handler{}
	ctx := context.Background()
	form := url.Values{"mirror_on": {"0", "1"}, "mirror_nextcloud": {"0", "1"}, "mirror_every_s": {"120"}, "mirror_trash_days": {"21"},
		"mirror_alarm_min": {"10"}, "mirror_final_s": {"90"}, "mirror_bwlimit_kb": {"2048"}, "nc_data_dir": {data}, "nc_app_dir": {data}}
	msg, err := h.haDoMirror(ctx, form)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(msg, "150 s") {
		t.Errorf("Warnung bei 90 s: %s", msg)
	}
	got, targets := mirrorSavedSettings(t, f)
	for k, want := range map[string]any{"mirror_on": true, "mirror_nextcloud": true, "mirror_every_s": 120, "mirror_trash_days": 21, "mirror_alarm_min": 10,
		"mirror_final_s": 90, "mirror_bwlimit_kb": 2048, "nc_data_dir": data, "nc_app_dir": data,
		// alles andere bleibt
		"tick_s": 1, "fence_s": 10, "takeover_trio_s": 30, "takeover_duo_s": 60, "cooldown_min": 15, "max_per_day": 3, "async_after_s": 10, "lag_limit_mb": 16, "slot_keep_gb": 20, "auto_mode": "observe"} {
		if toJSON(got[k]) != toJSON(want) {
			t.Errorf("%s: %v statt %v", k, got[k], want)
		}
	}
	if len(targets) != 1 || targets[0] != "192.168.1.1" {
		t.Errorf("Gateway-Ziele nicht behalten: %v", targets)
	}
	// Frist über 150 s: gespeichert, aber mit Warnung
	form.Set("mirror_final_s", "200")
	if msg, err = h.haDoMirror(ctx, form); err != nil || !strings.Contains(msg, "Eine Frist über 150 s kann ein Update mit Rollentausch abbrechen lassen") {
		t.Errorf("Warnung fehlt: %q %v", msg, err)
	}
	// Ungültige Werte: Meldung, kein Speichern
	puts := f.count("PUT /v1/settings")
	for name, mod := range map[string]func(url.Values){
		"Abstand zu klein":  func(v url.Values) { v.Set("mirror_every_s", "5") },
		"Papierkorb 0 Tage": func(v url.Values) { v.Set("mirror_trash_days", "0") },
		"Bandbreite < 0":    func(v url.Values) { v.Set("mirror_bwlimit_kb", "-1") },
		"Nextcloud ohne Datenordner": func(v url.Values) {
			v.Set("nc_data_dir", "")
		},
		"Ordner relativ": func(v url.Values) { v.Set("nc_data_dir", "nextcloud/data") },
		"Ordner /":       func(v url.Values) { v.Set("nc_data_dir", "/") },
	} {
		v := url.Values{}
		for k, x := range form {
			v[k] = x
		}
		v.Set("mirror_final_s", "90")
		mod(v)
		if _, err := h.haDoMirror(ctx, v); err == nil || err.Error() == "" {
			t.Errorf("%s: kein Fehler", name)
		}
	}
	if f.count("PUT /v1/settings") != puts {
		t.Errorf("ungültige Werte gespeichert: %v", f.callList())
	}
	// Fehlende Felder lassen den bisherigen Wert (Formular mit nur einem Feld)
	if _, err := h.haDoMirror(ctx, url.Values{"mirror_trash_days": {"3"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ = mirrorSavedSettings(t, f); toJSON(got["mirror_trash_days"]) != "3" || got["mirror_on"] != false {
		t.Errorf("Teilformular: %v", got)
	}
}

func TestHAMirrorPerms(t *testing.T) {
	f := &fakeHA{status: mirrorStatusJSON(t, "aktiv", nil, map[string]any{}), events: `[]`}
	startFakeHA(t, f)
	post := func(h *Handler, fn http.HandlerFunc) int {
		rec := httptest.NewRecorder()
		fn(rec, mwReq("/admin/server-config/ha/x", url.Values{"mirror_trash_days": {"3"}}))
		return rec.Code
	}
	web := func(h *Handler) map[string]http.HandlerFunc {
		return map[string]http.HandlerFunc{"mirror": h.HAMirrorWeb, "mirror-run": h.HAMirrorRunWeb, "mirror-trash-empty": h.HAMirrorTrashEmptyWeb}
	}
	// ohne system.file_mirror: 403 auf Einstellungen und Abgleich
	h := mwHandler(permFileMirror)
	for _, name := range []string{"mirror", "mirror-run"} {
		if c := post(h, web(h)[name]); c != http.StatusForbidden {
			t.Errorf("%s ohne system.file_mirror: %d", name, c)
		}
	}
	// ohne system.file_mirror_admin: 403 auf das Leeren, auch mit system.file_mirror
	h = mwHandler(permFileMirrorAdmin)
	if c := post(h, web(h)["mirror-trash-empty"]); c != http.StatusForbidden {
		t.Errorf("Papierkorb leeren mit system.file_mirror allein: %d", c)
	}
	// ohne beide
	h = mwHandler(permFileMirror, permFileMirrorAdmin)
	if c := post(h, web(h)["mirror-trash-empty"]); c != http.StatusForbidden {
		t.Errorf("Papierkorb leeren ohne Rechte: %d", c)
	}
	if n := len(f.callList()); n != 0 {
		t.Fatalf("Socket trotz fehlendem Recht aufgerufen: %v", f.callList())
	}
	// mit Recht: durchgereicht
	h = mwHandler()
	if c := post(h, web(h)["mirror-trash-empty"]); c != http.StatusSeeOther && c != http.StatusFound {
		t.Errorf("mit Recht: %d", c)
	}
	if f.count("POST /v1/mirror/trash-empty") != 1 {
		t.Errorf("Leeren nicht weitergereicht: %v", f.callList())
	}
	if perm := areaAdminPerm["file_mirror"]; perm != permFileMirrorAdmin {
		t.Errorf("areaAdminPerm: %q", perm)
	}
	// Kein Nicht-Administrator (kein vpnAdminGuard-Durchlass)
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(""))
	rec := httptest.NewRecorder()
	h.HAMirrorWeb(rec, req)
	if rec.Code == http.StatusSeeOther {
		t.Error("ohne Anmeldung durchgelassen")
	}
}

func TestHAMirrorRunNotBlocking(t *testing.T) {
	old := haMirrorRunWait
	haMirrorRunWait = 100 * time.Millisecond
	defer func() { haMirrorRunWait = old }()
	f := &fakeHA{status: mirrorStatusJSON(t, "passiv", nil, map[string]any{}), events: `[]`,
		replies: map[string]fakeReply{"/v1/mirror/run": {400, `{"ok":false,"error":"Nur auf dem aktiven Server möglich"}`}}}
	startFakeHA(t, f)
	h := &Handler{}
	// schnelle Ablehnung kommt zurück
	if _, err := h.haDoMirrorRun(context.Background(), nil); err == nil || err.Error() != "Nur auf dem aktiven Server möglich" {
		t.Fatalf("Ablehnung: %v", err)
	}
	// langer Lauf: der Aufruf kehrt trotzdem nach kurzer Zeit zurück
	f.mu.Lock()
	f.replies = nil
	f.mu.Unlock()
	if msg, err := h.haDoMirrorRun(context.Background(), nil); err != nil || msg == "" {
		t.Fatalf("Lauf: %q %v", msg, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for haMirrorBusy.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if haMirrorBusy.Load() {
		t.Error("Sperre bleibt hängen")
	}
}

func TestMirrorMigration155(t *testing.T) {
	b, err := os.ReadFile("../../migrations/155_file_mirror.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"'system.file_mirror'", "Datei-Spiegelung einstellen", "'system.file_mirror_admin'", "Datei-Spiegelung: Löschen am Partner freigeben und Papierkorb leeren",
		"'System & Sicherheit'", "o.key = 'system.server_config'", "ON CONFLICT (key) DO NOTHING", "ON CONFLICT DO NOTHING",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("Migration 155: %q fehlt", want)
		}
	}
	// nur aus überall geltenden Quellrechten; beide Rechte in derselben Vergabe
	if n := strings.Count(s, "rp.department_id IS NULL"); n != 1 {
		t.Errorf("Migration 155: department_id-IS-NULL-Bedingung %d-mal, erwartet 1", n)
	}
	if !strings.Contains(s, "p.key IN ('system.file_mirror', 'system.file_mirror_admin')") {
		t.Error("Migration 155: beide Rechte müssen an Rollen mit system.server_config gehen")
	}
	// Nummer frei
	m, _ := os.ReadDir("../../migrations")
	n := 0
	for _, e := range m {
		if strings.HasPrefix(e.Name(), "155_") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("Migrationsnummer 155 %d-mal vergeben", n)
	}
}

func TestMirrorBaseURLWarn(t *testing.T) {
	nodes := []string{"10.0.0.1", "10.0.0.2", "10.66.0.2"}
	for _, c := range []struct{ url, vip, want string }{
		{"https://cloud.example.org", "10.77.1.50/24", ""},
		{"https://cloud.example.org:8443/nc/", "10.77.1.50/24", ""},
		{"https://10.77.1.50", "10.77.1.50/24", ""},       // mitwandernde Adresse
		{"http://10.77.1.50:8080/", "10.77.1.50", ""},     // ohne Präfix
		{"https://10.0.0.2", "10.77.1.50/24", "10.0.0.2"}, // feste Adresse
		{"http://10.0.0.1:8080/nc", "10.77.1.50/24", "10.0.0.1"},
		{"10.66.0.2", "", "10.66.0.2"},            // ohne Schema
		{"https://10.9.9.9", "10.77.1.50/24", ""}, // fremde Adresse
		{"", "10.77.1.50/24", ""},
		{"https://[fd00::2]", "", ""},
		{"::::", "", ""},
	} {
		if got := mirrorBaseURLWarn(c.url, c.vip, nodes); got != c.want {
			t.Errorf("%q (VIP %q): %q statt %q", c.url, c.vip, got, c.want)
		}
	}
}

// mirrorCard: Karte für eine Statusattrappe (Rechte der Sitzung nach can, admin).
func mirrorCard(t *testing.T, status string, can, admin bool, base string) string {
	t.Helper()
	f := &fakeHA{status: status, events: `[]`}
	startFakeHA(t, f)
	v := (&Handler{}).haLoadView(context.Background())
	v.MirrorCan, v.MirrorAdmin, v.NCBaseURL = can, admin, base
	return renderDR(t, drView{HA: v})
}

func TestHAMirrorCardStates(t *testing.T) {
	sub := func(out, what string) {
		t.Helper()
		if !strings.Contains(out, what) {
			t.Errorf("Karte ohne %q", what)
		}
	}
	none := func(out, what string) {
		t.Helper()
		if strings.Contains(out, what) {
			t.Errorf("Karte mit %q", what)
		}
	}
	const form, run, trash = `action="/admin/server-config/ha/mirror"`, `action="/admin/server-config/ha/mirror-run"`, `action="/admin/server-config/ha/mirror-trash-empty"`
	// aus
	out := mirrorCard(t, mirrorStatusJSON(t, "aktiv", nil, map[string]any{"on": false}), true, true, "")
	sub(out, "Datei-Spiegelung")
	sub(out, "Die Spiegelung ist aus.")
	sub(out, form)
	sub(out, `name="mirror_every_s" min="30" max="3600" value="60"`)
	sub(out, `name="mirror_final_s" min="10" max="600" value="120"`)
	none(out, run)
	// läuft, mit Zeit und Quellen
	srcs := []any{map[string]any{"label": "uploads", "last_ok_ago": 4.0}, map[string]any{"label": "nextcloud-data", "last_ok_ago": -1.0, "unproven": true},
		map[string]any{"label": "nextcloud-app", "last_ok_ago": 30.0, "err": "rsync <boom>"}}
	out = mirrorCard(t, mirrorStatusJSON(t, "aktiv", map[string]any{"mirror_on": true}, map[string]any{"sources": srcs, "unproven": []string{"nextcloud-data"}}), true, true, "")
	for _, w := range []string{"bereit", "vor 12,0 s", "Dauer 3,2 s", "uploads", "vor 4,0 s", "noch nie", "ohne Löschen", "rsync &lt;boom&gt;", run, "Jetzt abgleichen"} {
		sub(out, w)
	}
	none(out, "<progress value=\"100\"")
	none(out, trash)
	// Erstabgleich mit Fortschritt
	out = mirrorCard(t, mirrorStatusJSON(t, "aktiv", map[string]any{"mirror_on": true}, map[string]any{"first_done": false, "first_percent": 42, "running": true, "last_ok_ago": -1.0}), true, true, "")
	sub(out, `<progress value="42" max="100"`)
	sub(out, "Erstabgleich")
	// Warnungen
	out = mirrorCard(t, mirrorStatusJSON(t, "aktiv", map[string]any{"mirror_on": true, "mirror_nextcloud": true, "mirror_final_s": 200}, map[string]any{
		"err": "Platte <voll>", "watch_err": "inotify kaputt", "slower_than_interval": true, "watch_limited": true,
		"env_same": false, "nc_ver_same": false, "cert_covers": false, "orphans": 3,
		"nc_missing": []string{"alice/files/<b>x</b>.txt", "bob/ü.pdf"}, "nc_missing_total": 57, "nc_checked": 1000}), true, true, "https://10.0.0.2")
	for _, w := range []string{"Der letzte Abgleich ist fehlgeschlagen", "Platte &lt;voll&gt;", "inotify kaputt", "länger als der eingestellte Abstand",
		"sudo sysctl -w fs.inotify.max_user_watches=", ".env ist auf den beiden Servern verschieden", "verschiedene Fassungen",
		"Zertifikat von Nextcloud enthält die mitwandernde Adresse nicht", "feste Adresse 10.0.0.2 eines Servers",
		"Eine Frist über 150 s kann ein Update mit Rollentausch abbrechen lassen", "Nach der Übernahme fehlen 57 Nextcloud-Dateien",
		"alice/files/&lt;b&gt;x&lt;/b&gt;.txt", "bob/ü.pdf", "… und 55 weitere", "3 Rückkehrer-Dateien"} {
		sub(out, w)
	}
	none(out, "<b>x</b>")
	// Namen und mitwandernde Adresse: keine Adresswarnung; Frist 120 s: keine Fristwarnung
	for _, base := range []string{"https://cloud.example.org", "https://10.77.1.50", ""} {
		out = mirrorCard(t, mirrorStatusJSON(t, "aktiv", map[string]any{"mirror_on": true, "mirror_nextcloud": true}, map[string]any{}), true, true, base)
		none(out, "feste Adresse")
		none(out, "Eine Frist über 150 s")
		none(out, "Der letzte Abgleich ist fehlgeschlagen")
	}
	// Adresswarnung nur mit Nextcloud
	out = mirrorCard(t, mirrorStatusJSON(t, "aktiv", map[string]any{"mirror_on": true}, map[string]any{}), true, true, "https://10.0.0.2")
	none(out, "feste Adresse")
	// Papierkorb: Knopf mit Recht, mit Größe in der Rückfrage
	out = mirrorCard(t, mirrorStatusJSON(t, "aktiv", map[string]any{"mirror_on": true}, map[string]any{"trash_bytes": 3 << 20}), true, true, "")
	sub(out, trash)
	sub(out, "Papierkorb mit 3.0 MB endgültig leeren?")
	out = mirrorCard(t, mirrorStatusJSON(t, "aktiv", map[string]any{"mirror_on": true}, map[string]any{"trash_bytes": 3 << 20}), true, false, "")
	none(out, trash)
	sub(out, "3.0 MB")
	out = mirrorCard(t, mirrorStatusJSON(t, "aktiv", map[string]any{"mirror_on": true}, map[string]any{"trash_bytes": 0}), true, true, "")
	none(out, trash)
	// ohne Recht: nur Anzeige
	out = mirrorCard(t, mirrorStatusJSON(t, "aktiv", map[string]any{"mirror_on": true}, map[string]any{"trash_bytes": 1 << 20}), false, false, "")
	sub(out, "Datei-Spiegelung")
	none(out, form)
	none(out, run)
	none(out, trash)
	sub(out, "darf nur, wer das Recht")
	// passiver Server: nur Anzeige, auch mit Recht (er ist hier auch der erste Server, die Zeiten bleiben aber am aktiven)
	st := mirrorStatusJSON(t, "passiv", map[string]any{"mirror_on": true}, map[string]any{"trash_bytes": 1 << 20})
	st = strings.Replace(st, `"ca_holder":true`, `"ca_holder":false`, 1)
	out = mirrorCard(t, st, true, true, "")
	sub(out, "Dieser Server empfängt die Dateien")
	none(out, form)
	none(out, run)
	none(out, trash)
	// Zeuge und Einzelbetrieb: keine Karte
	witness := mirrorEdit(t, mirrorStatusJSON(t, "zeuge", nil, nil), func(m map[string]any) { m["self"].(map[string]any)["kind"] = "witness" })
	out = mirrorCard(t, witness, true, true, "")
	none(out, "Datei-Spiegelung")
	single := strings.Replace(mirrorStatusJSON(t, "aktiv", nil, map[string]any{}), `"mode":"duo"`, `"mode":"einzel"`, 1)
	out = mirrorCard(t, single, true, true, "")
	none(out, "Datei-Spiegelung")
}

// mirrorEdit ändert eine Statusattrappe (JSON → Karte → JSON).
func mirrorEdit(t *testing.T, status string, fn func(map[string]any)) string {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal([]byte(status), &m); err != nil {
		t.Fatal(err)
	}
	fn(m)
	b, _ := json.Marshal(m)
	return string(b)
}

// R47: „Dieser Server ist vollständig“ nur mit system.file_mirror_admin, nur am aktiven Server
// und nur, wenn eine Quelle wartet; der passive Server lehnt über pdh-ha ab.
func TestHAMirrorConfirm(t *testing.T) {
	const form = `action="/admin/server-config/ha/mirror-confirm"`
	wait := map[string]any{"wait_confirm": []string{"uploads", "nginx"}}
	out := mirrorCard(t, mirrorStatusJSON(t, "aktiv", nil, wait), true, true, "")
	if !strings.Contains(out, form) || !strings.Contains(out, "Dieser Server ist vollständig") || !strings.Contains(out, "(2)") || !strings.Contains(out, "uploads") {
		t.Errorf("Knopf oder Quellen fehlen: %s", out)
	}
	if !strings.Contains(out, "onsubmit=\"return confirm(") || !strings.Contains(out, "Papierkorb") {
		t.Errorf("Rückfrage fehlt: %s", out)
	}
	if out := mirrorCard(t, mirrorStatusJSON(t, "aktiv", nil, wait), true, false, ""); strings.Contains(out, form) {
		t.Errorf("Knopf ohne Recht: %s", out)
	} else if !strings.Contains(out, "uploads") {
		t.Errorf("Hinweis ohne Recht fehlt: %s", out)
	}
	if out := mirrorCard(t, mirrorStatusJSON(t, "passiv", nil, wait), true, true, ""); strings.Contains(out, form) {
		t.Errorf("Knopf am passiven Server: %s", out)
	}
	if out := mirrorCard(t, mirrorStatusJSON(t, "aktiv", nil, map[string]any{}), true, true, ""); strings.Contains(out, form) {
		t.Errorf("Knopf ohne wartende Quelle: %s", out)
	}

	f := &fakeHA{status: mirrorStatusJSON(t, "aktiv", nil, wait), events: `[]`}
	startFakeHA(t, f)
	post := func(h *Handler) int {
		rec := httptest.NewRecorder()
		h.HAMirrorConfirmWeb(rec, mwReq("/admin/server-config/ha/mirror-confirm", url.Values{"labels": {"uploads"}}))
		return rec.Code
	}
	for _, missing := range [][]string{{permFileMirrorAdmin}, {permFileMirror, permFileMirrorAdmin}} {
		if c := post(mwHandler(missing...)); c != http.StatusForbidden {
			t.Errorf("ohne %v: %d", missing, c)
		}
	}
	if f.count("POST /v1/mirror/confirm") != 0 {
		t.Fatalf("Socket ohne Recht: %v", f.callList())
	}
	if c := post(mwHandler()); c != http.StatusSeeOther && c != http.StatusFound {
		t.Errorf("mit Recht: %d", c)
	}
	if f.count("POST /v1/mirror/confirm") != 1 {
		t.Errorf("nicht weitergereicht: %v", f.callList())
	}
	// Ablehnung am passiven Server wird angezeigt und nicht als Erfolg gemeldet
	f.mu.Lock()
	f.replies = map[string]fakeReply{"/v1/mirror/confirm": {400, `{"ok":false,"error":"Nur auf dem aktiven Server möglich"}`}}
	f.mu.Unlock()
	if msg, err := mwHandler().haDoMirrorConfirm(context.Background(), url.Values{"labels": {"uploads"}}); err == nil || !strings.Contains(err.Error(), "aktiven") {
		t.Errorf("Ablehnung: %q %v", msg, err)
	}
}

// R56: Beim Warten zeigt die Karte nicht grün „bereit“, und die Quellenzeile sagt es auch.
func TestHAMirrorWaitingNotReady(t *testing.T) {
	wait := map[string]any{"wait_confirm": []string{"uploads"}, "sources": []map[string]any{
		{"label": "uploads", "last_ok_ago": 5.0, "waiting": true}, {"label": "etc-pdh", "last_ok_ago": 5.0}}}
	out := mirrorCard(t, mirrorStatusJSON(t, "aktiv", nil, wait), true, true, "")
	if strings.Contains(out, ">{{t") || strings.Contains(out, "</span> bereit</span>") {
		t.Errorf("Karte meldet bereit: %s", out)
	}
	if !strings.Contains(out, "wartet auf Bestätigung") {
		t.Errorf("Zustand fehlt: %s", out)
	}
	row := strings.Index(out, "<code class=\"vp-key\">uploads</code>")
	other := strings.Index(out, "<code class=\"vp-key\">etc-pdh</code>")
	if row < 0 || other < row || !strings.Contains(out[row:other], "wartet") {
		t.Errorf("Quellenzeile: %s", out)
	}
	if !strings.Contains(out, "ha-mirror-wait") {
		t.Errorf("Wartemeldung ohne eigene Klasse: %s", out)
	}
	if ready := mirrorCard(t, mirrorStatusJSON(t, "aktiv", nil, map[string]any{}), true, true, ""); !strings.Contains(ready, "bereit") {
		t.Errorf("ohne Warten nicht bereit: %s", ready)
	}
}

// R56: Die Route ist am Router, und ohne Recht führt sie nicht zum Socket.
func TestHAMirrorConfirmRoute(t *testing.T) {
	b, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ReplaceAll(string(b), "\r", ""), `r.Post("/admin/server-config/ha/mirror-confirm", h.HAMirrorConfirmWeb)`) {
		t.Fatal("Route fehlt")
	}
	found := false
	walk := func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method == http.MethodPost && route == "/admin/server-config/ha/mirror-confirm" {
			found = true
		}
		return nil
	}
	if err := chi.Walk((&Handler{}).Routes(), walk); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("Route nicht am Router")
	}
}

// Minor (Runde 4): Bei einem Fehler des Abgleichs oder der Sofort-Übertragung zeigt der Kopf der Karte
// nicht grün „bereit“, sondern „gestört“.
func TestHAMirrorErrorNotReady(t *testing.T) {
	for _, m := range []map[string]any{{"err": "uploads: rsync 23"}, {"watch_err": "inotify"}} {
		out := mirrorCard(t, mirrorStatusJSON(t, "aktiv", nil, m), true, true, "")
		if strings.Contains(out, "</span> bereit</span>") || !strings.Contains(out, "</span> gestört</span>") {
			t.Errorf("Kopf bei %v: %s", m, out[:min(len(out), 2000)])
		}
	}
	if ready := mirrorCard(t, mirrorStatusJSON(t, "aktiv", nil, map[string]any{}), true, true, ""); !strings.Contains(ready, "</span> bereit</span>") {
		t.Errorf("ohne Fehler nicht bereit: %s", ready[:min(len(ready), 2000)])
	}
}

// R61 (T9): Die Karte sendet die angezeigten Quellen als versteckte Felder mit; der Handler reicht sie
// an pdh-ha weiter, und die Meldung nennt, was pdh-ha wirklich freigegeben hat.
func TestHAMirrorConfirmSendsLabels(t *testing.T) {
	wait := map[string]any{"wait_confirm": []string{"uploads", "nginx"}}
	out := mirrorCard(t, mirrorStatusJSON(t, "aktiv", nil, wait), true, true, "")
	for _, l := range []string{"uploads", "nginx"} {
		if !strings.Contains(out, `<input type="hidden" name="labels" value="`+l+`">`) {
			t.Errorf("verstecktes Feld für %s fehlt", l)
		}
	}
	f := &fakeHA{status: mirrorStatusJSON(t, "aktiv", nil, wait), events: `[]`,
		replies: map[string]fakeReply{"/v1/mirror/confirm": {200, `{"ok":true,"labels":["uploads"]}`}}}
	startFakeHA(t, f)
	msg, err := mwHandler().haDoMirrorConfirm(context.Background(), url.Values{"labels": {"uploads", "nginx"}})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	body := strings.Join(f.bodies, " ")
	f.mu.Unlock()
	if !strings.Contains(body, `"labels":["uploads","nginx"]`) {
		t.Errorf("Quellen nicht weitergereicht: %s", body)
	}
	if !strings.Contains(msg, "uploads") || strings.Contains(msg, "nginx") {
		t.Errorf("Meldung nennt nicht die freigegebenen Quellen: %q", msg)
	}
}

// R66: Die Web-Route bestätigt nur genannte Quellen; ohne Liste (alte Seite, Formular von Hand)
// lehnt sie ab, statt alle wartenden freizugeben. Die Befehlszeile ohne Liste bleibt erlaubt.
func TestHAMirrorConfirmNeedsLabels(t *testing.T) {
	f := &fakeHA{status: mirrorStatusJSON(t, "aktiv", nil, map[string]any{"wait_confirm": []string{"uploads"}}), events: `[]`}
	startFakeHA(t, f)
	for _, v := range []url.Values{{}, {"labels": {" "}}} {
		if msg, err := mwHandler().haDoMirrorConfirm(context.Background(), v); err == nil || !strings.Contains(err.Error(), "Keine Quelle") {
			t.Errorf("ohne Quellen %v: %q %v", v, msg, err)
		}
	}
	if n := f.count("POST /v1/mirror/confirm"); n != 0 {
		t.Fatalf("ohne Quellen an pdh-ha weitergereicht: %v", f.callList())
	}
}

// R66: Am passiven Server zeigt die Karte einen abgelehnten Empfang als „gestört“ mit dem Grund,
// nicht grün „empfängt“.
func TestHAMirrorRecvErrShown(t *testing.T) {
	out := mirrorCard(t, mirrorStatusJSON(t, "passiv", nil, map[string]any{"recv_err": "uploads: Zielordner ist ein anderer (Kennung fehlt oder weicht ab)"}), true, true, "")
	if strings.Contains(out, "</span> empfängt</span>") || !strings.Contains(out, "</span> gestört</span>") {
		t.Errorf("Kopf bei abgelehntem Empfang: %s", out[:min(len(out), 2000)])
	}
	if !strings.Contains(out, "Der Empfang wird abgelehnt") || !strings.Contains(out, "Zielordner ist ein anderer") {
		t.Errorf("Grund fehlt: %s", out)
	}
	if ok := mirrorCard(t, mirrorStatusJSON(t, "passiv", nil, map[string]any{}), true, true, ""); !strings.Contains(ok, "</span> empfängt</span>") {
		t.Errorf("ohne Ablehnung nicht „empfängt“: %s", ok[:min(len(ok), 2000)])
	}
}

// R66: Fehlermeldungen im Zustand „gestört“ stehen in Textfarbe (Kontrast über 4,5 in beiden Stilen,
// hell und dunkel) mit roter Markierung, nicht in Rot auf rötlichem Grund.
func TestHAMirrorErrorReadable(t *testing.T) {
	out := mirrorCard(t, mirrorStatusJSON(t, "aktiv", nil, map[string]any{"err": "uploads: rsync 23", "watch_err": "inotify",
		"sources": []map[string]any{{"label": "uploads", "last_ok_ago": 5.0, "err": "rsync 23"}}}), true, true, "")
	if n := strings.Count(out, `class="vp-msg err ha-mirror-err"`); n != 2 {
		t.Errorf("Fehlermeldungen ohne lesbare Klasse: %d", n)
	}
	if strings.Contains(out, `style="color:var(--red)">rsync 23`) || !strings.Contains(out, `ha-mirror-srcerr`) {
		t.Errorf("Fehler der Quelle in Rot: %s", out)
	}
}
