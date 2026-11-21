package web

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func appTestKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return k, base64.RawURLEncoding.EncodeToString(der)
}

func TestAppConfirmCode(t *testing.T) {
	if got := appConfirmCode("00000001" + strings.Repeat("0", 56)); got != "000001" {
		t.Errorf("00000001 → %q", got)
	}
	if got := appConfirmCode("ffffffff" + strings.Repeat("0", 56)); got != "967295" {
		t.Errorf("ffffffff → %q", got)
	}
	if got := appConfirmCode("zz"); got != "" {
		t.Errorf("ungültig → %q", got)
	}
	for i := 0; i < 20; i++ {
		_, b64 := appTestKey(t)
		c := appConfirmCode(appKeyFingerprint(b64))
		if len(c) != 6 {
			t.Fatalf("Code %q nicht 6-stellig", c)
		}
		for _, r := range c {
			if r < '0' || r > '9' {
				t.Fatalf("Code %q enthält Nichtziffer", c)
			}
		}
	}
}

func TestParseAppPublicKeyRejectsOtherCurves(t *testing.T) {
	_, ok := appTestKey(t)
	pub, err := parseAppPublicKey(ok)
	if err != nil || pub == nil {
		t.Fatalf("P-256 abgelehnt: %v", err)
	}
	// aufgefüllte Schreibweise wird ebenfalls angenommen
	k, _ := appTestKey(t)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if _, err := parseAppPublicKey(base64.URLEncoding.EncodeToString(der)); err != nil {
		t.Errorf("aufgefüllt abgelehnt: %v", err)
	}

	k384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	d384, _ := x509.MarshalPKIXPublicKey(&k384.PublicKey)
	if _, err := parseAppPublicKey(base64.RawURLEncoding.EncodeToString(d384)); err == nil {
		t.Error("P-384 angenommen")
	}
	kr, _ := rsa.GenerateKey(rand.Reader, 2048)
	dr, _ := x509.MarshalPKIXPublicKey(&kr.PublicKey)
	if _, err := parseAppPublicKey(base64.RawURLEncoding.EncodeToString(dr)); err == nil {
		t.Error("RSA angenommen")
	}
	if _, err := parseAppPublicKey("!!!"); err == nil {
		t.Error("Müll angenommen")
	}
}

