package web

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"pdh/internal/datanet"
	"pdh/internal/datanet/nodetest"
)

// connNetFake setzt die Auflösung des Weges Datennetz für einen Test.
func connNetFake(t *testing.T, addr string, err error) {
	t.Helper()
	old := getConnNetResolve()
	setConnNetResolve(func(_ context.Context, _, _ string) (string, error) { return addr, err })
	t.Cleanup(func() { setConnNetResolve(old) })
}

func TestConnDialTargetNet(t *testing.T) {
	connNetFake(t, "127.0.0.1:40000", nil)
	host, port, tlsName, err := connDialTarget(map[string]string{"route": "net:c1", "host": "sql01", "port": "1433"}, "1433")
	if err != nil || host != "127.0.0.1" || port != "40000" || tlsName != "sql01" {
		t.Fatalf("Datennetz: %q %q %q %v", host, port, tlsName, err)
	}
	host, port, tlsName, err = connDialTarget(map[string]string{"host": "sql01", "port": "1433"}, "1433")
	if err != nil || host != "sql01" || port != "1433" || tlsName != "sql01" {
		t.Fatalf("direkt: %q %q %q %v", host, port, tlsName, err)
	}
	// Vorgabe-Port, wenn keiner eingetragen ist
	_, port, _, _ = connDialTarget(map[string]string{"host": "db"}, "3306")
	if port != "3306" {
		t.Errorf("Vorgabe-Port: %q", port)
	}
	// Weg nicht bereit: verständliche Meldung statt Absturz
	connNetFake(t, "", errors.New("Client gesperrt"))
	_, _, _, err = connDialTarget(map[string]string{"route": "net:c1", "host": "sql01"}, "1433")
	if err == nil || !strings.Contains(err.Error(), "Client gesperrt") {
		t.Errorf("Fehler: %v", err)
	}
	// ohne gesetzte Auflösung (Datennetz nie gestartet)
	setConnNetResolve(nil)
	_, _, _, err = connDialTarget(map[string]string{"route": "net:c1", "host": "sql01"}, "1433")
	if err == nil || !strings.Contains(err.Error(), "Datennetz ist ausgeschaltet") {
		t.Errorf("Fehler ohne Auflösung: %v", err)
	}
}

