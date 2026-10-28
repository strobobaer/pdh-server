package web

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// Eigene Zertifizierungsstelle (CA) des PDH für Nextcloud (migrations/126):
//   - client: persönliches Zertifikat für die Nextcloud-App (mTLS, nginx/Cloudflare prüfen es)
//   - device: Geräte-/Firmenzertifikat ohne Person
//   - server: Server-Zertifikat für Nextcloud mit eigenen Namen/IPs; Geräte vertrauen der CA
// Ausgeliefert als passwortgeschützte .p12 (3DES/SHA-1 – lesen iOS, Android,
// Windows, macOS und OpenSSL 3 ohne Zusatz). Die CA liegt verschlüsselt in
// app_settings; die Sperrliste (CRL) und das CA-Zertifikat sind öffentlich
// (/pki/ca.crt, /pki/crl.pem), damit nginx und Geräte sie abrufen können.

const (
	pkiSettingCert = "pki.ca_cert"
	pkiSettingKey  = "pki.ca_key"
	pkiCAYears     = 10
	pkiServerDays  = 825 // Höchstdauer, die Apple-Geräte bei Server-Zertifikaten akzeptieren
	pkiCRLDays     = 30  // nginx lehnt nach Ablauf der CRL alle Zertifikate ab – Skript holt sie täglich
)

var pkiMu sync.Mutex // CA anlegen und Seriennummern nicht doppelt

var pkiKinds = map[string]string{"client": "Persönlich (Nextcloud-App)", "device": "Gerät / Firma", "server": "Server (Nextcloud)"}

type pkiCA struct {
	Cert    *x509.Certificate
	Key     *rsa.PrivateKey
	CertPEM []byte
}

// pkiKey: eigener Schlüssel für die CA und die gespeicherten p12-Dateien.
func (h *Handler) pkiKey() []byte {
	secret := h.mailCfg.CredentialsKey
	if secret == "" {
		secret = h.jwtSecret
	}
	sum := sha256.Sum256([]byte("pdh-pki:" + secret))
	return sum[:]
}

// pkiLoadCA: CA laden, beim ersten Aufruf anlegen.
func (h *Handler) pkiLoadCA(ctx context.Context) (*pkiCA, error) {
	pkiMu.Lock()
	defer pkiMu.Unlock()
	certPEM := h.getUpdateSetting(ctx, pkiSettingCert, "")
	keyEnc := h.getUpdateSetting(ctx, pkiSettingKey, "")
	if certPEM != "" && keyEnc != "" {
		keyPEM, err := decryptSecret(h.pkiKey(), keyEnc)
		if err != nil {
			return nil, errors.New("CA-Schlüssel nicht lesbar – wurde PDH_AUTH_JWTSECRET bzw. PDH_CREDENTIALS_KEY geändert?")
		}
		return parseCA([]byte(certPEM), []byte(keyPEM))
	}
	if h.Mirror().On { // Spiegel: keine Zertifizierungsstelle anlegen
		return nil, errMirrorNotCreated
	}
	ca, keyPEM, err := newCA(h.branding().AppName, time.Now())
	if err != nil {
		return nil, err
	}
	enc, err := encryptSecret(h.pkiKey(), string(keyPEM))
	if err != nil {
		return nil, err
	}
	if err := h.setUpdateSetting(ctx, pkiSettingKey, enc); err != nil {
		return nil, err
	}
	if err := h.setUpdateSetting(ctx, pkiSettingCert, string(ca.CertPEM)); err != nil {
		return nil, err
	}
	componentLog("zertifikate").Info().Str("ca", ca.Cert.Subject.CommonName).Msg("zertifizierungsstelle angelegt")
	return ca, nil
}

