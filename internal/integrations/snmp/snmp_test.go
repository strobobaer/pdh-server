package snmp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/gosnmp/gosnmp"
)

// fakeSession: nachgebautes Gerät im Speicher – die Tests laufen ohne Netz.
type fakeSession struct {
	vals map[string]gosnmp.SnmpPDU // Kennung ohne führenden Punkt

	connectErr error
	getErr     error
	walkErr    map[string]error // je Spalte

	gets   [][]string
	walks  []string
	rows   int // ausgelieferte Tabellenzeilen
	closed bool
}

func newFakeSession() *fakeSession {
	return &fakeSession{vals: map[string]gosnmp.SnmpPDU{}, walkErr: map[string]error{}}
}

func (f *fakeSession) str(oid, s string) {
	f.vals[trimDot(oid)] = gosnmp.SnmpPDU{Name: oid, Type: gosnmp.OctetString, Value: []byte(s)}
}

func (f *fakeSession) num(oid string, n int) {
	f.vals[trimDot(oid)] = gosnmp.SnmpPDU{Name: oid, Type: gosnmp.Integer, Value: n}
}

func (f *fakeSession) gauge(oid string, n uint) {
	f.vals[trimDot(oid)] = gosnmp.SnmpPDU{Name: oid, Type: gosnmp.Gauge32, Value: n}
}

func row(col string, idx int) string { return col + "." + strconv.Itoa(idx) }

func (f *fakeSession) Connect() error { return f.connectErr }
func (f *fakeSession) Close() error   { f.closed = true; return nil }

func (f *fakeSession) Get(oids []string) ([]gosnmp.SnmpPDU, error) {
	f.gets = append(f.gets, oids)
	if f.getErr != nil {
		return nil, f.getErr
	}
	out := make([]gosnmp.SnmpPDU, 0, len(oids))
	for _, o := range oids {
		if p, ok := f.vals[trimDot(o)]; ok {
			out = append(out, p)
		} else {
			out = append(out, gosnmp.SnmpPDU{Name: o, Type: gosnmp.NoSuchInstance})
		}
	}
	return out, nil
}

func oidLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			return x < y
		}
	}
	return len(as) < len(bs)
}

