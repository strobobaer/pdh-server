package web

// Updates übers Netz (0.82.25): Kennwort je Gerät, Recht esppdh.ota (Migration 194),
// Firmware-Kennung im Bauauftrag.

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pdh/internal/modules/esppdh"
)

func TestEsppdhOTAPasswordLazy(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	id := e.create(t, "otapw-"+e.sfx, url.Values{"web": {"on"}})
	ctx := context.Background()
	// älteres Gerät ohne Kennwort nachstellen
	if _, err := e.pool.Exec(ctx, `UPDATE esppdh_devices SET ota_password_enc = '' WHERE id = $1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	d, err := e.h.esppdhLoad(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	e.h.mirror = MirrorInfo{On: true}
	if _, err := e.h.esppdhOTAPassword(ctx, &d); err == nil {
		t.Errorf("Spiegel legt ein Kennwort an")
	}
	e.h.mirror = MirrorInfo{}
	pw1, err := e.h.esppdhOTAPassword(ctx, &d)
	if err != nil || len(pw1) < 20 {
		t.Fatalf("Kennwort: %q, %v", pw1, err)
	}
	var enc string
	if err := e.pool.QueryRow(ctx, `SELECT ota_password_enc FROM esppdh_devices WHERE id = $1::uuid`, id).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if enc == "" || strings.Contains(enc, pw1) {
		t.Fatalf("Kennwort nicht verschlüsselt gespeichert: %q", enc)
	}
	d2, _ := e.h.esppdhLoad(ctx, id)
	if pw2, err := e.h.esppdhOTAPassword(ctx, &d2); err != nil || pw2 != pw1 {
		t.Errorf("zweiter Aufruf: %q, %v – erwartet dasselbe Kennwort", pw2, err)
	}
}

func (e *esppdhEnvT) otaEnc(t *testing.T, id string) string {
	t.Helper()
	var enc string
	if err := e.pool.QueryRow(context.Background(), `SELECT ota_password_enc FROM esppdh_devices WHERE id = $1::uuid`, id).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	return enc
}

// Nach einem Wechsel des Schlüssels kein stilles neues Kennwort: Das Gerät nähme es nicht an.
// Der Ausweg ist „Neues MQTT-Kennwort“ (setzt auch das Update-Kennwort zurück) und einmal USB.
func TestEsppdhOTAPasswordUnreadable(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	id := e.create(t, "otakey-"+e.sfx, nil)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `UPDATE esppdh_devices SET ota_password_enc = 'kaputt' WHERE id = $1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	d, _ := e.h.esppdhLoad(ctx, id)
	if _, err := e.h.esppdhOTAPassword(ctx, &d); err == nil || !strings.Contains(err.Error(), "Neues MQTT-Kennwort") {
		t.Errorf("Fehler: %v", err)
	}
	if enc := e.otaEnc(t, id); enc != "kaputt" {
		t.Errorf("Kennwort still ersetzt: %q", enc)
	}
	if _, err := e.h.esppdhSetPasswords(ctx, id, "Neues-MQTT-Kennwort-12345", ""); err != nil {
		t.Fatal(err)
	}
	if enc := e.otaEnc(t, id); enc != "" {
		t.Errorf("Neues MQTT-Kennwort setzt das Update-Kennwort nicht zurück: %q", enc)
	}
}

// In der Wartung schreibt auch eine Seitenansicht nichts (kein neues Kennwort).
func TestEsppdhOTAPasswordNoWriteInMaintenance(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	id := e.create(t, "otamaint-"+e.sfx, nil)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `UPDATE esppdh_devices SET ota_password_enc = '' WHERE id = $1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	d, _ := e.h.esppdhLoad(ctx, id)
	e.h.mlock.locked = true
	_, err := e.h.esppdhOTAPassword(ctx, &d)
	e.h.mlock.locked = false
	if err == nil || e.otaEnc(t, id) != "" {
		t.Errorf("in der Wartung geschrieben (%v)", err)
	}
}

func TestEsppdhOTAPasswordOnCreate(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	id := e.create(t, "otanew-"+e.sfx, nil)
	var enc string
	if err := e.pool.QueryRow(context.Background(), `SELECT ota_password_enc FROM esppdh_devices WHERE id = $1::uuid`, id).Scan(&enc); err != nil || enc == "" {
		t.Errorf("neues Gerät ohne Update-Kennwort (%q, %v)", enc, err)
	}
}

func TestEsppdhOTARightGranted(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var label, cat string
	if err := pool.QueryRow(ctx, `SELECT label, category FROM permissions WHERE key = 'esppdh.ota'`).Scan(&label, &cat); err != nil {
		t.Fatalf("Recht esppdh.ota fehlt: %v", err)
	}
	if label != "ESPPDH: Firmware übers Netz aufspielen" || cat != "Daten & Schnittstellen" {
		t.Errorf("Recht: %q / %q", label, cat)
	}
	var dept, role string
	if err := pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Test OTA-Recht') RETURNING id::text`).Scan(&dept); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM departments WHERE id = $1::uuid`, dept) })
	if err := pool.QueryRow(ctx, `INSERT INTO roles (key, label, level) VALUES ('test_otarecht', 'Test OTA-Recht', 1) RETURNING id::text`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM role_permissions WHERE role_id = $1::uuid`, role)
		_, _ = pool.Exec(context.Background(), `DELETE FROM roles WHERE id = $1::uuid`, role)
	})
	exec(`INSERT INTO role_permissions (role_id, permission_id)
		SELECT $1::uuid, id FROM permissions WHERE key = 'esppdh.edit'`, role)
	m, _ := filepath.Glob("../../migrations/*_esppdh_ota.up.sql")
	if len(m) != 1 {
		t.Fatalf("Migration esppdh_ota: %v", m)
	}
	sql, err := os.ReadFile(m[0])
	if err != nil {
		t.Fatal(err)
	}
	exec(string(sql)) // die Migration ist wiederholbar
	var global bool
	if err := pool.QueryRow(ctx, `SELECT rp.department_id IS NULL FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
		WHERE rp.role_id = $1::uuid AND p.key = 'esppdh.ota'`, role).Scan(&global); err != nil || !global {
		t.Errorf("esppdh.ota nicht überall vergeben (global=%v, %v)", global, err)
	}
}

