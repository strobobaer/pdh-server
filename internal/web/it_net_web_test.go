package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/core/rbac"
	"pdh/internal/core/users"
)

const itNetTestID = "11111111-1111-4111-8111-111111111111"

// itNetDo ruft einen Handler auf (Formular bei POST, id im Pfad, uid "" = Gast).
func itNetDo(fn http.HandlerFunc, method, path, id, uid string, form url.Values) *httptest.ResponseRecorder {
	var req *http.Request
	if method == http.MethodPost {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rc := chi.NewRouteContext()
	if id != "" {
		rc.URLParams.Add("id", id)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	if uid != "" {
		ctx = context.WithValue(ctx, "user", &users.User{ID: uid, FirstName: "Ines", LastName: "Netz", Role: "viewer"})
	}
	w := httptest.NewRecorder()
	fn(w, req.WithContext(ctx))
	return w
}

// itNetPageEnv: Handler mit Testdatenbank, echtem Rechtedienst, Vorlagen und
// eigener Quelle (Vorschläge und Einstellungen werden danach aufgeräumt).
func itNetPageEnv(t *testing.T) (*Handler, *pgxpool.Pool, string) {
	t.Helper()
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	h, pool := newITNetTestHandler(t)
	ctx := context.Background()
	rb := rbac.NewService(rbac.NewRepository(pool))
	if err := rb.Warm(ctx); err != nil {
		t.Fatal(err)
	}
	h.rbac = rb
	h.tmpl = loadTestTemplates(t)
	cwd, _ := os.Getwd()
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	src := "unifiweb" + sfx
	h.itNet.source = src
	for _, k := range []string{keyITNetUnifiOn, keyITNetInterval, keyITNetGrace, keyITNetRespUser, keyITNetRespGroup, keyITNetPriority, keyITNetLastErr} {
		k, old := k, h.appSetting(ctx, k, "")
		t.Cleanup(func() { _ = h.setAppSetting(context.Background(), k, old) })
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM it_assets WHERE net_source = $1 OR (net_source IS NULL AND name LIKE $2)`, src, "%"+sfx)
		_, _ = pool.Exec(c, `DELETE FROM it_net_candidates WHERE source = $1`, src)
	})
	return h, pool, src
}

func TestITNetPagePerms(t *testing.T) {
	var asked []string
	h := &Handler{hasPermHook: func(_ *http.Request, p string) bool { asked = append(asked, p); return false }}
	if w := itNetDo(h.ITNetPage, "GET", "/it/net", "", "", nil); w.Code != http.StatusForbidden {
		t.Errorf("Seite ohne it.view: Status %d", w.Code)
	}
	if len(asked) == 0 || asked[len(asked)-1] != "it.view" {
		t.Errorf("Seite: geprüft %v", asked)
	}
	// Mit it.view, aber ohne it.netsync: alle Schreibrouten 403.
	h.hasPermHook = func(_ *http.Request, p string) bool { return p == "it.view" }
	for _, c := range []struct {
		name string
		fn   http.HandlerFunc
		path string
	}{
		{"Einstellungen", h.ITNetSettingsWeb, "/it/net/settings"},
		{"Abgleich", h.ITNetRunWeb, "/it/net/run"},
		{"Übernehmen", h.ITNetAdoptWeb, "/it/net/candidates/adopt"},
		{"Ausblenden", h.ITNetIgnoreWeb, "/it/net/candidates/ignore"},
	} {
		if w := itNetDo(c.fn, "POST", c.path, "", "", url.Values{"mac": {"aa:bb:cc:dd:ee:ff"}, "type": {"printer"}}); w.Code != http.StatusForbidden {
			t.Errorf("%s ohne it.netsync: Status %d", c.name, w.Code)
		}
	}
	// Monitor/Trennen ohne it.edit: 403
	for _, fn := range []http.HandlerFunc{h.ITNetMonitorWeb, h.ITNetDetachWeb, h.ITNetReattachWeb} {
		if w := itNetDo(fn, "POST", "/it/"+itNetTestID+"/net-monitor", itNetTestID, "", url.Values{"on": {"1"}}); w.Code != http.StatusForbidden {
			t.Errorf("Asset-Schalter ohne it.edit: Status %d", w.Code)
		}
	}

	// Gate-Regeln
	for _, c := range []struct{ path, perm, ref string }{
		{"/it/net/settings", "it.netsync", ""},
		{"/it/net/run", "it.netsync", ""},
		{"/it/net/candidates/adopt", "it.netsync", ""},
		{"/it/net/candidates/ignore", "it.netsync", ""},
		{"/it/" + itNetTestID + "/net-monitor", "it.edit", "it_asset"},
		{"/it/" + itNetTestID + "/net-detach", "it.edit", "it_asset"},
		{"/it/" + itNetTestID + "/net-reattach", "it.edit", "it_asset"},
	} {
		_, perm, ref, _, hit := matchGate("POST", c.path)
		if !hit || perm != c.perm || ref != c.ref {
			t.Errorf("Gate %s: %v %q %q", c.path, hit, perm, ref)
		}
	}

	// Darstellung mit Datenbank: it.view allein zeigt keine Formulare.
	hp, pool, src := itNetPageEnv(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO it_net_candidates (source, mac, name, ip, kind, last_seen) VALUES ($1, 'aa:bb:cc:01:02:03', 'Drucker Halle', '10.0.0.5', 'wired', now())`, src); err != nil {
		t.Fatal(err)
	}
	hp.hasPermHook = func(_ *http.Request, p string) bool { return p == "it.view" }
	w := itNetDo(hp.ITNetPage, "GET", "/it/net", "", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Seite mit it.view: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Drucker Halle") {
		t.Error("Vorschlag fehlt in der Liste")
	}
	for _, bad := range []string{`action="/it/net/settings"`, `action="/it/net/candidates/adopt"`, `action="/it/net/run"`, `action="/it/net/candidates/ignore"`} {
		if strings.Contains(body, bad) {
			t.Errorf("ohne it.netsync sichtbar: %s", bad)
		}
	}
	hp.hasPermHook = func(*http.Request, string) bool { return true }
	body = itNetDo(hp.ITNetPage, "GET", "/it/net", "", "", nil).Body.String()
	for _, want := range []string{`action="/it/net/settings"`, `action="/it/net/candidates/adopt"`, `action="/it/net/run"`, "Arbeitsplatz", "Telefon"} {
		if !strings.Contains(body, want) {
			t.Errorf("mit it.netsync fehlt: %s", want)
		}
	}
	if strings.Contains(body, `value="network"`) {
		t.Error("Typ Netzwerk darf beim Übernehmen nicht wählbar sein")
	}
}

func TestITNetSettingsValidation(t *testing.T) {
	ok := url.Values{"enabled": {"1"}, "interval_s": {"60"}, "grace_min": {"5"}, "priority": {"high"}}
	if _, e := itNetSettingsFromForm(ok); e != "" {
		t.Errorf("gültig: %q", e)
	}
	for name, mod := range map[string]func(url.Values){
		"interval":  func(v url.Values) { v.Set("interval_s", "29") },
		"interval2": func(v url.Values) { v.Set("interval_s", "3601") },
		"grace":     func(v url.Values) { v.Set("grace_min", "0") },
		"grace2":    func(v url.Values) { v.Set("grace_min", "1441") },
		"priority":  func(v url.Values) { v.Set("priority", "urgent") },
		"both": func(v url.Values) {
			v.Set("responsible_user", itNetTestID)
			v.Set("responsible_group", itNetTestID)
		},
	} {
		v := url.Values{}
		for k, x := range ok {
			v[k] = append([]string{}, x...)
		}
		mod(v)
		if _, e := itNetSettingsFromForm(v); e == "" {
			t.Errorf("%s: Fehler erwartet", name)
		}
	}

	h, _, _ := itNetPageEnv(t)
	ctx := context.Background()
	h.hasPermHook = func(*http.Request, string) bool { return true }
	_ = h.setAppSetting(ctx, keyITNetInterval, "120")
	bad := url.Values{"interval_s": {"60"}, "grace_min": {"5"}, "priority": {"high"}, "responsible_user": {itNetTestID}, "responsible_group": {itNetTestID}}
	w := itNetDo(h.ITNetSettingsWeb, "POST", "/it/net/settings", "", "", bad)
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "e=both") {
		t.Errorf("beides gesetzt: %d %s", w.Code, w.Header().Get("Location"))
	}
	if got := h.appSetting(ctx, keyITNetInterval, ""); got != "120" {
		t.Errorf("bei Fehler darf nichts gespeichert werden: Intervall %q", got)
	}
	good := url.Values{"enabled": {"1"}, "interval_s": {"90"}, "grace_min": {"7"}, "priority": {"critical"}, "responsible_group": {itNetTestID}}
	w = itNetDo(h.ITNetSettingsWeb, "POST", "/it/net/settings", "", "", good)
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "n=saved") {
		t.Errorf("gültig: %d %s", w.Code, w.Header().Get("Location"))
	}
	for k, want := range map[string]string{keyITNetUnifiOn: "1", keyITNetInterval: "90", keyITNetGrace: "7", keyITNetPriority: "critical", keyITNetRespGroup: itNetTestID, keyITNetRespUser: ""} {
		if got := h.appSetting(ctx, k, "?"); got != want {
			t.Errorf("%s = %q, erwartet %q", k, got, want)
		}
	}
}

