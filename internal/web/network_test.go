package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestNetworkSegmentForm(t *testing.T) {
	ok := url.Values{"name": {" Maschinen "}, "vlan": {"20"}, "subnet": {"10.10.20.7/24"}, "purpose": {"Sensoren"}}
	name, vlan, subnet, purpose, _, errKey := networkSegmentForm(ok)
	if errKey != "" || name != "Maschinen" || vlan == nil || *vlan != 20 || subnet != "10.10.20.0/24" || purpose != "Sensoren" {
		t.Fatalf("gültig: %q %v %q %q %q", name, vlan, subnet, purpose, errKey)
	}
	if _, vlan, subnet, _, _, errKey := networkSegmentForm(url.Values{"name": {"Büro"}}); errKey != "" || vlan != nil || subnet != "" {
		t.Errorf("ohne VLAN und Bereich: %v %q %q", vlan, subnet, errKey)
	}
	for want, f := range map[string]url.Values{
		"name":   {"name": {"x"}},
		"vlan":   {"name": {"Netz"}, "vlan": {"4095"}},
		"subnet": {"name": {"Netz"}, "subnet": {"10.10.20.0"}},
		"text":   {"name": {"Netz"}, "purpose": {strings.Repeat("a", 201)}},
	} {
		if _, _, _, _, _, got := networkSegmentForm(f); got != want {
			t.Errorf("%v: Fehler %q, erwartet %q", f, got, want)
		}
	}
	if _, _, _, _, _, got := networkSegmentForm(url.Values{"name": {"Netz"}, "vlan": {"0"}}); got != "vlan" {
		t.Errorf("VLAN 0: %q", got)
	}
}

func TestNetworkGateRules(t *testing.T) {
	id := "/11111111-1111-4111-8111-111111111111"
	for path, want := range map[string]string{
		"/admin/network/segments":                  permNetwork,
		"/admin/network/segments" + id:             permNetwork,
		"/admin/network/wlans":                     permNetwork,
		"/admin/network/wlans" + id:                permNetwork,
		"/admin/network/segments" + id + "/delete": permNetworkAdmin,
		"/admin/network/wlans" + id + "/delete":    permNetworkAdmin,
	} {
		if _, perm, _, _, ok := matchGate("POST", path); !ok || perm != want {
			t.Errorf("%s: Treffer %v Recht %q, erwartet %q", path, ok, perm, want)
		}
	}
	if areaAdminPerm["network"] != permNetworkAdmin {
		t.Error("Bereichsadministrator für „network“ fehlt")
	}
	// ohne Recht lehnen alle Handler ab, bevor sie die Datenbank anfassen
	h := &Handler{hasPermHook: func(*http.Request, string) bool { return false }}
	for name, fn := range map[string]http.HandlerFunc{"Seite": h.NetworkPage, "Netz": h.NetworkSegmentSaveWeb, "WLAN": h.NetworkWLANSaveWeb,
		"Netz löschen": h.NetworkSegmentDeleteWeb, "WLAN löschen": h.NetworkWLANDeleteWeb} {
		method := "POST"
		if name == "Seite" {
			method = "GET"
		}
		if w := itNetDo(fn, method, "/admin/network", itNetTestID, "", url.Values{}); w.Code != http.StatusForbidden {
			t.Errorf("%s ohne Recht: %d", name, w.Code)
		}
	}
	// Pflegen allein reicht nicht zum Löschen
	h.hasPermHook = func(_ *http.Request, p string) bool { return p == permNetwork }
	if w := itNetDo(h.NetworkWLANDeleteWeb, "POST", "/x", itNetTestID, "", url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("Löschen mit system.network: %d", w.Code)
	}
}

