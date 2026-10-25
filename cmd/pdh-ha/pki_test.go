package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net"
	"strings"
	"testing"
)

func parseCert(t *testing.T, p []byte) *x509.Certificate {
	t.Helper()
	b, _ := pem.Decode(p)
	if b == nil {
		t.Fatal("kein PEM")
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func issueNode(t *testing.T, caCert, caKey []byte, id, kind string) (certPEM, keyPEM []byte) {
	t.Helper()
	k, csr, err := newKeyCSR(id, kind)
	if err != nil {
		t.Fatal(err)
	}
	c, err := signCSR(caCert, caKey, csr, kind, 730)
	if err != nil {
		t.Fatal(err)
	}
	return c, k
}

func TestSignCSR(t *testing.T) {
	caCert, caKey, err := newCA("c1")
	if err != nil {
		t.Fatal(err)
	}
	ca := parseCert(t, caCert)
	if ca.Subject.CommonName != "PDH-HA CA c1" || !ca.IsCA {
		t.Fatalf("CA: %v", ca.Subject)
	}
	cert, _ := issueNode(t, caCert, caKey, "pdh-a", "node")
	c := parseCert(t, cert)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, err := c.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatal(err)
	}
	if c.Subject.CommonName != "pdh-a" || len(c.Subject.OrganizationalUnit) != 1 || c.Subject.OrganizationalUnit[0] != "node" {
		t.Fatalf("Subject: %v", c.Subject)
	}
	days := c.NotAfter.Sub(c.NotBefore).Hours() / 24
	if days < 729 || days > 731 {
		t.Fatalf("Laufzeit %.2f Tage", days)
	}
	if len(fingerprint(cert)) != 64 {
		t.Fatal("Fingerabdruck")
	}

	if _, _, err := newKeyCSR("../x", "node"); err == nil {
		t.Fatal("CN ../x akzeptiert")
	}
	if _, _, err := newKeyCSR("pdh-a", "boss"); err == nil {
		t.Fatal("Art boss akzeptiert")
	}
	_, csr, _ := newKeyCSR("pdh-b", "node")
	if _, err := signCSR(caCert, caKey, csr, "boss", 730); err == nil {
		t.Fatal("Art boss beim Signieren akzeptiert")
	}
}

func TestValidID(t *testing.T) {
	for _, bad := range []string{"../x", "-a", "a-", "", "A", strings.Repeat("a", 33)} {
		if validID(bad) {
			t.Errorf("%q akzeptiert", bad)
		}
	}
	if !validID("pdh-1") || !validID("a") {
		t.Fatal("gültige Kennung abgelehnt")
	}
}

// handshake lässt Server und Client über net.Pipe verhandeln.
func handshake(t *testing.T, srvCert, srvKey, srvCA, cliCert, cliKey, cliCA []byte) (tls.ConnectionState, error, error) {
	t.Helper()
	sc, err := tls.X509KeyPair(srvCert, srvKey)
	if err != nil {
		t.Fatal(err)
	}
	cc, err := tls.X509KeyPair(cliCert, cliKey)
	if err != nil {
		t.Fatal(err)
	}
	sp, cp := x509.NewCertPool(), x509.NewCertPool()
	sp.AppendCertsFromPEM(srvCA)
	cp.AppendCertsFromPEM(cliCA)
	a, b := net.Pipe()
	srv := tls.Server(a, &tls.Config{Certificates: []tls.Certificate{sc}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: sp, MinVersion: tls.VersionTLS13})
	cli := tls.Client(b, &tls.Config{Certificates: []tls.Certificate{cc}, RootCAs: cp, ServerName: "srv", MinVersion: tls.VersionTLS13})
	sch, cch := make(chan error, 1), make(chan error, 1)
	go func() { e := srv.Handshake(); a.Close(); sch <- e }()
	go func() { e := cli.Handshake(); b.Close(); cch <- e }()
	se, ce := <-sch, <-cch
	return srv.ConnectionState(), se, ce
}

func TestPeerIdentity(t *testing.T) {
	caCert, caKey, _ := newCA("c1")
	srvCert, srvKey := issueNode(t, caCert, caKey, "srv", "node")
	cliCert, cliKey := issueNode(t, caCert, caKey, "zeuge-1", "witness")

	cs, se, ce := handshake(t, srvCert, srvKey, caCert, cliCert, cliKey, caCert)
	if se != nil || ce != nil {
		t.Fatalf("Handshake: %v / %v", se, ce)
	}
	id, kind, err := peerIdentity(cs, nil)
	if err != nil || id != "zeuge-1" || kind != "witness" {
		t.Fatalf("%q %q %v", id, kind, err)
	}
	ser := serialHex(parseCert(t, cliCert))
	if _, _, err := peerIdentity(cs, []string{"00", ser}); err == nil || err.Error() != "Zertifikat gesperrt" {
		t.Fatalf("gesperrt: %v", err)
	}

	// fremde CA scheitert im Handshake
	otherCert, otherKey, _ := newCA("c2")
	fCert, fKey := issueNode(t, otherCert, otherKey, "evil", "node")
	_, se, _ = handshake(t, srvCert, srvKey, caCert, fCert, fKey, caCert)
	if se == nil {
		t.Fatal("fremdes Zertifikat akzeptiert")
	}

	// ohne Zertifikat
	if _, _, err := peerIdentity(tls.ConnectionState{}, nil); err == nil {
		t.Fatal("ohne Zertifikat akzeptiert")
	}
}

func TestPeerIdentityUnverified(t *testing.T) {
	caCert, caKey, _ := newCA("c1")
	cert, _ := issueNode(t, caCert, caKey, "pdh-a", "node")
	cs := tls.ConnectionState{PeerCertificates: []*x509.Certificate{parseCert(t, cert)}}
	if _, _, err := peerIdentity(cs, nil); err == nil || err.Error() != "Zertifikat nicht von der Cluster-CA" {
		t.Fatalf("unverifiziert: %v", err)
	}
}

func TestPeerIdentityRevokedNormalized(t *testing.T) {
	caCert, caKey, _ := newCA("c1")
	cert, key := issueNode(t, caCert, caKey, "pdh-a", "node")
	srvCert, srvKey := issueNode(t, caCert, caKey, "srv", "node")
	cs, se, ce := handshake(t, srvCert, srvKey, caCert, cert, key, caCert)
	if se != nil || ce != nil {
		t.Fatal(se, ce)
	}
	ser := strings.ToUpper(serialHex(parseCert(t, cert)))
	if _, _, err := peerIdentity(cs, []string{"000" + ser}); err == nil || err.Error() != "Zertifikat gesperrt" {
		t.Fatalf("normalisiert: %v", err)
	}
}

func TestSignCSRBadCN(t *testing.T) {
	caCert, caKey, _ := newCA("c1")
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	for _, cn := range []string{"../x", "-a", "", "A"} {
		der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, key)
		if err != nil {
			t.Fatal(err)
		}
		csr := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
		if _, err := signCSR(caCert, caKey, csr, "node", 730); err == nil {
			t.Errorf("CN %q akzeptiert", cn)
		}
	}
}

func TestSignCSRDays(t *testing.T) {
	caCert, caKey, _ := newCA("c1")
	_, csr, _ := newKeyCSR("pdh-a", "node")
	for _, d := range []int{0, -5} {
		if _, err := signCSR(caCert, caKey, csr, "node", d); err == nil {
			t.Errorf("days %d akzeptiert", d)
		}
	}
	c, err := signCSR(caCert, caKey, csr, "node", 100000)
	if err != nil {
		t.Fatal(err)
	}
	if parseCert(t, c).NotAfter.After(parseCert(t, caCert).NotAfter) {
		t.Fatal("Laufzeit über CA hinaus")
	}
}
