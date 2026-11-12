package web

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"pdh/internal/integrations/acme"
	"pdh/internal/integrations/cloudflare"
)

// ── Ersatz im Speicher ───────────────────────────────────────

type memCertStore struct {
	mu   sync.Mutex
	rows map[string]*certRow
	keys map[string][]byte
}

func newMemCertStore(rows ...certRow) *memCertStore {
	s := &memCertStore{rows: map[string]*certRow{}, keys: map[string][]byte{}}
	for i := range rows {
		r := rows[i]
		s.rows[r.Name] = &r
	}
	return s
}

func (s *memCertStore) get(name string) certRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.rows[name]; ok {
		return *r
	}
	return certRow{}
}

func (s *memCertStore) row(name string) (*certRow, error) {
	r, ok := s.rows[name]
	if !ok {
		return nil, errors.New("Zertifikat nicht gefunden")
	}
	return r, nil
}

func (s *memCertStore) certList(context.Context) ([]certRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []certRow
	for _, r := range s.rows {
		out = append(out, *r)
	}
	return out, nil
}

func (s *memCertStore) certAdd(_ context.Context, name string, staging bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[name] = &certRow{Name: name, Staging: staging, State: certStateNeu}
	return nil
}

func (s *memCertStore) certSetStep(_ context.Context, name, state, step, lastErr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.row(name)
	if err != nil {
		return err
	}
	r.State, r.Step, r.LastError = state, step, lastErr
	return nil
}

func (s *memCertStore) certSetChallenge(_ context.Context, name, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.row(name)
	if err != nil {
		return err
	}
	r.ChallengeID = id
	return nil
}

func (s *memCertStore) certSetInstalled(_ context.Context, name, fp string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.row(name)
	if err != nil {
		return err
	}
	r.InstalledFP = fp
	return nil
}

func (s *memCertStore) certSetRetry(_ context.Context, name string, until time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.row(name)
	if err != nil {
		return err
	}
	r.RetryAfter = until
	return nil
}

func (s *memCertStore) certSetLANRecord(_ context.Context, name, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.row(name)
	if err != nil {
		return err
	}
	r.LANRecordID = id
	return nil
}

func (s *memCertStore) certSaveIssued(_ context.Context, name string, is *acme.Issued) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.row(name)
	if err != nil {
		return err
	}
	r.ChainPEM, r.NotBefore, r.NotAfter, r.Fingerprint = string(is.ChainPEM), is.NotBefore, is.NotAfter, is.Fingerprint
	r.State, r.Step, r.LastError, r.RetryAfter, r.ChallengeID = certStateGueltig, "", "", time.Time{}, ""
	s.keys[name] = is.KeyPEM
	return nil
}

func (s *memCertStore) certKey(_ context.Context, name string) ([]byte, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.row(name)
	if err != nil {
		return nil, nil, err
	}
	if r.ChainPEM == "" || s.keys[name] == nil {
		return nil, nil, errors.New("noch kein Zertifikat ausgestellt")
	}
	return []byte(r.ChainPEM), s.keys[name], nil
}

func (s *memCertStore) certDelete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, name)
	return nil
}

// certFakeOrder stellt ein selbst signiertes Zertifikat aus.
type certFakeOrder struct {
	ca   *certFakeCA
	name string
}

func (o *certFakeOrder) Challenge() (string, string) { return "_acme-challenge." + o.name, "wert" }

func (o *certFakeOrder) Accept(context.Context) error { return o.ca.acceptErr }