// Ablauf mit Datenbank: anlegen, Dopplung, Kennwort verschlüsselt und nie auf der
// Seite, Bearbeiten behält das Kennwort, Löschen des Netzes lässt das WLAN stehen.
func TestNetworkFlow(t *testing.T) {
	h, pool, _ := itNetPageEnv(t)
	h.jwtSecret = "t"
	h.hasPermHook = func(*http.Request, string) bool { return true }
	ctx := context.Background()
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	segName, ssid, pw := "Testnetz "+sfx, "pdh-test-"+sfx[len(sfx)-8:], "Sehr-Geheim-"+sfx
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM net_wlans WHERE ssid = $1`, ssid)
		_, _ = pool.Exec(c, `DELETE FROM net_segments WHERE name = $1`, segName)
	})
	loc := func(fn http.HandlerFunc, id string, v url.Values) string {
		t.Helper()
		w := itNetDo(fn, "POST", "/x", id, pdhSystemUserID, v)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("Status %d: %s", w.Code, w.Body.String())
		}
		return w.Header().Get("Location")
	}
	if l := loc(h.NetworkSegmentSaveWeb, "", url.Values{"name": {segName}, "subnet": {"10.99.1.0/24"}}); !strings.Contains(l, "n=seg-new") {
		t.Fatalf("Netz anlegen: %s", l)
	}
	if l := loc(h.NetworkSegmentSaveWeb, "", url.Values{"name": {strings.ToUpper(segName)}}); !strings.Contains(l, "e=seg-dup") {
		t.Errorf("gleicher Name: %s", l)
	}
	var segID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM net_segments WHERE name = $1`, segName).Scan(&segID); err != nil {
		t.Fatal(err)
	}
	for want, v := range map[string]url.Values{
		"e=ssid":     {"ssid": {strings.Repeat("s", 33)}},
		"e=password": {"ssid": {ssid}, "password": {"kurz"}},
		"e=segment":  {"ssid": {ssid}, "segment": {itNetTestID}},
	} {
		if l := loc(h.NetworkWLANSaveWeb, "", v); !strings.Contains(l, want) {
			t.Errorf("%s erwartet: %s", want, l)
		}
	}
	if l := loc(h.NetworkWLANSaveWeb, "", url.Values{"ssid": {ssid}, "password": {pw}, "segment": {segID}, "hidden": {"on"}}); !strings.Contains(l, "n=wlan-new") {
		t.Fatalf("WLAN anlegen: %s", l)
	}
	if l := loc(h.NetworkWLANSaveWeb, "", url.Values{"ssid": {ssid}}); !strings.Contains(l, "e=wlan-dup") {
		t.Errorf("gleiches WLAN: %s", l)
	}
	var wlanID, enc string
	if err := pool.QueryRow(ctx, `SELECT id::text, password_enc FROM net_wlans WHERE ssid = $1`, ssid).Scan(&wlanID, &enc); err != nil {
		t.Fatal(err)
	}
	if enc == "" || strings.Contains(enc, pw) {
		t.Fatalf("Kennwort nicht verschlüsselt: %q", enc)
	}
	body := itNetDo(h.NetworkPage, "GET", "/admin/network", "", pdhSystemUserID, nil).Body.String()
	for _, want := range []string{segName, ssid, "10.99.1.0/24", "hinterlegt", "versteckt", `/admin/network/wlans/` + wlanID + `/delete`} {
		if !strings.Contains(body, want) {
			t.Errorf("Seite ohne %q", want)
		}
	}
	if strings.Contains(body, pw) || strings.Contains(body, enc) {
		t.Fatal("Kennwort steht auf der Seite")
	}
	// ohne Bereichsadministrator: kein Löschknopf
	h.hasPermHook = func(_ *http.Request, p string) bool { return p != permNetworkAdmin }
	if b := itNetDo(h.NetworkPage, "GET", "/admin/network", "", pdhSystemUserID, nil).Body.String(); strings.Contains(b, "/admin/network/wlans/"+wlanID+"/delete") || strings.Contains(b, "/admin/network/segments/"+segID+"/delete") {
		t.Error("Löschknopf ohne system.network_admin")
	}
	h.hasPermHook = func(*http.Request, string) bool { return true }
	// Bearbeiten mit leerem Kennwort behält es
	if l := loc(h.NetworkWLANSaveWeb, wlanID, url.Values{"ssid": {ssid}, "note": {"Halle 2"}, "segment": {segID}}); !strings.Contains(l, "n=wlan-saved") {
		t.Fatalf("WLAN ändern: %s", l)
	}
	if s, p, err := h.networkWLANSecret(ctx, wlanID); err != nil || s != ssid || p != pw {
		t.Errorf("Kennwort nach Bearbeiten: %q %v", s, err)
	}
	// Netz löschen: das WLAN bleibt, ohne Netz
	if l := loc(h.NetworkSegmentDeleteWeb, segID, url.Values{}); !strings.Contains(l, "n=seg-deleted") {
		t.Fatalf("Netz löschen: %s", l)
	}
	var seg *string
	if err := pool.QueryRow(ctx, `SELECT segment_id::text FROM net_wlans WHERE id = $1::uuid`, wlanID).Scan(&seg); err != nil || seg != nil {
		t.Errorf("WLAN nach Löschen des Netzes: %v %v", seg, err)
	}
	// Kennwort ausdrücklich entfernen
	loc(h.NetworkWLANSaveWeb, wlanID, url.Values{"ssid": {ssid}, "open": {"1"}})
	if _, p, err := h.networkWLANSecret(ctx, wlanID); err != nil || p != "" {
		t.Errorf("offenes WLAN: %q %v", p, err)
	}
	if l := loc(h.NetworkWLANDeleteWeb, wlanID, url.Values{}); !strings.Contains(l, "n=wlan-deleted") {
		t.Errorf("WLAN löschen: %s", l)
	}
	if l := loc(h.NetworkWLANDeleteWeb, wlanID, url.Values{}); !strings.Contains(l, "e=gone") {
		t.Errorf("zweites Löschen: %s", l)
	}
	if _, _, err := h.networkWLANSecret(ctx, wlanID); err == nil {
		t.Error("gelöschtes WLAN liefert noch Daten")
	}
}

