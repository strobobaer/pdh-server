//go:build integration

// Fassungen der Messstellen gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run 'TestEnergyVersion|TestEnergyMeterVersions' ./internal/web/
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/modules/energie"
	"pdh/pkg/database"
)

// versionWebEnv: A ohne Quelle (Zähler), V = [A] mit einem alten Wert vor drei Tagen.
type versionWebEnv struct {
	pool          *pgxpool.Pool
	h             *Handler
	st            *energie.Store
	perms         map[string]bool
	a, ak, v, vk  string
	uid, infra    string
	today, before time.Time
}

func newVersionWebEnv(t *testing.T) *versionWebEnv {
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
	clearRuns := func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM energy_recalc_runs WHERE state IN ('running', 'exporting')`)
	}
	clearRuns()
	e := &versionWebEnv{pool: pool, perms: map[string]bool{}, st: energie.NewStore(pool)}
	e.h = &Handler{db: pool, tmpl: loadTestTemplates(t), hasPermHook: func(_ *http.Request, p string) bool { return e.perms[p] }}
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	var infra string
	if err := pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ($1, 'line') RETURNING id::text`, "Fassungen "+sfx).Scan(&infra); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'Eva', 'Muster', 'admin', '', '') RETURNING id::text`, "fv"+sfx).Scan(&e.uid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		clearRuns()
		_, _ = pool.Exec(c, `DELETE FROM energy_recalc_runs WHERE created_by = $1::uuid`, e.uid)
		_, _ = pool.Exec(c, `DELETE FROM energy_meters WHERE infrastructure_id = $1::uuid`, infra)
		_, _ = pool.Exec(c, `DELETE FROM infrastructure WHERE id = $1::uuid`, infra)
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id = $1::uuid`, e.uid)
	})
	a := &energie.Meter{Key: "FA-" + sfx, Name: "Zähler A", InfraID: infra, Kind: energie.KindCounter, Active: true, Export: true}
	if _, err := e.st.Save(ctx, a, ""); err != nil {
		t.Fatal(err)
	}
	v := &energie.Meter{Key: "FV-" + sfx, Name: "Summe V", InfraID: infra, Kind: energie.KindCounter, Formula: "[" + a.Key + "]", Active: true, Export: true}
	if _, err := e.st.Save(ctx, v, ""); err != nil {
		t.Fatal(err)
	}
	e.a, e.ak, e.v, e.vk, e.infra = a.ID, a.Key, v.ID, v.Key, infra
	now := time.Now()
	e.today = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	e.before = e.today.AddDate(0, 0, -3)
	if _, err := pool.Exec(ctx, `INSERT INTO energy_values (meter_id, ts, value) VALUES ($1::uuid, $2, 1)`, v.ID, now.AddDate(0, 0, -3)); err != nil {
		t.Fatal(err)
	}
	// V gibt es schon länger.
	if _, err := pool.Exec(ctx, `UPDATE energy_formula_versions SET valid_from = $2::date WHERE meter_id = $1::uuid`, v.ID, e.today.AddDate(0, 0, -30).Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *versionWebEnv) save(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return meterDo(e.h.EnergyMeterVersionSaveWeb, "POST", "/energy/meters/"+e.v+"/versions/save-web", e.v, e.uid, body)
}

func TestEnergyVersionSaveRights(t *testing.T) {
	e := newVersionWebEnv(t)
	e.perms = map[string]bool{permEnergyView: true, permEnergyEdit: true}
	day := func(d time.Time) string { return d.Format("2006-01-02") }

	w := e.save(t, map[string]any{"valid_from": day(e.today.AddDate(0, 0, 1)), "formula": "[" + e.ak + "]*2"})
	if w.Code != http.StatusOK {
		t.Fatalf("geplante Fassung mit energy.edit: %d %s", w.Code, w.Body.String())
	}
	w = e.save(t, map[string]any{"valid_from": day(e.today), "formula": "[" + e.ak + "]*3", "recalc": true})
	if w.Code != http.StatusForbidden {
		t.Errorf("Neurechnung ohne energy.admin: %d %s", w.Code, w.Body.String())
	}
	w = meterDo(e.h.EnergyMeterVersionPreviewWeb, "POST", "/energy/meters/"+e.v+"/versions/preview", e.v, e.uid,
		map[string]any{"valid_from": day(e.before), "formula": "[" + e.ak + "]*3"})
	if w.Code != http.StatusForbidden {
		t.Errorf("Vorschau ohne energy.admin: %d", w.Code)
	}

	e.perms[permEnergyAdmin] = true
	w = meterDo(e.h.EnergyMeterVersionPreviewWeb, "POST", "/energy/meters/"+e.v+"/versions/preview", e.v, e.uid,
		map[string]any{"valid_from": day(e.before), "formula": "[" + e.ak + "]*3"})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "4 Tage") || !strings.Contains(w.Body.String(), e.vk) ||
		!strings.Contains(w.Body.String(), "Alarme werden für die Vergangenheit nicht neu bewertet") {
		t.Errorf("Vorschau: %d %s", w.Code, w.Body.String())
	}
	// A hat keine Werte: die Vorschau warnt (W3).
	if !strings.Contains(w.Body.String(), "["+e.ak+"] hat noch keine Werte") {
		t.Errorf("Vorschau ohne Warnung zum Bezug ohne Werte: %s", w.Body.String())
	}
	w = e.save(t, map[string]any{"valid_from": day(e.before), "formula": "[" + e.ak + "]*3", "recalc": true})
	var res struct {
		Status string `json:"status"`
		RunID  string `json:"run_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if w.Code != http.StatusOK || res.RunID == "" {
		t.Fatalf("Neurechnung mit energy.admin: %d %s", w.Code, w.Body.String())
	}
	// Eine zweite Neurechnung, solange die erste läuft: verständliche Meldung.
	w = e.save(t, map[string]any{"valid_from": day(e.before), "formula": "[" + e.ak + "]*4", "recalc": true})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "läuft gerade eine Neurechnung") {
		t.Errorf("zweite Neurechnung: %d %s", w.Code, w.Body.String())
	}
}

func TestEnergyVersionSaveMaint(t *testing.T) {
	e := newVersionWebEnv(t)
	e.perms = map[string]bool{permEnergyView: true, permEnergyEdit: true, permEnergyAdmin: true}
	e.h.mlock.locked = true
	w := e.save(t, map[string]any{"valid_from": e.before.Format("2006-01-02"), "formula": "[" + e.ak + "]*3", "recalc": true})
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "Wartung läuft") {
		t.Errorf("Neurechnung in der Wartung: %d %s", w.Code, w.Body.String())
	}
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM energy_recalc_runs WHERE meter_id = $1::uuid`, e.v).Scan(&n); err != nil || n != 0 {
		t.Errorf("Lauf in der Wartung angelegt: %d %v", n, err)
	}
}

