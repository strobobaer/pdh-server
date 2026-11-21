package web

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PDH-App: Geräteschlüssel (P-256, SubjectPublicKeyInfo) und Anfragesignatur.

var (
	errAppSigMissing  = errors.New("app: Signatur-Kopfzeilen fehlen oder sind ungültig")
	errAppSigStale    = errors.New("app: Zeitstempel außerhalb des Zeitfensters")
	errAppSigBad      = errors.New("app: Signatur ungültig")
	errAppNonceReused = errors.New("app: Nonce bereits verwendet")
)

const appSigWindow = 5 * time.Minute

// appNonces merkt verwendete Nonces je Gerät (Fingerabdruck + Nonce).
var appNonces = newNonceCache(10*time.Minute, 100000)

// appDecodeB64 liest base64url mit oder ohne Auffüllung.
func appDecodeB64(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimSpace(s), "="))
}

// parseAppPublicKey liest einen base64url-kodierten SPKI-Schlüssel; nur P-256.
func parseAppPublicKey(b64 string) (*ecdsa.PublicKey, error) {
	der, err := appDecodeB64(b64)
	if err != nil {
		return nil, fmt.Errorf("schlüssel: %w", err)
	}
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("schlüssel: %w", err)
	}
	pub, ok := k.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, errors.New("schlüssel: nur P-256 erlaubt")
	}
	return pub, nil
}

// appKeyFingerprint: hex(SHA-256 der DER-Bytes); leer bei ungültiger Eingabe.
func appKeyFingerprint(b64 string) string {
	der, err := appDecodeB64(b64)
	if err != nil {
		return ""
	}
	return appDERFingerprint(der)
}

func appDERFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// appConfirmCode: 6 Ziffern aus den ersten 4 Byte des Fingerabdrucks.
func appConfirmCode(fingerprint string) string {
	raw, err := hex.DecodeString(fingerprint)
	if err != nil || len(raw) < 4 {
		return "000000"
	}
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(raw[:4])%1_000_000)
}

// appCanonical ist die signierte Zeichenfolge einer Anfrage.
func appCanonical(method, pathWithQuery, ts, nonce string, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte("PDHAPP1\n" + method + "\n" + pathWithQuery + "\n" + ts + "\n" + nonce + "\n" + hex.EncodeToString(sum[:]))
}

func appNonceOK(n string) bool {
	if len(n) < 16 || len(n) > 64 {
		return false
	}
	for _, c := range n {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// verifyAppSignature prüft Zeitfenster, Signatur und Nonce. Die Nonce wird
// erst nach gültiger Signatur gemerkt.
func verifyAppSignature(pub *ecdsa.PublicKey, r *http.Request, body []byte, now time.Time) error {
	tss := r.Header.Get("X-PDH-Ts")
	nonce := r.Header.Get("X-PDH-Nonce")
	sigB64 := r.Header.Get("X-PDH-Sig")
	if tss == "" || nonce == "" || sigB64 == "" || !appNonceOK(nonce) {
		return errAppSigMissing
	}
	ts, err := strconv.ParseInt(tss, 10, 64)
	if err != nil {
		return errAppSigMissing
	}
	d := now.Sub(time.Unix(ts, 0))
	if d < -appSigWindow || d > appSigWindow {
		return errAppSigStale
	}
	sig, err := appDecodeB64(sigB64)
	if err != nil {
		return errAppSigBad
	}
	sum := sha256.Sum256(appCanonical(r.Method, r.URL.RequestURI(), tss, nonce, body))
	if !ecdsa.VerifyASN1(pub, sum[:], sig) {
		return errAppSigBad
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return errAppSigBad
	}
	if appNonces.seen(appDERFingerprint(der)+nonce, now) {
		return errAppNonceReused
	}
	return nil
}

// nonceCache merkt Schlüssel für eine feste Dauer, höchstens max Einträge.
type nonceCache struct {
	mu  sync.Mutex
	ttl time.Duration
	max int
	m   map[string]time.Time // Schlüssel → Ablaufzeit
}

func newNonceCache(ttl time.Duration, max int) *nonceCache {
	return &nonceCache{ttl: ttl, max: max, m: map[string]time.Time{}}
}

// seen meldet, ob der Schlüssel schon bekannt (und nicht abgelaufen) ist;
// andernfalls wird er gemerkt.
func (c *nonceCache) seen(key string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if exp, ok := c.m[key]; ok && now.Before(exp) {
		return true
	}
	delete(c.m, key)
	if len(c.m) >= c.max {
		for k, exp := range c.m { // Abgelaufenes zuerst
			if !now.Before(exp) {
				delete(c.m, k)
			}
		}
		for len(c.m) >= c.max && len(c.m) > 0 { // sonst die ältesten
			var oldK string
			var oldE time.Time
			first := true
			for k, exp := range c.m {
				if first || exp.Before(oldE) {
					oldK, oldE, first = k, exp, false
				}
			}
			delete(c.m, oldK)
		}
	}
	c.m[key] = now.Add(c.ttl)
	return false
}
