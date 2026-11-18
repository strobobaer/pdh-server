package snmp

import (
	"context"
	"net"
	"syscall"

	"github.com/gosnmp/gosnmp"
)

// session: die schmale Schnittstelle zur Bibliothek. Tests setzen eine nachgebaute Sitzung
// ein und laufen ohne Netz.
type session interface {
	Connect() error
	// Get liest einzelne Werte. Fehlende Werte kommen als NoSuchObject/NoSuchInstance zurück.
	Get(oids []string) ([]gosnmp.SnmpPDU, error)
	// BulkWalk läuft eine Spalte bzw. einen Teilbaum ab; ein Fehler von fn beendet den Lauf.
	BulkWalk(root string, fn func(gosnmp.SnmpPDU) error) error
	Close() error
}

func authProto(key string) gosnmp.SnmpV3AuthProtocol {
	switch key {
	case "sha1":
		return gosnmp.SHA
	case "sha224":
		return gosnmp.SHA224
	case "sha256":
		return gosnmp.SHA256
	case "sha384":
		return gosnmp.SHA384
	case "sha512":
		return gosnmp.SHA512
	}
	return gosnmp.NoAuth // Validate lässt das nicht zu; gosnmp lehnt authPriv ohne Verfahren ab
}

// privProto: Für AES-192 und AES-256 kennt gosnmp zwei Arten, den Schlüssel zu verlängern.
// Genommen wird die Standardvariante nach Blumenthal (gosnmp.AES192/AES256, wie net-snmp
// und die meisten Geräte), nicht die „C“-Variante nach Reeder (AES192C/AES256C, Cisco).
func privProto(key string) gosnmp.SnmpV3PrivProtocol {
	switch key {
	case "aes128":
		return gosnmp.AES
	case "aes192":
		return gosnmp.AES192
	case "aes256":
		return gosnmp.AES256
	}
	return gosnmp.NoPriv
}

// goSession: Sitzung über gosnmp (UDP, SNMP v3, authPriv).
type goSession struct {
	g *gosnmp.GoSNMP
	// recv zählt angekommene Pakete, ok merkt sich die erste angemeldet beantwortete
	// Anfrage. Daraus wird erkannt: Kennung beantwortet, angemeldete Anfrage nicht (errSilent).
	recv int
	ok   bool
}

// openGoSNMP baut die Sitzung zur bereits geprüften Adresse ip. Control prüft die
// Zieladresse unmittelbar vor dem Verbinden noch einmal.
func openGoSNMP(ctx context.Context, ip net.IP, cfg Config) session {
	s := &goSession{}
	s.g = &gosnmp.GoSNMP{
		Target:         ip.String(),
		Port:           uint16(cfg.Port),
		Transport:      "udp",
		Version:        gosnmp.Version3,
		Context:        ctx,
		Timeout:        reqTimeout,
		Retries:        reqRetries,
		MaxRepetitions: maxRepetitions,
		SecurityModel:  gosnmp.UserSecurityModel,
		MsgFlags:       gosnmp.AuthPriv,
		SecurityParameters: &gosnmp.UsmSecurityParameters{
			UserName:                 cfg.User,
			AuthenticationProtocol:   authProto(cfg.AuthProto),
			AuthenticationPassphrase: cfg.AuthPass,
			PrivacyProtocol:          privProto(cfg.PrivProto),
			PrivacyPassphrase:        cfg.PrivPass,
		},
		Control: func(_, address string, _ syscall.RawConn) error {
			h, _, err := net.SplitHostPort(address)
			if err != nil || !houseIP(net.ParseIP(h)) {
				return ErrNotHouse
			}
			return nil
		},
		OnRecv: func(*gosnmp.GoSNMP) { s.recv++ },
	}
	return s
}

func (s *goSession) Connect() error { return s.g.Connect() }
func (s *goSession) Close() error   { return s.g.Close() }

// fail deutet einen Fehler der Bibliothek: Kam vor der ersten angemeldeten Antwort schon
// ein Paket an (die Antwort auf die Suche nach der Kennung) und danach nichts mehr, lebt
// das Gerät – es schweigt nur zur angemeldeten Anfrage.
func (s *goSession) fail(err error) error {
	if !s.ok && s.recv > 0 && isTimeout(err) {
		return errSilent
	}
	return err
}

func (s *goSession) Get(oids []string) ([]gosnmp.SnmpPDU, error) {
	res, err := s.g.Get(oids)
	if err != nil {
		return nil, s.fail(err)
	}
	if res == nil {
		return nil, errReport
	}
	// Ein Report ist nie eine Antwort – auch nicht, wenn die Bibliothek ihn durchreicht
	// (z. B. zweimal „nicht im Zeitfenster“ hintereinander).
	if res.PDUType == gosnmp.Report {
		return nil, reportErr(res.Variables)
	}
	if res.Error == gosnmp.AuthorizationError {
		return nil, errNoAccess
	}
	s.ok = true
	return res.Variables, nil
}

func (s *goSession) BulkWalk(root string, fn func(gosnmp.SnmpPDU) error) error {
	if err := s.g.BulkWalk(root, fn); err != nil {
		return s.fail(err)
	}
	return nil
}

// reportErr deutet einen durchgereichten Report nach seiner Kennung (usmStats…, RFC 3414).
func reportErr(vars []gosnmp.SnmpPDU) error {
	if len(vars) == 1 {
		switch trimDot(vars[0].Name) {
		case "1.3.6.1.6.3.15.1.1.1.0":
			return gosnmp.ErrUnknownSecurityLevel
		case "1.3.6.1.6.3.15.1.1.2.0":
			return gosnmp.ErrNotInTimeWindow
		case "1.3.6.1.6.3.15.1.1.3.0":
			return gosnmp.ErrUnknownUsername
		case "1.3.6.1.6.3.15.1.1.4.0":
			return gosnmp.ErrUnknownEngineID
		case "1.3.6.1.6.3.15.1.1.5.0":
			return gosnmp.ErrWrongDigest
		case "1.3.6.1.6.3.15.1.1.6.0":
			return gosnmp.ErrDecryption
		}
	}
	return errReport
}