var uuidText = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func TestEnergyMeterVersionsFragment(t *testing.T) {
	e := newVersionWebEnv(t)
	ctx := context.Background()
	e.perms = map[string]bool{permEnergyView: true, permEnergyEdit: true}
	// geltende Fassung von heute (die vom Anlegen wird damit abgelöst) und eine geplante
	m, err := e.st.Get(ctx, e.v)
	if err != nil {
		t.Fatal(err)
	}
	m.Formula = "[" + e.ak + "]*2"
	if _, err := e.st.Save(ctx, m, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.SaveVersion(ctx, energie.VersionInput{MeterID: e.v, Formula: "[" + e.ak + "]*5", ValidFrom: e.today.AddDate(0, 0, 7)}, ""); err != nil {
		t.Fatal(err)
	}

	w := meterDo(e.h.EnergyMeterVersionsWeb, "GET", "/energy/meters/"+e.v+"/versions", e.v, e.uid, nil)
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("Fragment: %d %s", w.Code, body)
	}
	for _, want := range []string{">geplant<", ">gilt<", ">abgelöst<", "Neue Fassung", e.today.AddDate(0, 0, 7).Format("02.01.2006")} {
		if !strings.Contains(body, want) {
			t.Errorf("Fragment enthält %q nicht", want)
		}
	}
	if n := strings.Count(body, "evDelete("); n != 1 {
		t.Errorf("%d Löschen-Knöpfe, erwartet 1 (nur an der geplanten Fassung)", n)
	}
	text := regexp.MustCompile(`<[^>]*>`).ReplaceAllString(body, " ")
	if uuidText.MatchString(text) {
		t.Errorf("sichtbarer Text enthält eine Kennung: %s", uuidText.FindString(text))
	}
	// Ohne energy.edit: Liste ja, Formular nein.
	e.perms = map[string]bool{permEnergyView: true}
	body = meterDo(e.h.EnergyMeterVersionsWeb, "GET", "/energy/meters/"+e.v+"/versions", e.v, e.uid, nil).Body.String()
	if !strings.Contains(body, ">gilt<") || strings.Contains(body, "Neue Fassung") || strings.Contains(body, "evDelete(") {
		t.Error("ohne energy.edit: Liste ohne Formular und ohne Löschen erwartet")
	}
}

// Ein Bearbeiten-Formular von gestern nimmt eine inzwischen geltende geplante
// Formel nicht still zurück (Befund K4): unveränderte Formel bleibt die geltende.
func TestEnergyMeterSaveKeepsAppliedFormula(t *testing.T) {
	e := newVersionWebEnv(t)
	e.perms = map[string]bool{permEnergyView: true, permEnergyEdit: true}
	ctx := context.Background()
	applied := "[" + e.ak + "]*2"
	if _, err := e.pool.Exec(ctx, `UPDATE energy_meters SET formula = $2 WHERE id = $1::uuid`, e.v, applied); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"id": e.v, "key": e.vk, "name": "Neuer Name", "infrastructure_id": e.infra, "kind": "counter",
		"factor": 1, "formula": "[" + e.ak + "]", "formula_orig": "[" + e.ak + "]", "export": true, "active": true}
	w := meterDo(e.h.EnergyMeterSaveWeb, "POST", "/energy/meters/save-web", "", e.uid, body)
	if w.Code != http.StatusOK {
		t.Fatalf("Speichern: %d %s", w.Code, w.Body.String())
	}
	m, err := e.st.Get(ctx, e.v)
	if err != nil {
		t.Fatal(err)
	}
	if m.Formula != applied || m.Name != "Neuer Name" {
		t.Errorf("nach dem Speichern: Formel %q Name %q, erwartet %q und den neuen Namen", m.Formula, m.Name, applied)
	}
	// Wer die Formel im Formular ändert, ändert sie.
	body["formula"] = "[" + e.ak + "]*3"
	if w := meterDo(e.h.EnergyMeterSaveWeb, "POST", "/energy/meters/save-web", "", e.uid, body); w.Code != http.StatusOK {
		t.Fatalf("Speichern mit neuer Formel: %d %s", w.Code, w.Body.String())
	}
	if m, _ := e.st.Get(ctx, e.v); m == nil || m.Formula != "["+e.ak+"]*3" {
		t.Errorf("geänderte Formel nicht übernommen: %+v", m)
	}
}
