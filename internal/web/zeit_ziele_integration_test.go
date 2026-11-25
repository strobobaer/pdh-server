//go:build integration

// Zeit ohne Vorgang (zeit_ziele.go, terminal_zeit_ziel.go): Anlage, IT-Asset, Sonstiges am Terminal und in PDH.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run 'ZeitZiele|Migration225' ./internal/web/
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/timetracking"
	"pdh/pkg/middleware"
)

// newAsset legt ein IT-Asset an (infra "" = keiner Anlage zugeordnet).
func (z *zeitEnv) newAsset(name, infra string) string {
	z.e.t.Helper()
	id := z.e.id(`INSERT INTO it_assets (name, type, status, created_by, infrastructure_id) VALUES ($1, 'server', 'active', $2::uuid, NULLIF($3, '')::uuid) RETURNING id::text`, name+" "+z.e.sfx, z.anna, infra)
	z.e.t.Cleanup(func() { z.e.pool.Exec(context.Background(), `DELETE FROM it_assets WHERE id = $1::uuid`, id) })
	return id
}

func (z *zeitEnv) startZiel(who string, f url.Values) *httptest.ResponseRecorder {
	f.Set("who", who)
	return z.req("POST", "/terminal/zeit/arbeit/start", f, z.devCookie, nil)
}

// stopAfter beendet die laufende Arbeit der Person nach der angegebenen Dauer.
func (z *zeitEnv) stopAfter(who, ago string) *httptest.ResponseRecorder {
	z.e.exec(`UPDATE work_sessions SET started_at = now() - $2::interval WHERE user_id = $1::uuid AND ended_at IS NULL`, who, ago)
	return z.req("POST", "/terminal/zeit/arbeit/stop", url.Values{"who": {who}}, z.devCookie, nil)
}

type zeitEntryRow struct {
	RefType, RefID, Desc, Kind, Asset, Infra, Via, Activity, CostCenter string
	Minutes                                                             int
	Pending                                                             bool
}

func (z *zeitEnv) lastEntry(who string) zeitEntryRow {
	z.e.t.Helper()
	var r zeitEntryRow
	z.e.must(z.e.pool.QueryRow(z.e.ctx, `SELECT ref_type::text, ref_id::text, description, COALESCE(target_kind, ''), COALESCE(it_asset_id::text, ''), COALESCE(infrastructure_id::text, ''),
			origin_via, COALESCE(activity_id::text, ''), COALESCE(cost_center_id::text, ''), COALESCE(duration_min, 0), pending
		FROM time_entries WHERE user_id = $1::uuid ORDER BY created_at DESC, id DESC LIMIT 1`, who).
		Scan(&r.RefType, &r.RefID, &r.Desc, &r.Kind, &r.Asset, &r.Infra, &r.Via, &r.Activity, &r.CostCenter, &r.Minutes, &r.Pending))
	return r
}

func TestMigration225ZeitOhneVorgang(t *testing.T) {
	z := newZeitEnv(t, true)
	ctx := z.e.ctx
	ws := func(rt string) error {
		_, err := z.e.pool.Exec(ctx, `INSERT INTO work_sessions (user_id, ref_type, ref_id, title, started_at, ended_at) VALUES ($1::uuid, $2, pdh_uuid_v7(), 't', now() - interval '3 hours', now() - interval '2 hours')`, z.berta, rt)
		return err
	}
	for _, ok := range []string{"fault", "ticket", "task", "maintenance", "plant", "it_asset", "other"} {
		if err := ws(ok); err != nil {
			t.Errorf("work_sessions.ref_type %q abgelehnt: %v", ok, err)
		}
	}
	if err := ws("quatsch"); err == nil {
		t.Error("work_sessions.ref_type 'quatsch' angenommen")
	}
	z.e.exec(`DELETE FROM work_sessions WHERE user_id = $1::uuid`, z.berta)
	te := func(kind any) error {
		_, err := z.e.pool.Exec(ctx, `INSERT INTO time_entries (id, user_id, ref_type, ref_id, description, started_at, ended_at, duration_min, target_kind)
			VALUES (pdh_uuid_v7(), $1::uuid, 'production', pdh_uuid_v7(), 'x', now() - interval '2 hours', now() - interval '1 hour', 60, $2)`, z.berta, kind)
		return err
	}
	for _, ok := range []any{nil, "plant", "it_asset", "other"} {
		if err := te(ok); err != nil {
			t.Errorf("time_entries.target_kind %v abgelehnt: %v", ok, err)
		}
	}
	if err := te("quatsch"); err == nil {
		t.Error("time_entries.target_kind 'quatsch' angenommen")
	}
	// IT-Asset gelöscht: die Buchung bleibt, der Verweis wird leer
	asset := z.newAsset("MigAsset", "")
	z.e.exec(`UPDATE time_entries SET it_asset_id = $2::uuid WHERE user_id = $1::uuid AND target_kind = 'it_asset'`, z.berta, asset)
	z.e.exec(`DELETE FROM it_assets WHERE id = $1::uuid`, asset)
	if z.count(`time_entries WHERE user_id = $1::uuid AND target_kind = 'it_asset' AND it_asset_id IS NULL`, z.berta) != "1" {
		t.Error("Buchung nach dem Löschen des Assets")
	}
}

