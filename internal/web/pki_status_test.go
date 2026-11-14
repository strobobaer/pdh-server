package web

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func pkiTestCA(t *testing.T, app ...string) *pkiCA {
	t.Helper()
	name := "PDH Test"
	if len(app) > 0 {
		name = app[0]
	}
	ca, _, err := newCA(name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func pkiTestServer(t *testing.T, ca *pkiCA, ask bool) string {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	if ask {
		pool := x509.NewCertPool()
		pool.AddCert(ca.Cert)
		srv.TLS = &tls.Config{ClientAuth: tls.RequestClientCert, ClientCAs: pool}
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

func TestPKIProbe(t *testing.T) {
	ca := pkiTestCA(t)
	ctx := context.Background()

	if st := pkiProbe(ctx, pkiTestServer(t, ca, true), ca.Cert.RawSubject); st.State != pkiEnforceActive {
		t.Errorf("mit Zertifikatsanforderung: %+v", st)
	}
	if st := pkiProbe(ctx, pkiTestServer(t, ca, false), ca.Cert.RawSubject); st.State != pkiEnforceOff {
		t.Errorf("ohne Zertifikatsanforderung: %+v", st)
	}
	// Server nennt eine andere CA
	other := pkiTestCA(t, "Fremde CA")
	if st := pkiProbe(ctx, pkiTestServer(t, other, true), ca.Cert.RawSubject); st.State != pkiEnforceOff || st.Reason == "" {
		t.Errorf("andere CA: %+v", st)
	}
	// nicht erreichbar
	srv := httptest.NewUnstartedServer(nil)
	srv.StartTLS()
	addr := srv.Listener.Addr().String()
	srv.Close()
	if st := pkiProbe(ctx, addr, ca.Cert.RawSubject); st.State != pkiEnforceUnknown || st.Reason == "" {
		t.Errorf("nicht erreichbar: %+v", st)
	}
}

func TestPKIEnforcementFor(t *testing.T) {
	ca := pkiTestCA(t)
	subj := func() ([]byte, error) { return ca.Cert.RawSubject, nil }
	if st := pkiEnforcementFor(context.Background(), "", subj); st.State != pkiEnforceUnknown || !strings.Contains(st.Reason, "nicht eingerichtet") {
		t.Errorf("ohne Adresse: %+v", st)
	}
	if st := pkiEnforcementFor(context.Background(), "http://cloud.firma.de", subj); st.State != pkiEnforceUnknown {
		t.Errorf("http: %+v", st)
	}
	addr := pkiTestServer(t, ca, true)
	st := pkiEnforcementFor(context.Background(), "https://"+addr, subj)
	if st.State != pkiEnforceActive {
		t.Fatalf("aktiv erwartet: %+v", st)
	}
	// zweiter Aufruf kommt aus dem Zwischenspeicher
	if st2 := pkiEnforcementFor(context.Background(), "https://"+addr, subj); st2 != st {
		t.Errorf("Zwischenspeicher: %+v", st2)
	}
}

func TestPKIEnforceHTML(t *testing.T) {
	cases := []struct {
		st   pkiEnforceStatus
		want []string
	}{
		{pkiEnforceStatus{State: pkiEnforceActive}, []string{"Zertifikatspflicht aktiv – nginx fragt nach PDH-Zertifikaten"}},
		{pkiEnforceStatus{State: pkiEnforceOff}, []string{
			"Zertifikatspflicht nicht eingerichtet – Nextcloud fragt kein Zertifikat ab; die .p12 hat so keine Wirkung",
			"sudo bash scripts/configure-nextcloud-client-certs.sh --site /etc/nginx/sites-available/nextcloud.conf",
			"auf beiden Servern", "--exempt"}},
		{pkiEnforceStatus{State: pkiEnforceUnknown, Reason: "Nextcloud ist nicht eingerichtet"}, []string{"Zertifikatspflicht nicht prüfbar", "Nextcloud ist nicht eingerichtet"}},
	}
	for _, c := range cases {
		out := pkiEnforceHTML(c.st)
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: %q fehlt in %s", c.st.State, w, out)
			}
		}
	}
	if strings.Contains(pkiEnforceHTML(pkiEnforceStatus{State: pkiEnforceActive}), "sudo") {
		t.Error("Befehl bei aktiver Pflicht")
	}
}

func TestPKIServerHint(t *testing.T) {
	std := pkiServerHint("https://cloud.firma.de", nil)
	for _, w := range []string{"Für die Adresse cloud.firma.de gilt das Let's-Encrypt-Zertifikat aus dem Reiter Cloudflare-Tunnel.", "nextcloud.local"} {
		if !strings.Contains(std, w) {
			t.Errorf("Standardtext ohne %q: %s", w, std)
		}
	}
	rows := []certRow{
		{Name: "pdh.firma.de", NotAfter: time.Now().Add(24 * time.Hour), ChainPEM: "x"},
		{Name: "cloud.firma.de", NotAfter: time.Now().Add(24 * time.Hour), ChainPEM: "x"},
	}
	if got := pkiServerHint("https://cloud.firma.de", rows); got != "Nextcloud nutzt bereits das Let's-Encrypt-Zertifikat für cloud.firma.de." {
		t.Errorf("mit Zertifikat: %s", got)
	}
	// Zeile ohne ausgestelltes Zertifikat zählt nicht
	if got := pkiServerHint("https://cloud.firma.de", []certRow{{Name: "cloud.firma.de"}}); !strings.Contains(got, "Für die Adresse") {
		t.Errorf("ohne ausgestelltes Zertifikat: %s", got)
	}
}

func TestPKIPageServerHintRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	out := renderPage(t, tmpl, "pki", PKIPageData{CAName: "CA", ServerHint: "Nextcloud nutzt bereits das Let's-Encrypt-Zertifikat für cloud.firma.de."})
	for _, w := range []string{"Nextcloud nutzt bereits das Let&#39;s-Encrypt-Zertifikat für cloud.firma.de.", `hx-get="/account/certificates/enforcement"`} {
		if !strings.Contains(out, w) {
			t.Errorf("Zertifikatsseite ohne %q", w)
		}
	}
	ud := UserDetailData{User: UserView{ID: "u1", Username: "toni"}, Master: userMaster{Language: "de"}, IsSelf: true, CanSelfCert: true}
	if !strings.Contains(renderPage(t, tmpl, "user_detail", ud), `hx-get="/account/certificates/enforcement"`) {
		t.Error("Mein Konto lädt die Anzeige nicht")
	}
}
