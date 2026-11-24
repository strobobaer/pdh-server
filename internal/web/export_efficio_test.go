package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/modules/energie"
	"pdh/pkg/database"
)

func TestEfficioConfigDefaults(t *testing.T) {
	c, err := efficioConfig("abc", map[string]string{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.RunEvery != 15*time.Minute || c.Interval != time.Minute || c.Mode != energie.ModeEnd || c.FileDays != 1 ||
		c.RetentionDays != 30 || c.Prefix != "efficio" || c.ConnectionID != "abc" {
		t.Errorf("Vorgaben: %+v", c)
	}
	if c.CSV.TimeFormat != "dmy" || c.CSV.TimeBase != energie.TimeBaseLocal || !c.CSV.DecimalComma || !c.CSV.Header || c.CSV.Encoding != "utf-8" {
		t.Errorf("CSV: %+v", c.CSV)
	}
	if _, ok := c.Target.(energie.PickupTarget); !ok {
		t.Errorf("Ablage: %T", c.Target)
	}
	// 0 Tage heißt „eine Datei je Lauf“ und ist nicht die Vorgabe
	c, err = efficioConfig("", map[string]string{"file_days": "0", "decimal_comma": "false", "has_header": "false", "run_minutes": "30", "interval_minutes": "10"}, nil)
	if err != nil || c.FileDays != 0 || c.CSV.DecimalComma || c.CSV.Header || c.RunEvery != 30*time.Minute || c.Interval != 10*time.Minute {
		t.Errorf("Abweichungen: %+v %v", c, err)
	}
}

func TestEfficioConfigRejects(t *testing.T) {
	drive := func(id string) (*networkDrive, error) {
		return &networkDrive{ID: id, Name: "NAS", Kind: "local", Source: "/srv/nas", UseImportExport: id == "ok"}, nil
	}
	cases := map[string]map[string]string{
		"Takt 7":                 {"run_minutes": "7"},
		"Raster 10 bei Takt 15":  {"run_minutes": "15", "interval_minutes": "10"},
		"Raster größer als Takt": {"run_minutes": "5", "interval_minutes": "15"},
		"Tage -1":                {"file_days": "-1"},
		"Tage Text":              {"file_days": "viele"},
		"Aufbewahrung 0":         {"retention_days": "0"},
		"Präfix mit Pfad":        {"file_prefix": "../x"},
		"Zeitformat":             {"time_format": "us"},
		"Zeitbezug":              {"time_base": "cet"},
		"Zeichensatz":            {"encoding": "ebcdic"},
		"Ablage unbekannt":       {"target": "cloud"},
		"drive_folder mit ..":    {"target": "drive", "drive_id": "ok", "drive_folder": "a/../../b"},
		"Laufwerk fehlt":         {"target": "drive"},
		"Laufwerk ohne Freigabe": {"target": "drive", "drive_id": "nein"},
		"FTP ohne Host":          {"target": "ftp"},
		"SFTP ohne Host":         {"target": "sftp", "ftp_host": " "},
		"FTP-Port":               {"target": "ftp", "ftp_host": "h", "ftp_port": "70000"},
		"FTP-Ordner mit ..":      {"target": "ftp", "ftp_host": "h", "ftp_folder": "../x"},
		"Fingerabdruck":          {"target": "sftp", "ftp_host": "h", "sftp_host_key": "MD5:abc"},
		"Anlagen-ID":             {"filter_infra_ids": "keine-uuid"},
	}
	for name, cfg := range cases {
		if _, err := efficioConfig("", cfg, drive); err == nil {
			t.Errorf("%s: nicht abgewiesen", name)
		}
	}
	if _, err := efficioConfig("", map[string]string{"run_minutes": "15", "interval_minutes": "10"}, nil); err == nil ||
		!strings.Contains(err.Error(), "teilen") {
		t.Errorf("Meldung zum Raster: %v", err)
	}
	// gültige Ablagen
	c, err := efficioConfig("", map[string]string{"target": "drive", "drive_id": "ok", "drive_folder": "/energie/efficio/"}, drive)
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := c.Target.(energie.DirTarget); !ok || !strings.HasSuffix(strings.ReplaceAll(d.Dir, `\`, "/"), "/srv/nas/energie/efficio") {
		t.Errorf("Laufwerk: %+v", c.Target)
	}
	if c, err = efficioConfig("", map[string]string{"target": "sftp", "ftp_host": "h", "ftp_password": "x",
		"sftp_host_key": "SHA256:" + strings.Repeat("A", 43)}, nil); err != nil {
		t.Fatal(err)
	} else if s, ok := c.Target.(energie.SFTPTarget); !ok || s.Port != 22 || s.HostKey == "" {
		t.Errorf("SFTP: %+v", c.Target)
	}
	if c, _ = efficioConfig("", map[string]string{"target": "ftp", "ftp_host": "h"}, nil); c.Target.(energie.FTPTarget).Port != 21 {
		t.Errorf("FTP-Port: %+v", c.Target)
	}
}

func TestEfficioPasswordSealed(t *testing.T) {
	h := &Handler{jwtSecret: "t"}
	fields, ok := connectionKindFields("export", efficioKind)
	if !ok {
		t.Fatal("Art efficio fehlt")
	}
	found := false
	for _, f := range fields {
		found = found || f == "ftp_password"
	}
	if !found || !connSecretFields["ftp_password"] {
		t.Fatal("ftp_password ist kein Geheimfeld")
	}
	sealed, err := sealConnConfig(h.connKey(), map[string]string{"ftp_host": "h", "ftp_password": "Geheim123"})
	if err != nil {
		t.Fatal(err)
	}
	if sealed["ftp_password"] != "" || sealed["ftp_password_enc"] == "" || strings.Contains(sealed["ftp_password_enc"], "Geheim123") {
		t.Errorf("nicht verschlüsselt: %+v", sealed)
	}
	pub, _ := json.Marshal(publicConnConfig(sealed))
	if strings.Contains(string(pub), "Geheim123") || strings.Contains(string(pub), "ftp_password") {
		t.Errorf("Kennwort im öffentlichen Teil: %s", pub)
	}
	plain, err := openConnConfig(h.connKey(), sealed)
	if err != nil || plain["ftp_password"] != "Geheim123" {
		t.Errorf("Entschlüsseln: %v %v", plain, err)
	}
	// Bearbeiten mit leerem Kennwortfeld behält das gespeicherte
	kept := keepStoredSecrets(map[string]string{"ftp_password": ""}, sealed, fields)
	if kept["ftp_password_enc"] != sealed["ftp_password_enc"] {
		t.Error("gespeichertes Kennwort geht beim Bearbeiten verloren")
	}
	// Fehlertexte der Konfiguration nennen das Kennwort nie
	if _, err := efficioConfig("", map[string]string{"target": "sftp", "ftp_host": "h", "ftp_password": "Geheim123", "sftp_host_key": "Geheim123"}, nil); err == nil || strings.Contains(err.Error(), "Geheim123") {
		t.Errorf("Fehlertext: %v", err)
	}
}

func efficioReq(method, path, id, fileID string, form url.Values) *http.Request {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", id)
	rc.URLParams.Add("fileId", fileID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
}

func TestEfficioPagesRequirePermission(t *testing.T) {
	var asked []string
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { asked = append(asked, p); return false }}
	id := "11111111-1111-4111-8111-111111111111"
	for _, c := range []struct {
		name, method, path, want string
		fn                       http.HandlerFunc
	}{
		{"Herunterladen", "GET", "/export/connections/" + id + "/efficio/files/" + id, "export.read", h.EfficioDownload},
		{"Jetzt exportieren", "POST", "/export/connections/" + id + "/efficio/run-web", "export.write", h.EfficioRunWeb},
		{"Verbindung testen", "POST", "/export/connections/" + id + "/efficio/test-web", "export.write", h.EfficioTestWeb},
	} {
		asked = nil
		w := httptest.NewRecorder()
		c.fn(w, efficioReq(c.method, c.path, id, id, nil))
		if w.Code != http.StatusForbidden {
			t.Errorf("%s ohne Recht: Status %d", c.name, w.Code)
		}
		if len(asked) == 0 || asked[0] != c.want {
			t.Errorf("%s: geprüft %v, erwartet %s", c.name, asked, c.want)
		}
	}
	w := httptest.NewRecorder()
	h.EfficioPage(w, efficioReq("GET", "/export/connections/"+id+"/efficio", id, "", nil))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Errorf("Seite ohne Recht: %d %s", w.Code, w.Header().Get("Location"))
	}
	// Einstellungen der Energiedaten ändern: nur energy.admin
	asked = nil
	w = httptest.NewRecorder()
	h.EnergySettingsWeb(w, efficioReq("POST", "/energy/settings-web", "", "", url.Values{"stale_minutes": {"15"}}))
	if w.Code != http.StatusForbidden || len(asked) == 0 || asked[0] != permEnergyAdmin {
		t.Errorf("Einstellungen ohne Recht: %d %v", w.Code, asked)
	}
}

func TestEfficioGateRules(t *testing.T) {
	_, perm, _, _, hit := matchGate("POST", "/energy/settings-web")
	if !hit || perm != permEnergyAdmin {
		t.Errorf("Einstellungen: Treffer %v Recht %q", hit, perm)
	}
}

func TestEfficioInRegistries(t *testing.T) {
	var found bool
	for _, o := range exportKindOptions {
		found = found || (o.Value == efficioKind && o.Label == "Efficio-CSV (Energiedaten)")
	}
	if !found || !exportableKind(efficioKind) || exportKindLabel(efficioKind) == efficioKind {
		t.Error("Art efficio ist nicht überall eingetragen")
	}
	tools := connectionTools("export", efficioKind, "X", nil)
	if len(tools) != 2 || tools[0].Label != "Dateien & Protokoll" || tools[1].Label != "Messstellen" {
		t.Errorf("Werkzeuge: %+v", tools)
	}
	for _, f := range []string{"decimal_comma", "ftp_tls", "has_header"} {
		if !booleanConfigFields[f] {
			t.Errorf("%s ist kein Ja/Nein-Feld", f)
		}
	}
}

// ── mit Datenbank ────────────────────────────────────────────────────────

type efficioEnv struct {
	pool *pgxpool.Pool
	h    *Handler
	id   string
}

func newEfficioEnv(t *testing.T, cfg map[string]string) *efficioEnv {
	t.Helper()
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: pool, jwtSecret: "t", hasPermHook: func(*http.Request, string) bool { return true }}
	// keine echten Messstellen: der Export filtert auf eine Anlage, die es nicht gibt
	full := map[string]string{"filter_infra_ids": "99999999-9999-4999-8999-999999999999"}
	for k, v := range cfg {
		full[k] = v
	}
	sealed, err := sealConnConfig(h.connKey(), full)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(sealed)
	env := &efficioEnv{pool: pool, h: h}
	if err := pool.QueryRow(ctx, `INSERT INTO import_export_connections (direction, kind, name, config)
		VALUES ('export', 'efficio', $1, $2) RETURNING id::text`, "efficio-test-"+time.Now().Format("150405.000000"), b).Scan(&env.id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM import_export_connections WHERE id = $1::uuid`, env.id) })
	return env
}

func TestEfficioDownload(t *testing.T) {
	env := newEfficioEnv(t, map[string]string{"encoding": "windows-1252"})
	content := []byte("Zeitstempel;Messstellen-ID;Messwert\r\n15.11.2026 10:15;K\xe4se;4\r\n") // Windows-1252
	var fid string
	if err := env.pool.QueryRow(context.Background(), `INSERT INTO energy_export_files (connection_id, name, period_start, period_end, content, rows)
		VALUES ($1::uuid, 'efficio_20261115_0000.csv', now(), now() + interval '1 day', $2, 1) RETURNING id::text`, env.id, content).Scan(&fid); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	env.h.EfficioDownload(w, efficioReq("GET", "/x", env.id, fid, nil))
	if w.Code != 200 || w.Body.String() != string(content) {
		t.Fatalf("Download: %d %q", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/csv; charset=windows-1252" {
		t.Errorf("Content-Type %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, `filename=efficio_20261115_0000.csv`) {
		t.Errorf("Content-Disposition %q", cd)
	}
	// Der erste Download macht die Datei zu einer verschickten (eine Neurechnung lässt sie dann stehen).
	var first *time.Time
	if err := env.pool.QueryRow(context.Background(), `SELECT fetched_at FROM energy_export_files WHERE id = $1::uuid`, fid).Scan(&first); err != nil || first == nil {
		t.Fatalf("fetched_at nach dem Download: %v %v", first, err)
	}
	env.h.EfficioDownload(httptest.NewRecorder(), efficioReq("GET", "/x", env.id, fid, nil))
	var again *time.Time
	if err := env.pool.QueryRow(context.Background(), `SELECT fetched_at FROM energy_export_files WHERE id = $1::uuid`, fid).Scan(&again); err != nil || again == nil || !again.Equal(*first) {
		t.Errorf("fetched_at nach dem zweiten Download: %v, erwartet %v", again, first)
	}
	// eine fremde Datei-ID unter dieser Verbindung gibt es nicht
	w = httptest.NewRecorder()
	env.h.EfficioDownload(w, efficioReq("GET", "/x", env.id, "22222222-2222-4222-8222-222222222222", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("fremde Datei: %d", w.Code)
	}
}

func TestEfficioRunWeb(t *testing.T) {
	env := newEfficioEnv(t, map[string]string{"ftp_password": "Geheim123"})
	w := httptest.NewRecorder()
	env.h.EfficioRunWeb(w, efficioReq("POST", "/x", env.id, "", url.Values{}))
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "notice=") {
		t.Fatalf("Lauf: %d %s", w.Code, w.Header().Get("Location"))
	}
	var n int
	_ = env.pool.QueryRow(context.Background(), `SELECT count(*) FROM energy_export_runs WHERE connection_id = $1::uuid`, env.id).Scan(&n)
	if n != 1 {
		t.Fatalf("Protokollzeilen: %d", n)
	}
	// ohne Messwerte keine Datei
	_ = env.pool.QueryRow(context.Background(), `SELECT count(*) FROM energy_export_files WHERE connection_id = $1::uuid`, env.id).Scan(&n)
	if n != 0 {
		t.Errorf("Dateien ohne Werte: %d", n)
	}
	// die Seite zeigt die Protokollzeile, nie das Kennwort
	d, status := env.h.efficioPageData(efficioReq("GET", "/x", env.id, "", nil), env.id)
	if status != 0 || len(d.Runs) != 1 {
		t.Fatalf("Seitendaten: %d %+v", status, d.Runs)
	}
	out := renderPage(t, loadTestTemplates(t), "export_efficio", d)
	if !strings.Contains(out, "Protokoll") || !strings.Contains(out, d.Runs[0].Started) || !strings.Contains(out, d.Runs[0].ResultLabel) {
		t.Error("Protokollzeile nicht sichtbar")
	}
	if strings.Contains(out, "Geheim123") {
		t.Error("Kennwort auf der Seite")
	}
}

func TestEfficioNotOnMirror(t *testing.T) {
	env := newEfficioEnv(t, map[string]string{})
	env.h.mirror = MirrorInfo{On: true}
	for name, fn := range map[string]http.HandlerFunc{"jetzt": env.h.EfficioRunWeb, "testen": env.h.EfficioTestWeb} {
		w := httptest.NewRecorder()
		fn(w, efficioReq("POST", "/x", env.id, "", url.Values{}))
		if w.Code != http.StatusConflict {
			t.Errorf("%s auf dem Spiegel: Status %d", name, w.Code)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env.h.StartEnergyExporter(ctx) // startet auf dem Spiegel nichts
	time.Sleep(400 * time.Millisecond)
	count := func() int {
		var n int
		_ = env.pool.QueryRow(context.Background(), `SELECT count(*) FROM energy_export_state WHERE connection_id = $1::uuid`, env.id).Scan(&n)
		return n
	}
	if count() != 0 {
		t.Fatal("Spiegel hat exportiert")
	}
	// Gegenprobe: auf dem aktiven Server legt der Takt den Zustand an
	active := &Handler{db: env.pool, jwtSecret: "t"}
	active.StartEnergyExporter(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for count() == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if count() == 0 {
		t.Error("aktiver Server exportiert nicht")
	}
}

// Der Anlagenfilter zeigt Namen statt IDs (Verbindungsseite, Seite „Dateien & Protokoll“, Formular).
func TestEfficioInfraFilterShowsNames(t *testing.T) {
	env := newEfficioEnv(t, map[string]string{})
	ctx := context.Background()
	var parent, child string
	sfx := time.Now().Format("150405.000000")
	if err := env.pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ($1, 'building') RETURNING id::text`, "EfHalle "+sfx).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if err := env.pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type, parent_id) VALUES ($1, 'line', $2::uuid) RETURNING id::text`, "EfLinie "+sfx, parent).Scan(&child); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = env.pool.Exec(ctx, `DELETE FROM infrastructure WHERE id = $1::uuid`, child)
		_, _ = env.pool.Exec(ctx, `DELETE FROM infrastructure WHERE id = $1::uuid`, parent)
	})
	want := "EfHalle " + sfx + " / EfLinie " + sfx

	if got := env.h.efficioDisplay(ctx, "filter_infra_ids", child+","+parent); !strings.Contains(got, want) || strings.Contains(got, child) {
		t.Errorf("Verbindungsseite: %q", got)
	}
	ec := energie.ExportConfig{RunEvery: 15 * time.Minute, Interval: time.Minute, RetentionDays: 30, Prefix: "efficio",
		Target: energie.PickupTarget{}, InfraIDs: []string{child}}
	found := false
	for _, r := range env.h.efficioSettings(ctx, ec) {
		found = found || (r.Label == "Nur Anlagen" && r.Value == want)
	}
	if !found {
		t.Error("Seite Dateien & Protokoll zeigt keine Anlagennamen")
	}
	conns := []ConnectionView{{Kind: efficioKind, ConfigJSON: `{"filter_infra_ids":"` + child + `"}`}}
	var labels map[string]string
	if err := json.Unmarshal([]byte(env.h.efficioInfraJSON(ctx, conns)), &labels); err != nil || labels[child] != want {
		t.Errorf("Formular-Vorbelegung: %v %v", labels, err)
	}
	// Das Formular bindet den Anlagenbaum ein und kennt keine rohe ID-Eingabe mehr
	out := renderPage(t, loadTestTemplates(t), "export", ConnectionsPageData{Direction: "export", CanWrite: true,
		EfficioDefault: energie.DefaultSettings(), EfficioChoices: energie.MinuteChoices, EfficioInfra: "{}"})
	if !strings.Contains(out, "efficio-infra-pick") || !strings.Contains(out, "efficioInfraAdd") || strings.Contains(out, "Anlagen-IDs") {
		t.Error("Formular zeigt den Anlagenbaum nicht")
	}
}

func TestEnergySettingsWeb(t *testing.T) {
	env := newEfficioEnv(t, map[string]string{})
	ctx := context.Background()
	clean := func() { _, _ = env.pool.Exec(ctx, `DELETE FROM app_settings WHERE key LIKE 'energy.%'`) }
	clean()
	t.Cleanup(clean)
	form := func(mod func(url.Values)) url.Values {
		v := url.Values{"stale_minutes": {"20"}, "value_retention_days": {"300"}, "run_minutes": {"30"}, "interval_minutes": {"5"},
			"file_days": {"2"}, "retention_days": {"10"}, "time_format": {"iso"}, "time_base": {"offset"}, "encoding": {"windows-1252"}, "prefix": {"werk1"}, "decimal_comma": {"on"}}
		if mod != nil {
			mod(v)
		}
		return v
	}
	post := func(v url.Values) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		env.h.EnergySettingsWeb(w, efficioReq("POST", "/energy/settings-web", "", "", v))
		return w
	}
	// ungültig: Raster teilt den Takt nicht, negative Tage, keine Zahl
	for name, mod := range map[string]func(url.Values){
		"Raster":    func(v url.Values) { v.Set("run_minutes", "15"); v.Set("interval_minutes", "10") },
		"Tage":      func(v url.Values) { v.Set("file_days", "-1") },
		"Zahl":      func(v url.Values) { v.Set("stale_minutes", "abc") },
		"Zeitbezug": func(v url.Values) { v.Set("time_base", "cet") },
	} {
		w := post(form(mod))
		if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "err=") {
			t.Errorf("%s: %d %s", name, w.Code, w.Header().Get("Location"))
		}
	}
	got, _ := energie.NewStore(env.pool).Settings(ctx)
	if got != energie.DefaultSettings() {
		t.Fatalf("Ungültiges wurde gespeichert: %+v", got)
	}
	// gültig: speichern und lesen; der Reiter zeigt die Werte
	if w := post(form(nil)); w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "notice=") {
		t.Fatalf("Speichern: %d %s", w.Code, w.Header().Get("Location"))
	}
	got, _ = energie.NewStore(env.pool).Settings(ctx)
	if got.StaleMinutes != 20 || got.ValueRetentionDays != 300 || got.RunMinutes != 30 || got.IntervalMinutes != 5 || got.FileDays != 2 ||
		got.RetentionDays != 10 || got.TimeFormat != "iso" || got.TimeBase != energie.TimeBaseOffset || got.Encoding != "windows-1252" || got.Prefix != "werk1" || !got.DecimalComma || got.Header {
		t.Errorf("gelesen: %+v", got)
	}
	d := EnergyMetersData{Tab: "settings", Set: got, SetCanEdit: true, SetChoices: energie.MinuteChoices}
	out := renderPage(t, loadTestTemplates(t), "energy_meters", d)
	for _, want := range []string{`name="stale_minutes"`, `value="20"`, `value="werk1"`, "Vorgaben für neue Efficio-Exporte"} {
		if !strings.Contains(out, want) {
			t.Errorf("Reiter Einstellungen enthält %q nicht", want)
		}
	}
}

