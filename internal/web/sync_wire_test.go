package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/extsync"
)

// syncTestProfile legt ein Abgleich-Profil auf der Verbindung an und setzt den Zustand.
func syncTestProfile(t *testing.T, h *Handler, connID, state string) string {
	t.Helper()
	ctx := context.Background()
	p := &extsync.Profile{Name: fmt.Sprintf("zz-sync-wire-%d", time.Now().UnixNano()), ConnectionID: connID,
		TargetType: extsync.RefTicket, SourceSchema: "dbo", SourceTable: "Meldungen", KeyColumns: []string{"Nr"}}
	if err := h.syncStore().SaveProfile(ctx, p, pdhSystemUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = h.db.Exec(context.Background(), `DELETE FROM sync_profiles WHERE id = $1`, p.ID) })
	if _, err := h.db.Exec(ctx, `UPDATE sync_profiles SET state = $2, test_dry_at = now(), test_write_at = now() WHERE id = $1`, p.ID, state); err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func syncProfileState(t *testing.T, h *Handler, id string) (state, note string, tested bool) {
	t.Helper()
	if err := h.db.QueryRow(context.Background(), `SELECT state, state_note, test_dry_at IS NOT NULL FROM sync_profiles WHERE id = $1`,
		id).Scan(&state, &note, &tested); err != nil {
		t.Fatal(err)
	}
	return
}

// Ändert sich der Weg einer Verbindung, fallen ihre scharfen Profile auf
// „getestet“ zurück; ohne Änderung des Wegs bleibt alles, wie es ist.
func TestRouteChangeDemotesArmedProfiles(t *testing.T) {
	pool := connTestPool(t)
	h := &Handler{db: pool, jwtSecret: "t", hasPermHook: func(*http.Request, string) bool { return true }}
	sealed, _ := sealConnConfig(h.connKey(), map[string]string{"host": "h", "password": "pw", "route": "direct"})
	id := insertConnRow(t, pool, sealed)
	other := insertConnRow(t, pool, sealed)
	armed := syncTestProfile(t, h, id, extsync.StateArmed)
	draft := syncTestProfile(t, h, id, extsync.StateDraft)
	foreign := syncTestProfile(t, h, other, extsync.StateArmed)

	post := func(form url.Values) string {
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
		loc, _ := url.QueryUnescape(w.Header().Get("Location"))
		return loc
	}

	// Gleicher Weg (nur der Host ändert sich): nichts wird zurückgestuft.
	if loc := post(url.Values{"name": {"zz-test-secrets"}, "f_mssql_host": {"h2"}, "f_mssql_route": {"direct"}}); strings.Contains(loc, "Abgleich-Profil") {
		t.Errorf("Hinweis ohne geänderten Weg: %s", loc)
	}
	if st, _, _ := syncProfileState(t, h, armed); st != extsync.StateArmed {
		t.Fatalf("ohne geänderten Weg zurückgestuft: %s", st)
	}
	// Formular ohne Feld „Weg“ (z. B. Detailseite): der gespeicherte Weg bleibt, nichts wird zurückgestuft.
	post(url.Values{"name": {"zz-test-secrets"}, "f_mssql_host": {"h3"}})
	if st, _, _ := syncProfileState(t, h, armed); st != extsync.StateArmed {
		t.Fatalf("ohne Feld Weg zurückgestuft: %s", st)
	}

	// Weg direkt -> VPN: scharfes Profil fällt auf „getestet“, Tests gelten nicht mehr.
	loc := post(url.Values{"name": {"zz-test-secrets"}, "f_mssql_host": {"h3"}, "f_mssql_route": {"vpn:abc"}})
	if !strings.Contains(loc, "1 Abgleich-Profil(e)") || !strings.Contains(loc, "getestet") {
		t.Errorf("Hinweis fehlt: %s", loc)
	}
	st, note, tested := syncProfileState(t, h, armed)
	if st != extsync.StateTested || note != "Weg der Verbindung geändert" || tested {
		t.Fatalf("scharfes Profil: %s / %q / getestet=%v", st, note, tested)
	}
	if st, _, _ := syncProfileState(t, h, draft); st != extsync.StateDraft {
		t.Fatalf("Entwurf verändert: %s", st)
	}
	if st, _, _ := syncProfileState(t, h, foreign); st != extsync.StateArmed {
		t.Fatalf("Profil einer anderen Verbindung verändert: %s", st)
	}
}

// Der Grund, warum eine Verbindung für den Abgleich nicht nutzbar ist, steht in Anwendersprache im Lauf.
func TestSyncConnReasons(t *testing.T) {
	pool := connTestPool(t)
	h := &Handler{db: pool, jwtSecret: "t"}
	ctx := context.Background()
	sealed, _ := sealConnConfig(h.connKey(), map[string]string{"host": "h", "password": "Geheim-Wire-1", "allow_write": "true"})
	id := insertConnRow(t, pool, sealed)

	kind, cfg, err := h.syncConn(ctx, id)
	if err != nil || kind != "mssql" || cfg["password"] != "Geheim-Wire-1" || cfg["allow_write"] != "true" {
		t.Fatalf("lesbare Verbindung: %q %v %v", kind, cfg, err)
	}
	if _, _, err := h.syncConn(ctx, "00000000-0000-4000-8000-000000000000"); err == nil || err.Error() != "Verbindung nicht gefunden" {
		t.Errorf("unbekannte Verbindung: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE import_export_connections SET enabled = false WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.syncConn(ctx, id); err == nil || err.Error() != "Verbindung ist deaktiviert" {
		t.Errorf("deaktivierte Verbindung: %v", err)
	}
	// Anderer Schlüssel: Kennwort nicht lesbar, es erscheint nicht in der Konfiguration.
	h2 := &Handler{db: pool, jwtSecret: "anderer-schluessel"}
	_, cfg, err = h2.syncConn(ctx, id)
	if err == nil || err.Error() != "Kennwort der Verbindung nicht lesbar" {
		t.Errorf("unlesbares Kennwort: %v", err)
	}
	for k, v := range cfg {
		if strings.Contains(v, "Geheim-Wire-1") || strings.HasSuffix(k, "_enc") {
			t.Errorf("Geheimnis in der Konfiguration: %s", k)
		}
	}
}

// Ohne Datenbank (Tests, Spiegel ohne Start) gibt es keinen Taktgeber und keinen Absturz.
func TestStartSyncWithoutDB(t *testing.T) {
	h := &Handler{}
	h.StartSync(context.Background())
	if h.syncRunner != nil {
		t.Fatal("Taktgeber ohne Datenbank gestartet")
	}
	if n := h.syncDemoteOnRouteChange(context.Background(), "x", map[string]string{"route": "direct"}, map[string]string{"route": "vpn:a"}); n != 0 {
		t.Fatalf("Zurückstufen ohne Datenbank: %d", n)
	}
}