func (o *certFakeOrder) Finalize(ctx context.Context, csrDER []byte) ([]byte, error) {
	if o.ca.inFinalize != nil {
		o.ca.inFinalize <- struct{}{}
		<-o.ca.release
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, err
	}
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(7),
		Subject:      pkix.Name{CommonName: o.name},
		DNSNames:     []string{o.name},
		NotBefore:    o.ca.notBefore,
		NotAfter:     o.ca.notBefore.Add(90 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, csr.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

type certFakeCA struct {
	mu         sync.Mutex
	orders     int
	acceptErr  error
	orderErr   error
	notBefore  time.Time
	inFinalize chan struct{}
	release    chan struct{}
}

func (c *certFakeCA) NewOrder(_ context.Context, name string) (acme.Order, error) {
	c.mu.Lock()
	c.orders++
	c.mu.Unlock()
	if c.orderErr != nil {
		return nil, c.orderErr
	}
	return &certFakeOrder{ca: c, name: name}, nil
}

func (c *certFakeCA) Revoke(context.Context, []byte) error { return nil }

func (c *certFakeCA) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.orders
}

type certFakeDNS struct {
	mu      sync.Mutex
	present int
	cleaned []string
}

func (d *certFakeDNS) Present(context.Context, string, string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.present++
	return "rec-neu", nil
}

func (d *certFakeDNS) CleanUp(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cleaned = append(d.cleaned, id)
	return nil
}

func (d *certFakeDNS) Visible(context.Context, string, string) (bool, error) { return true, nil }

// certQuietLog schaltet das Protokoll für den Test stumm und stellt es danach wieder her.
func certQuietLog(t *testing.T) {
	old := log.Logger
	log.Logger = zerolog.Nop()
	t.Cleanup(func() { log.Logger = old })
}

// certTestEnv bündelt die Ersatzteile und zählt, was aufgerufen wurde.
type certTestEnv struct {
	store    *memCertStore
	ca       *certFakeCA
	dns      *certFakeDNS
	now      time.Time
	mu       sync.Mutex
	caCalls  int
	dnsCalls int
	installs []string
	statuses int
	alarms   []string
	settings map[string]string

	installErr error
	statusErr  error
	status     map[string]string
	blocked    bool
	caErr      error
	dnsErr     error
}

func newCertTestEnv(t *testing.T, rows ...certRow) *certTestEnv {
	t.Helper()
	certQuietLog(t)
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	return &certTestEnv{
		store:    newMemCertStore(rows...),
		ca:       &certFakeCA{notBefore: now.Add(-time.Hour)},
		dns:      &certFakeDNS{},
		now:      now,
		settings: map[string]string{},
	}
}

func (e *certTestEnv) deps() certDeps {
	return certDeps{
		store: e.store,
		ca: func(bool) (acme.CA, error) {
			e.mu.Lock()
			e.caCalls++
			e.mu.Unlock()
			if e.caErr != nil {
				return nil, e.caErr
			}
			return e.ca, nil
		},
		dns: func() (acme.DNS, error) {
			e.mu.Lock()
			e.dnsCalls++
			e.mu.Unlock()
			if e.dnsErr != nil {
				return nil, e.dnsErr
			}
			return e.dns, nil
		},
		install: func(_ context.Context, name string, chain, key []byte) (string, error) {
			e.mu.Lock()
			e.installs = append(e.installs, name)
			e.mu.Unlock()
			if e.installErr != nil {
				return "", e.installErr
			}
			return certTestFP(chain), nil
		},
		status: func(context.Context) (map[string]string, error) {
			e.mu.Lock()
			e.statuses++
			e.mu.Unlock()
			return e.status, e.statusErr
		},
		blocked: func() bool { return e.blocked },
		alarm: func(text string) {
			e.mu.Lock()
			e.alarms = append(e.alarms, text)
			e.mu.Unlock()
		},
		now: func() time.Time { return e.now },
		getSetting: func(_ context.Context, key string) string {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.settings[key]
		},
		setSetting: func(_ context.Context, key, value string) error {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.settings[key] = value
			return nil
		},
	}
}

// certTestFP berechnet den Fingerabdruck wie der Agent – in Großbuchstaben, damit der
// Vergleich ohne Rücksicht auf Groß-/Kleinschreibung geprüft ist.
func certTestFP(chain []byte) string {
	blk, _ := pem.Decode(chain)
	if blk == nil {
		return ""
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return ""
	}
	return strings.ToUpper(certFingerprint(c))
}

// certTestIssued: ein vorhandenes Zertifikat im Speicher (für Abgleich und Fehlerfälle).
func certTestIssued(t *testing.T, e *certTestEnv, name string, notBefore time.Time) *acme.Issued {
	t.Helper()
	e.ca.notBefore = notBefore
	ctx := context.Background()
	if e.store.get(name).Name == "" {
		_ = e.store.certAdd(ctx, name, false)
	}
	is, err := acme.Issue(ctx, e.ca, e.dns, name, nil, func(context.Context, time.Duration) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.certSaveIssued(ctx, name, is); err != nil {
		t.Fatal(err)
	}
	e.ca.notBefore = e.now.Add(-time.Hour)
	e.ca.orders, e.dns.present, e.dns.cleaned = 0, 0, nil
	return is
}

// ── Tests ────────────────────────────────────────────────────

// Erfolg: state "gueltig", installed_fp == Fingerprint, step leer.
func TestCertIssueStoresAndInstalls(t *testing.T) {
	e := newCertTestEnv(t, certRow{Name: "nc.firma.de", State: certStateNeu})
	if err := certIssue(context.Background(), e.deps(), "nc.firma.de"); err != nil {
		t.Fatal(err)
	}
	r := e.store.get("nc.firma.de")
	if r.State != certStateGueltig || r.Step != "" || r.LastError != "" {
		t.Fatalf("Zustand: %+v", r)
	}
	if r.Fingerprint == "" || r.InstalledFP != r.Fingerprint {
		t.Fatalf("installed_fp=%q fingerprint=%q", r.InstalledFP, r.Fingerprint)
	}
	if r.ChallengeID != "" {
		t.Fatalf("Prüf-Eintrag nicht vergessen: %q", r.ChallengeID)
	}
	if len(e.dns.cleaned) != 1 || e.dns.cleaned[0] != "rec-neu" {
		t.Fatalf("Prüf-Eintrag nicht gelöscht: %v", e.dns.cleaned)
	}
	if len(e.installs) != 1 || e.installs[0] != "nc.firma.de" {
		t.Fatalf("Agent: %v", e.installs)
	}
}

// Zweiter Aufruf während der erste in Finalize wartet → errCertBusy, CA nur einmal gefragt.
func TestCertIssueBusy(t *testing.T) {
	e := newCertTestEnv(t, certRow{Name: "busy.firma.de", State: certStateNeu})
	e.ca.inFinalize = make(chan struct{})
	e.ca.release = make(chan struct{})
	d := e.deps()
	done := make(chan error, 1)
	go func() { done <- certIssue(context.Background(), d, "busy.firma.de") }()
	<-e.ca.inFinalize
	if r := e.store.get("busy.firma.de"); r.State != certStateLaeuft {
		t.Errorf("während des Auftrags: state=%q", r.State)
	}
	if err := certIssue(context.Background(), d, "busy.firma.de"); !errors.Is(err, errCertBusy) {
		t.Errorf("zweiter Auftrag: %v", err)
	}
	close(e.ca.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := e.ca.count(); n != 1 {
		t.Fatalf("CA %d-mal gefragt", n)
	}
	// Danach ist der Name wieder frei.
	e.ca.inFinalize = nil
	if err := certIssue(context.Background(), d, "busy.firma.de"); err != nil {
		t.Fatalf("nach dem ersten Auftrag: %v", err)
	}
}

// Fehler in Accept: state "fehler", last_error gesetzt, bisheriges Zertifikat unverändert.
func TestCertIssueKeepsOldOnFailure(t *testing.T) {
	e := newCertTestEnv(t, certRow{Name: "nc.firma.de", State: certStateNeu})
	old := certTestIssued(t, e, "nc.firma.de", e.now.Add(-70*24*time.Hour))
	e.ca.acceptErr = errors.New("Let's Encrypt: Prüfung fehlgeschlagen")
	err := certIssue(context.Background(), e.deps(), "nc.firma.de")
	if err == nil {
		t.Fatal("Fehler erwartet")
	}
	r := e.store.get("nc.firma.de")
	if r.State != certStateFehler || !strings.Contains(r.LastError, "Prüfung fehlgeschlagen") {
		t.Fatalf("Zustand: state=%q last_error=%q", r.State, r.LastError)
	}
	if r.Fingerprint != old.Fingerprint || r.ChainPEM != string(old.ChainPEM) || !r.NotAfter.Equal(old.NotAfter) {
		t.Fatal("bisheriges Zertifikat verändert")
	}
	if len(e.installs) != 0 {
		t.Fatalf("Agent aufgerufen: %v", e.installs)
	}
	if len(e.dns.cleaned) != 1 {
		t.Fatalf("Prüf-Eintrag nicht gelöscht: %v", e.dns.cleaned)
	}
}

// RateLimited{Until}: retry_after == Until; certDue liefert den Namen vor Until nicht.
func TestCertIssueRateLimit(t *testing.T) {
	e := newCertTestEnv(t, certRow{Name: "nc.firma.de", State: certStateNeu})
	until := e.now.Add(3 * time.Hour)
	e.ca.orderErr = &acme.RateLimited{Until: until, Msg: "too many certificates"}
	err := certIssue(context.Background(), e.deps(), "nc.firma.de")
	var rl *acme.RateLimited
	if !errors.As(err, &rl) {
		t.Fatalf("Fehler: %v", err)
	}
	r := e.store.get("nc.firma.de")
	if !r.RetryAfter.Equal(until) || r.State != certStateFehler {
		t.Fatalf("retry_after=%v state=%q", r.RetryAfter, r.State)
	}
	rows, _ := e.store.certList(context.Background())
	if got := certDue(rows, until.Add(-time.Minute)); len(got) != 0 {
		t.Fatalf("vor Ablauf der Sperre fällig: %v", got)
	}
	if got := certDue(rows, until.Add(time.Minute)); len(got) != 1 {
		t.Fatalf("nach der Sperre nicht fällig: %v", got)
	}
	// Auch der Knopf fragt vor Ablauf der Sperre nicht nach.
	e.ca.orderErr = nil
	if err := certIssue(context.Background(), e.deps(), "nc.firma.de"); err == nil {
		t.Fatal("Auftrag trotz Sperrzeit")
	}
	if n := e.ca.count(); n != 1 {
		t.Fatalf("CA %d-mal gefragt", n)
	}
}

// install schlägt fehl: Zertifikat gespeichert, installed_fp leer, last_error beginnt mit "noch nicht eingerichtet".
func TestCertIssueAgentDown(t *testing.T) {
	e := newCertTestEnv(t, certRow{Name: "nc.firma.de", State: certStateNeu})
	e.installErr = errors.New("Update-Agent nicht erreichbar: connection refused")
	_ = certIssue(context.Background(), e.deps(), "nc.firma.de")
	r := e.store.get("nc.firma.de")
	if r.ChainPEM == "" || r.Fingerprint == "" {
		t.Fatal("Zertifikat nicht gespeichert")
	}
	if r.InstalledFP != "" {
		t.Fatalf("installed_fp=%q", r.InstalledFP)
	}
	if !strings.HasPrefix(r.LastError, "noch nicht eingerichtet") || !strings.Contains(r.LastError, "connection refused") {
		t.Fatalf("last_error=%q", r.LastError)
	}
	if r.State != certStateGueltig {
		t.Fatalf("state=%q", r.State)
	}
}

// gültig bis in 29 Tagen → fällig; 31 Tage → nicht; state "laeuft" → nicht; nie geholt → fällig.
func TestCertDue(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	cert := func(name string, left time.Duration) certRow {
		na := now.Add(left)
		return certRow{Name: name, State: certStateGueltig, ChainPEM: "x", Fingerprint: "ab", NotBefore: na.Add(-90 * 24 * time.Hour), NotAfter: na}
	}
	laeuft := cert("laeuft.firma.de", 29*24*time.Hour)
	laeuft.State = certStateLaeuft
	rows := []certRow{
		cert("bald.firma.de", 29*24*time.Hour),
		cert("spaeter.firma.de", 31*24*time.Hour),
		laeuft,
		{Name: "neu.firma.de", State: certStateNeu},
		{Name: "fehler.firma.de", State: certStateFehler, LastError: "x"},
	}
	got := certDue(rows, now)
	want := map[string]bool{"bald.firma.de": true, "neu.firma.de": true, "fehler.firma.de": true}
	if len(got) != len(want) {
		t.Fatalf("fällig: %v", got)
	}
	for _, n := range got {
		if !want[n] {
			t.Fatalf("fällig: %v", got)
		}
	}
}

// 15 Tage → kein Alarm; 14 Tage → "läuft in 14 Tagen ab"; abgelaufen → "ist abgelaufen".
func TestCertAlarm(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	row := func(left time.Duration) certRow {
		return certRow{Name: "nc.firma.de", State: certStateGueltig, ChainPEM: "x", NotBefore: now.Add(-60 * 24 * time.Hour), NotAfter: now.Add(left)}
	}
	if txt, ok := certAlarm(row(15*24*time.Hour), now); ok {
		t.Fatalf("15 Tage: %q", txt)
	}
	if txt, ok := certAlarm(row(14*24*time.Hour), now); !ok || !strings.Contains(txt, "läuft in 14 Tagen ab") || !strings.Contains(txt, "nc.firma.de") {
		t.Fatalf("14 Tage: %q %v", txt, ok)
	}
	if txt, ok := certAlarm(row(-time.Hour), now); !ok || !strings.Contains(txt, "ist abgelaufen") {
		t.Fatalf("abgelaufen: %q %v", txt, ok)
	}
	// Ohne Zertifikat (nie geholt) gibt es keinen Alarm.
	if txt, ok := certAlarm(certRow{Name: "neu.firma.de", State: certStateNeu}, now); ok {
		t.Fatalf("ohne Zertifikat: %q", txt)
	}
}

// Alarme: einmal bei 14 Tagen, ab 7 Tagen täglich, bei Ablauf sofort – ein zweiter Lauf am
// selben Tag (z. B. nach einem Neustart) meldet nicht doppelt.
func TestCertDailyAlarmRhythm(t *testing.T) {
	e := newCertTestEnv(t)
	is := certTestIssued(t, e, "nc.firma.de", e.now.Add(-76*24*time.Hour)) // läuft in 14 Tagen ab
	e.status = map[string]string{"nc.firma.de": is.Fingerprint}
	_ = e.store.certSetInstalled(context.Background(), "nc.firma.de", is.Fingerprint)
	_ = e.store.certSetRetry(context.Background(), "nc.firma.de", is.NotAfter.Add(30*24*time.Hour)) // keine Erneuerung im Test
	d := e.deps()
	run := func(at time.Time) int {
		e.now = at
		before := len(e.alarms)
		certDaily(context.Background(), d)
		return len(e.alarms) - before
	}
	start := e.now
	if n := run(start); n != 1 {
		t.Fatalf("14 Tage: %d Alarme", n)
	}
	if n := run(start.Add(time.Hour)); n != 0 {
		t.Fatalf("selber Tag: %d Alarme", n)
	}
	if n := run(start.Add(24 * time.Hour)); n != 0 {
		t.Fatalf("13 Tage: %d Alarme", n)
	}
	if n := run(start.Add(7 * 24 * time.Hour)); n != 1 {
		t.Fatalf("7 Tage: %d Alarme", n)
	}
	if n := run(start.Add(7*24*time.Hour + 2*time.Hour)); n != 0 {
		t.Fatalf("7 Tage, zweiter Lauf: %d Alarme", n)
	}
	if n := run(start.Add(8 * 24 * time.Hour)); n != 1 {
		t.Fatalf("6 Tage: %d Alarme", n)
	}
	if n := run(is.NotAfter.Add(time.Minute)); n != 1 || !strings.Contains(e.alarms[len(e.alarms)-1], "ist abgelaufen") {
		t.Fatalf("abgelaufen: %d Alarme %v", n, e.alarms)
	}
}

// blocked()==true (Spiegel oder Wartung): keine CA-Anfrage, kein Agent-Aufruf.
func TestCertDailySkipsWhenBlocked(t *testing.T) {
	e := newCertTestEnv(t, certRow{Name: "nc.firma.de", State: certStateNeu}, certRow{Name: "alt.firma.de", State: certStateLaeuft})
	e.blocked = true
	certDaily(context.Background(), e.deps())
	if e.caCalls != 0 || e.dnsCalls != 0 || len(e.installs) != 0 || e.statuses != 0 || len(e.alarms) != 0 {
		t.Fatalf("ca=%d dns=%d installs=%v status=%d alarme=%v", e.caCalls, e.dnsCalls, e.installs, e.statuses, e.alarms)
	}
	// Auch der Knopf startet nichts.
	if err := certIssue(context.Background(), e.deps(), "nc.firma.de"); err == nil || e.caCalls != 0 {
		t.Fatalf("Auftrag trotz Sperre: %v", err)
	}
	if r := e.store.get("alt.firma.de"); r.State != certStateLaeuft {
		t.Fatalf("am Spiegel verändert: %+v", r)
	}
}

// status meldet anderen Fingerabdruck → install mit dem Stand aus dem Speicher.
func TestCertDailyReinstallsOnMismatch(t *testing.T) {
	e := newCertTestEnv(t)
	is := certTestIssued(t, e, "nc.firma.de", e.now.Add(-10*24*time.Hour))
	_ = e.store.certSetInstalled(context.Background(), "nc.firma.de", is.Fingerprint)
	e.status = map[string]string{"nc.firma.de": "00ff"}
	var gotChain, gotKey []byte
	d := e.deps()
	d.install = func(_ context.Context, name string, chain, key []byte) (string, error) {
		e.installs = append(e.installs, name)
		gotChain, gotKey = chain, key
		return certTestFP(chain), nil
	}
	certDaily(context.Background(), d)
	if len(e.installs) != 1 || e.installs[0] != "nc.firma.de" {
		t.Fatalf("installs=%v", e.installs)
	}
	wantChain, wantKey, _ := e.store.certKey(context.Background(), "nc.firma.de")
	if !bytes.Equal(gotChain, wantChain) || !bytes.Equal(gotKey, wantKey) {
		t.Fatal("nicht der Stand aus dem Speicher installiert")
	}
	if e.caCalls != 0 {
		t.Fatalf("gültiges Zertifikat neu geholt (ca=%d)", e.caCalls)
	}
	// Stimmt der Fingerabdruck (andere Schreibweise), wird nichts installiert.
	e.installs = nil
	e.status = map[string]string{"nc.firma.de": strings.ToUpper(is.Fingerprint)}
	certDaily(context.Background(), d)
	if len(e.installs) != 0 {
		t.Fatalf("unnötig installiert: %v", e.installs)
	}
}

// Ohne Einträge (der Normalfall): keine CA-, DNS- oder Agent-Anfrage, kein Alarm und
// nichts im Protokoll oberhalb von debug.
func TestCertDailyNoRowsDoesNothing(t *testing.T) {
	e := newCertTestEnv(t)
	e.caErr = errors.New("darf nicht gefragt werden")
	e.dnsErr = errors.New("kein Cloudflare-Token")
	e.statusErr = errors.New("Der Update-Agent ist nicht eingerichtet")

	var buf bytes.Buffer
	log.Logger = zerolog.New(&buf).Level(zerolog.InfoLevel) // certQuietLog stellt zurück
	certDaily(context.Background(), e.deps())
	if e.caCalls != 0 || e.dnsCalls != 0 || e.statuses != 0 || len(e.installs) != 0 || len(e.alarms) != 0 {
		t.Fatalf("ca=%d dns=%d status=%d installs=%v alarme=%v", e.caCalls, e.dnsCalls, e.statuses, e.installs, e.alarms)
	}
	if buf.Len() != 0 {
		t.Fatalf("Protokoll: %s", buf.String())
	}
	// Gegenprobe: das Protokoll kommt wirklich im Puffer an.
	componentLog("zertifikate").Info().Msg("probe")
	if !strings.Contains(buf.String(), "probe") {
		t.Fatal("Protokoll wird nicht mitgeschnitten")
	}
}

// Fehlt das Cloudflare-Token oder ist der Agent nicht erreichbar, landet das beim Namen
// als last_error – kein Absturz, kein Alarm.
func TestCertDailyMissingCloudflareIsLastError(t *testing.T) {
	e := newCertTestEnv(t, certRow{Name: "nc.firma.de", State: certStateNeu})
	e.dnsErr = errors.New("Zuerst oben den API-Token eintragen und speichern")
	e.statusErr = errors.New("Der Update-Agent ist nicht eingerichtet")
	certDaily(context.Background(), e.deps())
	r := e.store.get("nc.firma.de")
	if r.State != certStateFehler || !strings.Contains(r.LastError, "API-Token") {
		t.Fatalf("Zustand: %+v", r)
	}
	if len(e.alarms) != 0 {
		t.Fatalf("Alarme: %v", e.alarms)
	}
}

// Ein Eintrag, der nach einem Absturz in "laeuft" hängt, gilt beim täglichen Lauf als
// unterbrochen und wird erneut geholt; ein laufender Auftrag bleibt unberührt.
func TestCertDailyResetsStuckJob(t *testing.T) {
	e := newCertTestEnv(t, certRow{Name: "nc.firma.de", State: certStateLaeuft, Step: "warten"})
	d := e.deps()
	d.install = func(context.Context, string, []byte, []byte) (string, error) {
		return "", errors.New("Agent fehlt")
	}
	certDaily(context.Background(), d)
	r := e.store.get("nc.firma.de")
	if r.State != certStateGueltig || r.Fingerprint == "" || e.ca.count() != 1 {
		t.Fatalf("nicht neu geholt: %+v orders=%d", r, e.ca.count())
	}

	// Hält ein laufender Auftrag den Namen, fasst der Lauf ihn nicht an.
	e2 := newCertTestEnv(t, certRow{Name: "lock.firma.de", State: certStateLaeuft, Step: "warten"})
	if !certTryLock("lock.firma.de") {
		t.Fatal("Sperre nicht frei")
	}
	defer certUnlock("lock.firma.de")
	certDaily(context.Background(), e2.deps())
	if r := e2.store.get("lock.firma.de"); r.State != certStateLaeuft || e2.caCalls != 0 {
		t.Fatalf("laufender Auftrag gestört: %+v ca=%d", r, e2.caCalls)
	}
}

// Ein gemerkter Prüf-Eintrag (Absturz zwischen Anlegen und Löschen) wird beim täglichen
// Lauf gelöscht; danach ist er vergessen.
func TestCertDailyCleansLeftoverChallenge(t *testing.T) {
	e := newCertTestEnv(t)
	is := certTestIssued(t, e, "nc.firma.de", e.now.Add(-10*24*time.Hour))
	_ = e.store.certSetInstalled(context.Background(), "nc.firma.de", is.Fingerprint)
	_ = e.store.certSetChallenge(context.Background(), "nc.firma.de", "rec-alt")
	e.status = map[string]string{"nc.firma.de": is.Fingerprint}
	certDaily(context.Background(), e.deps())
	if len(e.dns.cleaned) != 1 || e.dns.cleaned[0] != "rec-alt" {
		t.Fatalf("gelöscht: %v", e.dns.cleaned)
	}
	if r := e.store.get("nc.firma.de"); r.ChallengeID != "" {
		t.Fatalf("challenge_id=%q", r.ChallengeID)
	}
}

// Eine Panik in einer eingesetzten Funktion gibt die Sperre frei und hinterlässt "fehler".
func TestCertIssuePanicReleasesLock(t *testing.T) {
	e := newCertTestEnv(t, certRow{Name: "panik.firma.de", State: certStateNeu})
	d := e.deps()
	d.ca = func(bool) (acme.CA, error) { panic("kaputt") }
	if err := certIssue(context.Background(), d, "panik.firma.de"); err == nil {
		t.Fatal("Fehler erwartet")
	}
	if r := e.store.get("panik.firma.de"); r.State != certStateFehler {
		t.Fatalf("state=%q", r.State)
	}
	if !certTryLock("panik.firma.de") {
		t.Fatal("Sperre nach Panik nicht frei")
	}
	certUnlock("panik.firma.de")
}

func TestCertRole(t *testing.T) {
	cases := []struct{ name, host, nc, want string }{
		{"pdh.firma.de", "pdh.firma.de", "https://nc.firma.de", "pdh"},
		{"PDH.firma.de.", "pdh.firma.de:443", "", "pdh"},
		{"nc.firma.de", "pdh.firma.de", "https://nc.firma.de/", "nextcloud"},
		{"nc.firma.de", "", "https://NC.firma.de:8443/index.php", "nextcloud"},
		{"mail.firma.de", "pdh.firma.de", "https://nc.firma.de", ""},
		{"nc.firma.de", "", "", ""},
	}
	for _, c := range cases {
		if got := certRole(c.name, c.host, c.nc); got != c.want {
			t.Errorf("certRole(%q, %q, %q) = %q, erwartet %q", c.name, c.host, c.nc, got, c.want)
		}
	}
}

// Ohne Datenbank, Agent, Cloudflare und Verbund ist der Start harmlos: kein Absturz, kein Warten.
func TestCertStartRenewalWithoutAnything(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		(&Handler{}).StartCertRenewal(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StartCertRenewal blockiert")
	}
	// Auch die echten Abhängigkeiten ohne Einrichtung liefern Fehler statt Absturz.
	d := (&Handler{}).certDepsLive()
	if d.blocked() {
		t.Fatal("ohne Verbund gesperrt")
	}
	if _, err := d.dns(); err == nil {
		t.Fatal("DNS ohne Cloudflare")
	}
	if _, err := d.status(ctx); err == nil {
		t.Fatal("Status ohne Agent")
	}
	if _, err := d.install(ctx, "nc.firma.de", nil, nil); err == nil {
		t.Fatal("Installieren ohne Agent")
	}
	d.alarm("ohne Datenbank") // kein Absturz
	if v := d.getSetting(ctx, "certs.alarm.nc.firma.de"); v != "" {
		t.Fatalf("Einstellung ohne Datenbank: %q", v)
	}
}

// Zeitplan: erster Lauf nach der Startverzögerung, dann im Takt; nach einem Wechsel vom
// Spiegel zu aktiv ein Lauf nach der Rollenverzögerung, am Spiegel keiner.
func TestCertScheduleRoleChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	mirror := true
	runs := make(chan struct{}, 10)
	done := make(chan struct{})
	go func() {
		certSchedule(ctx, certTimes{first: time.Hour, every: time.Hour, afterRole: 2 * time.Millisecond, poll: time.Millisecond},
			func() bool { mu.Lock(); defer mu.Unlock(); return mirror },
			func() bool { runs <- struct{}{}; return true })
		close(done)
	}()
	select {
	case <-runs:
		t.Fatal("Lauf am Spiegel")
	case <-time.After(20 * time.Millisecond):
	}
	mu.Lock()
	mirror = false
	mu.Unlock()
	select {
	case <-runs:
	case <-time.After(2 * time.Second):
		t.Fatal("kein Lauf nach dem Rollenwechsel")
	}
	cancel()
	<-done
}

// Vorige Zertifikatszeilen: gemerkt, wenn der Agent sie meldet; eine Erneuerung (leer)
// überschreibt den gemerkten Stand nie.
func TestCertKeepPrevious(t *testing.T) {
	store := map[string]string{}
	put := func(_ context.Context, key, value string) error { store[key] = value; return nil }
	ctx := context.Background()
	if err := certKeepPrevious(ctx, put, "nc.firma.de", "/etc/ssl/alt/fullchain.pem", "/etc/ssl/alt/privkey.pem"); err != nil {
		t.Fatal(err)
	}
	if err := certKeepPrevious(ctx, put, "nc.firma.de", "", ""); err != nil {
		t.Fatal(err)
	}
	cert, key, ok := certPrevDecode(store["certs.prev.nc.firma.de"])
	if !ok || cert != "/etc/ssl/alt/fullchain.pem" || key != "/etc/ssl/alt/privkey.pem" {
		t.Fatalf("gemerkt: %q %q %v (%v)", cert, key, ok, store)
	}
	if _, _, ok := certPrevDecode(""); ok {
		t.Fatal("leerer Stand gilt als gemerkt")
	}
}

// Anfrage an den Agenten: Rolle aus PDH-Name bzw. Nextcloud-Adresse, Upstream nur für das PDH
// und nur lokal.
func TestCertInstallBody(t *testing.T) {
	b := certInstallBody("pdh.firma.de", []byte("KETTE"), []byte("SCHLUESSEL"), "pdh.firma.de", "http://localhost:8090/", "https://nc.firma.de")
	if b["role"] != "pdh" || b["upstream"] != "http://localhost:8090" || b["chain_pem"] != "KETTE" || b["key_pem"] != "SCHLUESSEL" || b["name"] != "pdh.firma.de" {
		t.Errorf("PDH: %v", b)
	}
	if b := certInstallBody("pdh.firma.de", nil, nil, "pdh.firma.de", "https://pdh.intern:8443", ""); b["upstream"] != "http://127.0.0.1:8090" {
		t.Errorf("fremdes Ziel nicht ersetzt: %v", b)
	}
	if b := certInstallBody("nc.firma.de", nil, nil, "pdh.firma.de", "", "https://nc.firma.de"); b["role"] != "nextcloud" || b["upstream"] != "" {
		t.Errorf("Nextcloud: %v", b)
	}
	if b := certInstallBody("mail.firma.de", nil, nil, "pdh.firma.de", "", "https://nc.firma.de"); b["role"] != "" {
		t.Errorf("sonstiger Name: %v", b)
	}
}

// Der echte Agent-Aufruf: POST /v1/certs/install mit Token, Fingerabdruck aus der Antwort;
// eine Absage des Agenten kommt als Fehler mit seinem Text zurück.
func TestCertDepsLiveInstallRequest(t *testing.T) {
	certQuietLog(t)
	var got []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/certs/install" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "falsch", http.StatusBadRequest)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body)
		if body["name"] == "kaputt.example.org" {
			_, _ = w.Write([]byte(`{"ok":false,"error":"nginx -t schlägt fehl – nicht geändert"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"fingerprint":"AB12"}`))
	}))
	defer srv.Close()
	d := (&Handler{updateAgentURL: srv.URL, updateAgentToken: "tok"}).certDepsLive()
	fp, err := d.install(context.Background(), "mail.example.org", []byte("KETTE"), []byte("SCHLUESSEL"))
	if err != nil || fp != "AB12" {
		t.Fatalf("fp=%q err=%v", fp, err)
	}
	if len(got) != 1 || got[0]["name"] != "mail.example.org" || got[0]["chain_pem"] != "KETTE" || got[0]["key_pem"] != "SCHLUESSEL" {
		t.Fatalf("Anfrage: %v", got)
	}
	if _, err := d.install(context.Background(), "kaputt.example.org", nil, nil); err == nil || !strings.Contains(err.Error(), "nicht geändert") {
		t.Fatalf("Absage: %v", err)
	}
}