func TestZeitZieleAnlageAssetSonstigesBuchen(t *testing.T) {
	z := newZeitEnv(t, true)
	asset := z.newAsset("Presse-PC", z.sub)

	// Anlage (Unteranlage im Terminalbaum) mit Notiz
	if rec := z.startZiel(z.anna, url.Values{"sel": {"plant:" + z.sub}, "text": {"Filter getauscht"}}); rec.Code != 303 {
		t.Fatalf("Anlage starten: %d %s", rec.Code, rec.Body.String())
	}
	if rec := z.stopAfter(z.anna, "25 minutes"); rec.Code != 303 {
		t.Fatalf("Anlage stoppen: %d", rec.Code)
	}
	e := z.lastEntry(z.anna)
	if e.RefType != "production" || e.Kind != "plant" || e.RefID != z.sub || e.Infra != z.sub || e.Asset != "" {
		t.Errorf("Anlage: %+v", e)
	}
	if !strings.HasPrefix(e.Desc, "Anlage: ") || !strings.Contains(e.Desc, "VGUnter") || !strings.HasSuffix(e.Desc, " – Filter getauscht") {
		t.Errorf("Beschreibung Anlage: %q", e.Desc)
	}
	if e.Minutes < 24 || e.Minutes > 27 || e.Via != vorgViaGewaehlt || !e.Pending {
		t.Errorf("Anlage Dauer/Herkunft/unbestätigt: %+v", e)
	}
	if z.e.id(`SELECT (time_entry_id IS NOT NULL)::text FROM work_sessions WHERE user_id = $1::uuid AND ref_type = 'plant'`, z.anna) != "true" {
		t.Error("Sitzung ohne Verweis auf den Zeiteintrag")
	}

	// IT-Asset (mit Anlage im Baum)
	if rec := z.startZiel(z.anna, url.Values{"type": {"it_asset"}, "id": {asset}}); rec.Code != 303 {
		t.Fatalf("Asset starten: %d %s", rec.Code, rec.Body.String())
	}
	if rec := z.stopAfter(z.anna, "10 minutes"); rec.Code != 303 {
		t.Fatalf("Asset stoppen: %d", rec.Code)
	}
	e = z.lastEntry(z.anna)
	if e.RefType != "production" || e.Kind != "it_asset" || e.Asset != asset || e.RefID != asset || e.Infra != z.sub || e.Desc != "IT-Asset: Presse-PC "+z.e.sfx {
		t.Errorf("Asset: %+v", e)
	}

	// Sonstiges mit Freitext
	if rec := z.startZiel(z.berta, url.Values{"type": {"other"}, "text": {"  Halle   kehren "}}); rec.Code != 303 {
		t.Fatalf("Sonstiges starten: %d %s", rec.Code, rec.Body.String())
	}
	if rec := z.stopAfter(z.berta, "40 minutes"); rec.Code != 303 {
		t.Fatalf("Sonstiges stoppen: %d", rec.Code)
	}
	e = z.lastEntry(z.berta)
	if e.RefType != "production" || e.Kind != "other" || e.Desc != "Halle kehren" || !uuidInPathRe.MatchString(e.RefID) || e.Asset != "" || e.Minutes < 39 {
		t.Errorf("Sonstiges: %+v", e)
	}
	if z.count(`work_sessions WHERE user_id = $1::uuid AND ref_type IN ('plant', 'it_asset', 'other') AND time_entry_id IS NOT NULL`, z.anna) != "2" {
		t.Error("beide Sitzungen von Anna sollen eine Buchung haben")
	}

	// Mindestens eine Minute, auch bei sofortigem Stopp
	if rec := z.startZiel(z.anna, url.Values{"sel": {"plant:" + z.plant}}); rec.Code != 303 {
		t.Fatalf("kurz: %d", rec.Code)
	}
	if rec := z.req("POST", "/terminal/zeit/arbeit/stop", url.Values{"who": {z.anna}}, z.devCookie, nil); rec.Code != 303 {
		t.Fatalf("kurz stoppen: %d", rec.Code)
	}
	if e = z.lastEntry(z.anna); e.Minutes != 1 || e.Kind != "plant" {
		t.Errorf("Mindestdauer: %+v", e)
	}
}

