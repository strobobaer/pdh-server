package web

import (
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

func TestPKIIssueAndP12(t *testing.T) {
	now := time.Now()
	ca, keyPEM, err := newCA("Strobl", now)
	if err != nil {
		t.Fatal(err)
	}
	if !ca.Cert.IsCA || ca.Cert.Subject.CommonName != "Strobl Zertifizierungsstelle" || len(keyPEM) == 0 {
		t.Fatalf("CA falsch: %+v", ca.Cert.Subject)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)

	for _, tc := range []struct {
		req pkiRequest
		eku x509.ExtKeyUsage
	}{
		{pkiRequest{Kind: "client", CommonName: "Anton Pröbstl (Handy)", Email: "anton@firma.de", Days: 730}, x509.ExtKeyUsageClientAuth},
		{pkiRequest{Kind: "device", CommonName: "Firmenhandys", Days: 365}, x509.ExtKeyUsageClientAuth},
		{pkiRequest{Kind: "server", CommonName: "cloud.firma.de", Hostnames: []string{"cloud.firma.de", "192.168.1.10"}, Days: 5000}, x509.ExtKeyUsageServerAuth},
	} {
		is, err := pkiIssue(ca, tc.req, now)
		if err != nil {
			t.Fatalf("%s: %v", tc.req.Kind, err)
		}
		key, cert, chain, err := pkcs12.DecodeChain(is.P12, is.Password)
		if err != nil {
			t.Fatalf("%s: p12 nicht lesbar: %v", tc.req.Kind, err)
		}
		if key == nil || len(chain) != 1 || !chain[0].Equal(ca.Cert) {
			t.Errorf("%s: Schlüssel oder CA fehlt in der p12", tc.req.Kind)
		}
		if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{tc.eku}, CurrentTime: now}); err != nil {
			t.Errorf("%s: Kette ungültig: %v", tc.req.Kind, err)
		}
		if _, _, _, err := pkcs12.DecodeChain(is.P12, "falsch"); err == nil {
			t.Errorf("%s: falsches Passwort angenommen", tc.req.Kind)
		}
		if hex.EncodeToString(cert.SerialNumber.Bytes()) != is.Serial {
			t.Errorf("%s: Seriennummer passt nicht", tc.req.Kind)
		}
		if tc.req.Kind == "server" {
			if cert.VerifyHostname("cloud.firma.de") != nil || cert.VerifyHostname("192.168.1.10") != nil {
				t.Error("Server: Name/IP fehlt")
			}
			if d := cert.NotAfter.Sub(now); d > (pkiServerDays+1)*24*time.Hour {
				t.Errorf("Server länger als %d Tage gültig: %v", pkiServerDays, d)
			}
		}
		if tc.req.Kind == "client" && (len(cert.EmailAddresses) != 1 || cert.EmailAddresses[0] != "anton@firma.de") {
			t.Error("Client: E-Mail fehlt")
		}
	}
	if _, err := pkiIssue(ca, pkiRequest{Kind: "server", CommonName: "x"}, now); err == nil {
		t.Error("Server ohne Namen ausgestellt")
	}
	if _, err := pkiIssue(ca, pkiRequest{Kind: "root", CommonName: "x"}, now); err == nil {
		t.Error("unbekannte Art ausgestellt")
	}

	// Sperrliste
	crlPEM, err := pkiBuildCRL(ca, []x509.RevocationListEntry{{SerialNumber: big.NewInt(4711), RevocationTime: now}}, now)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := pem.Decode(crlPEM)
	crl, err := x509.ParseRevocationList(b.Bytes)
	if err != nil || crl.CheckSignatureFrom(ca.Cert) != nil {
		t.Fatalf("CRL ungültig: %v", err)
	}
	if len(crl.RevokedCertificateEntries) != 1 || crl.RevokedCertificateEntries[0].SerialNumber.Int64() != 4711 || crl.NextUpdate.Before(now.AddDate(0, 0, 7)) {
		t.Error("CRL-Inhalt falsch")
	}

	// CA aus gespeichertem PEM wieder laden
	if again, err := parseCA(ca.CertPEM, keyPEM); err != nil || !again.Cert.Equal(ca.Cert) {
		t.Error("CA nicht wieder ladbar")
	}
}

func TestPKIHostnames(t *testing.T) {
	got, err := pkiParseHostnames("https://Cloud.Firma.de/, 192.168.1.10\nnextcloud.local;cloud.firma.de *.firma.de")
	if err != nil || strings.Join(got, "|") != "cloud.firma.de|192.168.1.10|nextcloud.local|*.firma.de" {
		t.Errorf("= %v, %v", got, err)
	}
	for _, bad := range []string{"cloud firma$.de", "-x.de", "a..b"} {
		if _, err := pkiParseHostnames(bad); err == nil {
			t.Errorf("%q angenommen", bad)
		}
	}
	if pw := pkiPassword(); len(pw) != 19 || strings.ContainsAny(pw, "0O1lI") {
		t.Errorf("Passwort %q", pw)
	}
}

func TestPKIPagesRender(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := PKIPageData{CAName: "PDH Zertifizierungsstelle", CAValid: "19.10.2036", CAPrint: "AB:CD", SelfService: true, NewID: "c1",
		Certs: []pkiCertRow{
			{ID: "c1", Kind: "client", KindLabel: pkiKinds["client"], CommonName: "Anton (Handy)", Password: "abcd-efgh-ijkm-npqr"},
			{ID: "c2", Kind: "server", KindLabel: pkiKinds["server"], CommonName: "cloud.firma.de", Hostnames: "cloud.firma.de"},
			{ID: "c3", Kind: "device", CommonName: "Alt", Revoked: true},
		}}
	out := renderPage(t, tmpl, "pki", d)
	checkScripts(t, "pki", out)
	for _, want := range []string{"/pki/ca.crt", "/pki/crl.pem", `href="/certificates/c1/p12"`, "abcd-efgh-ijkm-npqr", `href="/certificates/c2/key.pem"`,
		`href="/certificates/c2/fullchain.pem"`, `href="/certificates/c3/cert.pem"`, `action="/certificates/c1/revoke"`, `name="kind" value="server"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Zertifikatsseite enthält %q nicht", want)
		}
	}
	if strings.Contains(out, `href="/certificates/c3/p12"`) {
		t.Error("gesperrtes Zertifikat noch herunterladbar")
	}

	ud := UserDetailData{User: UserView{ID: "u1", Username: "toni"}, Master: userMaster{Language: "de"}, IsSelf: true, CanSelfCert: true,
		MyCerts: []pkiCertRow{{ID: "c1", Kind: "client", Label: "Handy"}}}
	out = renderPage(t, tmpl, "user_detail", ud)
	for _, want := range []string{"Zertifikat für die Nextcloud-App", `action="/account/certificates"`, `href="/certificates/c1/p12"`, `hx-get="/certificates/c1/password"`} {
		if !strings.Contains(out, want) {
			t.Errorf("Mein Konto enthält %q nicht", want)
		}
	}
}
