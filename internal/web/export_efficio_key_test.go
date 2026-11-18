package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"pdh/internal/modules/energie"
)

// SFTP mit Schlüssel: Anmeldeart in der Konfiguration, der Schlüssel ist ein Geheimfeld.
func TestEfficioSFTPKeyConfig(t *testing.T) {
	c, err := efficioConfig("", map[string]string{"target": "sftp", "ftp_host": "h", "ftp_password": "alt", "sftp_auth": "key", "sftp_key": "PEM"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := c.Target.(energie.SFTPTarget)
	if !ok || !st.KeyAuth || st.PrivateKey != "PEM" || st.Password != "" {
		t.Errorf("Ziel mit Schlüssel: %+v", st)
	}
	c, err = efficioConfig("", map[string]string{"target": "sftp", "ftp_host": "h", "ftp_password": "pw", "sftp_key": "PEM"}, nil)
	if st, _ := c.Target.(energie.SFTPTarget); err != nil || st.KeyAuth || st.PrivateKey != "" || st.Password != "pw" {
		t.Errorf("ohne Wahl gilt das Kennwort: %+v %v", st, err)
	}
	if _, err := efficioConfig("", map[string]string{"target": "sftp", "ftp_host": "h", "sftp_auth": "agent"}, nil); err == nil {
		t.Error("unbekannte Anmeldeart angenommen")
	}
	fields, _ := connectionKindFields("export", efficioKind)
	var hasAuth, hasKey bool
	for _, f := range fields {
		hasAuth, hasKey = hasAuth || f == "sftp_auth", hasKey || f == "sftp_key"
	}
	if !hasAuth || !hasKey || !connSecretFields["sftp_key"] {
		t.Fatal("sftp_auth/sftp_key fehlen oder sftp_key ist kein Geheimfeld")
	}
	h := &Handler{jwtSecret: "t"}
	sealed, err := sealConnConfig(h.connKey(), map[string]string{"sftp_auth": "key", "sftp_key": "GEHEIMER-SCHLUESSEL"})
	if err != nil || sealed["sftp_key"] != "" || sealed["sftp_key_enc"] == "" {
		t.Fatalf("nicht verschlüsselt: %+v %v", sealed, err)
	}
	if pub, _ := json.Marshal(publicConnConfig(sealed)); strings.Contains(string(pub), "sftp_key") {
		t.Errorf("Schlüssel im öffentlichen Teil: %s", pub)
	}
	// Bearbeiten der Verbindung (das Formular kennt den Schlüssel nicht) behält ihn
	if kept := keepStoredSecrets(map[string]string{"sftp_auth": "key", "sftp_key": ""}, sealed, fields); kept["sftp_key_enc"] != sealed["sftp_key_enc"] {
		t.Error("Schlüssel geht beim Bearbeiten verloren")
	}
}

// Schlüssel erzeugen: nur mit Recht, nicht auf dem Spiegel, nur bei SFTP mit
// Schlüssel; ein vorhandener wird nur mit replace=1 ersetzt.
func TestEfficioKeyWeb(t *testing.T) {
	env := newEfficioEnv(t, map[string]string{"target": "sftp", "ftp_host": "127.0.0.1", "ftp_username": "pdh", "sftp_auth": "key"})
	ctx := context.Background()
	post := func(form url.Values) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		env.h.EfficioKeyWeb(w, efficioReq("POST", "/x", env.id, "", form))
		return w
	}
	stored := func() map[string]string {
		var raw []byte
		if err := env.pool.QueryRow(ctx, `SELECT config FROM import_export_connections WHERE id = $1::uuid`, env.id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		_ = json.Unmarshal(raw, &m)
		return m
	}
	env.h.hasPermHook = func(*http.Request, string) bool { return false }
	if w := post(url.Values{}); w.Code != http.StatusForbidden {
		t.Errorf("ohne Recht: %d", w.Code)
	}
	env.h.hasPermHook = func(_ *http.Request, p string) bool { return p == "export.write" }
	env.h.mirror = MirrorInfo{On: true}
	if w := post(url.Values{}); w.Code != http.StatusConflict {
		t.Errorf("Spiegel: %d", w.Code)
	}
	env.h.mirror = MirrorInfo{}
	if stored()["sftp_key_enc"] != "" {
		t.Fatal("Schlüssel trotz Ablehnung gespeichert")
	}
	w := post(url.Values{})
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "notice=") {
		t.Fatalf("erzeugen: %d %s", w.Code, w.Header().Get("Location"))
	}
	first := stored()
	if first["sftp_key"] != "" || first["sftp_key_enc"] == "" || strings.Contains(first["sftp_key_enc"], "PRIVATE KEY") || first["ftp_host"] != "127.0.0.1" {
		t.Fatalf("gespeichert: %+v", first)
	}
	plain, err := openConnConfig(env.h.connKey(), first)
	if err != nil {
		t.Fatal(err)
	}
	line, fp, err := energie.SFTPPublicKey(plain["sftp_key"])
	if err != nil || !strings.HasPrefix(line, "ssh-ed25519 ") {
		t.Fatalf("Schlüssel nicht lesbar: %q %v", line, err)
	}
	// die Seite zeigt den öffentlichen Schlüssel, nie den privaten
	d, status := env.h.efficioPageData(efficioReq("GET", "/x", env.id, "", nil), env.id)
	if status != 0 || !d.KeyAuth || d.PublicKey != line || d.KeyFingerprint != fp {
		t.Errorf("Seitendaten: %d %+v", status, d.PublicKey)
	}
	// zweiter Aufruf ohne replace: bleibt
	if w := post(url.Values{}); !strings.Contains(w.Header().Get("Location"), "err=") || stored()["sftp_key_enc"] != first["sftp_key_enc"] {
		t.Error("vorhandener Schlüssel ohne replace ersetzt")
	}
	if w := post(url.Values{"replace": {"1"}}); !strings.Contains(w.Header().Get("Location"), "notice=") || stored()["sftp_key_enc"] == first["sftp_key_enc"] {
		t.Error("replace=1 hat keinen neuen Schlüssel erzeugt")
	}
	// Anmeldung mit Kennwort: kein Schlüssel
	pw := newEfficioEnv(t, map[string]string{"target": "sftp", "ftp_host": "127.0.0.1"})
	w = httptest.NewRecorder()
	pw.h.EfficioKeyWeb(w, efficioReq("POST", "/x", pw.id, "", url.Values{}))
	if !strings.Contains(w.Header().Get("Location"), "err=") {
		t.Errorf("Kennwort-Verbindung: %s", w.Header().Get("Location"))
	}
}
