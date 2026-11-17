package snmp

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/gosnmp/gosnmp"
)

// Kennungen der gelesenen Werte (SNMPv2-MIB, BRIDGE-MIB, IF-MIB, ENTITY-MIB).
const (
	oidSysDescr  = ".1.3.6.1.2.1.1.1.0"
	oidSysUpTime = ".1.3.6.1.2.1.1.3.0"
	oidSysName   = ".1.3.6.1.2.1.1.5.0"
	oidBridgeMAC = ".1.3.6.1.2.1.17.1.1.0" // dot1dBaseBridgeAddress

	colIfDescr      = ".1.3.6.1.2.1.2.2.1.2"
	colIfType       = ".1.3.6.1.2.1.2.2.1.3"
	colIfSpeed      = ".1.3.6.1.2.1.2.2.1.5"
	colIfPhysAddr   = ".1.3.6.1.2.1.2.2.1.6"
	colIfOperStatus = ".1.3.6.1.2.1.2.2.1.8"
	colIfName       = ".1.3.6.1.2.1.31.1.1.1.1"
	colIfHighSpeed  = ".1.3.6.1.2.1.31.1.1.1.15"

	colEntContainedIn = ".1.3.6.1.2.1.47.1.1.1.1.4"
	colEntClass       = ".1.3.6.1.2.1.47.1.1.1.1.5"
	colEntFirmware    = ".1.3.6.1.2.1.47.1.1.1.1.9"
	colEntSoftware    = ".1.3.6.1.2.1.47.1.1.1.1.10"
	colEntSerial      = ".1.3.6.1.2.1.47.1.1.1.1.11"
	colEntModel       = ".1.3.6.1.2.1.47.1.1.1.1.13"

	ifTypeEthernet  = 6 // ethernetCsmacd
	entClassChassis = 3
	operStatusUp    = 1

	maxRawString = 1024 // so viele Bytes eines Werts werden höchstens betrachtet
	maxName      = 128
	maxModel     = 80
	maxShort     = 64
)

// Art eines Geräts (Detail.Kind).
const (
	DeviceSwitch = "switch"
	DeviceOther  = "other"
)

// Port: ein Ethernet-Anschluss. Idx ist die Nummer des Geräts (ifIndex).
type Port struct {
	Idx       int
	Name      string
	Up        bool
	SpeedMbps int
}

// Detail: was ein Gerät über sich sagt. Leere Felder heißen: Das Gerät kennt die Angabe
// nicht (viele Drucker und USV haben z. B. keine ENTITY-MIB) – das ist kein Fehler.
type Detail struct {
	Name     string // sysName
	Model    string // Modell des Gehäuses, ersatzweise die gekürzte Beschreibung (sysDescr)
	Serial   string
	Firmware string
	MAC      string // aa:bb:cc:dd:ee:ff
	Kind     string // DeviceSwitch oder DeviceOther
	IP       string // die Adresse, an die gefragt wurde
	Ports    []Port // nur Ethernet, nach Idx sortiert, höchstens MaxPorts
	// PortsCut: Das Gerät hat mehr Anschlüsse als gelesen wurden.
	PortsCut bool
}

var errStop = errors.New("snmp: Durchlauf beendet")

func trimDot(oid string) string { return strings.TrimPrefix(oid, ".") }

// rowIndex liefert die Zeilennummer eines Werts der Spalte col (Tabellen mit einer Zahl als Index).
func rowIndex(name, col string) (int, bool) {
	rest, ok := strings.CutPrefix(trimDot(name), trimDot(col)+".")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// present: Der Wert ist da (nicht noSuchObject/noSuchInstance/endOfMibView/Null).
func present(p gosnmp.SnmpPDU) bool {
	switch p.Type {
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView, gosnmp.Null:
		return false
	}
	return p.Value != nil
}

func rawBytes(p gosnmp.SnmpPDU) []byte {
	if !present(p) {
		return nil
	}
	switch v := p.Value.(type) {
	case []byte:
		return v
	case string:
		return []byte(v)
	}
	return nil
}

func intVal(p gosnmp.SnmpPDU) (int64, bool) {
	if !present(p) {
		return 0, false
	}
	switch p.Value.(type) {
	case []byte, string:
		return 0, false
	}
	return gosnmp.ToBigInt(p.Value).Int64(), true
}

// clean säubert Text vom Gerät: keine Steuer- oder unsichtbaren Zeichen, einzeilig,
// Leerraum zusammengezogen, auf max Zeichen gekürzt.
func clean(b []byte, max int) string {
	if len(b) > maxRawString {
		b = b[:maxRawString]
	}
	s := strings.ToValidUTF8(string(b), "")
	s = strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max]))
	}
	return s
}

