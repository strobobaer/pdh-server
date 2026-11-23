//go:build integration

// Copilot-Vorschläge (copilot_proposals) gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run 'Proposal' ./internal/web/
package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"pdh/internal/core/users"
)

func TestProposalRoundTrip(t *testing.T) {
	e := newMappingEnv(t)
	ctx := context.Background()
	other := e.id(t, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'A', 'B', 'viewer', '', '') RETURNING id::text`, "zv"+e.sfx)
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1::uuid`, other) })

	p := &mappingProposal{UserID: e.uid, ConnectionID: e.imp, Direction: "import", Summary: "Nichts ändern",
		Rows: []proposalRow{{Op: "keep", SourceRef: "Strom"}}, Links: []copilotLink{{Title: "Quelle", URL: "https://example.org/a"}}}
	if err := e.h.saveProposal(ctx, p); err != nil || p.ID == "" {
		t.Fatal(err, p.ID)
	}
	got, err := e.h.loadProposal(ctx, p.ID, e.uid)
	if err != nil || got.Summary != "Nichts ändern" || len(got.Rows) != 1 || got.Kind != "csv" || len(got.Links) != 1 {
		t.Fatalf("laden: %+v %v", got, err)
	}
	if _, err := e.h.loadProposal(ctx, p.ID, other); !errors.Is(err, errProposalGone) {
		t.Errorf("fremd: %v", err)
	}
	if _, err := e.h.loadProposal(ctx, "00000000-0000-0000-0000-000000000000", e.uid); !errors.Is(err, errProposalGone) {
		t.Errorf("unbekannt: %v", err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE copilot_proposals SET expires_at = NOW() - INTERVAL '1 hour' WHERE id = $1::uuid`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.loadProposal(ctx, p.ID, e.uid); !errors.Is(err, errProposalExpired) {
		t.Errorf("abgelaufen: %v", err)
	}
}

func TestApplyProposal(t *testing.T) {
	e := newMappingEnv(t)
	ctx := context.Background()
	mq := e.id(t, `INSERT INTO import_export_connections (direction, kind, name, config) VALUES ('import', 'mqtt', $1, '{}'::jsonb) RETURNING id::text`, "Mq "+e.sfx)
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM import_export_connections WHERE id = $1::uuid`, mq)
	})
	m0, _, err := e.h.insertImportMapping(ctx, mq, e.uid, importMappingCreateInput{SourceRef: "halle/a/temp", InfrastructureID: e.assetA, Name: "Temp"})
	if err != nil {
		t.Fatal(err)
	}
	onlyA := func(id string) bool { return id == e.assetA }
	p := &mappingProposal{UserID: e.uid, ConnectionID: mq, Direction: "import", Summary: "Halle A", Rows: []proposalRow{
		{Op: "add", SourceRef: "halle/a/strom", InfrastructureID: e.assetA, Name: "Strom", Reason: "Thema enthält strom"},
		{Op: "change", MappingID: m0, Name: "Öltemperatur", Note: "Einheit °C"},
		{Op: "add", SourceRef: "halle/b/druck", InfrastructureID: e.assetB, Name: "Druck"},
		{Op: "delete", MappingID: m0},
	}}
	if err := e.h.checkProposalRows(ctx, p, onlyA); err != nil {
		t.Fatal(err)
	}
	if !p.Rows[0].Valid || !p.Rows[1].Valid || p.Rows[2].Valid || p.Rows[2].Problem != errAssetDenied || p.Rows[3].Valid || p.Rows[3].Problem != "Löschen bitte von Hand" {
		t.Fatalf("Prüfung: %+v", p.Rows)
	}
	if p.Rows[0].AssetPath == "" {
		t.Error("Pfad der Anlage fehlt")
	}
	if err := e.h.saveProposal(ctx, p); err != nil {
		t.Fatal(err)
	}

	res := e.h.applyProposal(ctx, p, []int{0, 1, 2}, e.uid, onlyA)
	if res.Created != 1 || res.Changed != 1 || len(res.Errors) != 1 || res.Errors[0].Index != 2 {
		t.Fatalf("Übernehmen: %+v", res)
	}
	if n := e.count(t, "import_mappings", mq); n != 2 {
		t.Errorf("Zuordnungen: %d", n)
	}
	var name, note string
	if err := e.pool.QueryRow(ctx, `SELECT name, note FROM import_mappings WHERE id = $1::uuid`, m0).Scan(&name, &note); err != nil || name != "Öltemperatur" || note != "Einheit °C" {
		t.Errorf("geändert: %q %q %v", name, note, err)
	}
	again, err := e.h.loadProposal(ctx, p.ID, e.uid)
	if err != nil || !again.Rows[0].Applied || !again.Rows[1].Applied || again.AppliedAt == nil {
		t.Fatalf("gespeichert: %+v %v", again, err)
	}
	res = e.h.applyProposal(ctx, again, []int{0, 1}, e.uid, onlyA)
	if res.Skipped != 2 || res.Created != 0 || res.Changed != 0 || len(res.Errors) != 0 {
		t.Errorf("zweites Mal: %+v", res)
	}
	res = e.h.applyProposal(ctx, again, []int{3, 9}, e.uid, onlyA)
	if len(res.Errors) != 2 || res.Errors[0].Error != "Löschen bitte von Hand" {
		t.Errorf("Löschen/ungültige Zeile: %+v", res)
	}
	if n := e.count(t, "import_mappings", mq); n != 2 {
		t.Errorf("nichts gelöscht: %d", n)
	}
}

