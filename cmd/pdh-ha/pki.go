package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
)

// Eigene Zertifizierungsstelle des Ausfallschutzes: CA ECDSA P-256 (10 Jahre),
// Knoten und Zeuge ECDSA P-256 (CN = Kennung, OU = node | witness).

var idRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// validID prüft eine Kennung: [a-z0-9-], 1–32 Zeichen, kein '-' am Rand.
func validID(id string) bool {
	return idRe.MatchString(id) && id[0] != '-' && id[len(id)-1] != '-'
}

// normHex macht Hex-Text vergleichbar: klein, ohne führende Nullen.
func normHex(s string) string {
	s = strings.TrimLeft(strings.ToLower(s), "0")
	if s == "" {
		return "0"
	}
	return s
}

func validKind(kind string) bool { return kind == "node" || kind == "witness" }

func randSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

// serialHex ist die Seriennummer als kleingeschriebener Hex-Text (Sperrliste).
func serialHex(c *x509.Certificate) string { return c.SerialNumber.Text(16) }

func encodeKey(k *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

func newCA(clusterID string) (certPEM, keyPEM []byte, err error) {
	if clusterID == "" {
		return nil, nil, errors.New("Cluster-Kennung fehlt")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "PDH-HA CA " + clusterID},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err = encodeKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyPEM, nil
}

// newKeyCSR erzeugt einen Schlüssel und die Signieranforderung für Kennung und Art.
func newKeyCSR(id, kind string) (keyPEM, csrPEM []byte, err error) {
	if !validID(id) {
		return nil, nil, fmt.Errorf("Ungültige Kennung %q", id)
	}
	if !validKind(kind) {
		return nil, nil, fmt.Errorf("Ungültige Art %q", kind)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: id, OrganizationalUnit: []string{kind}},
	}, key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err = encodeKey(key)
	if err != nil {
		return nil, nil, err
	}
	return keyPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// signCSR stellt aus der Anforderung ein Zertifikat der CA aus. Die Art kommt
// vom Aufrufer (aus dem Beitrittscode), nie aus der Anforderung.
func signCSR(caCertPEM, caKeyPEM, csrPEM []byte, kind string, days int) ([]byte, error) {
	if !validKind(kind) {
		return nil, fmt.Errorf("Ungültige Art %q", kind)
	}
	if days <= 0 {
		return nil, errors.New("Laufzeit muss größer als 0 Tage sein")
	}
	cb, _ := pem.Decode(caCertPEM)
	kb, _ := pem.Decode(caKeyPEM)
	rb, _ := pem.Decode(csrPEM)
	if cb == nil || kb == nil || rb == nil {
		return nil, errors.New("Zertifikatsdaten sind unlesbar")
	}
	ca, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, err
	}
	caKey, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, err
	}
	csr, err := x509.ParseCertificateRequest(rb.Bytes)
	if err != nil {
		return nil, err
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, err
	}
	id := csr.Subject.CommonName
	if !validID(id) {
		return nil, fmt.Errorf("Ungültige Kennung %q", id)
	}
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, errors.New("Schlüssel muss ECDSA P-256 sein")
	}
	serial, err := randSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	notAfter := now.AddDate(0, 0, days)
	if notAfter.After(ca.NotAfter) {
		notAfter = ca.NotAfter
	}
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: id, OrganizationalUnit: []string{kind}},
		DNSNames:     []string{id},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca, csr.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// fingerprint ist der SHA-256-Fingerabdruck (hex) über das DER des Zertifikats.
func fingerprint(certPEM []byte) string {
	b, _ := pem.Decode(certPEM)
	if b == nil {
		return ""
	}
	h := sha256.Sum256(b.Bytes)
	return hex.EncodeToString(h[:])
}

// peerIdentity liest Kennung und Art des Gegenübers aus dem (bereits von TLS
// gegen die CA geprüften) Zertifikat und weist gesperrte Seriennummern ab.
func peerIdentity(cs tls.ConnectionState, revoked []string) (id, kind string, err error) {
	if len(cs.VerifiedChains) == 0 || len(cs.VerifiedChains[0]) == 0 {
		return "", "", errors.New("Zertifikat nicht von der Cluster-CA")
	}
	c := cs.VerifiedChains[0][0]
	ser := normHex(serialHex(c))
	for _, r := range revoked {
		if normHex(r) == ser {
			return "", "", errors.New("Zertifikat gesperrt")
		}
	}
	if len(c.Subject.OrganizationalUnit) != 1 || !validKind(c.Subject.OrganizationalUnit[0]) || !validID(c.Subject.CommonName) {
		return "", "", errors.New("Zertifikat hat keine gültige Kennung")
	}
	return c.Subject.CommonName, c.Subject.OrganizationalUnit[0], nil
}
