package web

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Web Push ohne Fremdbibliothek: Verschluesselung nach RFC 8291 (aes128gcm,
// RFC 8188) und Absenderkennung VAPID nach RFC 8292. Die Push-Dienste der
// Browser (Google, Apple, Mozilla) sehen nur den verschluesselten Inhalt.

var b64 = base64.RawURLEncoding

// b64Decode akzeptiert URL- und Standard-Base64 mit oder ohne Auffuellung.
func b64Decode(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("ungültiges Base64")
}

// ── VAPID ────────────────────────────────────────────────────

// generateVAPIDKeys: neues P-256-Schluesselpaar (privat: 32 Byte, oeffentlich: 65 Byte unkomprimiert).
func generateVAPIDKeys() (private, public string, err error) {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return b64.EncodeToString(k.Bytes()), b64.EncodeToString(k.PublicKey().Bytes()), nil
}

// vapidKey macht aus dem gespeicherten privaten Schluessel einen ECDSA-Schluessel.
func vapidKey(private string) (*ecdsa.PrivateKey, error) {
	d, err := b64Decode(private)
	if err != nil || len(d) != 32 {
		return nil, errors.New("ungültiger VAPID-Schlüssel")
	}
	k, err := ecdh.P256().NewPrivateKey(d)
	if err != nil {
		return nil, err
	}
	pub := k.PublicKey().Bytes() // 0x04 || X || Y
	return &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(pub[1:33]), Y: new(big.Int).SetBytes(pub[33:])},
		D:         new(big.Int).SetBytes(d),
	}, nil
}

// vapidHeader: Authorization-Kopfzeile fuer einen Push-Endpunkt (gilt 12 Stunden).
func vapidHeader(endpoint, subject, private, public string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("ungültiger Push-Endpunkt")
	}
	key, err := vapidKey(private)
	if err != nil {
		return "", err
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"aud": u.Scheme + "://" + u.Host,
		"exp": now.Add(12 * time.Hour).Unix(),
		"sub": subject,
	})
	signed, err := tok.SignedString(key)
	if err != nil {
		return "", err
	}
	return "vapid t=" + signed + ", k=" + public, nil
}

// ── Verschluesselung (RFC 8291) ──────────────────────────────

const pushRecordSize = 4096

// encryptPush verschluesselt den Inhalt fuer ein Abonnement (p256dh, auth aus dem Browser).
func encryptPush(p256dh, auth string, plaintext []byte) ([]byte, error) {
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	return encryptPushWith(as, salt, p256dh, auth, plaintext)
}

// encryptPushWith: wie encryptPush mit festem Server-Schluessel und Salz (Tests nach RFC 8291 Anhang A).
func encryptPushWith(as *ecdh.PrivateKey, salt []byte, p256dh, auth string, plaintext []byte) ([]byte, error) {
	uaPub, err := b64Decode(p256dh)
	if err != nil {
		return nil, fmt.Errorf("p256dh: %w", err)
	}
	authSecret, err := b64Decode(auth)
	if err != nil || len(authSecret) < 16 {
		return nil, errors.New("auth: ungültig")
	}
	ua, err := ecdh.P256().NewPublicKey(uaPub)
	if err != nil {
		return nil, fmt.Errorf("p256dh: %w", err)
	}
	shared, err := as.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPub := as.PublicKey().Bytes()

	// IKM = HKDF(auth_secret, ecdh_secret, "WebPush: info" || 0x00 || ua_public || as_public, 32)
	info := append(append([]byte("WebPush: info\x00"), uaPub...), asPub...)
	prkKey := hmacSHA256(authSecret, shared)
	ikm := hmacSHA256(prkKey, append(info, 0x01))

	prk := hmacSHA256(salt, ikm)
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// ein einziger Datensatz: Inhalt || 0x02 (Ende), muss in die Datensatzgroesse passen
	if len(plaintext)+1+16 > pushRecordSize {
		return nil, errors.New("Push-Inhalt zu groß")
	}
	record := gcm.Seal(nil, nonce, append(append([]byte(nil), plaintext...), 0x02), nil)

	var out bytes.Buffer
	out.Write(salt)
	_ = binary.Write(&out, binary.BigEndian, uint32(pushRecordSize))
	out.WriteByte(byte(len(asPub)))
	out.Write(asPub)
	out.Write(record)
	return out.Bytes(), nil
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

// ── Senden ───────────────────────────────────────────────────

type pushSubscription struct {
	ID, UserID, Endpoint, P256dh, Auth string
}

// errPushGone: Abonnement existiert nicht mehr (404/410) – loeschen.
var errPushGone = errors.New("push-abonnement abgelaufen")

var pushHTTP = &http.Client{Timeout: 15 * time.Second}

type pushVAPID struct{ Private, Public, Subject string }

// sendPush schickt einen verschluesselten Inhalt an ein Abonnement.
// topic ersetzt eine noch nicht zugestellte Nachricht mit gleichem Thema.
func sendPush(ctx context.Context, v pushVAPID, s pushSubscription, payload []byte, ttl time.Duration, urgency, topic string) error {
	body, err := encryptPush(s.P256dh, s.Auth, payload)
	if err != nil {
		return err
	}
	auth, err := vapidHeader(s.Endpoint, v.Subject, v.Private, v.Public, time.Now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int(ttl.Seconds())))
	if urgency != "" {
		req.Header.Set("Urgency", urgency)
	}
	if topic != "" {
		req.Header.Set("Topic", topic)
	}
	resp, err := pushHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return errPushGone
	case resp.StatusCode >= 300:
		return fmt.Errorf("push-dienst: HTTP %d %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	return nil
}