// firstLine: nur die erste Zeile einer Beschreibung.
func firstLine(b []byte) []byte {
	b = bytes.TrimLeft(b, "\r\n \t")
	if i := bytes.IndexAny(b, "\r\n"); i >= 0 {
		b = b[:i]
	}
	return b
}

// macOf deutet eine Hardware-Adresse: 6 Bytes (oder Text wie "00:11:22:33:44:55"), nicht
// nur Nullen und nicht die Rundruf-Adresse.
func macOf(b []byte) net.HardwareAddr {
	if len(b) != 6 {
		hw, err := net.ParseMAC(strings.TrimSpace(string(b)))
		if err != nil || len(hw) != 6 {
			return nil
		}
		b = hw
	}
	if bytes.Equal(b, make([]byte, 6)) || bytes.Equal(b, bytes.Repeat([]byte{0xff}, 6)) {
		return nil
	}
	return append(net.HardwareAddr(nil), b...)
}

// walkCol läuft eine Spalte ab und ruft fn je Zeile; fn beendet den Lauf mit false. Nach
// limit Zeilen ist Schluss (cut = true). Fehlt die Spalte, kommt einfach nichts.
func walkCol(s session, col string, limit int, fn func(idx int, p gosnmp.SnmpPDU) bool) (cut bool, err error) {
	rows := 0
	err = s.BulkWalk(col, func(p gosnmp.SnmpPDU) error {
		if rows++; rows > limit {
			cut = true
			return errStop
		}
		idx, ok := rowIndex(p.Name, col)
		if !ok {
			return nil
		}
		if !fn(idx, p) {
			return errStop
		}
		return nil
	})
	if errors.Is(err, errStop) {
		err = nil
	}
	return cut, err
}

// Detail liest Name, Modell, Seriennummer, Firmware, MAC und die Ethernet-Anschlüsse.
// Gelesen werden gezielt einzelne Werte und Spalten, nie ganze Tabellen.
func (c *Client) Detail(ctx context.Context) (Detail, error) {
	ctx, cancel := context.WithTimeout(ctx, detailLimit)
	defer cancel()
	s, ip, err := c.connect(ctx)
	if err != nil {
		return Detail{}, err
	}
	defer s.Close()

	d := Detail{Kind: DeviceOther, IP: ip.String()}
	vars, err := s.Get([]string{oidSysDescr, oidSysName, oidBridgeMAC})
	if err != nil {
		return Detail{}, wrap(err)
	}
	var descr string
	var bridge net.HardwareAddr
	for _, p := range vars {
		switch "." + trimDot(p.Name) {
		case oidSysDescr:
			descr = clean(firstLine(rawBytes(p)), maxModel)
		case oidSysName:
			d.Name = clean(rawBytes(p), maxName)
		case oidBridgeMAC:
			bridge = macOf(rawBytes(p))
		}
	}

	if err := readEntity(s, &d); err != nil {
		return Detail{}, wrap(err)
	}
	if d.Model == "" {
		d.Model = descr
	}

	ports, cut, err := readPorts(s)
	if err != nil {
		return Detail{}, wrap(err)
	}
	d.Ports, d.PortsCut = ports, cut

	mac := bridge
	if mac == nil && len(ports) > 0 {
		if mac, err = smallestPortMAC(s, ports); err != nil {
			return Detail{}, wrap(err)
		}
	}
	if mac != nil {
		d.MAC = mac.String()
	}
	if bridge != nil && len(ports) >= 4 {
		d.Kind = DeviceSwitch
	}
	return d, nil
}

// readEntity sucht das Gehäuse in der ENTITY-MIB – den ersten Eintrag der Klasse „chassis“,
// sonst den ersten ohne Elternteil – und liest dessen Modell, Seriennummer und Firmware.
func readEntity(s session, d *Detail) error {
	chassis, seen := 0, false
	cut, err := walkCol(s, colEntClass, maxEntRows, func(idx int, p gosnmp.SnmpPDU) bool {
		seen = true
		if v, ok := intVal(p); ok && v == entClassChassis {
			chassis = idx
			return false
		}
		return true
	})
	if err != nil {
		return err
	}
	if !seen || cut {
		return nil // keine ENTITY-MIB, oder so groß, dass nicht weiter gesucht wird
	}
	if chassis == 0 {
		if _, err := walkCol(s, colEntContainedIn, maxEntRows, func(idx int, p gosnmp.SnmpPDU) bool {
			if v, ok := intVal(p); ok && v == 0 {
				chassis = idx
				return false
			}
			return true
		}); err != nil {
			return err
		}
	}
	if chassis == 0 {
		return nil
	}
	row := "." + strconv.Itoa(chassis)
	vars, err := s.Get([]string{colEntModel + row, colEntSerial + row, colEntSoftware + row, colEntFirmware + row})
	if err != nil {
		return err
	}
	var sw, fw string
	for _, p := range vars {
		switch "." + trimDot(p.Name) {
		case colEntModel + row:
			d.Model = clean(rawBytes(p), maxModel)
		case colEntSerial + row:
			d.Serial = clean(rawBytes(p), maxShort)
		case colEntSoftware + row:
			sw = clean(rawBytes(p), maxShort)
		case colEntFirmware + row:
			fw = clean(rawBytes(p), maxShort)
		}
	}
	if d.Firmware = sw; sw == "" {
		d.Firmware = fw
	}
	return nil
}

