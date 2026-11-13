package web

import (
	"context"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseConnRoute(t *testing.T) {
	cases := []struct {
		cfg  map[string]string
		want connRoute
	}{
		{nil, connRoute{}},
		{map[string]string{}, connRoute{}},
		{map[string]string{"route": ""}, connRoute{}},
		{map[string]string{"route": "direct"}, connRoute{}},
		{map[string]string{"route": "vpn:abc"}, connRoute{ViaVPN: true, PeerID: "abc"}},
		{map[string]string{"route": " vpn:abc "}, connRoute{ViaVPN: true, PeerID: "abc"}},
		{map[string]string{"route": "vpn:"}, connRoute{}},
		{map[string]string{"route": "unsinn"}, connRoute{}},
	}
	for _, c := range cases {
		if got := parseConnRoute(c.cfg); got != c.want {
			t.Errorf("parseConnRoute(%v) = %+v, erwartet %+v", c.cfg, got, c.want)
		}
	}
}

func TestConnQueryTimeout(t *testing.T) {
	vpn := func(v string) map[string]string {
		m := map[string]string{"route": "vpn:abc"}
		if v != "" {
			m["vpn_timeout_seconds"] = v
		}
		return m
	}
	cases := []struct {
		name string
		cfg  map[string]string
		want time.Duration
	}{
		{"direkt", nil, 15 * time.Second},
		{"direkt, VPN-Wert wirkungslos", map[string]string{"vpn_timeout_seconds": "120"}, 15 * time.Second},
		{"VPN ohne Wert", vpn(""), 60 * time.Second},
		{"VPN unlesbar", vpn("abc"), 60 * time.Second},
		{"VPN 2 -> 5", vpn("2"), 5 * time.Second},
		{"VPN 9999 -> 600", vpn("9999"), 600 * time.Second},
		{"VPN 90", vpn("90"), 90 * time.Second},
	}
	for _, c := range cases {
		if got := connQueryTimeout(c.cfg); got != c.want {
			t.Errorf("%s: %v, erwartet %v", c.name, got, c.want)
		}
	}
}

func TestConnPollInterval(t *testing.T) {
	vpn := func(v string) map[string]string {
		m := map[string]string{"route": "vpn:abc"}
		if v != "" {
			m["vpn_poll_minutes"] = v
		}
		return m
	}
	cases := []struct {
		name    string
		cfg     map[string]string
		profile int
		want    time.Duration
	}{
		{"direkt zählt Profil", nil, 10, 10 * time.Minute},
		{"VPN ohne Wert: 5, Profil zählt nicht", vpn(""), 10, 5 * time.Minute},
		{"VPN 0 -> 1", vpn("0"), 10, 1 * time.Minute},
		{"VPN 99999 -> 1440", vpn("99999"), 10, 1440 * time.Minute},
		{"VPN 15", vpn("15"), 10, 15 * time.Minute},
	}
	for _, c := range cases {
		if got := connPollInterval(c.cfg, c.profile); got != c.want {
			t.Errorf("%s: %v, erwartet %v", c.name, got, c.want)
		}
	}
}

func TestRouteDecisionReasons(t *testing.T) {
	good := func() *vpnPeer {
		return &vpnPeer{Kind: "outbound", Routes: "10.20.0.0/24, 192.168.50.0/24", ConnState: "on"}
	}
	cases := []struct {
		name   string
		peer   func() *vpnPeer
		host   string
		wantOK bool
		reason string
	}{
		{"nicht gefunden", func() *vpnPeer { return nil }, "10.20.0.5", false, "Tunnel nicht gefunden"},
		{"kein ausgehender", func() *vpnPeer { p := good(); p.Kind = "gateway"; return p }, "10.20.0.5", false, "Tunnel ist kein ausgehender Tunnel"},
		{"steht nicht", func() *vpnPeer { p := good(); p.ConnState = "lost"; return p }, "10.20.0.5", false, "Tunnel steht nicht"},
		{"gesperrt steht nicht", func() *vpnPeer { p := good(); p.ConnState = "revoked"; return p }, "10.20.0.5", false, "Tunnel steht nicht"},
		{"Adresse ausserhalb", good, "10.99.0.5", false, "Adresse liegt nicht in den Netzen des Tunnels"},
		{"keine Adresse", good, "", false, "Adresse liegt nicht in den Netzen des Tunnels"},
		{"Gutfall", good, "192.168.50.7", true, ""},
		{"Gutfall Einzeladresse", func() *vpnPeer { p := good(); p.Routes = "10.30.0.9/32"; return p }, "10.30.0.9", true, ""},
	}
	for _, c := range cases {
		ok, reason := routeDecision(c.peer(), net.ParseIP(c.host))
		if ok != c.wantOK || reason != c.reason {
			t.Errorf("%s: (%v, %q), erwartet (%v, %q)", c.name, ok, reason, c.wantOK, c.reason)
		}
	}
}

func TestConnRouteReadyDirect(t *testing.T) {
	h := &Handler{}
	ok, reason := h.connRouteReady(context.Background(), map[string]string{"host": "db.example"})
	if !ok || reason != "" {
		t.Fatalf("direkt muss immer bereit sein, bekam (%v, %q)", ok, reason)
	}
}

func TestConnSourceAddr(t *testing.T) {
	got := connSourceAddr("127.0.0.1", "1433")
	if got != "127.0.0.1" {
		t.Errorf("connSourceAddr(127.0.0.1) = %q", got)
	}
	if got := connSourceAddr("", "1433"); got != "" {
		t.Errorf("leerer Host muss leer bleiben, bekam %q", got)
	}
	if got := connSourceAddr("nicht aufloesbar.invalid", "1433"); got != "" {
		t.Errorf("nicht auflösbarer Host muss leer bleiben, bekam %q", got)
	}
}

func TestNormalizeFingerprint(t *testing.T) {
	const hex = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	for _, in := range []string{
		hex,
		strings.ToUpper(hex),
		"AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99",
		" " + hex + " ",
	} {
		if got := normalizeFingerprint(in); got != hex {
			t.Errorf("normalizeFingerprint(%q) = %q", in, got)
		}
	}
	if got := normalizeFingerprint("kein hex"); got != "" {
		t.Errorf("ungültig muss leer werden, bekam %q", got)
	}
	if got := normalizeFingerprint("abcd"); got != "" {
		t.Errorf("zu kurz muss leer werden, bekam %q", got)
	}
}

// Neue Felder sind keine Geheimnisse: sie bleiben in der öffentlichen Konfiguration,
// damit das Bearbeiten-Formular sie vorbelegen kann.
func TestPublicConnConfigKeepsRouteFields(t *testing.T) {
	cfg := map[string]string{
		"host": "db", "password": "geheim", "route": "vpn:abc", "vpn_poll_minutes": "7",
		"vpn_timeout_seconds": "90", "allow_write": "true", "tls_fingerprint": "aa",
	}
	pub := publicConnConfig(cfg)
	for _, k := range []string{"route", "vpn_poll_minutes", "vpn_timeout_seconds", "allow_write", "tls_fingerprint"} {
		if pub[k] != cfg[k] {
			t.Errorf("%s fehlt in der öffentlichen Konfiguration", k)
		}
	}
	if _, ok := pub["password"]; ok {
		t.Error("Kennwort darf nicht in die öffentliche Konfiguration")
	}
}

func TestConnectionKindFieldsRoute(t *testing.T) {
	want := []string{"route", "vpn_poll_minutes", "vpn_timeout_seconds", "allow_write", "tls_fingerprint"}
	for _, dir := range []string{"import", "export"} {
		fields, _ := connectionKindFields(dir, "mssql")
		for _, w := range want {
			found := false
			for _, f := range fields {
				found = found || f == w
			}
			if !found {
				t.Errorf("%s/mssql: Feld %s fehlt", dir, w)
			}
		}
		other, _ := connectionKindFields(dir, "mysql")
		for _, f := range other {
			for _, w := range want {
				if f == w {
					t.Errorf("%s/mysql darf %s nicht bekommen", dir, w)
				}
			}
		}
	}
	if !booleanConfigFields["allow_write"] {
		t.Error("allow_write muss ein Schalter sein")
	}
}

func TestPermissionGateFingerprint(t *testing.T) {
	for dir, perm := range map[string]string{"import": "import.write", "export": "export.write"} {
		_, got, _, _, ok := matchGate("POST", "/"+dir+"/connections/fingerprint")
		if !ok || got != perm {
			t.Errorf("%s: Recht %q, erwartet %q", dir, got, perm)
		}
	}
}

// labConfig: Verbindungswerte aus PDH_TEST_MSSQL_DSN (Labor), sonst Überspringen.
func labMssqlConfig(t *testing.T) map[string]string {
	t.Helper()
	raw := os.Getenv("PDH_TEST_MSSQL_DSN")
	if raw == "" {
		t.Skip("PDH_TEST_MSSQL_DSN nicht gesetzt")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	pw, _ := u.User.Password()
	return map[string]string{
		"host": u.Hostname(), "port": u.Port(), "database": u.Query().Get("database"),
		"username": u.User.Username(), "password": pw, "use_tls": "true",
	}
}

func TestMssqlFingerprintMismatch(t *testing.T) {
	cfg := labMssqlConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fp, err := mssqlServerFingerprint(ctx, cfg)
	if err != nil {
		t.Fatalf("Fingerabdruck lesen: %v", err)
	}
	if len(fp) != 64 || fp != strings.ToLower(fp) {
		t.Fatalf("Fingerabdruck muss 64 Zeichen klein geschriebenes Hex sein, bekam %q", fp)
	}

	// falscher Fingerabdruck: Verbindung muss scheitern, Fehler nennt den Fingerabdruck
	bad := map[string]string{}
	for k, v := range cfg {
		bad[k] = v
	}
	bad["tls_fingerprint"] = strings.Repeat("0", 64)
	if db, err := openMssqlDB(bad); err == nil {
		db.Close()
		t.Fatal("falscher Fingerabdruck wurde akzeptiert")
	} else if !strings.Contains(err.Error(), "Fingerabdruck") {
		t.Fatalf("Fehler nennt den Fingerabdruck nicht: %v", err)
	}

	// richtiger Fingerabdruck (auch in Großschreibung mit Doppelpunkten): Verbindung steht
	good := map[string]string{}
	for k, v := range cfg {
		good[k] = v
	}
	good["tls_fingerprint"] = strings.ToUpper(fp)
	db, err := openMssqlDB(good)
	if err != nil {
		t.Fatalf("richtiger Fingerabdruck abgelehnt: %v", err)
	}
	defer db.Close()
	var one int
	if err := db.QueryRow("SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("Abfrage über gepinnte Verbindung: %v", err)
	}
}

func TestMssqlOpenWithoutNewFieldsUnchanged(t *testing.T) {
	cfg := labMssqlConfig(t)
	cfg["use_tls"] = "false"
	db, err := openMssqlDB(cfg)
	if err != nil {
		t.Fatalf("bestehende Verbindung ohne neue Felder: %v", err)
	}
	db.Close()
}

func TestCheckMssqlRightsLab(t *testing.T) {
	cfg := labMssqlConfig(t)
	cfg["use_tls"] = "false"
	db, err := openMssqlDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rep := &checkReport{}
	checkMssqlRights(ctx, rep, db, true)
	got := map[string]string{}
	for _, it := range rep.Items {
		got[it.Name] = it.Status
	}
	if got["Leserecht"] != "ok" || got["Schreibrecht"] != "ok" {
		t.Fatalf("Lese-/Schreibrecht für pdh_sync im Labor: %+v", rep.Items)
	}
	if _, warn := got["Rechte"]; warn {
		t.Errorf("pdh_sync darf nicht als zu mächtig gelten: %+v", rep.Items)
	}
	rep = &checkReport{}
	checkMssqlRights(ctx, rep, db, false)
	for _, it := range rep.Items {
		if it.Name == "Schreibrecht" {
			t.Error("Schreibrecht darf ohne „Schreiben erlaubt“ nicht geprüft werden")
		}
	}
}

// Formular: Felder für Weg, VPN-Werte, Schreiben erlaubt und Fingerabdruck stehen in
// Import und Export; Tunnel aus den Seitendaten erscheinen als Auswahl.
func TestConnectionFormRouteFields(t *testing.T) {
	tmpl := loadTestTemplates(t)
	for _, dir := range []string{"import", "export"} {
		out := renderPage(t, tmpl, dir, ConnectionsPageData{Direction: dir, CanWrite: true,
			Tunnels: []connTunnelOption{{ID: "t-1", Name: "Werk Nord"}}})
		for _, w := range []string{"Weg</label>", `<option value="direct">Direkt</option>`, `<option value="vpn:t-1">Werk Nord</option>`,
			"Abfragetakt über VPN (Minuten)", "Zeitgrenze je Abfrage über VPN (Sekunden)", "Schreiben erlaubt",
			"Fingerabdruck lesen und bestätigen", "f_mssql_tls_fingerprint", "'allow_write'", "/connections/fingerprint"} {
			if !strings.Contains(out, w) {
				t.Errorf("%s: %q fehlt", dir, w)
			}
		}
		if n := strings.Count(out, "function mssqlReadFingerprint("); n != 1 {
			t.Errorf("%s: mssqlReadFingerprint %d× definiert", dir, n)
		}
		// ohne VPN-Einrichtung: nur „Direkt“
		out = renderPage(t, tmpl, dir, ConnectionsPageData{Direction: dir, CanWrite: true})
		if strings.Contains(out, `value="vpn:`) {
			t.Errorf("%s: ohne Tunnel darf es keine VPN-Option geben", dir)
		}
		checkScripts(t, dir, out)
	}
}

func TestKeepStoredRoute(t *testing.T) {
	stored := map[string]string{"route": "vpn:weg-1"}
	// Feld fehlt (Tunnel wird nicht mehr angeboten): gespeicherter Weg bleibt
	cfg := map[string]string{"route": "direct"}
	keepStoredRoute(cfg, stored, "mssql", url.Values{"name": {"x"}})
	if cfg["route"] != "vpn:weg-1" {
		t.Errorf("Weg ging verloren: %q", cfg["route"])
	}
	// ausdrücklich direkt: Wechsel gilt
	cfg = map[string]string{"route": "direct"}
	keepStoredRoute(cfg, stored, "mssql", url.Values{"f_mssql_route": {"direct"}})
	if cfg["route"] != "direct" {
		t.Errorf("ausdrückliche Wahl direkt wurde überschrieben: %q", cfg["route"])
	}
	// anderer Tunnel gewählt
	cfg = map[string]string{"route": "vpn:neu"}
	keepStoredRoute(cfg, stored, "mssql", url.Values{"f_mssql_route": {"vpn:neu"}})
	if cfg["route"] != "vpn:neu" {
		t.Errorf("neue Wahl überschrieben: %q", cfg["route"])
	}
	// gespeichert war direkt, Feld fehlt: bleibt direkt
	cfg = map[string]string{"route": "direct"}
	keepStoredRoute(cfg, map[string]string{}, "mssql", url.Values{})
	if cfg["route"] != "direct" {
		t.Errorf("direct erwartet: %q", cfg["route"])
	}
	// andere Typen unberührt
	cfg = map[string]string{}
	keepStoredRoute(cfg, stored, "mysql", url.Values{})
	if _, ok := cfg["route"]; ok {
		t.Error("mysql darf keinen Weg bekommen")
	}
}

func TestMssqlRightsOnlyForImport(t *testing.T) {
	if !mssqlRightsApply("import") || mssqlRightsApply("export") {
		t.Error("Rechteprüfung gilt nur für Import-Verbindungen")
	}
}

func TestConnectionFormKeepsMissingRouteScript(t *testing.T) {
	tmpl := loadTestTemplates(t)
	out := renderPage(t, tmpl, "import", ConnectionsPageData{Direction: "import", CanWrite: true})
	for _, w := range []string{"function mssqlEnsureRoute(", "Tunnel nicht mehr vorhanden", "mssqlEnsureRoute(config.route)"} {
		if !strings.Contains(out, w) {
			t.Errorf("%q fehlt", w)
		}
	}
}