// Wem esppdh.edit persönlich verboten ist, dem bleibt auch esppdh.ota verboten (sonst gäbe es
// die Rolle nach dem Update frei).
func TestEsppdhOTAKeepsDenial(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()
	var uid string
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role)
		VALUES ('ota-verbot', 'ota-verbot@example.invalid', 'x', 'Ota', 'Verbot', 'technician') RETURNING id::text`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_permissions WHERE user_id = $1::uuid`, uid)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1::uuid`, uid)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO user_permissions (user_id, permission_id, granted)
		SELECT $1::uuid, id, false FROM permissions WHERE key = 'esppdh.edit'`, uid); err != nil {
		t.Fatal(err)
	}
	m, _ := filepath.Glob("../../migrations/*_esppdh_ota.up.sql")
	sql, err := os.ReadFile(m[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	var granted bool
	if err := pool.QueryRow(ctx, `SELECT up.granted FROM user_permissions up JOIN permissions p ON p.id = up.permission_id
		WHERE up.user_id = $1::uuid AND p.key = 'esppdh.ota'`, uid).Scan(&granted); err != nil || granted {
		t.Errorf("Verbot nicht übernommen (granted=%v, %v)", granted, err)
	}
}

func TestEsppdhJobForSetsFirmwareID(t *testing.T) {
	e := newEsppdhEnv(t, nil)
	id := e.buildable(t, "otajob-"+e.sfx)
	ctx := context.Background()
	d, err := e.h.esppdhLoad(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	mqttPW, apPW, err := e.h.esppdhSecrets(d)
	if err != nil {
		t.Fatal(err)
	}
	res, msgs := e.h.esppdhResolve(ctx, d, mqttPW, apPW, "")
	if len(msgs) > 0 {
		t.Fatalf("Auflösen: %v", msgs)
	}
	if res.Dev.OTAPassword == "" {
		t.Fatalf("Gerät ohne Update-Kennwort aufgelöst")
	}
	claim := func(bid string) esppdhClaim {
		return esppdhClaim{ID: bid, DeviceID: id, Host: res.Broker.Host, Port: res.Broker.Port, TLS: res.Broker.TLS}
	}
	const a, b = "01a1ffff-0000-7000-8000-0000000000aa", "01a1ffff-0000-7000-8000-0000000000bb"
	jobA, hashA, err := e.h.esppdhJobFor(ctx, claim(a))
	if err != nil {
		t.Fatal(err)
	}
	_, hashB, err := e.h.esppdhJobFor(ctx, claim(b))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(jobA.Config), a) {
		t.Errorf("Firmware-Kennung %s fehlt in der YAML", a)
	}
	if hashA != hashB || hashA != esppdh.ConfigHash(res.Dev) {
		t.Errorf("Prüfwert hängt von der Lauf-Kennung ab: %s / %s / %s", hashA, hashB, esppdh.ConfigHash(res.Dev))
	}
	found := false
	for _, s := range jobA.SecretValues {
		found = found || s == res.Dev.OTAPassword
	}
	if !found {
		t.Errorf("Update-Kennwort wird im Verlauf nicht geschwärzt")
	}
}
