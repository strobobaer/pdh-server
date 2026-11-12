package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

// calls hält die Reihenfolge der Aufrufe über CA und DNS hinweg fest.
type calls struct{ log []string }

func (c *calls) add(s string) { c.log = append(c.log, s) }

func (c *calls) count(s string) int {
	n := 0
	for _, l := range c.log {
		if l == s {
			n++
		}
	}
	return n
}

type fakeOrder struct {
	c           *calls
	name        string
	acceptErr   error
	finalizeErr error
}

func (o *fakeOrder) Challenge() (string, string) { return "_acme-challenge." + o.name, "wert-123" }

func (o *fakeOrder) Accept(ctx context.Context) error {
	o.c.add("Accept")
	return o.acceptErr
}

func (o *fakeOrder) Finalize(ctx context.Context, csrDER []byte) ([]byte, error) {
	o.c.add("Finalize")
	if o.finalizeErr != nil {
		return nil, o.finalizeErr
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, err
	}
	if len(csr.DNSNames) != 1 || csr.DNSNames[0] != o.name {
		return nil, errors.New("CSR hat falsche Namen")
	}
	// Selbstsigniertes Blatt-Zertifikat mit dem Schlüssel der CSR als Ersatz für die CA.
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: o.name},
		DNSNames:     []string{o.name},
		NotBefore:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, csr.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

type fakeCA struct {
	c          *calls
	order      *fakeOrder
	newOrderEr error
}

func (f *fakeCA) NewOrder(ctx context.Context, name string) (Order, error) {
	f.c.add("NewOrder")
	if f.newOrderEr != nil {
		return nil, f.newOrderEr
	}
	f.order.name = name
	return f.order, nil
}

func (f *fakeCA) Revoke(ctx context.Context, certPEM []byte) error { return nil }

type fakeDNS struct {
	c          *calls
	presentErr error
	visible    bool
	visibleErr error
}

func (d *fakeDNS) Present(ctx context.Context, fqdn, value string) (string, error) {
	d.c.add("Present")
	if d.presentErr != nil {
		return "", d.presentErr
	}
	return "eintrag-1", nil
}

func (d *fakeDNS) CleanUp(ctx context.Context, id string) error {
	d.c.add("CleanUp")
	return nil
}

func (d *fakeDNS) Visible(ctx context.Context, fqdn, value string) (bool, error) {
	d.c.add("Visible")
	return d.visible, d.visibleErr
}

func newFakes() (*calls, *fakeCA, *fakeDNS) {
	c := &calls{}
	return c, &fakeCA{c: c, order: &fakeOrder{c: c}}, &fakeDNS{c: c, visible: true}
}

func noWait(ctx context.Context, d time.Duration) error { return nil }

func TestIssueHappyPath(t *testing.T) {
	c, ca, dns := newFakes()
	var steps []string
	got, err := Issue(context.Background(), ca, dns, "nc.firma.de", func(s string) { steps = append(steps, s) }, noWait)
	if err != nil {
		t.Fatal(err)
	}
	if want := "NewOrder,Present,Visible,Accept,Finalize,CleanUp"; strings.Join(c.log, ",") != want {
		t.Errorf("Reihenfolge = %v, erwartet %s", c.log, want)
	}
	if want := "auftrag,dns,warten,pruefung,abholen"; strings.Join(steps, ",") != want {
		t.Errorf("Schritte = %v, erwartet %s", steps, want)
	}
	blk, _ := pem.Decode(got.KeyPEM)
	if blk == nil {
		t.Fatal("KeyPEM ist kein PEM")
	}
	key, err := x509.ParseECPrivateKey(blk.Bytes)
	if err != nil {
		t.Fatalf("KeyPEM ist kein EC-Schlüssel: %v", err)
	}
	if key.Curve != elliptic.P256() {
		t.Errorf("Kurve = %v, erwartet P-256", key.Curve.Params().Name)
	}
	if len(got.ChainPEM) == 0 || got.Fingerprint == "" || got.NotAfter.IsZero() || got.NotBefore.IsZero() {
		t.Errorf("Issued unvollständig: %+v", got)
	}
}

