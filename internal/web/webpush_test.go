package web

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// RFC 8291 Anhang A: Beispielwerte des Standards.
const (
	rfcPlain    = "When I grow up, I want to be a watermelon"
	rfcASPriv   = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfcUAPriv   = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfcUAPub    = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfcSalt     = "DGv6ra1nlYgDCS1FRnbzlw"
	rfcAuth     = "BTBZMqHH6r4Tts7J_aSIgg"
	rfcExpected = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

func TestEncryptPushRFC8291(t *testing.T) {
	asRaw, _ := b64Decode(rfcASPriv)
	as, err := ecdh.P256().NewPrivateKey(asRaw)
	if err != nil {
		t.Fatal(err)
	}
	salt, _ := b64Decode(rfcSalt)
	got, err := encryptPushWith(as, salt, rfcUAPub, rfcAuth, []byte(rfcPlain))
	if err != nil {
		t.Fatal(err)
	}
	if b64.EncodeToString(got) != rfcExpected {
		t.Errorf("weicht von RFC 8291 ab:\n%s\n%s", b64.EncodeToString(got), rfcExpected)
	}
}

// Gegenprobe: mit zufaelligem Schluessel verschluesseln, als Browser entschluesseln.
func TestEncryptPushRoundTrip(t *testing.T) {
	body, err := encryptPush(rfcUAPub, rfcAuth, []byte(`{"title":"Störung"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := decryptPushForTest(t, body, rfcUAPriv, rfcAuth); got != `{"title":"Störung"}` {
		t.Errorf("entschlüsselt: %q", got)
	}
	if _, err := encryptPush(rfcUAPub, rfcAuth, make([]byte, 5000)); err == nil {
		t.Error("zu großer Inhalt angenommen")
	}
}

// decryptPushForTest: Empfaengerseite nach RFC 8291 (nur fuer Tests).
func decryptPushForTest(t *testing.T, body []byte, uaPriv, auth string) string {
	t.Helper()
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	asPub, rec := body[21:21+idlen], body[21+idlen:]
	if rs != pushRecordSize {
		t.Fatalf("Datensatzgröße %d", rs)
	}
	raw, _ := b64Decode(uaPriv)
	ua, _ := ecdh.P256().NewPrivateKey(raw)
	as, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := ua.ECDH(as)
	authSecret, _ := b64Decode(auth)
	info := append(append([]byte("WebPush: info\x00"), ua.PublicKey().Bytes()...), asPub...)
	ikm := hmacSHA256(hmacSHA256(authSecret, shared), append(info, 0x01))
	prk := hmacSHA256(salt, ikm)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain[len(plain)-1] != 0x02 {
		t.Fatal("Ende-Kennung fehlt")
	}
	return string(plain[:len(plain)-1])
}

func TestVAPIDHeader(t *testing.T) {
	priv, pub, err := generateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := b64Decode(pub); len(p) != 65 || p[0] != 4 {
		t.Fatalf("öffentlicher Schlüssel: %d Byte", len(p))
	}
	h, err := vapidHeader("https://fcm.googleapis.com/fcm/send/abc", "mailto:pdh@example.com", priv, pub, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "vapid t=") || !strings.HasSuffix(h, ", k="+pub) {
		t.Fatalf("Kopfzeile: %s", h)
	}
	tokStr := strings.TrimSuffix(strings.TrimPrefix(h, "vapid t="), ", k="+pub)
	key, _ := vapidKey(priv)
	tok, err := jwt.Parse(tokStr, func(*jwt.Token) (interface{}, error) { return &key.PublicKey, nil }, jwt.WithValidMethods([]string{"ES256"}))
	if err != nil || !tok.Valid {
		t.Fatalf("Signatur ungültig: %v", err)
	}
	claims := tok.Claims.(jwt.MapClaims)
	if claims["aud"] != "https://fcm.googleapis.com" || claims["sub"] != "mailto:pdh@example.com" {
		t.Errorf("Claims: %v", claims)
	}
	var _ *ecdsa.PublicKey = &key.PublicKey
	if _, err := vapidHeader("kein-url", "mailto:x@y", priv, pub, time.Now()); err == nil {
		t.Error("ungültiger Endpunkt angenommen")
	}
}