func TestMssqlParseNetKeepsCertName(t *testing.T) {
	connNetFake(t, "127.0.0.1:40000", nil)
	cfg := map[string]string{"route": "net:c1", "host": "sql01", "port": "1433", "use_tls": "true", "database": "x"}
	p, err := mssqlParse(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Host != "127.0.0.1" || p.Port != 40000 || !p.HostInCertificateProvided {
		t.Fatalf("Ziel: %q:%d, Name im Zertifikat %v", p.Host, p.Port, p.HostInCertificateProvided)
	}
	if p.TLSConfig == nil || p.TLSConfig.ServerName != "sql01" {
		t.Fatalf("TLS-Name: %+v", p.TLSConfig)
	}
	// ohne TLS kein Zertifikatsname
	cfg["use_tls"] = "false"
	if p, err = mssqlParse(cfg, false); err != nil || p.HostInCertificateProvided {
		t.Errorf("ohne TLS: %v %v", p.HostInCertificateProvided, err)
	}
	// benannte Instanz ohne Port geht nicht
	cfg = map[string]string{"route": "net:c1", "host": "sql01", "instance_name": "SQLEXPRESS"}
	_, err = mssqlParse(cfg, false)
	if err == nil || err.Error() != "Beim Weg Datennetz bitte den Port eintragen (benannte Instanz geht nicht)" {
		t.Fatalf("Instanz ohne Port: %v", err)
	}
	// mit Port ist eine Instanz erlaubt (der Port bestimmt das Ziel)
	cfg["port"] = "1433"
	if p, err = mssqlParse(cfg, false); err != nil || p.Port != 40000 {
		t.Errorf("Instanz mit Port: %d %v", p.Port, err)
	}
	// direkt bleibt wie bisher
	p, err = mssqlParse(map[string]string{"host": "sql01", "port": "1433"}, false)
	if err != nil || p.Host != "sql01" || p.Port != 1433 {
		t.Errorf("direkt: %q %d %v", p.Host, p.Port, err)
	}
}

func TestMysqlNetTLSServerName(t *testing.T) {
	connNetFake(t, "127.0.0.1:40000", nil)
	cfg, err := mysqlConfig(map[string]string{"route": "net:c1", "host": "sql01", "port": "3306", "use_tls": "true", "database": "d"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:40000" || cfg.TLS == nil || cfg.TLS.ServerName != "sql01" {
		t.Fatalf("Addr %q, TLS %+v", cfg.Addr, cfg.TLS)
	}
	cfg, err = mysqlConfig(map[string]string{"route": "net:c1", "host": "sql01", "use_tls": "false"})
	if err != nil || cfg.TLS != nil {
		t.Errorf("ohne TLS: %+v %v", cfg.TLS, err)
	}
	cfg, err = mysqlConfig(map[string]string{"host": "sql01", "use_tls": "true"})
	if err != nil || cfg.Addr != "sql01:3306" || cfg.TLS == nil || cfg.TLS.ServerName != "sql01" {
		t.Errorf("direkt: %q %+v %v", cfg.Addr, cfg.TLS, err)
	}
	if _, err = mysqlConfig(map[string]string{"route": "net:c1"}); err == nil {
		t.Error("ohne Host muss es scheitern")
	}
}

func TestNormalizeRouteNet(t *testing.T) {
	cfg := map[string]string{"route": " net:c-1 "}
	normalizeMssqlRouteFields(cfg)
	if cfg["route"] != "net:c-1" {
		t.Errorf("mssql: %q", cfg["route"])
	}
	cfg = map[string]string{"route": "net:c-1", "vpn_poll_minutes": "9", "vpn_timeout_seconds": "99999"}
	normalizeRouteFields(cfg)
	if cfg["route"] != "net:c-1" || cfg["vpn_poll_minutes"] != "9" || cfg["vpn_timeout_seconds"] != "600" {
		t.Errorf("mysql: %+v", cfg)
	}
	if _, ok := cfg["tls_fingerprint"]; ok {
		t.Error("MySQL bekommt keinen Fingerabdruck")
	}
	cfg = map[string]string{"route": "net:"}
	normalizeRouteFields(cfg)
	if cfg["route"] != "direct" {
		t.Errorf("leerer Client: %q", cfg["route"])
	}
	for _, f := range []string{"route", "vpn_poll_minutes", "vpn_timeout_seconds"} {
		for _, dir := range []string{"import", "export"} {
			fields, _ := connectionKindFields(dir, "mysql")
			if !strings.Contains(strings.Join(fields, ","), f) {
				t.Errorf("%s/mysql: Feld %s fehlt", dir, f)
			}
		}
	}
}

func TestValidateConnRoute(t *testing.T) {
	bad := map[string]string{"route": "net:c1", "host": "sql01", "instance_name": "EXPRESS"}
	if err := validateConnRoute("mssql", bad); err == nil || !strings.Contains(err.Error(), "benannte Instanz geht nicht") {
		t.Errorf("Instanz ohne Port: %v", err)
	}
	for _, ok := range []map[string]string{
		{"route": "net:c1", "host": "sql01", "instance_name": "EXPRESS", "port": "1433"},
		{"route": "net:c1", "host": "sql01"},
		{"route": "direct", "host": "sql01", "instance_name": "EXPRESS"},
		{"route": "vpn:t1", "host": "sql01", "instance_name": "EXPRESS"},
	} {
		if err := validateConnRoute("mssql", ok); err != nil {
			t.Errorf("%v: %v", ok, err)
		}
	}
	if err := validateConnRoute("mysql", bad); err != nil {
		t.Errorf("MySQL kennt keine Instanzen: %v", err)
	}
}

func TestParseConnRouteNet(t *testing.T) {
	cases := []struct {
		cfg  map[string]string
		want connRoute
	}{
		{map[string]string{"route": "net:abc"}, connRoute{ViaNet: true, ClientID: "abc"}},
		{map[string]string{"route": " net:abc "}, connRoute{ViaNet: true, ClientID: "abc"}},
		{map[string]string{"route": "net:"}, connRoute{}},
		{map[string]string{"route": "vpn:x"}, connRoute{ViaVPN: true, PeerID: "x"}},
	}
	for _, c := range cases {
		if got := parseConnRoute(c.cfg); got != c.want {
			t.Errorf("parseConnRoute(%v) = %+v, erwartet %+v", c.cfg, got, c.want)
		}
	}
	if !(connRoute{ViaNet: true}).Remote() || !(connRoute{ViaVPN: true}).Remote() || (connRoute{}).Remote() {
		t.Error("Remote()")
	}
	net1 := map[string]string{"route": "net:abc"}
	if d := connQueryTimeout(net1); d != 60*time.Second {
		t.Errorf("Zeitgrenze Vorgabe: %s", d)
	}
	net1["vpn_timeout_seconds"] = "30"
	if d := connQueryTimeout(net1); d != 30*time.Second {
		t.Errorf("Zeitgrenze: %s", d)
	}
	if d := connPollInterval(map[string]string{"route": "net:abc", "vpn_poll_minutes": "12"}, 20); d != 12*time.Minute {
		t.Errorf("Takt: %s", d)
	}
	if d := connQueryTimeout(map[string]string{"host": "x"}); d != 15*time.Second {
		t.Errorf("direkt: %s", d)
	}
}

// dataNetTestRunning startet einen Manager mit Attrappe bis „läuft“ und setzt Brücken.
func dataNetTestRunning(t *testing.T, h *Handler, fake *nodetest.Fake) {
	t.Helper()
	m := datanet.NewManager(func(datanet.NodeConfig) (datanet.Node, error) { return fake, nil }, time.Minute)
	m.Start(datanet.NodeConfig{Dir: t.TempDir(), AuthKey: "k"}, nil)
	t.Cleanup(m.Stop)
	deadline := time.Now().Add(5 * time.Second)
	for m.Status().State != datanet.StateRunning {
		if time.Now().After(deadline) {
			t.Fatalf("Knoten läuft nicht: %+v", m.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
	b := datanet.NewBridges(fake)
	t.Cleanup(b.Close)
	h.dataNetMu.Lock()
	h.dataNet, h.dataNetBridges = m, b
	h.dataNetMu.Unlock()
}

func dataNetTestClient(t *testing.T, h *Handler, name, addr string, targets ...datanet.TargetStatus) dataNetClient {
	t.Helper()
	ctx := context.Background()
	c, _, err := h.dataNetCreate(ctx, name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		h.db.Exec(context.Background(), `DELETE FROM datanet_events WHERE client_name = $1`, name)
		h.db.Exec(context.Background(), `DELETE FROM datanet_clients WHERE name = $1`, name)
	})
	if addr != "" {
		if err := h.dataNetSaveReport(ctx, c.ID, addr, datanet.Report{Hostname: "h", Targets: targets}); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestDataNetRouteReady(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()
	h := &Handler{db: pool}
	suffix := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	now := time.Now()
	okT := datanet.TargetStatus{Port: 3306, OK: true, CheckedAt: now}

	good := dataNetTestClient(t, h, "rdy-ok-"+suffix, "100.64.0.20", okT)
	revoked := dataNetTestClient(t, h, "rdy-rev-"+suffix, "100.64.0.21", okT)
	old := dataNetTestClient(t, h, "rdy-old-"+suffix, "100.64.0.22", okT)
	nodb := dataNetTestClient(t, h, "rdy-db-"+suffix, "100.64.0.23", datanet.TargetStatus{Port: 3306, OK: false, Error: "refused", CheckedAt: now})
	fresh := dataNetTestClient(t, h, "rdy-new-"+suffix, "")
	if err := h.dataNetRevoke(ctx, revoked.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE datanet_clients SET last_seen_at = NOW() - interval '200 seconds' WHERE id::text = $1`, old.ID); err != nil {
		t.Fatal(err)
	}

	// Netz aus
	if ok, why := h.dataNetRouteReady(ctx, good.ID, "3306"); ok || why != "Datennetz ist ausgeschaltet" {
		t.Errorf("aus: %v %q", ok, why)
	}
	// Netz startet noch
	h.dataNet = datanet.NewManager(func(datanet.NodeConfig) (datanet.Node, error) { return &nodetest.Fake{BlockUp: true}, nil }, time.Minute)
	h.dataNet.Start(datanet.NodeConfig{Dir: t.TempDir(), AuthKey: "k"}, nil)
	if ok, why := h.dataNetRouteReady(ctx, good.ID, "3306"); ok || why != "Datennetz nicht verbunden" {
		t.Errorf("startet: %v %q", ok, why)
	}
	h.dataNet.Stop()

	dataNetTestRunning(t, h, &nodetest.Fake{Addrs: []netip.Addr{netip.MustParseAddr("100.64.0.1")}})
	cases := []struct {
		name, id, port, want string
	}{
		{"fehlt", "00000000-0000-0000-0000-000000000000", "3306", "Client nicht gefunden"},
		{"gesperrt", revoked.ID, "3306", "Client gesperrt"},
		{"offline", old.ID, "3306", "Client offline (letzter Kontakt "},
		{"nie gemeldet", fresh.ID, "3306", "Client offline (noch nie gemeldet)"},
		{"Port fehlt", good.ID, "1433", "Port 1433 bietet der Client nicht an"},
		{"Ziel down", nodb.ID, "3306", "Datenbank am Client nicht erreichbar"},
		{"gut", good.ID, "3306", ""},
	}
	for _, c := range cases {
		ok, why := h.dataNetRouteReady(ctx, c.id, c.port)
		if c.want == "" {
			if !ok || why != "" {
				t.Errorf("%s: %v %q", c.name, ok, why)
			}
			continue
		}
		if ok || !strings.HasPrefix(why, c.want) {
			t.Errorf("%s: %v %q, erwartet %q", c.name, ok, why, c.want)
		}
	}

	// connRouteReady nimmt den Weg ohne Adressprüfung des VPN-Wegs
	if ok, why := h.connRouteReady(ctx, "mssql", map[string]string{"route": "net:" + good.ID, "host": "gibt-es-nicht.invalid", "port": "3306", "instance_name": ""}); !ok {
		t.Errorf("connRouteReady Datennetz: %q", why)
	}
	// ohne Port gilt je Treiber die Vorgabe: MySQL 3306
	if ok, why := h.connRouteReady(ctx, "mysql", map[string]string{"route": "net:" + good.ID, "host": "x"}); !ok {
		t.Errorf("connRouteReady MySQL-Vorgabe: %q", why)
	}
	if ok, why := h.connRouteReady(ctx, "mssql", map[string]string{"route": "net:" + good.ID, "host": "x", "instance_name": ""}); ok || !strings.Contains(why, "Port 1433") {
		t.Errorf("connRouteReady SQL-Server-Vorgabe: %v %q", ok, why)
	}

	// Auflösung: Brücke zur Datennetz-Adresse, erneut gleiche Adresse
	a1, err := h.dataNetResolve(ctx, good.ID, "3306")
	if err != nil || !strings.HasPrefix(a1, "127.0.0.1:") {
		t.Fatalf("dataNetResolve: %q %v", a1, err)
	}
	if a2, _ := h.dataNetResolve(ctx, good.ID, "3306"); a2 != a1 {
		t.Errorf("Brücke wechselt: %q %q", a1, a2)
	}
	if _, err := h.dataNetResolve(ctx, revoked.ID, "3306"); err == nil || err.Error() != "Client gesperrt" {
		t.Errorf("Auflösung gesperrt: %v", err)
	}
}

func TestConnNetOptions(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()
	h := &Handler{db: pool}
	suffix := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	a := dataNetTestClient(t, h, "opt-a-"+suffix, "")
	b := dataNetTestClient(t, h, "opt-b-"+suffix, "")
	if err := h.dataNetRevoke(ctx, b.ID, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM app_settings WHERE key = $1`, dataNetKeyEnabled) })
	pool.Exec(ctx, `DELETE FROM app_settings WHERE key = $1`, dataNetKeyEnabled)
	if got := h.connNetOptions(ctx); len(got) != 0 {
		t.Errorf("Datennetz aus: %+v", got)
	}
	if err := h.dataNetPut(ctx, dataNetKeyEnabled, "true"); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, o := range h.connNetOptions(ctx) {
		if o.ID == b.ID {
			t.Errorf("gesperrter Client angeboten: %+v", o)
		}
		if o.ID == a.ID {
			found = true
			if o.Name != "Datennetz: opt-a-"+suffix {
				t.Errorf("Name: %q", o.Name)
			}
		}
	}
	if !found {
		t.Error("aktiver Client fehlt")
	}
}

func TestRevokeClosesBridges(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()
	h := &Handler{db: pool}
	suffix := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")

	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { echo.Close() })
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()

	for _, del := range []bool{false, true} {
		name := "cut-" + strconv.FormatBool(del) + "-" + suffix
		addr := "100.64.1.5"
		if del {
			addr = "100.64.1.6"
		}
		c := dataNetTestClient(t, h, name, addr, datanet.TargetStatus{Port: 1433, OK: true, CheckedAt: time.Now()})
		fake := &nodetest.Fake{Routes: map[string]string{addr + ":1433": echo.Addr().String()}}
		b := datanet.NewBridges(fake)
		t.Cleanup(b.Close)
		h.dataNetMu.Lock()
		h.dataNetBridges = b
		h.dataNetMu.Unlock()

		local, err := b.Addr(addr + ":1433")
		if err != nil {
			t.Fatal(err)
		}
		conn, err := net.Dial("tcp", local)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		buf := make([]byte, 4)
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := conn.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping" {
			t.Fatalf("Echo vor dem Trennen: %q %v", buf, err)
		}
		if del {
			err = h.dataNetDeleteAndCut(ctx, c.ID, "")
		} else {
			err = h.dataNetRevokeAndCut(ctx, c.ID, "")
		}
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := conn.Read(buf); err == nil {
			t.Errorf("del=%v: Verbindung steht noch", del)
		} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Errorf("del=%v: Verbindung nicht getrennt (Zeitüberschreitung)", del)
		}
	}
	// ohne laufende Brücken kein Absturz
	h.dataNetMu.Lock()
	h.dataNetBridges = nil
	h.dataNetMu.Unlock()
	c := dataNetTestClient(t, h, "cut-none-"+suffix, "100.64.1.7")
	if err := h.dataNetRevokeAndCut(ctx, c.ID, ""); err != nil {
		t.Errorf("ohne Brücken: %v", err)
	}
}

func TestConnectionFormNetRoute(t *testing.T) {
	tmpl := loadTestTemplates(t)
	for _, dir := range []string{"import", "export"} {
		out := renderPage(t, tmpl, dir, ConnectionsPageData{Direction: dir, CanWrite: true,
			NetClients: []connTunnelOption{{ID: "c-1", Name: "Datennetz: Werk Süd"}}})
		for _, w := range []string{`<optgroup label="Datennetz">`, `<option value="net:c-1">Datennetz: Werk Süd</option>`,
			`id="f_mysql_route"`, `id="f_mssql_route"`, `name="f_mysql_vpn_poll_minutes"`, `name="f_mysql_vpn_timeout_seconds"`} {
			if !strings.Contains(out, w) {
				t.Errorf("%s: %q fehlt", dir, w)
			}
		}
		if strings.Contains(out, `f_mysql_tls_fingerprint`) {
			t.Errorf("%s: MySQL hat keinen Fingerabdruck", dir)
		}
		out = renderPage(t, tmpl, dir, ConnectionsPageData{Direction: dir, CanWrite: true})
		if strings.Contains(out, `<optgroup label="Datennetz">`) {
			t.Errorf("%s: ohne Datennetz keine Auswahl", dir)
		}
		checkScripts(t, dir, out)
	}
}

func TestMysqlConfigBracketHost(t *testing.T) {
	cfg, err := mysqlConfig(map[string]string{"host": "[::1]", "port": "3306", "use_tls": "true"})
	if err != nil || cfg.Addr != "[::1]:3306" || cfg.TLS == nil || cfg.TLS.ServerName != "::1" {
		t.Fatalf("Addr %q, TLS %+v, %v", cfg.Addr, cfg.TLS, err)
	}
	cfg, err = mysqlConfig(map[string]string{"host": "::1"})
	if err != nil || cfg.Addr != "[::1]:3306" {
		t.Errorf("ohne Klammern: %q %v", cfg.Addr, err)
	}
}

func TestConnDefaultPortByKind(t *testing.T) {
	if connDefaultPort("mssql") != "1433" || connDefaultPort("mysql") != "3306" {
		t.Error("Standard-Ports")
	}
	if _, err := netPortNumber("0"); err == nil {
		t.Error("0 muss scheitern")
	}
	if _, err := netPortNumber("70000"); err == nil {
		t.Error("70000 muss scheitern")
	}
	if n, err := netPortNumber("01433"); err != nil || n != 1433 {
		t.Errorf("01433: %d %v", n, err)
	}
}

// Schreibweisen desselben Ports ergeben dieselbe Brücke; SQL Server ohne
// instance_name-Feld und ohne Port gilt als 1433.
func TestDataNetResolvePortNormalized(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()
	h := &Handler{db: pool}
	suffix := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	c := dataNetTestClient(t, h, "port-"+suffix, "100.64.0.30",
		datanet.TargetStatus{Port: 1433, OK: true, CheckedAt: time.Now()})
	dataNetTestRunning(t, h, &nodetest.Fake{Addrs: []netip.Addr{netip.MustParseAddr("100.64.0.1")}})
	a1, err := h.dataNetResolve(ctx, c.ID, "1433")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"01433", "+1433", " 1433 "} {
		if a, err := h.dataNetResolve(ctx, c.ID, p); err != nil || a != a1 {
			t.Errorf("%q: %q %v, erwartet %q", p, a, err, a1)
		}
	}
	if ok, why := h.dataNetRouteReady(ctx, c.ID, "0"); ok || !strings.Contains(why, "ungültig") {
		t.Errorf("Port 0: %v %q", ok, why)
	}
	if ok, why := h.connRouteReady(ctx, "mssql", map[string]string{"route": "net:" + c.ID, "host": "x"}); !ok {
		t.Errorf("SQL Server ohne instance_name: %q", why)
	}
	if ok, _ := h.connRouteReady(ctx, "mysql", map[string]string{"route": "net:" + c.ID, "host": "x"}); ok {
		t.Error("MySQL-Vorgabe 3306 wird nicht angeboten")
	}
}