func TestIssueCleansUpOnEveryFailure(t *testing.T) {
	boom := errors.New("kaputt")
	cases := []struct {
		name    string
		setup   func(*fakeCA, *fakeDNS)
		cleanup int
	}{
		{"Present", func(ca *fakeCA, d *fakeDNS) { d.presentErr = boom }, 0},
		{"Visible", func(ca *fakeCA, d *fakeDNS) { d.visibleErr = boom }, 1},
		{"Accept", func(ca *fakeCA, d *fakeDNS) { ca.order.acceptErr = boom }, 1},
		{"Finalize", func(ca *fakeCA, d *fakeDNS) { ca.order.finalizeErr = boom }, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, ca, dns := newFakes()
			tc.setup(ca, dns)
			_, err := Issue(context.Background(), ca, dns, "nc.firma.de", func(string) {}, noWait)
			if !errors.Is(err, boom) {
				t.Fatalf("Fehler = %v, erwartet durchgereicht", err)
			}
			if n := c.count("CleanUp"); n != tc.cleanup {
				t.Errorf("CleanUp %d-mal, erwartet %d", n, tc.cleanup)
			}
		})
	}
}

func TestIssueGivesUpAfterTwoMinutes(t *testing.T) {
	c, ca, dns := newFakes()
	dns.visible = false
	var total time.Duration
	wait := func(ctx context.Context, d time.Duration) error { total += d; return nil }
	_, err := Issue(context.Background(), ca, dns, "nc.firma.de", func(string) {}, wait)
	if err == nil || !strings.Contains(err.Error(), "nicht sichtbar") {
		t.Fatalf("Fehler = %v, erwartet \"nicht sichtbar\"", err)
	}
	if total != 2*time.Minute {
		t.Errorf("Wartezeit = %v, erwartet 2m", total)
	}
	if c.count("CleanUp") != 1 {
		t.Errorf("CleanUp %d-mal, erwartet 1", c.count("CleanUp"))
	}
	if c.count("Accept") != 0 {
		t.Error("Accept darf nicht erfolgen")
	}
}

func TestIssuePassesRateLimit(t *testing.T) {
	c, ca, dns := newFakes()
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ca.newOrderEr = &RateLimited{Until: t0, Msg: "zu viele"}
	_, err := Issue(context.Background(), ca, dns, "nc.firma.de", func(string) {}, noWait)
	var rl *RateLimited
	if !errors.As(err, &rl) || !rl.Until.Equal(t0) {
		t.Fatalf("Fehler = %v, erwartet RateLimited bis %v", err, t0)
	}
	if c.count("Present") != 0 {
		t.Error("Present darf nicht erfolgen")
	}
}

func TestRenewAt(t *testing.T) {
	nb := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// 90 Tage Laufzeit → 30 Tage vor Ablauf
	if got := RenewAt(nb, nb.AddDate(0, 0, 90)); !got.Equal(nb.AddDate(0, 0, 60)) {
		t.Errorf("90 Tage: %v", got)
	}
	// 6 Tage Laufzeit → ein Drittel vor Ablauf (2 Tage)
	if got := RenewAt(nb, nb.AddDate(0, 0, 6)); !got.Equal(nb.AddDate(0, 0, 4)) {
		t.Errorf("6 Tage: %v", got)
	}
}

func TestNormalizeName(t *testing.T) {
	if got, err := NormalizeName("NC.Firma.de."); err != nil || got != "nc.firma.de" {
		t.Errorf("NormalizeName = %q, %v", got, err)
	}
	for _, bad := range []string{"*.firma.de", "", "a b.de", "firma", "-x.firma.de"} {
		if got, err := NormalizeName(bad); err == nil {
			t.Errorf("%q: erwartet Fehler, bekam %q", bad, got)
		}
	}
}
