package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"pdh/internal/core/users"
	"pdh/internal/modules/it"
)

// itCatAssetCat legt eine eigene Kategorie an (optional ausgeblendet).
func itCatAssetCat(t *testing.T, h *Handler, name, icon string, sort int, active bool) string {
	t.Helper()
	ctx := context.Background()
	c := &it.Category{Name: name, Icon: icon, SortOrder: sort}
	if err := h.it.CreateCategory(ctx, c); err != nil {
		t.Fatal(err)
	}
	if !active {
		if err := h.it.SetCategoryActive(ctx, c.Key, false); err != nil {
			t.Fatal(err)
		}
	}
	return c.Key
}

// itCatAssetRow legt ein Asset in einer Kategorie an (Name endet auf die Kennung → Aufräumen).
func itCatAssetRow(t *testing.T, h *Handler, name, key, src string) string {
	t.Helper()
	var id string
	var err error
	if src == "" {
		err = h.db.QueryRow(context.Background(), `INSERT INTO it_assets (name, type, status, created_by) VALUES ($1, $2, 'active', $3::uuid) RETURNING id::text`,
			name, key, pdhSystemUserID).Scan(&id)
	} else {
		err = h.db.QueryRow(context.Background(), `INSERT INTO it_assets (name, type, status, created_by, net_source, net_external_id, net_role, net_monitored)
			VALUES ($1, $2, 'active', $3::uuid, $4, $5, 'client', true) RETURNING id::text`, name, key, pdhSystemUserID, src, name+"-ext").Scan(&id)
	}
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestITListUsesCategories(t *testing.T) {
	h, sfx := itCatEnv(t)
	h.hasPermHook = func(*http.Request, string) bool { return true }
	kam := itCatAssetCat(t, h, "Kameras "+sfx, "ti-camera", 900, true)
	leer := itCatAssetCat(t, h, "Leere "+sfx, "ti-plane", 901, true)
	alt := itCatAssetCat(t, h, "Altgeräte "+sfx, "ti-ghost", 902, false)
	itCatAssetRow(t, h, "Kamera Tor "+sfx, kam, "")
	itCatAssetRow(t, h, "Altes Gerät "+sfx, alt, "")

	body := itNetDo(h.ITPage, "GET", "/it", "", "", nil).Body.String()
	for _, want := range []string{"Kameras " + sfx, "ti-camera", "Altgeräte " + sfx, "ti-ghost", `href="/it?type=` + kam + `"`} {
		if !strings.Contains(body, want) {
			t.Errorf("Liste ohne %q", want)
		}
	}
	if strings.Contains(body, `href="/it?type=`+leer+`"`) {
		t.Error("Kategorie ohne Asset hat einen Filterknopf")
	}
	if strings.Contains(body, `href="/it?type=`+alt+`"`) {
		t.Error("ausgeblendete Kategorie hat einen Filterknopf")
	}
	// Neu-Formular: nur aktive Kategorien (auch die leere), keine ausgeblendete
	form := body[strings.Index(body, `id="new-asset"`):]
	form = form[:strings.Index(form, "</select>")]
	if !strings.Contains(form, `value="`+kam+`"`) || !strings.Contains(form, `value="`+leer+`"`) || !strings.Contains(form, `value="server"`) {
		t.Error("Neu-Formular ohne aktive Kategorien")
	}
	if strings.Contains(form, `value="`+alt+`"`) {
		t.Error("Neu-Formular bietet ausgeblendete Kategorie an")
	}
	// Filter auf ausgeblendete Kategorie funktioniert weiter
	body = itNetDo(h.ITPage, "GET", "/it?type="+alt, "", "", nil).Body.String()
	if !strings.Contains(body, "Altes Gerät "+sfx) {
		t.Error("Filter auf ausgeblendete Kategorie zeigt das Asset nicht")
	}
}

func TestITDetailUsesCategories(t *testing.T) {
	h, sfx := itCatEnv(t)
	h.users = users.NewService(users.NewRepository(h.db), "t", 1)
	h.hasPermHook = func(*http.Request, string) bool { return true }
	kam := itCatAssetCat(t, h, "Kameras "+sfx, "ti-camera", 900, true)
	alt := itCatAssetCat(t, h, "Altgeräte "+sfx, "ti-ghost", 902, false)
	idKam := itCatAssetRow(t, h, "Kamera Tor "+sfx, kam, "")
	idAlt := itCatAssetRow(t, h, "Altes Gerät "+sfx, alt, "")

	body := itNetDo(h.ITDetail, "GET", "/it/"+idKam, idKam, "", nil).Body.String()
	if !strings.Contains(body, "Kameras "+sfx) || !strings.Contains(body, "ti-camera") {
		t.Error("Detail ohne Name/Symbol der Kategorie")
	}
	if strings.Contains(body, `value="`+alt+`"`) {
		t.Error("Bearbeiten bietet fremde ausgeblendete Kategorie an")
	}
	if !strings.Contains(body, `value="`+kam+`" selected`) {
		t.Error("eigene Kategorie nicht gewählt")
	}
	body = itNetDo(h.ITDetail, "GET", "/it/"+idAlt, idAlt, "", nil).Body.String()
	if !strings.Contains(body, "ti-ghost") || !strings.Contains(body, "Altgeräte "+sfx) {
		t.Error("ausgeblendete Kategorie am Asset nicht sichtbar")
	}
	if !strings.Contains(body, `value="`+alt+`" selected`) || !strings.Contains(body, "(ausgeblendet)") {
		t.Error("Bearbeiten bietet die ausgeblendete Kategorie des Assets nicht an")
	}
	if !strings.Contains(body, `value="`+kam+`"`) {
		t.Error("Bearbeiten ohne aktive Kategorien")
	}
}

func TestITCreateEditCategoryChecks(t *testing.T) {
	h, sfx := itCatEnv(t)
	h.hasPermHook = func(*http.Request, string) bool { return true }
	ctx := context.Background()
	kam := itCatAssetCat(t, h, "Kameras "+sfx, "ti-camera", 900, true)
	alt := itCatAssetCat(t, h, "Altgeräte "+sfx, "ti-ghost", 902, false)
	user := pdhSystemUserID

	w := itNetDo(h.ITCreate, "POST", "/it", "", user, url.Values{"name": {"Neu " + sfx}, "type": {kam}})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Kameras "+sfx) || !strings.Contains(w.Body.String(), "ti-camera") || strings.Contains(w.Body.String(), "Fehler") {
		t.Errorf("anlegen: %d %s", w.Code, w.Body.String())
	}
	for _, typ := range []string{alt, "gibtsnicht", ""} {
		w = itNetDo(h.ITCreate, "POST", "/it", "", user, url.Values{"name": {"Neu2 " + sfx}, "type": {typ}})
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Fehler") || !strings.Contains(w.Body.String(), "Kategorie") {
			t.Errorf("anlegen mit %q: %d %s", typ, w.Code, w.Body.String())
		}
	}
	var n int
	_ = h.db.QueryRow(ctx, `SELECT count(*) FROM it_assets WHERE name = $1`, "Neu2 "+sfx).Scan(&n)
	if n != 0 {
		t.Error("Asset mit ungültiger Kategorie angelegt")
	}

	// Bearbeiten: Asset in ausgeblendeter Kategorie darf bleiben, aber nicht auf eine andere ausgeblendete wechseln
	alt2 := itCatAssetCat(t, h, "Altgeräte2 "+sfx, "ti-ghost", 903, false)
	id := itCatAssetRow(t, h, "Altes Gerät "+sfx, alt, "")
	post := func(typ string) int {
		return itNetDo(h.ITEditWeb, "POST", "/it/"+id+"/edit-web", id, user, url.Values{"name": {"Altes Gerät " + sfx}, "type": {typ}}).Code
	}
	if c := post(alt); c != http.StatusFound {
		t.Errorf("eigene ausgeblendete Kategorie behalten: %d", c)
	}
	for _, typ := range []string{alt2, "gibtsnicht"} {
		if c := post(typ); c != http.StatusBadRequest {
			t.Errorf("Wechsel auf %q: %d, erwartet 400", typ, c)
		}
	}
	if c := post(kam); c != http.StatusFound {
		t.Errorf("Wechsel auf aktive Kategorie: %d", c)
	}
	var typ string
	_ = h.db.QueryRow(ctx, `SELECT type FROM it_assets WHERE id = $1::uuid`, id).Scan(&typ)
	if typ != kam {
		t.Errorf("Kategorie nach Bearbeiten %q", typ)
	}
}

