package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSealOpenRoundTrip(t *testing.T) {
	key := (&Handler{jwtSecret: "t"}).connKey()
	in := map[string]string{"host": "a", "password": "g3h€im"}
	sealed, err := sealConnConfig(key, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sealed["password"]; ok {
		t.Error("Klartextfeld password darf nicht bleiben")
	}
	if !strings.HasPrefix(sealed["password_enc"], "v1:") {
		t.Errorf("password_enc = %q", sealed["password_enc"])
	}
	if in["password"] != "g3h€im" {
		t.Error("Eingabe darf nicht verändert werden")
	}
	open, err := openConnConfig(key, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if open["password"] != "g3h€im" || open["host"] != "a" {
		t.Errorf("geöffnet: %v", open)
	}
	if _, ok := open["password_enc"]; ok {
		t.Error("password_enc darf nach dem Öffnen nicht bleiben")
	}
}

func TestOpenKeepsLegacyPlaintext(t *testing.T) {
	open, err := openConnConfig([]byte("0123456789abcdef0123456789abcdef"), map[string]string{"password": "alt"})
	if err != nil || open["password"] != "alt" {
		t.Errorf("%v %v", open, err)
	}
}

func TestOpenWrongKeyFails(t *testing.T) {
	sealed, _ := sealConnConfig((&Handler{jwtSecret: "a"}).connKey(), map[string]string{"password": "x"})
	open, err := openConnConfig((&Handler{jwtSecret: "b"}).connKey(), sealed)
	if err == nil || open != nil {
		t.Fatal("falscher Schlüssel muss einen Fehler liefern")
	}
	if !strings.Contains(err.Error(), "nicht entschlüsselt werden") {
		t.Errorf("Fehlertext: %v", err)
	}
}

func TestPublicConfigHidesSecrets(t *testing.T) {
	pub := publicConnConfig(map[string]string{"host": "h", "password": "p", "password_enc": "v1:x", "bearer_token_enc": "v1:y", "api_key_value": "k"})
	if len(pub) != 1 || pub["host"] != "h" {
		t.Errorf("öffentlich: %v", pub)
	}
}

func connTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func insertConnRow(t *testing.T, pool *pgxpool.Pool, cfg map[string]string) string {
	t.Helper()
	b, _ := json.Marshal(cfg)
	var id string
	if err := pool.QueryRow(context.Background(), `INSERT INTO import_export_connections (direction, kind, name, config)
		VALUES ('import', 'mssql', 'zz-test-secrets', $1) RETURNING id::text`, b).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM import_export_connections WHERE id=$1`, id)
	})
	return id
}

func TestEncryptConnectionSecretsIdempotent(t *testing.T) {
	pool := connTestPool(t)
	h := &Handler{db: pool, jwtSecret: "t"}
	ctx := context.Background()
	// nur die eigene Zeile anfassen (gemeinsame Test-Datenbank)
	id := insertConnRow(t, pool, map[string]string{"host": "h", "password": "Geheim-Klartext-42"})
	n, err := h.encryptConnectionSecretsIn(ctx, []string{id})
	if err != nil || n != 1 {
		t.Fatalf("erster Lauf: n=%d err=%v", n, err)
	}
	rawOf := func() string {
		var raw string
		if err := pool.QueryRow(ctx, `SELECT config::text FROM import_export_connections WHERE id=$1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	raw := rawOf()
	if strings.Contains(raw, "Geheim-Klartext-42") || !strings.Contains(raw, "password_enc") {
		t.Errorf("Rohtext: %s", raw)
	}
	n, err = h.encryptConnectionSecretsIn(ctx, []string{id})
	if err != nil || n != 0 {
		t.Fatalf("zweiter Lauf: n=%d err=%v", n, err)
	}
	if rawOf() != raw {
		t.Error("zweiter Lauf hat die Zeile verändert")
	}
	_, _, _, cfg, err := h.connectionRow(ctx, id, "import")
	if err != nil || cfg["password"] != "Geheim-Klartext-42" {
		t.Errorf("connectionRow: %v %v", cfg, err)
	}
}

func TestEditKeepsPasswordWhenBlank(t *testing.T) {
	pool := connTestPool(t)
	h := &Handler{db: pool, jwtSecret: "t", hasPermHook: func(*http.Request, string) bool { return true }}
	ctx := context.Background()
	sealed, _ := sealConnConfig(h.connKey(), map[string]string{"host": "h", "password": "altes-Kennwort"})
	id := insertConnRow(t, pool, sealed)

	post := func(form url.Values) {
		t.Helper()
		req := httptest.NewRequest("POST", "/import/connections/"+id+"/edit-web", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", id)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
		w := httptest.NewRecorder()
		h.editConnection(w, req, "import")
		if w.Code != http.StatusSeeOther {
			t.Fatalf("Status %d: %s", w.Code, w.Body.String())
		}
	}
	stored := func() map[string]string {
		t.Helper()
		_, _, _, cfg, err := h.connectionRow(ctx, id, "import")
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	post(url.Values{"name": {"zz-test-secrets"}, "f_mssql_host": {"neuer-host"}, "f_mssql_password": {""}})
	cfg := stored()
	if cfg["password"] != "altes-Kennwort" || cfg["host"] != "neuer-host" {
		t.Errorf("leeres Feld muss Kennwort behalten: %v", cfg)
	}
	post(url.Values{"name": {"zz-test-secrets"}, "f_mssql_host": {"neuer-host"}, "f_mssql_password": {"neues-Kennwort"}})
	if got := stored()["password"]; got != "neues-Kennwort" {
		t.Errorf("neues Kennwort: %q", got)
	}
	var raw string
	_ = pool.QueryRow(ctx, `SELECT config::text FROM import_export_connections WHERE id=$1`, id).Scan(&raw)
	if strings.Contains(raw, "neues-Kennwort") || strings.Contains(raw, "altes-Kennwort") {
		t.Errorf("Klartext in der Spalte: %s", raw)
	}
}

func TestConnectionListHasNoSecret(t *testing.T) {
	pool := connTestPool(t)
	h := &Handler{db: pool, jwtSecret: "t"}
	sealed, _ := sealConnConfig(h.connKey(), map[string]string{"host": "h", "password": "Geheim-Liste-77", "bearer_token": "tok-88"})
	id := insertConnRow(t, pool, sealed)
	// zusätzlich eine alte Klartextzeile: ConfigJSON darf auch sie nicht zeigen
	insertConnRow(t, pool, map[string]string{"host": "h2", "password": "Alt-Klartext-99"})
	conns, err := h.importExportConnections(context.Background(), "import")
	if err != nil {
		t.Fatal(err)
	}
	var mine []ConnectionView
	for _, c := range conns {
		if c.Name == "zz-test-secrets" {
			mine = append(mine, c)
		}
	}
	if len(mine) != 2 {
		t.Fatalf("Verbindungen nicht gefunden: %d (id %s)", len(mine), id)
	}
	out := renderPage(t, loadTestTemplates(t), "import", ConnectionsPageData{Direction: "import", Connections: mine, CanWrite: true, CanExecute: true})
	for _, bad := range []string{"Geheim-Liste-77", "tok-88", "Alt-Klartext-99", "_enc", "v1:"} {
		if strings.Contains(out, bad) {
			t.Errorf("Ausgabe enthält %q", bad)
		}
	}
	if !strings.Contains(out, "zz-test-secrets") {
		t.Error("Verbindung fehlt in der Ausgabe")
	}
}

// Schlüsselwechsel: Metadaten-Aufrufer arbeiten weiter, Pfade mit Kennwort
// melden die deutsche Entschlüsselungsmeldung.
func TestUndecryptableRowMetadataVsSecretPaths(t *testing.T) {
	pool := connTestPool(t)
	old := &Handler{db: pool, jwtSecret: "alter-schluessel"}
	sealed, _ := sealConnConfig(old.connKey(), map[string]string{"host": "h", "password": "x"})
	id := insertConnRow(t, pool, sealed)
	h := &Handler{db: pool, jwtSecret: "neuer-schluessel"}
	ctx := context.Background()

	_, kind, _, cfg, err := h.connectionRow(ctx, id, "import")
	if !isConnSecretErr(err) || kind != "mssql" {
		t.Fatalf("connectionRow: kind=%q err=%v", kind, err)
	}
	if _, ok := cfg["password"]; ok || cfg["host"] != "h" {
		t.Errorf("Fehlerfall liefert nur öffentliche Felder: %v", cfg)
	}
	// Metadaten-Aufrufer (Vorlage anwenden): kommt bis zur Vorlagen-Suche
	_, err = h.applyConnectionTemplate(ctx, id, "import", "00000000-0000-0000-0000-000000000000", "")
	if err == nil || err.Error() != "Vorlage nicht gefunden" {
		t.Errorf("applyConnectionTemplate: %v", err)
	}
	// Pfad mit Kennwort: deutsche Meldung statt „nicht gefunden“
	_, err = h.runConnectionChecks(ctx, id, "import")
	if err == nil || !strings.Contains(err.Error(), "nicht entschlüsselt werden") {
		t.Errorf("runConnectionChecks: %v", err)
	}
	if ok, failed, _ := h.runAllConnQueries(ctx, id); ok != 0 || failed != 0 {
		t.Errorf("runAllConnQueries: %d/%d", ok, failed)
	}
}