func TestZeitZieleTaetigkeitKostenstelleKarte(t *testing.T) {
	z := newZeitEnv(t, true)
	ctx := z.e.ctx
	cc := z.e.id(`INSERT INTO cost_centers (number, name, created_by) VALUES ($1, 'ZZ-KS', $2::uuid) RETURNING id::text`, "Z"+z.e.sfx[:min(len(z.e.sfx), 8)], z.anna)
	act := z.e.id(`INSERT INTO cost_activities (number, name, cost_center_id, created_by) VALUES ('T1', 'Reinigen', $1::uuid, $2::uuid) RETURNING id::text`, cc, z.anna)
	inactive := z.e.id(`INSERT INTO cost_activities (number, name, active, created_by) VALUES ('T2', 'Alt', false, $1::uuid) RETURNING id::text`, z.anna)
	t.Cleanup(func() {
		z.e.pool.Exec(context.Background(), `DELETE FROM time_entries WHERE activity_id IN ($1::uuid, $2::uuid)`, act, inactive)
		z.e.pool.Exec(context.Background(), `DELETE FROM cost_activities WHERE id IN ($1::uuid, $2::uuid)`, act, inactive)
		z.e.pool.Exec(context.Background(), `UPDATE infrastructure SET cost_center_id = NULL WHERE id = $1::uuid`, z.sub)
		z.e.pool.Exec(context.Background(), `DELETE FROM cost_centers WHERE id = $1::uuid`, cc)
	})
	// Tätigkeit aus der Dropdown-Liste: inaktive wird abgewiesen
	if rec := z.startZiel(z.anna, url.Values{"sel": {"plant:" + z.sub}, "activity_id": {inactive}}); rec.Code != 400 {
		t.Errorf("inaktive Tätigkeit: %d", rec.Code)
	}
	if rec := z.startZiel(z.anna, url.Values{"sel": {"plant:" + z.sub}, "activity_id": {"kein-uuid"}}); rec.Code != 400 {
		t.Errorf("ungültige Tätigkeit: %d", rec.Code)
	}
	if z.count(`work_sessions WHERE user_id = $1::uuid`, z.anna) != "0" {
		t.Fatal("abgewiesene Tätigkeit hat eine Sitzung angelegt")
	}
	if rec := z.startZiel(z.anna, url.Values{"sel": {"plant:" + z.sub}, "activity_id": {act}}); rec.Code != 303 {
		t.Fatalf("mit Tätigkeit: %d %s", rec.Code, rec.Body.String())
	}
	z.stopAfter(z.anna, "20 minutes")
	e := z.lastEntry(z.anna)
	if e.Activity != act || e.CostCenter != cc {
		t.Errorf("Tätigkeit/Kostenstelle: %+v", e)
	}
	// ohne Tätigkeit kommt die Kostenstelle von der Anlage
	z.e.exec(`UPDATE infrastructure SET cost_center_id = $2::uuid WHERE id = $1::uuid`, z.sub, cc)
	asset := z.newAsset("KS-Asset", z.sub)
	z.startZiel(z.berta, url.Values{"sel": {"it_asset:" + asset}})
	z.stopAfter(z.berta, "5 minutes")
	if e = z.lastEntry(z.berta); e.Activity != "" || e.CostCenter != cc {
		t.Errorf("Kostenstelle von der Anlage: %+v", e)
	}
	// Karte: nicht unbestätigt
	z.pdhEnv.card(z.vgEnv.card)
	if rec := z.req("POST", "/terminal/zeit/arbeit/start", url.Values{"sel": {"other:"}, "text": {"Aufräumen"}}, z.devCookie, nil); rec.Code != 303 {
		t.Fatalf("mit Karte: %d %s", rec.Code, rec.Body.String())
	}
	z.e.exec(`UPDATE work_sessions SET started_at = now() - interval '12 minutes' WHERE user_id = $1::uuid AND ended_at IS NULL`, z.vgEnv.card)
	if rec := z.req("POST", "/terminal/zeit/arbeit/stop", url.Values{}, z.devCookie, nil); rec.Code != 303 {
		t.Fatalf("Karte stoppen: %d", rec.Code)
	}
	e = z.lastEntry(z.vgEnv.card)
	if e.Pending || e.Kind != "other" || e.Via == vorgViaGewaehlt {
		t.Errorf("Karten-Buchung: %+v", e)
	}
	_ = ctx
}