func TestITNetAdoptCategoryOptions(t *testing.T) {
	h, sfx := itCatEnv(t)
	h.hasPermHook = func(*http.Request, string) bool { return true }
	ctx := context.Background()
	kam := itCatAssetCat(t, h, "Kameras "+sfx, "ti-camera", 900, true)
	alt := itCatAssetCat(t, h, "Altgeräte "+sfx, "ti-ghost", 902, false)
	// Vorschläge zeigt nur eine eingeschaltete Quelle
	itNetKeepSettings(t, h, keyITNetUnifiOn)
	if err := h.setAppSetting(ctx, keyITNetUnifiOn, "1"); err != nil {
		t.Fatal(err)
	}
	src := h.itNetSource()
	if _, err := h.db.Exec(ctx, `INSERT INTO it_net_candidates (source, mac, name, ip, kind, uplink, last_seen) VALUES ($1, 'aa:bb:cc:00:00:01', 'Kandidat', '10.0.0.9', 'wired', '', now())`, src); err != nil {
		t.Fatal(err)
	}
	body := itNetDo(h.ITNetPage, "GET", "/it/net", "", "", nil).Body.String()
	sel := body[strings.Index(body, `aria-label="Typ des neuen Assets"`):]
	sel = sel[:strings.Index(sel, "</select>")]
	if !strings.Contains(sel, `value="`+kam+`"`) || !strings.Contains(sel, "Kameras "+sfx) || !strings.Contains(sel, `value="workstation"`) {
		t.Error("Auswahl ohne aktive Kategorien")
	}
	if strings.Contains(sel, `value="network"`) || strings.Contains(sel, `value="`+alt+`"`) {
		t.Error("Auswahl enthält network oder ausgeblendete Kategorie")
	}
	if strings.Contains(body, "Arbeitsplätze, Telefone und Tablets werden zunächst nicht überwacht") {
		t.Error("alter Festtext steht noch da")
	}
	if !strings.Contains(body, `href="/it/categories"`) || !strings.Contains(body, "überwachen") {
		t.Error("Hinweis auf die Einstellung „überwachen“ der Kategorie fehlt")
	}
	for _, typ := range []string{alt, "network", "gibtsnicht"} {
		w := itNetDo(h.ITNetAdoptWeb, "POST", "/it/net/candidates/adopt", "", pdhSystemUserID, url.Values{"mac": {"aa:bb:cc:00:00:01"}, "type": {typ}})
		if !strings.Contains(w.Header().Get("Location"), "e=type") {
			t.Errorf("Übernehmen als %q: %s", typ, w.Header().Get("Location"))
		}
	}
}