func appSignedRequest(t *testing.T, k *ecdsa.PrivateKey, method, target string, body []byte, ts time.Time, nonce string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	tss := strconv.FormatInt(ts.Unix(), 10)
	sum := sha256.Sum256(appCanonical(method, r.URL.RequestURI(), tss, nonce, body))
	sig, err := ecdsa.SignASN1(rand.Reader, k, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("X-PDH-Ts", tss)
	r.Header.Set("X-PDH-Nonce", nonce)
	r.Header.Set("X-PDH-Sig", base64.RawURLEncoding.EncodeToString(sig))
	return r
}

func TestVerifyAppSignature(t *testing.T) {
	old := appNonces
	appNonces = newNonceCache(10*time.Minute+time.Second, 100000)
	defer func() { appNonces = old }()

	k, _ := appTestKey(t)
	now := time.Now()
	body := []byte(`{"a":1}`)
	n := func(s string) string { return "nonce-" + s + "-0123456789" }

	r := appSignedRequest(t, k, "POST", "/app/api/x?a=1", body, now, n("ok"))
	if err := verifyAppSignature(&k.PublicKey, r, body, now); err != nil {
		t.Fatalf("gültig: %v", err)
	}
	// gleiche Nonce zweimal
	if err := verifyAppSignature(&k.PublicKey, r, body, now); !errors.Is(err, errAppNonceReused) {
		t.Errorf("Wiederholung: %v", err)
	}
	// geänderter Body
	r = appSignedRequest(t, k, "POST", "/app/api/x", body, now, n("body"))
	if err := verifyAppSignature(&k.PublicKey, r, []byte(`{"a":2}`), now); !errors.Is(err, errAppSigBad) {
		t.Errorf("Body: %v", err)
	}
	// fehlerhafte Signatur verbraucht die Nonce nicht
	if err := verifyAppSignature(&k.PublicKey, r, body, now); err != nil {
		t.Errorf("Nonce nach Fehlversuch verbraucht: %v", err)
	}
	// zu alt
	r = appSignedRequest(t, k, "GET", "/app/api/x", nil, now.Add(-6*time.Minute), n("old"))
	if err := verifyAppSignature(&k.PublicKey, r, nil, now); !errors.Is(err, errAppSigStale) {
		t.Errorf("alt: %v", err)
	}
	// zu neu
	r = appSignedRequest(t, k, "GET", "/app/api/x", nil, now.Add(6*time.Minute), n("new"))
	if err := verifyAppSignature(&k.PublicKey, r, nil, now); !errors.Is(err, errAppSigStale) {
		t.Errorf("neu: %v", err)
	}
	// fehlende Kopfzeilen
	for _, h := range []string{"X-PDH-Ts", "X-PDH-Nonce", "X-PDH-Sig"} {
		r = appSignedRequest(t, k, "GET", "/app/api/x", nil, now, n("miss"+h))
		r.Header.Del(h)
		if err := verifyAppSignature(&k.PublicKey, r, nil, now); !errors.Is(err, errAppSigMissing) {
			t.Errorf("fehlt %s: %v", h, err)
		}
	}
	// ungültige Nonce
	r = appSignedRequest(t, k, "GET", "/app/api/x", nil, now, "kurz")
	if err := verifyAppSignature(&k.PublicKey, r, nil, now); !errors.Is(err, errAppSigMissing) {
		t.Errorf("kurze Nonce: %v", err)
	}
	// Query gehört zur Signatur
	r = appSignedRequest(t, k, "GET", "/app/api/x?a=1", nil, now, n("q"))
	r.URL.RawQuery = "a=2"
	r.RequestURI = ""
	if err := verifyAppSignature(&k.PublicKey, r, nil, now); !errors.Is(err, errAppSigBad) {
		t.Errorf("Query: %v", err)
	}
	// fremder Schlüssel
	k2, _ := appTestKey(t)
	r = appSignedRequest(t, k2, "GET", "/app/api/x", nil, now, n("other"))
	if err := verifyAppSignature(&k.PublicKey, r, nil, now); !errors.Is(err, errAppSigBad) {
		t.Errorf("fremder Schlüssel: %v", err)
	}
}

func TestAppNonceCache(t *testing.T) {
	c := newNonceCache(10*time.Minute, 3)
	now := time.Now()
	if c.seen("a", now) {
		t.Error("a erstmals gesehen")
	}
	if !c.seen("a", now.Add(time.Minute)) {
		t.Error("a nicht erkannt")
	}
	if c.seen("a", now.Add(11*time.Minute)) {
		t.Error("a nach Ablauf noch bekannt")
	}
	// genau an der Ablaufgrenze noch bekannt
	c = newNonceCache(10*time.Minute, 3)
	c.seen("b", now)
	if !c.seen("b", now.Add(10*time.Minute)) {
		t.Error("b an der Grenze nicht erkannt")
	}
	// Abgelaufenes wird zuerst entfernt
	c = newNonceCache(10*time.Minute, 2)
	c.seen("x", now)
	c.seen("y", now.Add(9*time.Minute))
	if c.seen("z", now.Add(11*time.Minute)) {
		t.Error("z trotz abgelaufenem x abgelehnt")
	}
	if _, ok := c.m["x"]; ok || len(c.m) != 2 {
		t.Errorf("x nicht entfernt: %v", c.m)
	}
	// Voll mit gültigen Einträgen: neue Nonce wird abgelehnt, nichts verdrängt
	c = newNonceCache(10*time.Minute, 2)
	c.seen("p", now)
	c.seen("q", now)
	if !c.seen("r", now.Add(time.Minute)) {
		t.Error("volle Liste nahm neue Nonce an")
	}
	if len(c.m) != 2 || c.m["p"].IsZero() || c.m["q"].IsZero() {
		t.Errorf("Eintrag verdrängt: %v", c.m)
	}
}

func TestAppNonceReplayAtWindowEdge(t *testing.T) {
	old := appNonces
	appNonces = newNonceCache(10*time.Minute+time.Second, 100000)
	defer func() { appNonces = old }()
	k, _ := appTestKey(t)
	ts := time.Now().Truncate(time.Second)
	r := appSignedRequest(t, k, "GET", "/app/api/x", nil, ts, "edge-nonce-0123456789")
	if err := verifyAppSignature(&k.PublicKey, r, nil, ts.Add(-5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := verifyAppSignature(&k.PublicKey, r, nil, ts.Add(5*time.Minute)); !errors.Is(err, errAppNonceReused) {
		t.Errorf("Wiederholung bei ts+5min: %v", err)
	}
}