// Der Zeitbezug: gültige Werte werden übernommen, die Vorgabe aus den
// Einstellungen ist im Formular für neue Exporte vorausgewählt.
func TestEfficioTimeBase(t *testing.T) {
	for _, v := range []string{energie.TimeBaseLocal, energie.TimeBaseUTC, energie.TimeBaseOffset} {
		c, err := efficioConfig("", map[string]string{"time_base": v}, nil)
		if err != nil || c.CSV.TimeBase != v {
			t.Errorf("%s: %+v %v", v, c.CSV, err)
		}
	}
	def := energie.DefaultSettings()
	def.TimeBase = energie.TimeBaseUTC
	out := renderPage(t, loadTestTemplates(t), "export", ConnectionsPageData{Direction: "export", CanWrite: true,
		EfficioDefault: def, EfficioChoices: energie.MinuteChoices, EfficioInfra: "{}"})
	if !strings.Contains(out, `<option value="utc" selected>`) || strings.Contains(out, `<option value="local" selected>`) {
		t.Error("Vorgabe des Zeitbezugs nicht vorausgewählt")
	}
	d := EnergyMetersData{Tab: "settings", Set: def, SetCanEdit: true, SetChoices: energie.MinuteChoices}
	if out := renderPage(t, loadTestTemplates(t), "energy_meters", d); !strings.Contains(out, `name="time_base"`) ||
		!strings.Contains(out, `<option value="utc" selected>`) {
		t.Error("Reiter Einstellungen zeigt den Zeitbezug nicht")
	}
	if got := (&Handler{}).efficioDisplay(context.Background(), "time_base", "offset"); got != "Ortszeit mit Zeitzonenangabe" {
		t.Errorf("Anzeige: %q", got)
	}
}