// Mit dem Cloudflare-Zugang: nur der gemerkte Eintrag wird gelöscht, ein fremder
// _acme-challenge-Eintrag bleibt; ein gemerkter, aber schon fehlender Eintrag wird vergessen.
func TestCertDailyLeftoverOnlyRemembered(t *testing.T) {
	e := newCertTestEnv(t)
	for _, n := range []string{"nc.firma.de", "pdh.firma.de"} {
		is := certTestIssued(t, e, n, e.now.Add(-10*24*time.Hour))
		_ = e.store.certSetInstalled(context.Background(), n, is.Fingerprint)
	}
	rows, _ := e.store.certList(context.Background())
	e.status = map[string]string{}
	for _, r := range rows {
		e.status[r.Name] = r.Fingerprint
	}
	_ = e.store.certSetChallenge(context.Background(), "nc.firma.de", "rec-alt")
	_ = e.store.certSetChallenge(context.Background(), "pdh.firma.de", "rec-weg")
	api := &fakeCFRecords{recs: []cloudflare.Record{
		{ID: "rec-alt", Type: "TXT", Name: "_acme-challenge.nc.firma.de"},
		{ID: "rec-fremd", Type: "TXT", Name: "_acme-challenge.nc.firma.de"},
	}}
	d := e.deps()
	d.dns = func() (acme.DNS, error) { return &certDNS{api: api, zoneID: "z1"}, nil }
	certDaily(context.Background(), d)
	if len(api.deleted) != 1 || api.deleted[0] != "rec-alt" {
		t.Fatalf("gelöscht: %v", api.deleted)
	}
	for _, n := range []string{"nc.firma.de", "pdh.firma.de"} {
		if r := e.store.get(n); r.ChallengeID != "" {
			t.Errorf("%s: challenge_id=%q", n, r.ChallengeID)
		}
	}
}