func TestITNetGraceRoute(t *testing.T) {
	h, sfx := itCatEnv(t)
	ctx := context.Background()
	src := h.itNetSource()
	kam := itCatAssetCat(t, h, "Kameras "+sfx, "ti-camera", 900, true)
	id := itCatAssetRow(t, h, "Kamera Tor "+sfx, kam, src)
	grace := func() (g *int) {
		_ = h.db.QueryRow(ctx, `SELECT net_grace_minutes FROM it_assets WHERE id = $1::uuid`, id).Scan(&g)
		return
	}
	do := func(path, aid string, form url.Values) *httptest.ResponseRecorder {
		return itNetDo(h.ITNetGraceWeb, "POST", path, aid, pdhSystemUserID, form)
	}

	h.hasPermHook = func(*http.Request, string) bool { return false }
	if w := do("/it/"+id+"/net-grace", id, url.Values{"minutes": {"20"}}); w.Code != http.StatusForbidden || grace() != nil {
		t.Errorf("ohne it.edit: %d", w.Code)
	}
	h.hasPermHook = func(*http.Request, string) bool { return true }
	if w := do("/it/"+id+"/net-grace", id, url.Values{"minutes": {"20"}}); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/it/"+id+"?n=grace" {
		t.Errorf("setzen: %d %s", w.Code, w.Header().Get("Location"))
	}
	if g := grace(); g == nil || *g != 20 {
		t.Errorf("gesetzt: %v", g)
	}
	for _, bad := range []string{"0", "1441", "abc", "-3"} {
		w := do("/it/"+id+"/net-grace", id, url.Values{"minutes": {bad}})
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/it/"+id+"?e=grace" {
			t.Errorf("ungültig %q: %d %s", bad, w.Code, w.Header().Get("Location"))
		}
		if g := grace(); g == nil || *g != 20 {
			t.Errorf("ungültig %q ändert den Wert: %v", bad, g)
		}
	}
	if w := do("/it/"+id+"/net-grace", id, url.Values{"minutes": {" "}}); w.Header().Get("Location") != "/it/"+id+"?n=grace-reset" || grace() != nil {
		t.Errorf("leeren: %s %v", w.Header().Get("Location"), grace())
	}
	if w := do("/it/x/net-grace", "x", url.Values{"minutes": {"5"}}); w.Code != http.StatusNotFound {
		t.Errorf("ungültige ID: %d", w.Code)
	}
	if w := do("/it/y/net-grace", "00000000-0000-4000-8000-00000000dead", url.Values{"minutes": {"5"}}); w.Code != http.StatusNotFound {
		t.Errorf("unbekanntes Asset: %d", w.Code)
	}
	_, perm, ref, _, hit := matchGate("POST", "/it/"+id+"/net-grace")
	if !hit || perm != "it.edit" || ref != "it_asset" {
		t.Errorf("Gate: %v %q %q", hit, perm, ref)
	}
}