// Route „Übernehmen“: fremder, abgelaufener Vorschlag, fehlendes Recht und
// Wartung werden mit dem Grund abgewiesen und legen nichts an.
func TestProposalApplyRoute(t *testing.T) {
	e := newMappingEnv(t)
	ctx := context.Background()
	p := &mappingProposal{UserID: e.uid, ConnectionID: e.imp, Direction: "import",
		Rows: []proposalRow{{Op: "add", SourceRef: "q:x:y", InfrastructureID: e.assetA, Name: "Strom", Valid: true}}}
	if err := e.h.saveProposal(ctx, p); err != nil {
		t.Fatal(err)
	}
	call := func(h *Handler, uid, id, rows string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/copilot/proposals/"+id+"/apply", strings.NewReader("rows="+rows))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", id)
		c := context.WithValue(r.Context(), chi.RouteCtxKey, rc)
		w := httptest.NewRecorder()
		h.CopilotProposalApplyWeb(w, r.WithContext(context.WithValue(c, "user", &users.User{ID: uid, Role: "admin"})))
		return w
	}
	allowed := &Handler{db: e.pool, hasPermHook: func(*http.Request, string) bool { return true }}
	denied := &Handler{db: e.pool, hasPermHook: func(*http.Request, string) bool { return false }}

	if w := call(allowed, "00000000-0000-0000-0000-000000000001", p.ID, "0"); w.Code != http.StatusNotFound {
		t.Errorf("fremd: %d %s", w.Code, w.Body.String())
	}
	if w := call(denied, e.uid, p.ID, "0"); w.Code != http.StatusForbidden {
		t.Errorf("ohne Recht: %d", w.Code)
	}
	allowed.mlock.locked = true
	if w := call(allowed, e.uid, p.ID, "0"); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "Wartung läuft") {
		t.Errorf("Wartung: %d %s", w.Code, w.Body.String())
	}
	allowed.mlock.locked = false
	if w := call(allowed, e.uid, p.ID, "x"); w.Code != http.StatusBadRequest {
		t.Errorf("Zeilen: %d", w.Code)
	}
	// ohne Rechteverwaltung (rbac) darf niemand an die Anlage: Zeile abgelehnt
	if w := call(allowed, e.uid, p.ID, "0"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"created":0`) || !strings.Contains(w.Body.String(), errAssetDenied) {
		t.Errorf("Anlage gesperrt: %d %s", w.Code, w.Body.String())
	}
	if _, err := e.pool.Exec(ctx, `UPDATE copilot_proposals SET expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1::uuid`, p.ID); err != nil {
		t.Fatal(err)
	}
	if w := call(allowed, e.uid, p.ID, "0"); w.Code != http.StatusGone || !strings.Contains(w.Body.String(), "abgelaufen") {
		t.Errorf("abgelaufen: %d %s", w.Code, w.Body.String())
	}
	if n := e.count(t, "import_mappings", e.imp); n != 0 {
		t.Errorf("nichts angelegt erwartet: %d", n)
	}
}

// Werkzeuge der Verbindungsseite über copilotData.exec: propose_mappings legt
// nur den Vorschlag ab (keine Zuordnung), ohne Schreibrecht gar nichts; das
// Kennwort der Verbindung steht in keiner Ausgabe.
func TestCopilotConnPropose(t *testing.T) {
	e := newMappingEnv(t)
	ctx := context.Background()
	mq := e.id(t, `INSERT INTO import_export_connections (direction, kind, name, config) VALUES ('import', 'mqtt', $1, '{}'::jsonb) RETURNING id::text`, "Mq "+e.sfx)
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM import_export_connections WHERE id = $1::uuid`, mq)
	})
	conn := &copilotConn{Direction: "import", ID: mq, Kind: "mqtt", Name: "Mq", CanWrite: true,
		Config: map[string]string{"ext_password": "geheim123", "ext_host": "10.0.0.5"},
		Allow:  func(id string) bool { return id == e.assetA }}
	d := &copilotData{h: e.h, userID: e.uid, conn: conn, can: func(string) bool { return true }}
	in := `{"summary":"Halle A zuordnen","rows":[
		{"op":"add","source_ref":"halle/a/strom","infrastructure_id":"` + e.assetA + `","name":"Strom","reason":"Thema nennt strom"},
		{"op":"add","source_ref":"halle/b/geheim123","infrastructure_id":"` + e.assetB + `","name":"Druck"}]}`
	out, err := d.exec(ctx, "propose_mappings", []byte(in))
	if err != nil || !strings.Contains(out, `"uebernehmbar": 1`) && !strings.Contains(out, `"uebernehmbar":1`) {
		t.Fatalf("Vorschlag: %s %v", out, err)
	}
	if strings.Contains(out, "geheim123") || !strings.Contains(out, errAssetDenied) {
		t.Errorf("Ausgabe: %s", out)
	}
	if conn.Proposal == nil || conn.Proposal.ID == "" || !conn.Proposal.Rows[0].Valid || conn.Proposal.Rows[1].Valid {
		t.Fatalf("Vorschlag am Kontext: %+v", conn.Proposal)
	}
	if got, err := e.h.loadProposal(ctx, conn.Proposal.ID, e.uid); err != nil || got.Summary != "Halle A zuordnen" || len(got.Rows) != 2 {
		t.Errorf("gespeichert: %+v %v", got, err)
	}
	if n := e.count(t, "import_mappings", mq); n != 0 {
		t.Errorf("Vorschlag darf nichts anlegen: %d", n)
	}
	view := proposalView(conn.Proposal)
	if rows := view["rows"].([]map[string]any); len(rows) != 2 || rows[0]["asset"] == "" || rows[1]["problem"] != errAssetDenied {
		t.Errorf("Ansicht: %+v", view)
	}
	for _, tool := range []string{"connection_info", "mappings", "asset_tree"} {
		res, err := d.exec(ctx, tool, []byte(`{"query":"`+e.sfx+`"}`))
		if err != nil || res == "" || strings.Contains(res, "geheim123") {
			t.Errorf("%s: %s %v", tool, res, err)
		}
	}
	if res, _ := d.exec(ctx, "asset_tree", []byte(`{"query":"Presse A `+e.sfx+`"}`)); !strings.Contains(res, e.assetA) || strings.Contains(res, e.assetB) {
		t.Errorf("Anlagenbaum gefiltert: %s", res)
	}

	conn.CanWrite, conn.Proposal = false, nil
	if _, err := d.exec(ctx, "propose_mappings", []byte(in)); err == nil || conn.Proposal != nil {
		t.Errorf("ohne Schreibrecht: %v %+v", err, conn.Proposal)
	}
	d.conn = nil
	if _, err := d.exec(ctx, "mappings", []byte(`{}`)); err == nil {
		t.Error("ohne Verbindungsseite darf es das Werkzeug nicht geben")
	}
}

