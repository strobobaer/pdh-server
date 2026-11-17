package web

import (
	"context"
	"errors"
	"strings"
	"testing"

	"pdh/internal/integrations/snmp"
)

// Profile: Kennwörter nur verschlüsselt, leer = unverändert, Name eindeutig, Löschen nur
// ohne Geräte.
func TestSNMPStoreProfiles(t *testing.T) {
	e := newSNMPEnv(t) // legt das Profil „Switches“ an
	ctx := context.Background()
	h, pool := e.h, e.pool

	secrets := func(id string) (auth, priv string) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT auth_secret_enc, priv_secret_enc FROM it_snmp_profiles WHERE id = $1::uuid`, id).Scan(&auth, &priv); err != nil {
			t.Fatal(err)
		}
		return
	}
	plain := func(enc string) string {
		t.Helper()
		s, err := decryptSecret(h.pkiKey(), enc)
		if err != nil {
			t.Fatalf("nicht entschlüsselbar: %v", err)
		}
		return s
	}
	auth0, priv0 := secrets(e.profile)
	for _, enc := range []string{auth0, priv0} {
		if !strings.HasPrefix(enc, "v1:") || strings.Contains(enc, "Test-") {
			t.Errorf("Kennwort nicht verschlüsselt abgelegt: %q", enc)
		}
	}
	if plain(auth0) != snmpTestAuth || plain(priv0) != snmpTestPriv {
		t.Error("Kennwörter nicht wiederherstellbar")
	}
	// Kein Kennwort in irgendeiner Spalte der Tabelle im Klartext.
	var leaks int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM it_snmp_profiles p WHERE p::text LIKE '%' || $1 || '%' OR p::text LIKE '%' || $2 || '%'`,
		snmpTestAuth, snmpTestPriv).Scan(&leaks)
	if leaks != 0 {
		t.Error("Kennwort steht lesbar in der Tabelle")
	}

	// Ändern ohne Kennwörter: beide bleiben, wie sie sind.
	in := SNMPProfileInput{Name: " Switches Halle ", User: "pdh2", AuthProto: "sha512", PrivProto: "aes256"}
	if err := h.snmpProfileUpdate(ctx, e.profile, in); err != nil {
		t.Fatalf("Ändern ohne Kennwort: %v", err)
	}
	if a, p := secrets(e.profile); a != auth0 || p != priv0 {
		t.Error("leeres Kennwort hat das gespeicherte verändert")
	}
	list, err := h.snmpProfiles(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("Profile: %v %v", list, err)
	}
	if p := list[0]; p.ID != e.profile || p.Name != "Switches Halle" || p.User != "pdh2" || p.AuthProto != "sha512" || p.PrivProto != "aes256" || p.Targets != 0 {
		t.Errorf("Profil: %+v", p)
	}
	// Nur das Anmelde-Kennwort neu: das andere bleibt.
	in.AuthPass = "Neues-Auth-9876"
	if err := h.snmpProfileUpdate(ctx, e.profile, in); err != nil {
		t.Fatal(err)
	}
	if a, p := secrets(e.profile); plain(a) != "Neues-Auth-9876" || p != priv0 {
		t.Error("nur das Anmelde-Kennwort sollte sich ändern")
	}
	if cfg, err := h.snmpProfileConfig(ctx, e.profile, "10.78.0.1", 161); err != nil || cfg.AuthPass != "Neues-Auth-9876" || cfg.PrivPass != snmpTestPriv ||
		cfg.User != "pdh2" || strings.Contains(cfg.String(), "Neues-Auth") {
		t.Errorf("Zugang aus dem Profil: %v %v", cfg, err)
	}

	// Ungültige Eingaben: Klartext für Anwender, nichts gespeichert, kein Kennwort im Text.
	good := SNMPProfileInput{Name: "Drucker", User: "pdh", AuthProto: "sha256", AuthPass: snmpTestAuth, PrivProto: "aes128", PrivPass: snmpTestPriv}
	for what, mod := range map[string]func(*SNMPProfileInput){
		"ohne Name":            func(p *SNMPProfileInput) { p.Name = "  " },
		"Name zu lang":         func(p *SNMPProfileInput) { p.Name = strings.Repeat("ä", 81) },
		"ohne Benutzer":        func(p *SNMPProfileInput) { p.User = "" },
		"MD5":                  func(p *SNMPProfileInput) { p.AuthProto = "md5" },
		"DES":                  func(p *SNMPProfileInput) { p.PrivProto = "des" },
		"kurzes Kennwort":      func(p *SNMPProfileInput) { p.AuthPass = "kurz123" },
		"ohne Verschlüsselung": func(p *SNMPProfileInput) { p.PrivPass = "" },
		"gleicher Name":        func(p *SNMPProfileInput) { p.Name = "SWITCHES halle" },
	} {
		p := good
		mod(&p)
		_, err := h.snmpProfileCreate(ctx, p)
		text, user := snmpUserText(err)
		if err == nil || !user || text == "" || strings.Contains(text, snmpTestAuth) || strings.Contains(text, snmpTestPriv) {
			t.Errorf("%s: %v (Klartext %q, %v)", what, err, text, user)
		}
	}
	if _, err := h.snmpProfileCreate(ctx, SNMPProfileInput{Name: "switches HALLE", User: "x", AuthProto: "sha1", AuthPass: snmpTestAuth,
		PrivProto: "aes128", PrivPass: snmpTestPriv}); !errors.Is(err, errSNMPNameTaken) {
		t.Errorf("gleicher Name in anderer Schreibweise: %v", err)
	}
	other, err := h.snmpProfileCreate(ctx, good)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.snmpProfileUpdate(ctx, other, SNMPProfileInput{Name: "Switches Halle", User: "pdh", AuthProto: "sha256", PrivProto: "aes128"}); !errors.Is(err, errSNMPNameTaken) {
		t.Errorf("Umbenennen auf vorhandenen Namen: %v", err)
	}
	if err := h.snmpProfileUpdate(ctx, other, SNMPProfileInput{Name: "Drucker", User: "pdh", AuthProto: "sha256", AuthPass: "kurz", PrivProto: "aes128"}); err == nil {
		t.Error("zu kurzes neues Kennwort angenommen")
	}
	if n := h.snmpProfileCount(ctx); n != 2 {
		t.Errorf("%d Profile, erwartet 2", n)
	}

	// Löschen: scheitert verständlich, solange ein Gerät das Profil nutzt.
	tid, err := h.snmpTargetCreate(ctx, "10.78.0.1", 0, e.profile, "")
	if err != nil {
		t.Fatal(err)
	}
	err = h.snmpProfileDelete(ctx, e.profile)
	if text, user := snmpUserText(err); !errors.Is(err, errSNMPProfileInUse) || !user || !strings.Contains(text, "noch von Geräten benutzt") {
		t.Errorf("Löschen eines benutzten Profils: %v", err)
	}
	if n := h.snmpProfileCount(ctx); n != 2 {
		t.Error("benutztes Profil wurde gelöscht")
	}
	if list, _ := h.snmpProfiles(ctx); len(list) != 2 || list[1].Targets != 1 {
		t.Errorf("Zahl der Geräte am Profil: %+v", list)
	}
	if err := h.snmpTargetRemove(ctx, tid); err != nil {
		t.Fatal(err)
	}
	if err := h.snmpProfileDelete(ctx, e.profile); err != nil {
		t.Errorf("Löschen ohne Geräte: %v", err)
	}
	if err := h.snmpProfileDelete(ctx, e.profile); !errors.Is(err, errSNMPNotFound) {
		t.Errorf("zweites Löschen: %v", err)
	}
	if err := h.snmpProfileUpdate(ctx, "kein-uuid", good); !errors.Is(err, errSNMPNotFound) {
		t.Errorf("ungültige ID: %v", err)
	}
	// Programmfehler sind kein Klartext für Anwender.
	if _, user := snmpUserText(errors.New("pq: kaputt")); user {
		t.Error("Datenbankfehler als Anwendertext ausgegeben")
	}
}