func TestZeitZieleTerminalScopeUndEingaben(t *testing.T) {
	z := newZeitEnv(t, true)
	inside := z.newAsset("Innen", z.sub)
	outside := z.newAsset("Aussen", z.foreign)
	noPlant := z.newAsset("OhneAnlage", "")
	check := func(name string, f url.Values, want ...int) {
		t.Helper()
		rec := z.startZiel(z.anna, f)
		for _, w := range want {
			if rec.Code == w {
				return
			}
		}
		t.Errorf("%s: %d (erwartet %v) %s", name, rec.Code, want, rec.Body.String())
	}
	check("Anlage außerhalb des Baums", url.Values{"sel": {"plant:" + z.foreign}}, 403)
	check("Asset außerhalb des Baums", url.Values{"sel": {"it_asset:" + outside}}, 403)
	check("Asset ohne Anlage", url.Values{"sel": {"it_asset:" + noPlant}}, 403)
	check("unbekanntes Asset", url.Values{"sel": {"it_asset:00000000-0000-4000-8000-000000000000"}}, 404)
	check("ungültige Anlage", url.Values{"sel": {"plant:quatsch"}}, 400)
	check("ungültiges Asset", url.Values{"sel": {"it_asset:quatsch"}}, 400)
	check("Sonstiges ohne Text", url.Values{"sel": {"other:"}}, 422)
	check("Sonstiges nur Leerraum", url.Values{"sel": {"other:"}, "text": {"   "}}, 422)
	check("Text zu lang", url.Values{"sel": {"other:"}, "text": {strings.Repeat("ä", 201)}}, 400)
	if z.count(`work_sessions WHERE user_id = $1::uuid`, z.anna) != "0" {
		t.Fatal("abgewiesene Wahl hat eine Sitzung angelegt")
	}
	check("200 Zeichen sind erlaubt", url.Values{"sel": {"other:"}, "text": {strings.Repeat("ä", 200)}}, 303)
	// dieselbe Sonstiges-Arbeit nicht doppelt
	if rec := z.startZiel(z.anna, url.Values{"sel": {"other:"}, "text": {strings.Repeat("ä", 200)}}); rec.Code != 409 {
		t.Errorf("doppelt: %d", rec.Code)
	}
	check("Asset im Baum", url.Values{"sel": {"it_asset:" + inside}}, 303)
	if n := z.count(`work_sessions WHERE user_id = $1::uuid AND ended_at IS NULL`, z.anna); n != "1" {
		t.Errorf("laufend: %s", n)
	}
	// Person außerhalb der Auswahl bleibt abgewiesen (422), auch bei neuen Zielen
	if rec := z.startZiel(z.carla, url.Values{"sel": {"plant:" + z.plant}}); rec.Code != 422 {
		t.Errorf("Person außerhalb: %d", rec.Code)
	}
	// ohne Gerätesitzung kein Schreibzugriff
	f := url.Values{"sel": {"plant:" + z.plant}, "who": {z.berta}}
	if rec := z.req("POST", "/terminal/zeit/arbeit/start", f, z.plainCookie, nil); rec.Code != 403 {
		t.Errorf("ohne Gerät: %d", rec.Code)
	}
}

func TestZeitZieleFensterSeiten(t *testing.T) {
	z := newZeitEnv(t, true)
	inside := z.newAsset("Fenster-Innen", z.sub)
	outside := z.newAsset("Fenster-Aussen", z.foreign)
	noPlant := z.newAsset("Fenster-Ohne", "")
	base := "/terminal/zeit?arbeit=1&wer=" + z.anna
	get := func(q string) string {
		t.Helper()
		rec := z.req("GET", base+q, nil, z.devCookie, nil)
		if rec.Code != 200 {
			t.Fatalf("%s: %d", q, rec.Code)
		}
		return rec.Body.String()
	}
	main := get("")
	for _, want := range []string{"Arbeit ohne Vorgang", `value="plant"`, `value="it_asset"`, `value="other"`} {
		if !strings.Contains(main, want) {
			t.Errorf("Auswahl ohne %q", want)
		}
	}
	if strings.Contains(main, "Arbeit an Anlage") {
		t.Error("alter Eintrag „Arbeit an Anlage“ steht noch in der Liste")
	}
	pl := get("&ziel=plant")
	if !strings.Contains(pl, "VGUnter") || !strings.Contains(pl, "wählen") {
		t.Error("Anlagenwahl zeigt die Unteranlage nicht")
	}
	if strings.Contains(pl, "VGFremd") {
		t.Error("Anlagenwahl zeigt eine fremde Anlage")
	}
	sel := get("&ziel=plant&infra=" + z.sub)
	if !strings.Contains(sel, `value="plant:`+z.sub+`"`) || !strings.Contains(sel, "Starten") {
		t.Error("gewählte Anlage ohne Start")
	}
	if strings.Contains(get("&ziel=plant&infra="+z.foreign), `value="plant:`+z.foreign+`"`) {
		t.Error("fremde Anlage ist wählbar")
	}
	as := get("&ziel=it_asset")
	if !strings.Contains(as, `value="it_asset:`+inside+`"`) || strings.Contains(as, outside) || strings.Contains(as, noPlant) {
		t.Errorf("Asset-Liste: innen=%v außen=%v ohne=%v", strings.Contains(as, inside), strings.Contains(as, outside), strings.Contains(as, noPlant))
	}
	if q := get("&ziel=it_asset&q=nichtda"); strings.Contains(q, `value="it_asset:`+inside+`"`) {
		t.Error("Suche filtert nicht")
	}
	if q := get("&ziel=it_asset&q=fenster-innen"); !strings.Contains(q, `value="it_asset:`+inside+`"`) {
		t.Error("Suche findet das Asset nicht")
	}
	if q := get("&ziel=it_asset&infra=" + z.plant); !strings.Contains(q, `value="it_asset:`+inside+`"`) {
		t.Error("Anlagenfilter schließt Unteranlagen aus")
	}
	if q := get("&ziel=it_asset&infra=" + z.foreign); strings.Contains(q, outside) {
		t.Error("fremder Anlagenfilter wirkt")
	}
	ot := get("&ziel=other")
	if !strings.Contains(ot, `name="text"`) || !strings.Contains(ot, `maxlength="200"`) {
		t.Error("Sonstiges ohne Textfeld")
	}
	// Tätigkeit nur, wenn es aktive gibt – kein Fehler ohne
	if rec := z.req("GET", base+"&ziel=quatsch", nil, z.devCookie, nil); rec.Code != 200 || strings.Contains(rec.Body.String(), `name="text"`) {
		t.Errorf("unbekanntes Ziel: %d", rec.Code)
	}
	// who= wandert durch die Schritte
	multi := "/terminal/zeit?arbeit=1&wer=" + z.anna + "&who=" + z.anna + "&who=" + z.berta + "&ziel=other"
	if b := z.req("GET", multi, nil, z.devCookie, nil).Body.String(); !strings.Contains(b, `name="who" value="`+z.berta+`"`) {
		t.Error("gewählte Person geht im Schritt verloren")
	}
}