func newCA(app string, now time.Time) (*pkiCA, []byte, error) {
	if strings.TrimSpace(app) == "" {
		app = "PDH"
	}
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return nil, nil, err
	}
	serial, err := pkiSerial()
	if err != nil {
		return nil, nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: app + " Zertifizierungsstelle", Organization: []string{app}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(pkiCAYears, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	ca, err := parseCA(certPEM, keyPEM)
	return ca, keyPEM, err
}

func parseCA(certPEM, keyPEM []byte) (*pkiCA, error) {
	cb, _ := pem.Decode(certPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || kb == nil {
		return nil, errors.New("CA beschädigt")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKCS1PrivateKey(kb.Bytes)
	if err != nil {
		return nil, err
	}
	return &pkiCA{Cert: cert, Key: key, CertPEM: certPEM}, nil
}

func pkiSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

// pkiPassword: gut abtippbares Passwort für die p12 (ohne 0/O, 1/l/I).
func pkiPassword() string {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, 16)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[n.Int64()]
	}
	return string(b[:4]) + "-" + string(b[4:8]) + "-" + string(b[8:12]) + "-" + string(b[12:])
}

type pkiRequest struct {
	Kind, CommonName, Label, UserID, Email string
	Hostnames                              []string // server: DNS-Namen und IPs
	Days                                   int
}

type pkiIssued struct {
	CertPEM, KeyPEM []byte
	P12             []byte
	Password        string
	Serial          string
	NotBefore       time.Time
	NotAfter        time.Time
}

// pkiIssue erzeugt Schlüssel, Zertifikat und p12 (ohne Datenbank).
func pkiIssue(ca *pkiCA, req pkiRequest, now time.Time) (*pkiIssued, error) {
	if _, ok := pkiKinds[req.Kind]; !ok {
		return nil, errors.New("unbekannte Zertifikatsart")
	}
	cn := strings.TrimSpace(req.CommonName)
	if cn == "" {
		return nil, errors.New("Bitte einen Namen angeben")
	}
	days := req.Days
	if days <= 0 {
		days = 730
	}
	if req.Kind == "server" && days > pkiServerDays {
		days = pkiServerDays
	}
	notAfter := now.AddDate(0, 0, days)
	if notAfter.After(ca.Cert.NotAfter) {
		notAfter = ca.Cert.NotAfter
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	serial, err := pkiSerial()
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn, Organization: ca.Cert.Subject.Organization},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		BasicConstraintsValid: true,
	}
	switch req.Kind {
	case "client", "device":
		tpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		if req.Email != "" {
			tpl.EmailAddresses = []string{req.Email}
		}
	case "server":
		tpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		for _, hname := range req.Hostnames {
			if ip := net.ParseIP(hname); ip != nil {
				tpl.IPAddresses = append(tpl.IPAddresses, ip)
			} else {
				tpl.DNSNames = append(tpl.DNSNames, hname)
			}
		}
		if len(tpl.DNSNames)+len(tpl.IPAddresses) == 0 {
			return nil, errors.New("Bitte mindestens einen Servernamen oder eine IP angeben")
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	pw := pkiPassword()
	p12, err := pkcs12.LegacyDES.Encode(key, cert, []*x509.Certificate{ca.Cert}, pw)
	if err != nil {
		return nil, err
	}
	return &pkiIssued{
		CertPEM:   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:    pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
		P12:       p12,
		Password:  pw,
		Serial:    hex.EncodeToString(serial.Bytes()),
		NotBefore: cert.NotBefore,
		NotAfter:  cert.NotAfter,
	}, nil
}

// pkiParseHostnames: „cloud.firma.de, 192.168.1.10 nextcloud.local“ → Liste.
func pkiParseHostnames(s string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t' }) {
		f = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(f), "."))
		f = strings.TrimPrefix(strings.TrimPrefix(f, "https://"), "http://")
		f = strings.TrimRight(f, "/")
		if f == "" || seen[f] {
			continue
		}
		if net.ParseIP(f) == nil && !pkiHostRe(f) {
			return nil, fmt.Errorf("„%s“ ist kein gültiger Servername", f)
		}
		seen[f] = true
		out = append(out, f)
	}
	return out, nil
}

func pkiHostRe(s string) bool {
	if len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimPrefix(s, "*."), ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// pkiStore: ausgestelltes Zertifikat speichern (p12 und Passwort verschlüsselt).
func (h *Handler) pkiStore(ctx context.Context, req pkiRequest, is *pkiIssued, actorID string) (string, error) {
	p12Enc, err := encryptSecret(h.pkiKey(), base64.StdEncoding.EncodeToString(is.P12))
	if err != nil {
		return "", err
	}
	pwEnc, err := encryptSecret(h.pkiKey(), is.Password)
	if err != nil {
		return "", err
	}
	keyEnc := ""
	if req.Kind == "server" { // für nginx als PEM herunterladbar
		if keyEnc, err = encryptSecret(h.pkiKey(), string(is.KeyPEM)); err != nil {
			return "", err
		}
	}
	var id string
	err = h.db.QueryRow(ctx, `INSERT INTO pki_certificates (kind, serial, common_name, label, user_id, hostnames, not_before, not_after,
			cert_pem, key_enc, p12_enc, password_enc, created_by)
		VALUES ($1, $2, $3, $4, $5::uuid, $6, $7, $8, $9, $10, $11, $12, $13::uuid) RETURNING id::text`,
		req.Kind, is.Serial, req.CommonName, req.Label, nullID(req.UserID), strings.Join(req.Hostnames, ", "), is.NotBefore, is.NotAfter,
		string(is.CertPEM), keyEnc, p12Enc, pwEnc, nullID(actorID)).Scan(&id)
	return id, err
}

// pkiCRL: Sperrliste aller gesperrten Zertifikate, von der CA signiert.
func (h *Handler) pkiCRL(ctx context.Context) ([]byte, error) {
	ca, err := h.pkiLoadCA(ctx)
	if err != nil {
		return nil, err
	}
	var revoked []x509.RevocationListEntry
	if rows, err := h.db.Query(ctx, `SELECT serial, revoked_at FROM pki_certificates WHERE revoked_at IS NOT NULL AND not_after > NOW()`); err == nil {
		for rows.Next() {
			var serial string
			var at time.Time
			if rows.Scan(&serial, &at) != nil {
				continue
			}
			if b, err := hex.DecodeString(serial); err == nil {
				revoked = append(revoked, x509.RevocationListEntry{SerialNumber: new(big.Int).SetBytes(b), RevocationTime: at})
			}
		}
		rows.Close()
	}
	return pkiBuildCRL(ca, revoked, time.Now())
}

func pkiBuildCRL(ca *pkiCA, revoked []x509.RevocationListEntry, now time.Time) ([]byte, error) {
	der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{
		Number:                    big.NewInt(now.Unix()),
		ThisUpdate:                now.Add(-time.Minute),
		NextUpdate:                now.AddDate(0, 0, pkiCRLDays),
		RevokedCertificateEntries: revoked,
	}, ca.Cert, ca.Key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der}), nil
}

// pkiFingerprint: SHA-256 des Zertifikats, „AB:CD:…“ zum Abgleichen auf Geräten.
func pkiFingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}
