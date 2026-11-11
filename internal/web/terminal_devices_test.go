package web

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestPairingCodeFormat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		c, err := newPairingCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != 8 {
			t.Fatalf("Länge %d: %q", len(c), c)
		}
		for _, r := range c {
			if !strings.ContainsRune(pairingAlphabet, r) {
				t.Fatalf("Zeichen %q nicht im Alphabet: %q", r, c)
			}
		}
		if seen[c] {
			t.Fatalf("doppelter Code %q", c)
		}
		seen[c] = true
	}
}

func TestNormalizePairingCode(t *testing.T) {
	for _, in := range []string{"abcd-efgh", " ABCD EFGH ", "ABCDEFGH", "ab cd-ef gh"} {
		if got := normalizePairingCode(in); got != "ABCDEFGH" {
			t.Errorf("%q -> %q", in, got)
		}
	}
}

func TestFormatPairingCode(t *testing.T) {
	if got := formatPairingCode("ABCDEFGH"); got != "ABCD-EFGH" {
		t.Errorf("got %q", got)
	}
}

func TestDeviceSecretFormat(t *testing.T) {
	s, err := newDeviceSecret()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s, "pdht_") {
		t.Fatalf("Präfix fehlt: %q", s)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, "pdht_"))
	if err != nil || len(raw) != 32 {
		t.Fatalf("Base64url-Teil: %d Bytes, %v", len(raw), err)
	}
}

func TestHashesDomainSeparated(t *testing.T) {
	if hashPairingCode("x") == hashDeviceSecret("x") {
		t.Error("Hashes nicht domänengetrennt")
	}
	if len(hashPairingCode("x")) != 64 {
		t.Error("kein Hex-SHA-256")
	}
}

func TestUserDetailDevicesCard(t *testing.T) {
	tmpl := loadTestTemplates(t)
	d := UserDetailData{CanDevices: true, User: UserView{ID: "u1", IsSystemUser: true}}
	if out := renderPage(t, tmpl, "user_detail", d); !strings.Contains(out, `action="/users/u1/terminal-code"`) || !strings.Contains(out, "Noch kein Gerät gekoppelt.") {
		t.Error("Karte „Gekoppelte Geräte“ mit Knopf fehlt")
	}
	d.CanDevices = false
	if out := renderPage(t, tmpl, "user_detail", d); strings.Contains(out, "terminal-code") {
		t.Error("Knopf ohne Recht sichtbar")
	}
}