// Schlussprüfung I2: Ohne „ausführen“ oder „verwalten“ fragt der Copilot das
// Gerät nicht live ab (kein Mitschnitt am Broker) – er zeigt nur Spalten
// gespeicherter Abfrage-Ergebnisse und nennt das fehlende Recht.
func TestCopilotConnSourceValuesNeedsRight(t *testing.T) {
	e := newMappingEnv(t)
	ctx := context.Background()
	mq := e.id(t, `INSERT INTO import_export_connections (direction, kind, name, config) VALUES ('import', 'mqtt', $1, '{}'::jsonb) RETURNING id::text`, "Mq "+e.sfx)
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM import_export_connections WHERE id = $1::uuid`, mq)
	})
	conn := &copilotConn{Direction: "import", ID: mq, Kind: "mqtt", Name: "Mq",
		Config: map[string]string{"broker_mode": "external", "ext_host": "127.0.0.1", "ext_port": "1"}, Allow: func(string) bool { return true }}
	d := &copilotData{h: e.h, userID: e.uid, conn: conn, can: func(string) bool { return true }}

	out, err := d.exec(ctx, "source_values", []byte(`{}`))
	if err != nil || strings.Contains(out, "Broker nicht erreichbar") || !strings.Contains(out, "Import ausführen") {
		t.Errorf("nur lesen: kein Zugriff auf das Gerät, Hinweis auf das Recht erwartet: %s %v", out, err)
	}
	conn.CanExecute = true
	out, err = d.exec(ctx, "source_values", []byte(`{}`))
	if err != nil || !strings.Contains(out, "Broker nicht erreichbar") {
		t.Errorf("mit Ausführen-Recht wird der Broker gefragt (hier nicht erreichbar): %s %v", out, err)
	}
}