func (f *fakeSession) BulkWalk(root string, fn func(gosnmp.SnmpPDU) error) error {
	f.walks = append(f.walks, root)
	if err := f.walkErr[root]; err != nil {
		return err
	}
	var keys []string
	for k := range f.vals {
		if strings.HasPrefix(k, trimDot(root)+".") {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return oidLess(keys[i], keys[j]) })
	for _, k := range keys {
		f.rows++
		if err := fn(f.vals[k]); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeSession) walked(col string) bool {
	for _, w := range f.walks {
		if w == col {
			return true
		}
	}
	return false
}

var testConfig = Config{Host: "192.168.10.20", Port: 161, User: "pdh", AuthProto: "sha256", AuthPass: "anmelde-kennwort",
	PrivProto: "aes128", PrivPass: "schluessel-kennwort"}

func clientWith(f *fakeSession, cfg Config) *Client {
	c := New(cfg)
	c.open = func(context.Context, net.IP, Config) session { return f }
	return c
}

// fakeSwitch: Switch mit ENTITY-MIB (Stapel → Gehäuse → Module) und 24 Anschlüssen,
// dazu VLAN-, Loopback- und Bündel-Schnittstellen, die nicht als Anschluss zählen.
func fakeSwitch() *fakeSession {
	f := newFakeSession()
	f.str(oidSysDescr, "Beispiel Switch OS 7.1\r\nCopyright (c) Beispiel AG")
	f.str(oidSysName, "sw-halle-1")
	f.str(oidBridgeMAC, "\x00\x11\x22\x33\x44\x55")

	f.num(row(colEntClass, 1), 11) // Stapel
	f.num(row(colEntContainedIn, 1), 0)
	f.num(row(colEntClass, 1001), entClassChassis)
	f.num(row(colEntContainedIn, 1001), 1)
	f.str(row(colEntModel, 1001), "XS-2400-24P")
	f.str(row(colEntSerial, 1001), "SN123456789")
	f.str(row(colEntSoftware, 1001), "7.1.4")
	f.str(row(colEntFirmware, 1001), "boot-1.0")
	for i := 1002; i < 1010; i++ {
		f.num(row(colEntClass, i), 9) // Modul
		f.num(row(colEntContainedIn, i), 1001)
		f.str(row(colEntModel, i), "Modul")
	}

	for i := 1; i <= 24; i++ {
		f.num(row(colIfType, i), ifTypeEthernet)
		f.str(row(colIfName, i), fmt.Sprintf("GE1/0/%d", i))
		f.str(row(colIfDescr, i), fmt.Sprintf("GigabitEthernet1/0/%d", i))
		f.str(row(colIfPhysAddr, i), string([]byte{0, 0x11, 0x22, 0x33, 0x44, byte(0x55 + i)}))
		if i%2 == 1 {
			f.num(row(colIfOperStatus, i), 1)
			f.gauge(row(colIfHighSpeed, i), 1000)
			f.gauge(row(colIfSpeed, i), 1000000000)
		} else {
			f.num(row(colIfOperStatus, i), 2)
			f.gauge(row(colIfHighSpeed, i), 0)
			f.gauge(row(colIfSpeed, i), 0)
		}
	}
	for i, typ := range map[int]int{100: 53, 101: 24, 102: 161} { // VLAN, Loopback, Bündel
		f.num(row(colIfType, i), typ)
		f.str(row(colIfName, i), "virtuell")
		f.num(row(colIfOperStatus, i), 1)
		f.gauge(row(colIfHighSpeed, i), 1000)
	}
	return f
}

func TestDetailSwitch(t *testing.T) {
	f := fakeSwitch()
	d, err := clientWith(f, testConfig).Detail(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "sw-halle-1" || d.Model != "XS-2400-24P" || d.Serial != "SN123456789" || d.Firmware != "7.1.4" {
		t.Errorf("Stammdaten falsch: %+v", d)
	}
	if d.MAC != "00:11:22:33:44:55" || d.Kind != DeviceSwitch || d.IP != "192.168.10.20" {
		t.Errorf("MAC/Art/IP falsch: %q %q %q", d.MAC, d.Kind, d.IP)
	}
	if len(d.Ports) != 24 || d.PortsCut {
		t.Fatalf("Anschlüsse = %d (abgeschnitten %v), erwartet 24", len(d.Ports), d.PortsCut)
	}
	if p := d.Ports[0]; p != (Port{Idx: 1, Name: "GE1/0/1", Up: true, SpeedMbps: 1000}) {
		t.Errorf("Anschluss 1 = %+v", p)
	}
	if p := d.Ports[23]; p != (Port{Idx: 24, Name: "GE1/0/24", Up: false, SpeedMbps: 0}) {
		t.Errorf("Anschluss 24 = %+v", p)
	}
	// gezielte Spalten: nicht benötigte werden gar nicht erst gelesen
	for _, col := range []string{colIfDescr, colIfSpeed, colIfPhysAddr, colEntContainedIn} {
		if f.walked(col) {
			t.Errorf("Spalte %s gelesen, obwohl nicht nötig", col)
		}
	}
	if !f.closed {
		t.Error("Sitzung nicht geschlossen")
	}
}

// Firmware: ohne Software-Stand zählt der Firmware-Stand; ohne Gehäuse-Klasse der Eintrag
// ohne Elternteil.
func TestDetailEntityErsatz(t *testing.T) {
	f := newFakeSession()
	f.str(oidSysName, "usv-1")
	f.str(oidSysDescr, "USV 3000")
	f.num(row(colEntClass, 4), 9)
	f.num(row(colEntContainedIn, 4), 5)
	f.num(row(colEntClass, 5), 9)
	f.num(row(colEntContainedIn, 5), 0)
	f.str(row(colEntModel, 5), "USV-3000-RT")
	f.str(row(colEntSoftware, 5), "")
	f.str(row(colEntFirmware, 5), "fw-2.3")
	f.str(row(colEntModel, 4), "falsch")
	d, err := clientWith(f, testConfig).Detail(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.Model != "USV-3000-RT" || d.Firmware != "fw-2.3" || d.Serial != "" {
		t.Errorf("Detail = %+v", d)
	}
	if d.Kind != DeviceOther || len(d.Ports) != 0 || d.MAC != "" {
		t.Errorf("ohne Anschlüsse erwartet: other, keine MAC – bekam %+v", d)
	}
}

// Drucker: keine ENTITY-MIB, keine Bridge-Adresse, keine ifXTable – nichts davon ist ein Fehler.
func TestDetailDrucker(t *testing.T) {
	f := newFakeSession()
	f.str(oidSysName, "drucker-buero")
	f.str(oidSysDescr, "  Beispiel LaserJet 400 \x07 Farbe, mit sehr langer Beschreibung die weit über achtzig Zeichen hinausgeht und gekürzt wird\nZeile 2")
	f.num(row(colIfType, 1), 24) // Loopback
	f.str(row(colIfDescr, 1), "lo")
	f.str(row(colIfPhysAddr, 1), "")
	f.num(row(colIfType, 2), ifTypeEthernet)
	f.str(row(colIfDescr, 2), "Ethernet\x00\x00")
	f.gauge(row(colIfSpeed, 2), 100000000)
	f.num(row(colIfOperStatus, 2), 1)
	f.str(row(colIfPhysAddr, 2), "\xa4\x5d\x36\x00\x00\x09")
	f.num(row(colIfType, 3), ifTypeEthernet) // zweiter Anschluss mit kleinerer MAC
	f.str(row(colIfDescr, 3), "Ethernet 2")
	f.num(row(colIfOperStatus, 3), 2)
	f.str(row(colIfPhysAddr, 3), "\xa4\x5d\x36\x00\x00\x02")
	f.num(row(colIfType, 4), ifTypeEthernet) // ungültige MAC zählt nicht
	f.str(row(colIfPhysAddr, 4), "\x00\x00\x00\x00\x00\x00")

	d, err := clientWith(f, testConfig).Detail(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "drucker-buero" || d.Serial != "" || d.Firmware != "" {
		t.Errorf("Detail = %+v", d)
	}
	if n := len([]rune(d.Model)); n > 80 || !strings.HasPrefix(d.Model, "Beispiel LaserJet 400 Farbe, mit") || strings.Contains(d.Model, "Zeile 2") {
		t.Errorf("Modell = %q (%d Zeichen)", d.Model, n)
	}
	if d.MAC != "a4:5d:36:00:00:02" {
		t.Errorf("MAC = %q, erwartet die kleinste gültige", d.MAC)
	}
	if d.Kind != DeviceOther {
		t.Errorf("Art = %q", d.Kind)
	}
	if len(d.Ports) != 3 || d.Ports[0] != (Port{Idx: 2, Name: "Ethernet", Up: true, SpeedMbps: 100}) {
		t.Errorf("Anschlüsse = %+v", d.Ports)
	}
}

// Vier Anschlüsse ohne Bridge-Adresse sind kein Switch.
func TestDetailArtOhneBridge(t *testing.T) {
	f := fakeSwitch()
	delete(f.vals, trimDot(oidBridgeMAC))
	d, err := clientWith(f, testConfig).Detail(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != DeviceOther || d.MAC != "00:11:22:33:44:56" {
		t.Errorf("Art/MAC = %q %q", d.Kind, d.MAC)
	}
}

func TestDetailGrenzen(t *testing.T) {
	f := newFakeSession()
	f.str(oidSysName, "kern\x00switch\x1b[31m\r\n"+strings.Repeat("x", 500))
	f.str(oidBridgeMAC, "\x00\x11\x22\x33\x44\x55")
	for i := 1; i <= 600; i++ {
		f.num(row(colIfType, i), ifTypeEthernet)
		f.str(row(colIfName, i), "p‮"+strconv.Itoa(i)+"\t\x01"+strings.Repeat("y", 200))
		f.gauge(row(colIfHighSpeed, i), 4000000000)
	}
	d, err := clientWith(f, testConfig).Detail(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Ports) != MaxPorts || !d.PortsCut {
		t.Errorf("Anschlüsse = %d, abgeschnitten = %v", len(d.Ports), d.PortsCut)
	}
	if len([]rune(d.Name)) > 128 || !strings.HasPrefix(d.Name, "kern switch [31m xxx") {
		t.Errorf("Name nicht gesäubert/gekürzt: %q", d.Name)
	}
	for _, p := range d.Ports {
		if len([]rune(p.Name)) > 64 || strings.ContainsFunc(p.Name, func(r rune) bool { return r < 0x20 || r == 0x202e }) {
			t.Fatalf("Anschlussname nicht gesäubert: %q", p.Name)
		}
		if p.SpeedMbps > 1_000_000 {
			t.Fatalf("Geschwindigkeit nicht begrenzt: %d", p.SpeedMbps)
		}
	}
	// Die Spalten werden nur so weit gelesen wie es Anschlüsse gibt – nicht 600 Zeilen je Spalte.
	if f.rows > 4*MaxPorts+50 {
		t.Errorf("%d Zeilen gelesen – der Durchlauf ist nicht begrenzt", f.rows)
	}
}

// Ein Gerät mit riesiger Schnittstellen-Tabelle ohne Ethernet: Der Durchlauf endet an der Grenze.
func TestDetailTabellenGrenze(t *testing.T) {
	f := newFakeSession()
	f.str(oidSysName, "router")
	for i := 1; i <= maxIfRows+500; i++ {
		f.num(row(colIfType, i), 53)
	}
	for i := 1; i <= maxEntRows+500; i++ {
		f.num(row(colEntClass, i), 10)
	}
	d, err := clientWith(f, testConfig).Detail(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Ports) != 0 || !d.PortsCut {
		t.Errorf("Anschlüsse = %d, abgeschnitten = %v", len(d.Ports), d.PortsCut)
	}
	if f.rows > maxIfRows+maxEntRows+2 {
		t.Errorf("%d Zeilen gelesen", f.rows)
	}
	if f.walked(colEntContainedIn) {
		t.Error("nach abgeschnittener Klassen-Spalte wurde trotzdem weiter gesucht")
	}
}

func TestPing(t *testing.T) {
	f := newFakeSession()
	f.vals[trimDot(oidSysUpTime)] = gosnmp.SnmpPDU{Name: oidSysUpTime, Type: gosnmp.TimeTicks, Value: uint32(4711)}
	if err := clientWith(f, testConfig).Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.gets) != 1 || len(f.gets[0]) != 1 || f.gets[0][0] != oidSysUpTime || len(f.walks) != 0 {
		t.Errorf("erwartet genau eine Anfrage auf sysUpTime, bekam %v / %v", f.gets, f.walks)
	}
	if !f.closed {
		t.Error("Sitzung nicht geschlossen")
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestFehlerarten(t *testing.T) {
	const auth = "Zugang abgelehnt – Profil prüfen"
	for _, tc := range []struct {
		name string
		err  error
		kind Kind
		msg  string // Anfang des Klartexts
	}{
		{"Zeit abgelaufen", fmt.Errorf("request timeout (after 1 retries): %w", gosnmp.ErrMaxRetriesExceeded), KindTimeout, "Keine Antwort"},
		{"Frist des Aufrufers", context.DeadlineExceeded, KindTimeout, "Keine Antwort"},
		{"Netz: Zeit", &net.OpError{Op: "read", Net: "udp", Err: timeoutErr{}}, KindTimeout, "Keine Antwort"},
		{"Netz: Port zu", &net.OpError{Op: "read", Net: "udp", Err: os.NewSyscallError("recvfrom", syscall.ECONNREFUSED)}, KindTimeout, "Keine Antwort"},
		{"Netz: nicht erreichbar", fmt.Errorf("error establishing connection to host: %w", &net.OpError{Op: "dial", Net: "udp", Err: syscall.ENETUNREACH}), KindTimeout, "Keine Antwort"},
		{"unbekannter Benutzer", gosnmp.ErrUnknownUsername, KindAuth, auth},
		{"falsches Anmelde-Kennwort", gosnmp.ErrWrongDigest, KindAuth, auth},
		{"falsches Anmelde-Kennwort, Report verworfen", errors.New("incoming packet is not authentic, discarding"), KindAuth, auth},
		{"falsches Verschlüsselungs-Kennwort", gosnmp.ErrDecryption, KindAuth, auth},
		{"Zeitfenster", gosnmp.ErrNotInTimeWindow, KindAuth, auth},
		{"Kennung unbekannt", gosnmp.ErrUnknownEngineID, KindAuth, auth},
		{"Sicherheitsstufe", gosnmp.ErrUnknownSecurityLevel, KindAuth, auth},
		{"kein Leserecht", errNoAccess, KindAuth, auth},
		{"Gerät schweigt nach der Kennung", errSilent, KindAuth, auth},
		{"anderer Report", gosnmp.ErrUnknownReportPDU, KindOther, "Das Gerät lehnt die Abfrage ab"},
		{"abgebrochen", context.Canceled, KindOther, "Abfrage abgebrochen"},
		{"Unsinn", errors.New("error parsing SNMPV3 scoped PDU"), KindOther, "Antwort des Geräts unverständlich"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if k := Classify(tc.err); k != tc.kind {
				t.Errorf("Classify (roh) = %v, erwartet %v", k, tc.kind)
			}
			// über den Client: Ping und Detail liefern dieselbe Art
			f := newFakeSession()
			f.getErr = tc.err
			c := clientWith(f, testConfig)
			err := c.Ping(context.Background())
			if k := Classify(err); k != tc.kind {
				t.Errorf("Ping: Art = %v, erwartet %v", k, tc.kind)
			}
			if m := Message(err); !strings.HasPrefix(m, tc.msg) || m != err.Error() {
				t.Errorf("Klartext = %q / %q, erwartet Anfang %q", m, err.Error(), tc.msg)
			}
			if !errors.Is(err, tc.err) {
				t.Error("Ursache nicht mehr erreichbar (errors.Is)")
			}
			_, derr := c.Detail(context.Background())
			if k := Classify(derr); k != tc.kind {
				t.Errorf("Detail: Art = %v, erwartet %v", k, tc.kind)
			}
			for target, kind := range map[error]Kind{ErrTimeout: KindTimeout, ErrAuth: KindAuth, ErrResolve: KindResolve, ErrConfig: KindConfig} {
				if got := errors.Is(err, target); got != (kind == tc.kind) {
					t.Errorf("errors.Is(err, %v) = %v", target, got)
				}
			}
			if !f.closed {
				t.Error("Sitzung nach Fehler nicht geschlossen")
			}
		})
	}
	if Classify(nil) != KindNone || Message(nil) != "" {
		t.Error("ohne Fehler: erwartet KindNone und leeren Text")
	}
}

// Fehler beim Verbinden und mitten im Tabellen-Durchlauf.
func TestFehlerVerbindenUndDurchlauf(t *testing.T) {
	f := newFakeSession()
	f.connectErr = fmt.Errorf("error establishing connection to host: %w", &net.OpError{Op: "dial", Net: "udp", Err: ErrNotHouse})
	if err := clientWith(f, testConfig).Ping(context.Background()); Classify(err) != KindConfig || !errors.Is(err, ErrNotHouse) {
		t.Errorf("Verbinden zu fremder Adresse: %v (%v)", err, Classify(err))
	}
	if !f.closed {
		t.Error("Sitzung nach Fehler beim Verbinden nicht geschlossen")
	}

	for _, col := range []string{colEntClass, colIfType, colIfName, colIfOperStatus} {
		f = fakeSwitch()
		f.walkErr[col] = fmt.Errorf("request timeout (after 1 retries): %w", gosnmp.ErrMaxRetriesExceeded)
		d, err := clientWith(f, testConfig).Detail(context.Background())
		if Classify(err) != KindTimeout {
			t.Errorf("Abbruch in %s: Art = %v", col, Classify(err))
		}
		if d.Name != "" || len(d.Ports) != 0 {
			t.Errorf("Abbruch in %s: halbes Ergebnis geliefert: %+v", col, d)
		}
	}
}

// Die echte Sitzung: Kennung beantwortet, angemeldete Anfrage nicht → Gerät lebt.
func TestSitzungSchweigen(t *testing.T) {
	timeout := fmt.Errorf("request timeout (after 1 retries): %w", gosnmp.ErrMaxRetriesExceeded)
	if err := (&goSession{}).fail(timeout); Classify(err) != KindTimeout {
		t.Errorf("gar keine Antwort: %v", Classify(err))
	}
	if err := (&goSession{recv: 1}).fail(timeout); Classify(err) != KindAuth {
		t.Errorf("nur die Kennung beantwortet: %v, erwartet auth", Classify(err))
	}
	if err := (&goSession{recv: 5, ok: true}).fail(timeout); Classify(err) != KindTimeout {
		t.Errorf("Ausfall mitten in der Abfrage: %v, erwartet timeout", Classify(err))
	}
	if err := (&goSession{recv: 1}).fail(gosnmp.ErrUnknownUsername); !errors.Is(err, gosnmp.ErrUnknownUsername) {
		t.Errorf("andere Fehler bleiben unverändert: %v", err)
	}
}

func TestReportIstKeineAntwort(t *testing.T) {
	for oid, want := range map[string]error{
		".1.3.6.1.6.3.15.1.1.1.0": gosnmp.ErrUnknownSecurityLevel,
		".1.3.6.1.6.3.15.1.1.2.0": gosnmp.ErrNotInTimeWindow,
		".1.3.6.1.6.3.15.1.1.3.0": gosnmp.ErrUnknownUsername,
		".1.3.6.1.6.3.15.1.1.4.0": gosnmp.ErrUnknownEngineID,
		".1.3.6.1.6.3.15.1.1.5.0": gosnmp.ErrWrongDigest,
		".1.3.6.1.6.3.15.1.1.6.0": gosnmp.ErrDecryption,
	} {
		err := reportErr([]gosnmp.SnmpPDU{{Name: oid, Type: gosnmp.Counter32, Value: uint(1)}})
		if !errors.Is(err, want) || Classify(err) != KindAuth {
			t.Errorf("%s: %v (%v)", oid, err, Classify(err))
		}
	}
	if err := reportErr(nil); Classify(err) != KindOther {
		t.Errorf("unbekannter Report: %v", Classify(err))
	}
}

func TestVerfahren(t *testing.T) {
	var keys []string
	for _, p := range AuthProtos() {
		keys = append(keys, p.Key)
		if authProto(p.Key) == gosnmp.NoAuth || authProto(p.Key) == gosnmp.MD5 || p.Name == "" || ProtoName(p.Key) != p.Name {
			t.Errorf("Anmelde-Verfahren %q falsch zugeordnet", p.Key)
		}
		if Deprecated(p.Key) != (p.Key == "sha1") {
			t.Errorf("veraltet(%q) falsch", p.Key)
		}
	}
	for _, p := range PrivProtos() {
		keys = append(keys, p.Key)
		if privProto(p.Key) == gosnmp.NoPriv || privProto(p.Key) == gosnmp.DES || Deprecated(p.Key) {
			t.Errorf("Verschlüsselungs-Verfahren %q falsch zugeordnet", p.Key)
		}
	}
	// Die Schlüssel stehen in der Datenbank – sie dürfen sich nicht ändern.
	if got := strings.Join(keys, ","); got != "sha1,sha224,sha256,sha384,sha512,aes128,aes192,aes256" {
		t.Errorf("Schlüssel = %s", got)
	}
	// Standardvariante (Blumenthal), nicht die „C“-Variante
	if privProto("aes192") != gosnmp.AES192 || privProto("aes256") != gosnmp.AES256 || privProto("aes128") != gosnmp.AES {
		t.Error("AES-Variante falsch")
	}
	if !known(authProtos, DefaultAuthProto) || !known(privProtos, DefaultPrivProto) {
		t.Error("Vorschlag ist kein erlaubtes Verfahren")
	}
	// Die Liste ist eine Kopie – Ändern wirkt nicht zurück.
	AuthProtos()[0].Key = "md5"
	if AuthProtos()[0].Key != "sha1" {
		t.Error("AuthProtos gibt die eigene Liste heraus")
	}
}

func TestValidate(t *testing.T) {
	if err := testConfig.Validate(); err != nil {
		t.Fatalf("gültige Einstellung abgelehnt: %v", err)
	}
	for _, tc := range []struct {
		name string
		edit func(*Config)
	}{
		{"Adresse leer", func(c *Config) { c.Host = " " }},
		{"Adresse öffentlich", func(c *Config) { c.Host = "8.8.8.8" }},
		{"Adresse Loopback", func(c *Config) { c.Host = "127.0.0.1" }},
		{"Adresse Link-Local", func(c *Config) { c.Host = "169.254.1.1" }},
		{"Adresse IPv6 öffentlich", func(c *Config) { c.Host = "2001:db8::1" }},
		{"Adresse mit Schema", func(c *Config) { c.Host = "http://192.168.1.1" }},
		{"Adresse mit Port", func(c *Config) { c.Host = "switch1:161" }},
		{"Adresse unvollständig", func(c *Config) { c.Host = "192.168.1" }},
		{"Adresse mit Leerzeichen", func(c *Config) { c.Host = "switch 1" }},
		{"Port 0", func(c *Config) { c.Port = 0 }},
		{"Port zu groß", func(c *Config) { c.Port = 65536 }},
		{"Benutzer leer", func(c *Config) { c.User = "  " }},
		{"Benutzer zu lang", func(c *Config) { c.User = strings.Repeat("u", 33) }},
		{"Anmeldung MD5", func(c *Config) { c.AuthProto = "md5" }},
		{"Anmeldung leer", func(c *Config) { c.AuthProto = "" }},
		{"Anmeldung Großschreibung", func(c *Config) { c.AuthProto = "SHA256" }},
		{"Verschlüsselung DES", func(c *Config) { c.PrivProto = "des" }},
		{"Verschlüsselung leer", func(c *Config) { c.PrivProto = "" }},
		{"Verschlüsselung C-Variante", func(c *Config) { c.PrivProto = "aes256c" }},
		{"Anmelde-Kennwort kurz", func(c *Config) { c.AuthPass = "1234567" }},
		{"Anmelde-Kennwort zu lang", func(c *Config) { c.AuthPass = strings.Repeat("a", 129) }},
		{"Verschlüsselungs-Kennwort kurz", func(c *Config) { c.PrivPass = "1234567" }},
		{"Verschlüsselungs-Kennwort leer", func(c *Config) { c.PrivPass = "" }},
	} {
		c := testConfig
		tc.edit(&c)
		err := c.Validate()
		if Classify(err) != KindConfig || !errors.Is(err, ErrConfig) || Message(err) == "" {
			t.Errorf("%s: %v (Art %v), erwartet config", tc.name, err, Classify(err))
		}
		// und es wird gar nicht erst gefragt
		opened := false
		cl := New(c)
		cl.open = func(context.Context, net.IP, Config) session { opened = true; return newFakeSession() }
		if perr := cl.Ping(context.Background()); Classify(perr) != KindConfig || opened {
			t.Errorf("%s: Ping fragt trotz ungültiger Einstellung (%v, geöffnet %v)", tc.name, perr, opened)
		}
	}
	for _, ok := range []func(*Config){
		func(c *Config) { c.Host = "10.1.2.3" },
		func(c *Config) { c.Host = "172.16.0.9" },
		func(c *Config) { c.Host = "fd12:3456::1" },
		func(c *Config) { c.Host = " Switch-1.Werk.Example " },
		func(c *Config) { c.AuthPass, c.PrivPass = "12345678", "äöüßäöüß" },
		func(c *Config) { c.Port = 65535 },
	} {
		c := testConfig
		ok(&c)
		if err := c.Validate(); err != nil {
			t.Errorf("%v abgelehnt: %v", c, err)
		}
	}
}

func TestAdressregel(t *testing.T) {
	for ip, want := range map[string]bool{
		"10.0.0.1": true, "172.16.5.5": true, "172.31.255.254": true, "192.168.0.1": true, "fd00::1": true, "fc00::1": true,
		"172.32.0.1": false, "8.8.8.8": false, "100.64.0.1": false, "127.0.0.1": false, "::1": false, "169.254.0.1": false,
		"fe80::1": false, "0.0.0.0": false, "224.0.0.1": false, "255.255.255.255": false, "2001:db8::1": false, "::ffff:8.8.8.8": false,
	} {
		if got := houseIP(net.ParseIP(ip)); got != want {
			t.Errorf("houseIP(%s) = %v, erwartet %v", ip, got, want)
		}
	}
	if houseIP(nil) {
		t.Error("houseIP(nil) = true")
	}
	allowLoopback = true
	defer func() { allowLoopback = false }()
	if !houseIP(net.ParseIP("127.0.0.1")) || !houseIP(net.ParseIP("::1")) || houseIP(net.ParseIP("169.254.0.1")) || houseIP(net.ParseIP("8.8.8.8")) {
		t.Error("Stellschraube für Tests erlaubt nur Loopback")
	}
}

func stubLookup(t *testing.T, fn func(host string) ([]net.IP, error)) {
	t.Helper()
	old := lookupIP
	lookupIP = func(_ context.Context, host string) ([]net.IP, error) { return fn(host) }
	t.Cleanup(func() { lookupIP = old })
}

func TestNameAufloesen(t *testing.T) {
	cfg := testConfig
	cfg.Host = "Switch-1.werk.example"

	// Name zeigt ins Haus: gefragt wird genau die geprüfte Adresse, IPv4 bevorzugt.
	stubLookup(t, func(host string) ([]net.IP, error) {
		if host != "switch-1.werk.example" {
			t.Errorf("aufgelöst wird %q", host)
		}
		return []net.IP{net.ParseIP("fd00::5"), net.ParseIP("192.168.7.5")}, nil
	})
	var asked net.IP
	f := fakeSwitch()
	c := New(cfg)
	c.open = func(_ context.Context, ip net.IP, _ Config) session { asked = ip; return f }
	d, err := c.Detail(context.Background())
	if err != nil || asked.String() != "192.168.7.5" || d.IP != "192.168.7.5" {
		t.Errorf("Detail über Namen: %v, gefragt %v, IP %q", err, asked, d.IP)
	}

	// Zeigt auch nur eine Adresse nach außen, wird nicht gefragt.
	stubLookup(t, func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("192.168.7.5"), net.ParseIP("93.184.216.34")}, nil
	})
	asked = nil
	if err := c.Ping(context.Background()); Classify(err) != KindConfig || !errors.Is(err, ErrNotHouse) || asked != nil {
		t.Errorf("Name nach außen: %v (%v), gefragt %v", err, Classify(err), asked)
	}

	// Name unbekannt: eigene Fehlerart.
	stubLookup(t, func(string) ([]net.IP, error) {
		return nil, &net.DNSError{Err: "no such host", Name: "switch-1.werk.example", IsNotFound: true}
	})
	err = c.Ping(context.Background())
	if Classify(err) != KindResolve || !errors.Is(err, ErrResolve) || asked != nil {
		t.Errorf("Name unbekannt: %v (%v)", err, Classify(err))
	}
	if !strings.Contains(Message(err), "nicht auflösbar") {
		t.Errorf("Klartext = %q", Message(err))
	}
	stubLookup(t, func(string) ([]net.IP, error) { return nil, nil })
	if err := c.Ping(context.Background()); Classify(err) != KindResolve {
		t.Errorf("Name ohne Adresse: %v", Classify(err))
	}
}

// Kennwörter erscheinen in keiner Ausgabe.
func TestKennwoerterVerborgen(t *testing.T) {
	c := New(testConfig)
	f := newFakeSession()
	f.getErr = gosnmp.ErrWrongDigest
	c.open = func(context.Context, net.IP, Config) session { return f }
	err := c.Ping(context.Background())
	out := fmt.Sprintf("%v %+v %#v %s | %v %+v %#v | %v %+v", testConfig, testConfig, testConfig, testConfig, c, c, c, err, err)
	if strings.Contains(out, testConfig.AuthPass) || strings.Contains(out, testConfig.PrivPass) {
		t.Errorf("Kennwort in der Ausgabe: %s", out)
	}
	if !strings.Contains(out, "192.168.10.20") {
		t.Errorf("Adresse fehlt in der Ausgabe: %s", out)
	}
}