// readPorts liest die Ethernet-Anschlüsse (ifType 6) aus ifTable/ifXTable.
func readPorts(s session) ([]Port, bool, error) {
	var ports []Port
	pos := map[int]int{}
	tooMany := false
	cut, err := walkCol(s, colIfType, maxIfRows, func(idx int, p gosnmp.SnmpPDU) bool {
		if v, ok := intVal(p); !ok || v != ifTypeEthernet {
			return true
		}
		if _, dup := pos[idx]; dup {
			return true
		}
		if len(ports) >= MaxPorts {
			tooMany = true
			return false
		}
		pos[idx] = len(ports)
		ports = append(ports, Port{Idx: idx})
		return true
	})
	if err != nil {
		return nil, false, err
	}
	cut = cut || tooMany
	if len(ports) == 0 {
		return nil, cut, nil
	}
	last := 0
	for _, p := range ports {
		last = max(last, p.Idx)
	}
	// col läuft eine Spalte nur so weit ab, wie es Ethernet-Anschlüsse gibt.
	col := func(oid string, set func(p *Port, v gosnmp.SnmpPDU)) error {
		_, err := walkCol(s, oid, maxIfRows, func(idx int, v gosnmp.SnmpPDU) bool {
			if i, ok := pos[idx]; ok {
				set(&ports[i], v)
			}
			return idx < last
		})
		return err
	}

	speedKnown := make([]bool, len(ports))
	if err := col(colIfName, func(p *Port, v gosnmp.SnmpPDU) { p.Name = clean(rawBytes(v), maxShort) }); err != nil {
		return nil, false, err
	}
	if err := col(colIfHighSpeed, func(p *Port, v gosnmp.SnmpPDU) {
		if n, ok := intVal(v); ok && n >= 0 {
			p.SpeedMbps, speedKnown[pos[p.Idx]] = int(min(n, 1_000_000)), true
		}
	}); err != nil {
		return nil, false, err
	}
	if err := col(colIfOperStatus, func(p *Port, v gosnmp.SnmpPDU) {
		n, ok := intVal(v)
		p.Up = ok && n == operStatusUp
	}); err != nil {
		return nil, false, err
	}

	needName, needSpeed := false, false
	for i, p := range ports {
		needName = needName || p.Name == ""
		needSpeed = needSpeed || !speedKnown[i]
	}
	if needName { // ohne ifXTable: Name aus ifDescr
		if err := col(colIfDescr, func(p *Port, v gosnmp.SnmpPDU) {
			if p.Name == "" {
				p.Name = clean(rawBytes(v), maxShort)
			}
		}); err != nil {
			return nil, false, err
		}
	}
	if needSpeed { // ohne ifHighSpeed: ifSpeed in Bit/s
		if err := col(colIfSpeed, func(p *Port, v gosnmp.SnmpPDU) {
			if n, ok := intVal(v); ok && n >= 0 && !speedKnown[pos[p.Idx]] {
				p.SpeedMbps = int(n / 1_000_000)
			}
		}); err != nil {
			return nil, false, err
		}
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i].Idx < ports[j].Idx })
	return ports, cut, nil
}

// smallestPortMAC: die kleinste gültige Hardware-Adresse eines Ethernet-Anschlusses – für
// Geräte ohne Bridge-Adresse (Drucker, USV, NAS).
func smallestPortMAC(s session, ports []Port) (net.HardwareAddr, error) {
	eth := make(map[int]bool, len(ports))
	last := 0
	for _, p := range ports {
		eth[p.Idx] = true
		last = max(last, p.Idx)
	}
	var best net.HardwareAddr
	_, err := walkCol(s, colIfPhysAddr, maxIfRows, func(idx int, p gosnmp.SnmpPDU) bool {
		if eth[idx] {
			if m := macOf(rawBytes(p)); m != nil && (best == nil || bytes.Compare(m, best) < 0) {
				best = m
			}
		}
		return idx < last
	})
	return best, err
}
