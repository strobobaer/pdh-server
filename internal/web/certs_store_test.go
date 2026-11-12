package web

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// Das Schlüssel-PEM wird verschlüsselt abgelegt und kommt unverändert zurück;
// mit einem anderen Schlüssel schlägt das Öffnen fehl.
func TestCertKeyRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	other := make([]byte, 32)
	other[0] = 1
	pem := []byte("-----BEGIN EC PRIVATE KEY-----\ngeheim\n-----END EC PRIVATE KEY-----\n")

	enc, err := certSealKey(key, pem)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(enc, "geheim") {
		t.Fatal("Schlüssel liegt im Klartext vor")
	}
	got, err := certOpenKey(key, enc)
	if err != nil || string(got) != string(pem) {
		t.Fatalf("Rundlauf: %q, %v", got, err)
	}
	if _, err := certOpenKey(other, enc); err == nil {
		t.Fatal("anderer Schlüssel hätte scheitern müssen")
	}
}

// Die Migration 157 legt beide Rechte an und vergibt sie an alle Rollen mit
// „Cloudflare verwalten“ (Textprüfung, der Test braucht keine Datenbank).
func TestMigration157Grants(t *testing.T) {
	b, err := os.ReadFile("../../migrations/157_le_certs.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"'system.certs'", "'system.unifi'", "System & Sicherheit",
		"o.key = 'system.cloudflare'", "rp.department_id IS NULL",
		"CREATE TABLE IF NOT EXISTS le_certs", "lan_record_id", "ON CONFLICT",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("Migration 157 enthält %q nicht", want)
		}
	}
}

// Kontoschlüssel: nie überschreiben bei Lesefehler oder unlesbarem Wert, einmal anlegen.
func TestCertAccountKeyFrom(t *testing.T) {
	key := make([]byte, 32)
	writes := 0
	failRead := func() (string, bool, error) { return "", false, errors.New("verbindung weg") }
	ins := func(string) error { writes++; return nil }

	if _, err := certAccountKeyFrom(key, failRead, ins); err == nil || writes != 0 {
		t.Fatalf("Lesefehler: err=%v writes=%d", err, writes)
	}

	enc, _ := certSealKey(key, []byte("vorhanden"))
	got, err := certAccountKeyFrom(key, func() (string, bool, error) { return enc, true, nil }, ins)
	if err != nil || string(got) != "vorhanden" || writes != 0 {
		t.Fatalf("vorhanden: %q %v writes=%d", got, err, writes)
	}

	other := make([]byte, 32)
	other[0] = 9
	if _, err := certAccountKeyFrom(other, func() (string, bool, error) { return enc, true, nil }, ins); err == nil || writes != 0 {
		t.Fatalf("unlesbar: err=%v writes=%d", err, writes)
	}

	stored, has := "", false
	read := func() (string, bool, error) { return stored, has, nil }
	insert := func(e string) error {
		writes++
		if !has {
			stored, has = e, true
		}
		return nil
	}
	a, err := certAccountKeyFrom(key, read, insert)
	if err != nil || len(a) == 0 || writes != 1 {
		t.Fatalf("anlegen: %v writes=%d", err, writes)
	}
	b, err := certAccountKeyFrom(key, read, insert)
	if err != nil || string(a) != string(b) || writes != 1 {
		t.Fatalf("zweiter Aufruf: %v writes=%d", err, writes)
	}
}