// Geräte: Adresse nur im Haus, Port 161 als Vorgabe, eindeutig, höchstens 200.
func TestSNMPStoreTargets(t *testing.T) {
	e := newSNMPEnv(t)
	ctx := context.Background()
	h, pool := e.h, e.pool

	id, err := h.snmpTargetCreate(ctx, " SW-Halle1.Firma.Lan ", 0, e.profile, pdhSystemUserID)
	if err != nil {
		t.Fatal(err)
	}
	var by *string
	_ = pool.QueryRow(ctx, `SELECT created_by::text FROM it_snmp_targets WHERE id = $1::uuid`, id).Scan(&by)
	if tg := e.target(id); tg.Host != "sw-halle1.firma.lan" || tg.Port != 161 || !tg.Enabled || tg.ProfileID != e.profile ||
		tg.LastOK != nil || tg.LastError != "" || tg.AssetID != "" || by == nil || *by != pdhSystemUserID {
		t.Errorf("neues Gerät: %+v (angelegt von %v)", tg, by)
	}
	for what, c := range map[string]struct {
		host string
		port int
		prof string
		want error
	}{
		"schon in der Liste":     {"sw-halle1.firma.lan", 161, e.profile, errSNMPTargetTaken},
		"andere Schreibweise":    {"SW-HALLE1.firma.lan", 0, e.profile, errSNMPTargetTaken},
		"unbekanntes Profil":     {"10.78.1.9", 161, "00000000-0000-4000-8000-000000000001", errSNMPNoProfile},
		"Profil keine ID":        {"10.78.1.9", 161, "x", errSNMPNoProfile},
		"Adresse außer Haus":     {"8.8.8.8", 161, e.profile, nil},
		"Adresse des Servers":    {"127.0.0.1", 161, e.profile, nil},
		"Adresse mit Schema":     {"http://10.78.1.9", 161, e.profile, nil},
		"Adresse leer":           {"  ", 161, e.profile, nil},
		"Port zu groß":           {"10.78.1.9", 70000, e.profile, nil},
		"Port negativ":           {"10.78.1.9", -1, e.profile, nil},
		"unvollständige Adresse": {"192.168.1", 161, e.profile, nil},
	} {
		_, err := h.snmpTargetCreate(ctx, c.host, c.port, c.prof, "")
		text, user := snmpUserText(err)
		if err == nil || !user || text == "" || (c.want != nil && !errors.Is(err, c.want)) {
			t.Errorf("%s: %v (Klartext %q, %v)", what, err, text, user)
		}
	}
	// Derselbe Name auf anderem Port ist ein anderes Gerät.
	id2, err := h.snmpTargetCreate(ctx, "sw-halle1.firma.lan", 1161, e.profile, "")
	if err != nil {
		t.Fatalf("anderer Port: %v", err)
	}
	// Ändern: Adresse, Port, Profil; Kollision und ungültige Werte werden abgelehnt.
	p2 := e.mkProfile("USV")
	if err := h.snmpTargetUpdate(ctx, id2, "10.78.1.20", 0, p2); err != nil {
		t.Fatal(err)
	}
	if tg := e.target(id2); tg.Host != "10.78.1.20" || tg.Port != 161 || tg.ProfileName != "USV" {
		t.Errorf("geändert: %+v", tg)
	}
	if err := h.snmpTargetUpdate(ctx, id2, "sw-halle1.firma.lan", 161, p2); !errors.Is(err, errSNMPTargetTaken) {
		t.Errorf("Ändern auf vorhandenes Gerät: %v", err)
	}
	if err := h.snmpTargetUpdate(ctx, id2, "1.1.1.1", 161, p2); err == nil {
		t.Error("Ändern auf Adresse außer Haus angenommen")
	}
	if err := h.snmpTargetUpdate(ctx, e.profile, "10.78.1.21", 161, p2); !errors.Is(err, errSNMPNotFound) {
		t.Errorf("Ändern eines unbekannten Geräts: %v", err)
	}
	// Ein-/Ausschalten; Ausschalten leert den Fehlertext.
	if _, err := pool.Exec(ctx, `UPDATE it_snmp_targets SET last_error = 'Keine Antwort' WHERE id = $1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	if err := h.snmpTargetSetEnabled(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if tg := e.target(id); tg.Enabled || tg.LastError != "" {
		t.Errorf("ausgeschaltet: %+v", tg)
	}
	if err := h.snmpTargetSetEnabled(ctx, id, true); err != nil || !e.target(id).Enabled {
		t.Errorf("eingeschaltet: %v", err)
	}
	if err := h.snmpTargetSetEnabled(ctx, p2, true); !errors.Is(err, errSNMPNotFound) {
		t.Errorf("unbekanntes Gerät: %v", err)
	}

	// Grenze: 200 Geräte.
	if _, err := pool.Exec(ctx, `INSERT INTO it_snmp_targets (host, profile_id)
		SELECT '10.79.' || (g / 250) || '.' || (g % 250 + 1), $1::uuid FROM generate_series(1, 198) g`, e.profile); err != nil {
		t.Fatal(err)
	}
	if _, err := h.snmpTargetCreate(ctx, "10.78.1.30", 161, e.profile, ""); !errors.Is(err, errSNMPTargetLimit) {
		t.Errorf("201. Gerät: %v", err)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM it_snmp_targets`).Scan(&n)
	if n != snmpMaxTargets {
		t.Errorf("%d Geräte, erwartet %d", n, snmpMaxTargets)
	}
}