func TestZeitZieleVergesseneArbeitDannGeprueft(t *testing.T) {
	z := newZeitEnv(t, true)
	z.chdirRoot(t)
	if rec := z.startZiel(z.anna, url.Values{"sel": {"plant:" + z.sub}, "text": {"Revision"}}); rec.Code != 303 {
		t.Fatalf("start: %d", rec.Code)
	}
	z.e.exec(`UPDATE work_sessions SET started_at = now() - interval '30 hours' WHERE user_id = $1::uuid AND ended_at IS NULL`, z.anna)
	z.req("POST", "/terminal/zeit/arbeit/stop", url.Values{"who": {z.anna}}, z.devCookie, nil)
	id := z.e.id(`SELECT id::text FROM work_sessions WHERE user_id = $1::uuid`, z.anna)
	if z.count(`time_entries WHERE user_id = $1::uuid`, z.anna) != "0" {
		t.Error("vergessene Arbeit wurde als Zeit gebucht")
	}
	admin := z.e.user("zielmadm", "admin", false)
	z.e.warm()
	c := z.pdhCookie(admin, "admin")
	now := time.Now().In(workLocation)
	f := url.Values{"start": {now.Add(-3 * time.Hour).Format(attInputFmt)}, "end": {now.Add(-2 * time.Hour).Format(attInputFmt)}, "reason": {"geprüft"}}
	if rec := z.pdhReq("POST", "/attendance/work/"+id+"/edit", f, c); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "n=ok") {
		t.Fatalf("prüfen: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	e := z.lastEntry(z.anna)
	if e.Kind != "plant" || e.RefType != "production" || e.Minutes != 60 || e.Via != zeitViaPDH || e.Pending || !strings.HasSuffix(e.Desc, " – Revision") {
		t.Errorf("Buchung nach Prüfung: %+v", e)
	}
	if z.e.id(`SELECT (time_entry_id IS NOT NULL)::text FROM work_sessions WHERE id = $1::uuid`, id) != "true" {
		t.Error("Sitzung ohne Verweis")
	}
	// ältere Anlagen-Sitzung ohne Buchung (nicht vergessen): bleibt ohne Buchung
	old := z.e.id(`INSERT INTO work_sessions (user_id, ref_type, ref_id, infrastructure_id, title, started_at, ended_at) VALUES ($1::uuid, 'plant', $2::uuid, $2::uuid, 'alt', now() - interval '9 hours', now() - interval '8 hours') RETURNING id::text`, z.berta, z.plant)
	f2 := url.Values{"start": {now.Add(-9 * time.Hour).Format(attInputFmt)}, "end": {now.Add(-7 * time.Hour).Format(attInputFmt)}, "reason": {"x"}}
	if rec := z.pdhReq("POST", "/attendance/work/"+old+"/edit", f2, c); !strings.Contains(rec.Header().Get("Location"), "n=ok") {
		t.Fatalf("alte Sitzung: %s", rec.Header().Get("Location"))
	}
	if z.count(`time_entries WHERE user_id = $1::uuid`, z.berta) != "0" {
		t.Error("alte Anlagen-Sitzung bekam nachträglich eine Buchung")
	}
}

// timeRouter: /time-Wege der PDH-Seite (Start, manuell, IT-Asset-Suche) und Stopp.
func (z *zeitEnv) timeRouter() http.Handler {
	z.h.time = timetracking.NewService(timetracking.NewRepository(z.e.pool))
	r := chi.NewRouter()
	r.Use(z.h.authMiddleware)
	r.Get("/time/it-assets", z.h.TimeITAssets)
	r.Post("/time/start-web", z.h.TimeStartWeb)
	r.Post("/time/manual-web", z.h.TimeManualWeb)
	r.Post("/time/{id}/stop-web", z.h.TimeStopWeb)
	return r
}

func (z *zeitEnv) timeReq(mux http.Handler, method, target string, form url.Values, c *http.Cookie) *httptest.ResponseRecorder {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestZeitZielePDHStartUndManuell(t *testing.T) {
	z := newZeitEnv(t, true)
	mux := z.timeRouter()
	asset := z.newAsset("PDH-Router", z.sub)
	loose := z.newAsset("PDH-Lose", "")
	admin := z.e.user("zielpadm", "admin", false)
	z.e.warm()
	c := z.pdhCookie(admin, "admin")
	cc := z.e.id(`INSERT INTO cost_centers (number, name, created_by) VALUES ($1, 'ZP-KS', $2::uuid) RETURNING id::text`, "P"+z.e.sfx[:min(len(z.e.sfx), 8)], z.anna)
	act := z.e.id(`INSERT INTO cost_activities (number, name, cost_center_id, created_by) VALUES ('P1', 'Prüfen', $1::uuid, $2::uuid) RETURNING id::text`, cc, z.anna)
	t.Cleanup(func() {
		bg := context.Background()
		z.e.pool.Exec(bg, `DELETE FROM time_entries WHERE user_id = $1::uuid`, admin)
		z.e.pool.Exec(bg, `DELETE FROM cost_activities WHERE id = $1::uuid`, act)
		z.e.pool.Exec(bg, `DELETE FROM cost_centers WHERE id = $1::uuid`, cc)
	})
	post := func(path string, f url.Values) *httptest.ResponseRecorder { return z.timeReq(mux, "POST", path, f, c) }
	last := func() zeitEntryRow { return z.lastEntry(admin) }
	manual := func(f url.Values) *httptest.ResponseRecorder {
		f.Set("started_at", time.Now().Add(-2*time.Hour).Format("2006-01-02T15:04"))
		f.Set("ended_at", time.Now().Add(-1*time.Hour).Format("2006-01-02T15:04"))
		return post("/time/manual-web", f)
	}

	// Anlage manuell, mit Tätigkeit und Freitext
	if rec := manual(url.Values{"target_kind": {"plant"}, "infrastructure_id": {z.sub}, "description": {"Wartungsgang"}, "activity_id": {act}}); rec.Code != 204 {
		t.Fatalf("Anlage manuell: %d %s", rec.Code, rec.Body.String())
	}
	e := last()
	if e.RefType != "production" || e.Kind != "plant" || e.RefID != z.sub || e.Infra != z.sub || !strings.HasPrefix(e.Desc, "Anlage: ") || !strings.HasSuffix(e.Desc, " – Wartungsgang") || e.Activity != act || e.CostCenter != cc || e.Minutes != 60 {
		t.Errorf("Anlage manuell: %+v", e)
	}
	// Anlage ohne Angabe / unbekannt
	if rec := manual(url.Values{"target_kind": {"plant"}}); rec.Code != 400 {
		t.Errorf("Anlage ohne Wahl: %d", rec.Code)
	}
	if rec := manual(url.Values{"target_kind": {"plant"}, "infrastructure_id": {"00000000-0000-4000-8000-000000000000"}}); rec.Code != 404 {
		t.Errorf("unbekannte Anlage: %d", rec.Code)
	}
	// IT-Asset starten (läuft), Anlage kommt vom Asset; dann stoppen
	if rec := post("/time/start-web", url.Values{"target_kind": {"it_asset"}, "it_asset_id": {asset}, "infrastructure_id": {z.foreign}}); rec.Code != 204 {
		t.Fatalf("Asset starten: %d %s", rec.Code, rec.Body.String())
	}
	e = last()
	if e.Kind != "it_asset" || e.Asset != asset || e.RefID != asset || e.Infra != z.sub || e.Desc != "IT-Asset: PDH-Router "+z.e.sfx || e.Minutes != 0 {
		t.Errorf("Asset starten: %+v", e)
	}
	if rec := post("/time/"+z.e.id(`SELECT id::text FROM time_entries WHERE user_id = $1::uuid AND ended_at IS NULL`, admin)+"/stop-web", nil); rec.Code != 200 {
		t.Errorf("stoppen: %d", rec.Code)
	}
	// Asset ohne Anlage: erlaubt, Anlage leer
	if rec := manual(url.Values{"target_kind": {"it_asset"}, "it_asset_id": {loose}}); rec.Code != 204 {
		t.Fatalf("Asset ohne Anlage: %d %s", rec.Code, rec.Body.String())
	}
	if e = last(); e.Infra != "" || e.Asset != loose {
		t.Errorf("Asset ohne Anlage: %+v", e)
	}
	if rec := manual(url.Values{"target_kind": {"it_asset"}, "it_asset_id": {"00000000-0000-4000-8000-000000000000"}}); rec.Code != 404 {
		t.Errorf("unbekanntes Asset: %d", rec.Code)
	}
	if rec := manual(url.Values{"target_kind": {"it_asset"}}); rec.Code != 400 {
		t.Errorf("Asset ohne Wahl: %d", rec.Code)
	}
	// Sonstiges: Freitext Pflicht, Kostenstelle (Anlage) optional
	if rec := manual(url.Values{"target_kind": {"other"}}); rec.Code != 422 {
		t.Errorf("Sonstiges ohne Text: %d", rec.Code)
	}
	if rec := manual(url.Values{"target_kind": {"other"}, "description": {strings.Repeat("x", 201)}}); rec.Code != 400 {
		t.Errorf("Sonstiges zu lang: %d", rec.Code)
	}
	if rec := manual(url.Values{"target_kind": {"other"}, "description": {"Lager aufräumen"}, "ref_type": {"fault"}, "ref_id": {"x"}, "infrastructure_id": {z.plant}}); rec.Code != 204 {
		t.Fatalf("Sonstiges: %d %s", rec.Code, rec.Body.String())
	}
	if e = last(); e.RefType != "production" || e.Kind != "other" || e.Desc != "Lager aufräumen" || e.Infra != z.plant || !uuidInPathRe.MatchString(e.RefID) {
		t.Errorf("Sonstiges: %+v", e)
	}
	if rec := post("/time/start-web", url.Values{"target_kind": {"quatsch"}}); rec.Code != 400 {
		t.Errorf("unbekannte Zielart: %d", rec.Code)
	}
	if rec := manual(url.Values{"target_kind": {"other"}, "description": {"x"}, "activity_id": {"00000000-0000-4000-8000-000000000000"}}); rec.Code != 400 {
		t.Errorf("unbekannte Tätigkeit: %d", rec.Code)
	}
	// bisheriges Verhalten bleibt: Vorgang ohne Ziel
	if rec := manual(url.Values{"ref_type": {"fault"}, "ref_id": {z.fault}, "description": {"alt"}}); rec.Code != 204 {
		t.Fatalf("Vorgang: %d", rec.Code)
	}
	if e = last(); e.Kind != "" || e.RefType != "fault" {
		t.Errorf("Vorgang: %+v", e)
	}
	if rec := manual(url.Values{"description": {"x"}}); rec.Code != 400 {
		t.Errorf("ohne Typ und Bezug: %d", rec.Code)
	}

	// IT-Asset-Suche: Rechte und Abteilung
	list := func(q string, cookie *http.Cookie) (int, []timeAssetOption) {
		rec := z.timeReq(mux, "GET", "/time/it-assets?q="+url.QueryEscape(q), nil, cookie)
		var out struct{ Data []timeAssetOption }
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out.Data
	}
	code, items := list("PDH-Router", c)
	if code != 200 || len(items) != 1 || items[0].ID != asset || !strings.HasPrefix(items[0].Infra, "VGUnter") {
		t.Errorf("Suche: %d %+v", code, items)
	}
	if _, items = list("%", c); len(items) != 0 {
		t.Errorf("Prozentzeichen ist kein Platzhalter: %+v", items)
	}
	viewer := z.e.user("zielpview", "viewer", false)
	z.e.warm()
	vc := z.pdhCookie(viewer, "viewer")
	if code, _ = list("", vc); code != 403 {
		t.Errorf("ohne it.view: %d", code)
	}
	if rec := z.timeReq(mux, "POST", "/time/start-web", url.Values{"target_kind": {"it_asset"}, "it_asset_id": {asset}}, vc); rec.Code != 403 {
		t.Errorf("Start ohne it.view: %d", rec.Code)
	}
	z.e.grant(viewer, "it.view", "", true)
	z.e.warm()
	if code, items = list("PDH-Router", vc); code != 200 || len(items) != 1 {
		t.Errorf("mit it.view: %d %+v", code, items)
	}
	// Anlage und Sonstiges brauchen kein IT-Recht (jede Person bucht ihre eigene Zeit)
	z.e.exec(`DELETE FROM user_permissions WHERE user_id = $1::uuid`, viewer)
	z.e.warm()
	f := url.Values{"target_kind": {"other"}, "description": {"Botengang"}, "started_at": {time.Now().Add(-90 * time.Minute).Format("2006-01-02T15:04")}, "ended_at": {time.Now().Add(-30 * time.Minute).Format("2006-01-02T15:04")}}
	if rec := z.timeReq(mux, "POST", "/time/manual-web", f, vc); rec.Code != 204 {
		t.Errorf("Sonstiges ohne IT-Recht: %d %s", rec.Code, rec.Body.String())
	}
	z.e.pool.Exec(context.Background(), `DELETE FROM time_entries WHERE user_id = $1::uuid`, viewer)
}

func TestZeitZieleRESTStartUndCSV(t *testing.T) {
	z := newZeitEnv(t, true)
	z.h.time = timetracking.NewService(timetracking.NewRepository(z.e.pool))
	asset := z.newAsset("REST-Gerät", z.sub)
	admin := z.e.user("zielradm", "admin", false)
	z.e.warm()
	t.Cleanup(func() {
		z.e.pool.Exec(context.Background(), `DELETE FROM time_entries WHERE user_id = $1::uuid`, admin)
	})
	api := timetracking.NewHandler(z.h.time)
	api.SetTargetResolver(z.h.TimeTargetResolverFor())
	call := func(h http.HandlerFunc, method, target, body, uid, role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		ctx := context.WithValue(req.Context(), middleware.UserIDKey, uid)
		ctx = context.WithValue(ctx, middleware.RoleKey, role)
		rec := httptest.NewRecorder()
		h(rec, req.WithContext(ctx))
		return rec
	}
	if rec := call(api.Start, "POST", "/", `{"target_kind":"it_asset","it_asset_id":"`+asset+`"}`, admin, "admin"); rec.Code != 201 {
		t.Fatalf("Asset: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(api.Start, "POST", "/", `{"target_kind":"plant","infrastructure_id":"`+z.sub+`","description":"Rundgang"}`, admin, "admin"); rec.Code != 201 {
		t.Fatalf("Anlage: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(api.Start, "POST", "/", `{"target_kind":"other","description":"Schulung"}`, admin, "admin"); rec.Code != 201 {
		t.Fatalf("Sonstiges: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(api.Start, "POST", "/", `{"target_kind":"other"}`, admin, "admin"); rec.Code != 422 {
		t.Errorf("Sonstiges ohne Text: %d", rec.Code)
	}
	if rec := call(api.Start, "POST", "/", `{"target_kind":"bogus"}`, admin, "admin"); rec.Code != 400 {
		t.Errorf("unbekannte Zielart: %d", rec.Code)
	}
	if rec := call(api.Start, "POST", "/", `{"it_asset_id":"`+asset+`","ref_type":"production","ref_id":"`+newRefUUID()+`"}`, admin, "admin"); rec.Code != 400 {
		t.Errorf("it_asset_id ohne target_kind: %d", rec.Code)
	}
	// rückwärtskompatibel: alte Aufrufe ohne die neuen Felder
	if rec := call(api.Start, "POST", "/", `{"ref_type":"production","ref_id":"`+newRefUUID()+`","description":"alt"}`, admin, "admin"); rec.Code != 201 {
		t.Errorf("alter Aufruf: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data timetracking.TimeEntry `json:"data"`
	}
	rec := call(api.Start, "POST", "/", `{"target_kind":"it_asset","it_asset_id":"`+asset+`"}`, admin, "admin")
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Data.TargetKind == nil || *resp.Data.TargetKind != "it_asset" || resp.Data.ITAssetID == nil || *resp.Data.ITAssetID != asset || resp.Data.RefType != "production" {
		t.Errorf("Antwort: %+v", resp.Data)
	}
	// Rechte: ohne it.view kein Asset
	viewer := z.e.user("zielrview", "viewer", false)
	z.e.warm()
	if rec := call(api.Start, "POST", "/", `{"target_kind":"it_asset","it_asset_id":"`+asset+`"}`, viewer, "viewer"); rec.Code != 403 {
		t.Errorf("Asset ohne Recht: %d", rec.Code)
	}
	// CSV: Typ und Beschreibung mit dem Ziel; alte Einträge unverändert
	z.e.exec(`UPDATE time_entries SET ended_at = started_at + interval '30 minutes', duration_min = 30 WHERE user_id = $1::uuid`, admin)
	now := time.Now()
	rec = call(api.ExportCSV, "GET", "/export.csv?year="+now.Format("2006")+"&month="+now.Format("1"), "", admin, "admin")
	body := rec.Body.String()
	for _, want := range []string{";Anlage;", ";IT-Asset;", ";Sonstiges;", "Anlage: ", "IT-Asset: REST-Gerät", "Sonstiges: Schulung"} {
		if !strings.Contains(strings.ReplaceAll(body, ",", ";"), want) {
			t.Errorf("CSV ohne %q:\n%s", want, body)
		}
	}
	if !strings.Contains(strings.ReplaceAll(body, ",", ";"), ";production;") {
		t.Errorf("CSV: alter Eintrag ohne Ziel soll wie bisher „production“ zeigen:\n%s", body)
	}
}