func TestITNetRunRespectsMaintLock(t *testing.T) {
	h := &Handler{hasPermHook: func(*http.Request, string) bool { return true }}
	h.mlock.locked = true
	w := itNetDo(h.ITNetRunWeb, "POST", "/it/net/run", "", "", url.Values{})
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "e=maint") {
		t.Errorf("Wartungssperre: %d %s", w.Code, w.Header().Get("Location"))
	}
}

func TestITNetAdoptFlow(t *testing.T) {
	h, pool, src := itNetPageEnv(t)
	ctx := context.Background()
	h.hasPermHook = func(*http.Request, string) bool { return true }
	if _, err := pool.Exec(ctx, `INSERT INTO it_net_candidates (source, mac, name, ip, kind, last_seen) VALUES ($1, 'aa:bb:cc:0a:0b:0c', 'PC Werkstatt', '10.0.0.9', 'wired', now())`, src); err != nil {
		t.Fatal(err)
	}
	// ungültiger Typ: nichts passiert
	w := itNetDo(h.ITNetAdoptWeb, "POST", "/it/net/candidates/adopt", "", pdhSystemUserID, url.Values{"mac": {"AA-BB-CC-0A-0B-0C"}, "type": {"network"}})
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "e=type") {
		t.Errorf("Typ Netzwerk: %d %s", w.Code, w.Header().Get("Location"))
	}
	// ausblenden und zurückholen
	w = itNetDo(h.ITNetIgnoreWeb, "POST", "/it/net/candidates/ignore", "", pdhSystemUserID, url.Values{"mac": {"aa:bb:cc:0a:0b:0c"}, "ignored": {"1"}})
	var ignored bool
	_ = pool.QueryRow(ctx, `SELECT ignored FROM it_net_candidates WHERE source = $1`, src).Scan(&ignored)
	if w.Code != http.StatusSeeOther || !ignored {
		t.Errorf("ausblenden: %d ignored=%v", w.Code, ignored)
	}
	itNetDo(h.ITNetIgnoreWeb, "POST", "/it/net/candidates/ignore", "", pdhSystemUserID, url.Values{"mac": {"aa:bb:cc:0a:0b:0c"}, "ignored": {"0"}})
	_ = pool.QueryRow(ctx, `SELECT ignored FROM it_net_candidates WHERE source = $1`, src).Scan(&ignored)
	if ignored {
		t.Error("zurückholen hat nicht gewirkt")
	}
	// übernehmen
	w = itNetDo(h.ITNetAdoptWeb, "POST", "/it/net/candidates/adopt", "", pdhSystemUserID, url.Values{"mac": {"AA-BB-CC-0A-0B-0C"}, "type": {"printer"}})
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "n=adopted") {
		t.Fatalf("übernehmen: %d %s", w.Code, w.Header().Get("Location"))
	}
	var typ, role string
	if err := pool.QueryRow(ctx, `SELECT type::text, net_role FROM it_assets WHERE net_source = $1`, src).Scan(&typ, &role); err != nil {
		t.Fatalf("Asset fehlt: %v", err)
	}
	if typ != "printer" || role != "client" {
		t.Errorf("Asset: Typ %q Rolle %q", typ, role)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM it_net_candidates WHERE source = $1`, src).Scan(&n)
	if n != 0 {
		t.Errorf("Vorschlag muss weg sein: %d", n)
	}
	// unbekannten Vorschlag ausblenden: Meldung statt Erfolg
	w = itNetDo(h.ITNetIgnoreWeb, "POST", "/it/net/candidates/ignore", "", pdhSystemUserID, url.Values{"mac": {"aa:bb:cc:0a:0b:0c"}, "ignored": {"1"}})
	if !strings.Contains(w.Header().Get("Location"), "e=nocand") {
		t.Errorf("unbekannten Vorschlag ausblenden: %s", w.Header().Get("Location"))
	}
	// noch einmal: kein Vorschlag mehr
	w = itNetDo(h.ITNetAdoptWeb, "POST", "/it/net/candidates/adopt", "", pdhSystemUserID, url.Values{"mac": {"aa:bb:cc:0a:0b:0c"}, "type": {"printer"}})
	if !strings.Contains(w.Header().Get("Location"), "e=nocand") {
		t.Errorf("zweites Übernehmen: %s", w.Header().Get("Location"))
	}
}

// Abteilungsrecht: it.edit nur in Abteilung A → Schalter an Asset in B 403.
func TestITNetMonitorToggleNeedsRecPerm(t *testing.T) {
	h, pool, src := itNetPageEnv(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	id := func(q string, args ...any) string {
		t.Helper()
		var s string
		must(pool.QueryRow(ctx, q, args...).Scan(&s))
		return s
	}
	sfx := strings.TrimPrefix(src, "unifiweb")
	deptA := id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, "ITNet A "+sfx)
	deptB := id(`INSERT INTO departments (name) VALUES ($1) RETURNING id::text`, "ITNet B "+sfx)
	infraA := id(`INSERT INTO infrastructure (name, type, department_id) VALUES ($1, 'line', $2::uuid) RETURNING id::text`, "ITNet-Anlage A "+sfx, deptA)
	infraB := id(`INSERT INTO infrastructure (name, type, department_id) VALUES ($1, 'line', $2::uuid) RETURNING id::text`, "ITNet-Anlage B "+sfx, deptB)
	asset := func(ext, infra string) string {
		return id(`INSERT INTO it_assets (name, type, status, created_by, infrastructure_id, net_source, net_external_id, net_role, net_monitored)
			VALUES ($1, 'network', 'active', $2::uuid, $3::uuid, $4, $5, 'device', true) RETURNING id::text`, "SW "+ext+" "+sfx, pdhSystemUserID, infra, src, ext)
	}
	assetA, assetB := asset("a", infraA), asset("b", infraB)
	user := id(`INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'Ines', 'Netz', 'viewer', '', '') RETURNING id::text`, "itnet"+sfx)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM it_assets WHERE id IN ($1::uuid, $2::uuid)`, assetA, assetB)
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id = $1::uuid`, user)
		_, _ = pool.Exec(c, `DELETE FROM infrastructure WHERE id IN ($1::uuid, $2::uuid)`, infraA, infraB)
		_, _ = pool.Exec(c, `DELETE FROM departments WHERE id IN ($1::uuid, $2::uuid)`, deptA, deptB)
	})
	_, err := pool.Exec(ctx, `INSERT INTO user_permissions (user_id, permission_id, granted, department_id)
		SELECT $1::uuid, p.id, true, $2::uuid FROM permissions p WHERE p.key = 'it.edit'`, user, deptA)
	must(err)
	must(h.rbac.Warm(ctx))
	h.hasPermHook = nil

	monitored := func(aid string) bool {
		var on bool
		must(pool.QueryRow(ctx, `SELECT net_monitored FROM it_assets WHERE id = $1::uuid`, aid).Scan(&on))
		return on
	}
	if w := itNetDo(h.ITNetMonitorWeb, "POST", "/it/"+assetB+"/net-monitor", assetB, user, url.Values{"on": {"0"}}); w.Code != http.StatusForbidden {
		t.Errorf("Abteilung B: %d, erwartet 403", w.Code)
	}
	if !monitored(assetB) {
		t.Error("Asset in B darf sich nicht ändern")
	}
	if w := itNetDo(h.ITNetDetachWeb, "POST", "/it/"+assetB+"/net-detach", assetB, user, url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("Trennen in B: %d, erwartet 403", w.Code)
	}
	if w := itNetDo(h.ITNetMonitorWeb, "POST", "/it/"+assetA+"/net-monitor", assetA, user, url.Values{"on": {"0"}}); w.Code != http.StatusSeeOther {
		t.Errorf("Abteilung A: %d, erwartet 303", w.Code)
	}
	if monitored(assetA) {
		t.Error("Asset in A sollte nicht mehr überwacht sein")
	}
	// Trennen in A: Quelle und ID bleiben, getrennt, nicht überwacht, Ports und Alarmzustand gelöscht
	_, err = pool.Exec(ctx, `UPDATE it_assets SET net_monitored = true, net_state = 'online', net_state_since = now() WHERE id = $1::uuid`, assetA)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO it_asset_ports (asset_id, idx) VALUES ($1::uuid, 1)`, assetA)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO it_net_alarm_state (asset_id) VALUES ($1::uuid)`, assetA)
	must(err)
	if w := itNetDo(h.ITNetDetachWeb, "POST", "/it/"+assetA+"/net-detach", assetA, user, url.Values{}); w.Code != http.StatusSeeOther {
		t.Errorf("Trennen in A: %d", w.Code)
	}
	var keep, detached, mon, sinceNull bool
	var state string
	var ports, alarms int
	must(pool.QueryRow(ctx, `SELECT net_source = $2 AND net_external_id = 'a', net_detached, net_monitored, net_state, net_state_since IS NULL
		FROM it_assets WHERE id = $1::uuid`, assetA, src).Scan(&keep, &detached, &mon, &state, &sinceNull))
	must(pool.QueryRow(ctx, `SELECT count(*) FROM it_asset_ports WHERE asset_id = $1::uuid`, assetA).Scan(&ports))
	must(pool.QueryRow(ctx, `SELECT count(*) FROM it_net_alarm_state WHERE asset_id = $1::uuid`, assetA).Scan(&alarms))
	if !keep || !detached || mon || state != "unknown" || !sinceNull || ports != 0 || alarms != 0 {
		t.Errorf("Trennen: Quelle behalten %v, getrennt %v, überwacht %v, Zustand %s, seit leer %v, Ports %d, Alarm %d",
			keep, detached, mon, state, sinceNull, ports, alarms)
	}
	// Überwachen am getrennten Asset ändert nichts
	itNetDo(h.ITNetMonitorWeb, "POST", "/it/"+assetA+"/net-monitor", assetA, user, url.Values{"on": {"1"}})
	if monitored(assetA) {
		t.Error("getrenntes Asset darf nicht überwacht werden")
	}

	// Wieder verbinden: Abteilungsrecht zählt; Netzwerk-Typ wird wieder überwacht
	_, err = pool.Exec(ctx, `UPDATE it_assets SET net_detached = true, net_monitored = false WHERE id = $1::uuid`, assetB)
	must(err)
	if w := itNetDo(h.ITNetReattachWeb, "POST", "/it/"+assetB+"/net-reattach", assetB, user, url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("Wieder verbinden in B: %d, erwartet 403", w.Code)
	}
	if w := itNetDo(h.ITNetReattachWeb, "POST", "/it/"+assetA+"/net-reattach", assetA, user, url.Values{}); w.Code != http.StatusSeeOther {
		t.Errorf("Wieder verbinden in A: %d", w.Code)
	}
	must(pool.QueryRow(ctx, `SELECT net_detached, net_monitored, net_state FROM it_assets WHERE id = $1::uuid`, assetA).Scan(&detached, &mon, &state))
	if detached || !mon || state != "unknown" {
		t.Errorf("Wieder verbinden: getrennt %v, überwacht %v, Zustand %s", detached, mon, state)
	}
	// Telefon wird wieder verbunden, aber nicht überwacht
	_, err = pool.Exec(ctx, `UPDATE it_assets SET type = 'phone', net_detached = true, net_monitored = false WHERE id = $1::uuid`, assetA)
	must(err)
	itNetDo(h.ITNetReattachWeb, "POST", "/it/"+assetA+"/net-reattach", assetA, user, url.Values{})
	if monitored(assetA) {
		t.Error("Telefon: nach Wiederverbinden nicht überwacht erwartet")
	}
	// Arbeitsplatz ebenfalls nicht, Server und Drucker ja
	for typ, want := range map[string]bool{"workstation": false, "server": true, "printer": true, "other": true} {
		_, err = pool.Exec(ctx, `UPDATE it_assets SET type = $2::text, net_detached = true, net_monitored = false WHERE id = $1::uuid`, assetA, typ)
		must(err)
		itNetDo(h.ITNetReattachWeb, "POST", "/it/"+assetA+"/net-reattach", assetA, user, url.Values{})
		if monitored(assetA) != want {
			t.Errorf("%s: nach Wiederverbinden überwacht=%v, erwartet %v", typ, !want, want)
		}
	}
}

func TestITListNetBadge(t *testing.T) {
	tmpl := loadTestTemplates(t)
	data := ITPageData{Stats: map[string]int{}, NetCandidates: 3, Assets: []ITAssetView{
		{ID: itNetTestID, Name: "Switch Halle", StatusLabel: "Aktiv", StatusClass: "b-green",
			NetSource: true, NetStateLabel: "offline", NetStateClass: "b-red", FirmwareUpdate: true},
		{ID: "22222222-2222-4222-8222-222222222222", Name: "PC ohne Quelle", StatusLabel: "Aktiv", StatusClass: "b-green"},
		{ID: "33333333-3333-4333-8333-333333333333", Name: "Getrennter Drucker", StatusLabel: "Aktiv", StatusClass: "b-green",
			NetSource: true, NetDetached: true, NetStateLabel: "unbekannt", NetStateClass: "b-gray"},
	}}
	html := renderPage(t, tmpl, "it", data)
	if !strings.Contains(html, `badge b-red`) {
		t.Error("Abzeichen offline (b-red) fehlt")
	}
	if !strings.Contains(html, ">Update<") {
		t.Error("Update-Abzeichen fehlt")
	}
	if !strings.Contains(html, `href="/it/net"`) || !strings.Contains(html, ">3<") {
		t.Error("Knopf Netz-Abgleich mit Zähler fehlt")
	}
	if !strings.Contains(html, `href="/it?net=offline"`) {
		t.Error("Filter nur offline fehlt")
	}
	if strings.Count(html, "it-net-state") != 1 {
		t.Errorf("Zustandsabzeichen nur bei Assets mit Quelle (nicht getrennt): %d", strings.Count(html, "it-net-state"))
	}
	if !strings.Contains(html, "Quelle getrennt") {
		t.Error("Abzeichen „Quelle getrennt“ fehlt")
	}
	// Filter: ohne Filter ist „Alle“ hervorgehoben, mit Filter nicht; Typ und net=offline kombinierbar
	allActive := regexp.MustCompile(`href="/it"[^>]*btn-primary`)
	if !allActive.MatchString(html) {
		t.Error("„Alle“ ohne Filter nicht hervorgehoben")
	}
	if !strings.Contains(html, `href="/it?type=server&amp;net=offline"`) && !strings.Contains(html, `href="/it?net=offline"`) {
		t.Error("„Nur offline“ fehlt")
	}
	data.NetFilter, data.Filter = "offline", "server"
	html = renderPage(t, tmpl, "it", data)
	if allActive.MatchString(html) {
		t.Error("„Alle“ bei aktivem Filter hervorgehoben")
	}
	if !strings.Contains(html, `href="/it?type=network&amp;net=offline"`) {
		t.Error("Typfilter verliert net=offline")
	}
	if !regexp.MustCompile(`href="/it\?type=server"[^>]*btn-primary`).MatchString(html) {
		t.Error("„Nur offline“ (aktiv) muss beim Abwählen den Typ behalten und hervorgehoben sein")
	}
	data.NetFilter = ""
	html = renderPage(t, tmpl, "it", data)
	if !strings.Contains(html, `href="/it?type=server&amp;net=offline"`) {
		t.Error("„Nur offline“ verliert den Typfilter")
	}
}

func TestITDetailNetCard(t *testing.T) {
	tmpl := loadTestTemplates(t)
	data := ITDetailData{Asset: ITAssetDetailView{ID: itNetTestID, Name: "Switch Halle"}, CanNetEdit: true,
		Net: &ITNetDetailView{StateLabel: "online", StateClass: "b-green", Monitored: true, Firmware: "7.1.2", FirmwareUpdate: true,
			Uplink: "UDM Pro", Ports: []ITNetPortView{{Idx: 1, State: "UP", Speed: "1 Gbit/s"}}}}
	html := renderPage(t, tmpl, "it_detail", data)
	for _, want := range []string{"Netz", "7.1.2", "UDM Pro", "overflow-x:auto", `action="/it/` + itNetTestID + `/net-monitor"`, `action="/it/` + itNetTestID + `/net-detach"`, "confirm("} {
		if !strings.Contains(html, want) {
			t.Errorf("Netzkarte ohne %q", want)
		}
	}
	data.CanNetEdit = false
	html = renderPage(t, tmpl, "it_detail", data)
	if strings.Contains(html, "/net-monitor") || strings.Contains(html, "/net-detach") {
		t.Error("ohne it.edit keine Schalter")
	}
	data.Net.Source = "UniFi"
	if html = renderPage(t, tmpl, "it_detail", data); !strings.Contains(html, "UniFi ·") {
		t.Error("Quelle als „UniFi“ erwartet")
	}
	data.Net = nil
	if html = renderPage(t, tmpl, "it_detail", data); strings.Contains(html, "it-net-card") {
		t.Error("ohne Quelle keine Netzkarte")
	}
	// getrennt: keine Netzkarte, Abzeichen und Knopf „Wieder verbinden“
	data.NetDetached, data.CanNetEdit = true, true
	html = renderPage(t, tmpl, "it_detail", data)
	if strings.Contains(html, "it-net-card") || !strings.Contains(html, "Quelle getrennt") || !strings.Contains(html, `action="/it/`+itNetTestID+`/net-reattach"`) {
		t.Error("getrennt: Abzeichen und Wieder verbinden erwartet, keine Netzkarte")
	}
	data.CanNetEdit = false
	if html = renderPage(t, tmpl, "it_detail", data); strings.Contains(html, "/net-reattach") {
		t.Error("ohne it.edit kein Wieder verbinden")
	}
}

func TestITNetSourceName(t *testing.T) {
	if itNetSourceName("unifi") != "UniFi" || itNetSourceName("anders") != "anders" {
		t.Errorf("Quellnamen: %q %q", itNetSourceName("unifi"), itNetSourceName("anders"))
	}
}