// „Prüfen“: sofortige Abfrage mit dem Zugang des Profils; gespeichert wird nichts.
func TestSNMPCheck(t *testing.T) {
	e := newSNMPEnv(t)
	ctx := context.Background()
	id, fd := e.dev("10.78.2.1", snmpSwitch("pr-a", "02:78:02:00:00:01"))
	d, err := e.h.snmpTargetCheck(ctx, id)
	if err != nil || d.Name != "pr-a" || d.Model != "XS-24" || d.Firmware != "1.2.3" || len(d.Ports) != 2 {
		t.Fatalf("Prüfen: %+v %v", d, err)
	}
	if tg := e.target(id); tg.LastOK != nil || tg.AssetID != "" {
		t.Errorf("Prüfen hat etwas gespeichert: %+v", tg)
	}
	e.set(fd, func(f *fakeSNMPDev) { f.detailErr = snmpErrAuth })
	_, err = e.h.snmpTargetCheck(ctx, id)
	if text, user := snmpUserText(err); !user || text != "Zugang abgelehnt – Profil prüfen" || snmp.Classify(err) != snmp.KindAuth {
		t.Errorf("abgelehnter Zugang: %v", err)
	}
	// vor dem Aufnehmen: Adresse, Port und Profil frei wählbar; eine Adresse ohne Gerät antwortet nicht
	if _, err := e.h.snmpCheck(ctx, "10.78.2.99", 0, e.profile); snmp.Classify(err) != snmp.KindTimeout {
		t.Errorf("kein Gerät: %v", err)
	}
	if _, err := e.h.snmpCheck(ctx, "8.8.8.8", 161, e.profile); snmp.Classify(err) != snmp.KindConfig {
		t.Errorf("Adresse außer Haus: %v", err)
	}
	if _, err := e.h.snmpCheck(ctx, "10.78.2.1", 161, "00000000-0000-4000-8000-000000000001"); !errors.Is(err, errSNMPNoProfile) {
		t.Errorf("unbekanntes Profil: %v", err)
	}
	if _, err := e.h.snmpTargetCheck(ctx, e.profile); !errors.Is(err, errSNMPNotFound) {
		t.Errorf("unbekanntes Gerät: %v", err)
	}
}