// Haken „Für ESPPDH-Geräte nutzbar“ je WLAN: Vorgabe aus, setzen und wieder entfernen über das
// Formular, Abzeichen in der Liste, Hinweis unter dem Haken. Ohne system.network lässt er sich
// nicht setzen (403, nichts geändert) – auch nicht mit den Rechten von ESPPDH.
func TestNetworkWLANForESPPDH(t *testing.T) {
	h, pool, _ := itNetPageEnv(t)
	h.jwtSecret = "t"
	h.hasPermHook = func(*http.Request, string) bool { return true }
	ctx := context.Background()
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	ssid := "pdh-espok-" + sfx[len(sfx)-8:]
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM net_wlans WHERE ssid = $1`, ssid) })
	flag := func() bool {
		t.Helper()
		var ok bool
		if err := pool.QueryRow(ctx, `SELECT esppdh_ok FROM net_wlans WHERE ssid = $1`, ssid).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	save := func(id string, v url.Values) *http.Response {
		t.Helper()
		return itNetDo(h.NetworkWLANSaveWeb, "POST", "/x", id, pdhSystemUserID, v).Result()
	}
	if r := save("", url.Values{"ssid": {ssid}, "password": {"Geraete-Geheim-1"}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("WLAN anlegen: %d", r.StatusCode)
	}
	if flag() {
		t.Fatal("neues WLAN ohne Haken ist für ESPPDH freigegeben")
	}
	var id string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM net_wlans WHERE ssid = $1`, ssid).Scan(&id); err != nil {
		t.Fatal(err)
	}
	body := itNetDo(h.NetworkPage, "GET", "/admin/network", "", pdhSystemUserID, nil).Body.String()
	for _, want := range []string{`name="esppdh_ok"`, "Für ESPPDH-Geräte nutzbar",
		"Wer ESPPDH-Geräte bauen darf, kann das Kennwort dieses WLANs aus der Firmware lesen. Am besten ein eigenes WLAN nur für Geräte verwenden."} {
		if !strings.Contains(body, want) {
			t.Errorf("Seite ohne %q", want)
		}
	}
	if strings.Contains(body, `data-esppdh="`+id+`"`) {
		t.Error("Abzeichen ESPPDH ohne Haken")
	}
	// Haken setzen: gespeichert, Abzeichen in der Liste, Haken im Formular gesetzt
	if r := save(id, url.Values{"ssid": {ssid}, "esppdh_ok": {"on"}}); r.StatusCode != http.StatusSeeOther || !flag() {
		t.Fatalf("Haken setzen: %d %v", r.StatusCode, flag())
	}
	body = itNetDo(h.NetworkPage, "GET", "/admin/network", "", pdhSystemUserID, nil).Body.String()
	if !strings.Contains(body, `data-esppdh="`+id+`"`) || !strings.Contains(body, `id="nw-w-esp-`+id+`" name="esppdh_ok" checked`) {
		t.Error("Abzeichen in der Liste oder gesetzter Haken im Formular fehlt")
	}
	// ohne system.network: abgelehnt, auch mit allen Rechten von ESPPDH
	h.hasPermHook = func(_ *http.Request, p string) bool { return p == permESPView || p == permESPEdit || p == permESPAdmin }
	if _, err := pool.Exec(ctx, `UPDATE net_wlans SET esppdh_ok = false WHERE id = $1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	if r := save(id, url.Values{"ssid": {ssid}, "esppdh_ok": {"on"}}); r.StatusCode != http.StatusForbidden || flag() {
		t.Errorf("Haken ohne system.network: %d, gesetzt %v", r.StatusCode, flag())
	}
	if r := save("", url.Values{"ssid": {ssid + "x"}, "esppdh_ok": {"on"}}); r.StatusCode != http.StatusForbidden {
		t.Errorf("WLAN mit Haken anlegen ohne system.network: %d", r.StatusCode)
	}
	// mit system.network: wieder setzen, dann ohne Haken speichern = entfernt
	h.hasPermHook = func(_ *http.Request, p string) bool { return p == permNetwork }
	save(id, url.Values{"ssid": {ssid}, "esppdh_ok": {"on"}})
	if !flag() {
		t.Fatal("Haken mit system.network nicht gesetzt")
	}
	save(id, url.Values{"ssid": {ssid}})
	if flag() {
		t.Error("Haken nach dem Speichern ohne Haken noch gesetzt")
	}
}

// Die Migration vergibt die neuen Rechte an die Rollen mit system.unifi.
func TestNetworkMigrationGrants(t *testing.T) {
	_, pool, _ := itNetPageEnv(t)
	ctx := context.Background()
	var perms, missing int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM permissions WHERE key IN ('system.network', 'system.network_admin')`).Scan(&perms)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM role_permissions rp JOIN permissions o ON o.id = rp.permission_id AND o.key = 'system.unifi'
		WHERE NOT EXISTS (SELECT 1 FROM role_permissions x JOIN permissions p ON p.id = x.permission_id AND p.key = 'system.network' WHERE x.role_id = rp.role_id)`).Scan(&missing)
	if perms != 2 || missing != 0 {
		t.Errorf("Rechte %d (2 erwartet), Rollen mit system.unifi ohne system.network: %d", perms, missing)
	}
}