func TestITDetailGraceOrigin(t *testing.T) {
	h, sfx := itCatEnv(t)
	h.users = users.NewService(users.NewRepository(h.db), "t", 1)
	ctx := context.Background()
	h.hasPermHook = func(*http.Request, string) bool { return true }
	src := h.itNetSource()
	kam := itCatAssetCat(t, h, "Kameras "+sfx, "ti-camera", 900, true)
	id := itCatAssetRow(t, h, "Kamera Tor "+sfx, kam, src)
	general := int(h.itNetGrace(ctx).Minutes())
	page := func() string { return itNetDo(h.ITDetail, "GET", "/it/"+id, id, pdhSystemUserID, nil).Body.String() }
	want := func(body, text string) {
		t.Helper()
		if !strings.Contains(body, text) {
			t.Errorf("Karte Netz ohne %q", text)
		}
	}
	want(page(), "Karenz bis zum Alarm: "+strconv.Itoa(general)+" min (allgemein)")
	if _, err := h.db.Exec(ctx, `UPDATE it_asset_categories SET grace_minutes = 45 WHERE key = $1`, kam); err != nil {
		t.Fatal(err)
	}
	want(page(), "Karenz bis zum Alarm: 45 min (Kategorie)")
	if _, err := h.db.Exec(ctx, `UPDATE it_assets SET net_grace_minutes = 7 WHERE id = $1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	body := page()
	want(body, "Karenz bis zum Alarm: 7 min (eigener Wert)")
	want(body, `action="/it/`+id+`/net-grace"`)
	h.hasPermHook = func(_ *http.Request, p string) bool { return p != "it.edit" }
	if body = page(); strings.Contains(body, "/net-grace") {
		t.Error("ohne it.edit ein Feld für die Karenz")
	}
	want(body, "Karenz bis zum Alarm: 7 min (eigener Wert)")
}

func TestITDetailGraceMessagesWrap(t *testing.T) {
	h, sfx := itCatEnv(t)
	h.hasPermHook = func(*http.Request, string) bool { return true }
	h.users = users.NewService(users.NewRepository(h.db), "t", 1)
	kam := itCatAssetCat(t, h, "Kameras "+sfx, "ti-camera", 900, true)
	id := itCatAssetRow(t, h, "Kamera Tor "+sfx, kam, h.itNetSource())
	for q, want := range map[string]string{
		"n=grace-reset": "Eigener Wert entfernt", "e=grace": "Karenz: bitte 1 bis 1440",
	} {
		body := itNetDo(h.ITDetail, "GET", "/it/"+id+"?"+q, id, pdhSystemUserID, nil).Body.String()
		i := strings.Index(body, want)
		if i < 0 {
			t.Fatalf("%s: Meldung %q fehlt", q, want)
		}
		// umbrechende Karte, kein nowrap-Abzeichen
		if open := body[strings.LastIndex(body[:i], "<div"):i]; !strings.Contains(open, `class="card"`) || strings.Contains(open, "badge") {
			t.Errorf("%s: Meldung nicht als Karte: %s", q, open)
		}
	}
}
