//go:build integration

// Befunde der Schlussprüfung (Schutz, eingebaute Verbindung, Copilot).
package web

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/modules/calc"
)

// I2: Platzhalter in der Kennung der Abfrage umgehen den Schutz nicht.
func TestQueryDeleteWildcardBlocked(t *testing.T) {
	e := newCalcWebEnv(t)
	ctx := context.Background()
	qid := e.id(t, `INSERT INTO connection_queries (connection_id, name) VALUES ($1::uuid, 'Stände') RETURNING id::text`, e.imp)
	col := e.id(t, `INSERT INTO import_mappings (connection_id, source_ref, infrastructure_id, name, note, created_by, last_value, last_received_at)
		VALUES ($1::uuid, $2, $3::uuid, $4, '', $5::uuid, '7', NOW()) RETURNING id::text`, e.imp, queryRef(qid, "stand"), e.assetA, "Stand "+e.sfx, e.uid)
	if _, err := e.st.Save(ctx, calc.SaveInput{Name: "Doppelt " + e.sfx, InfraID: e.assetA, Formula: "S * 2",
		Inputs: []calc.InputRef{{Short: "S", MappingID: col}}}, e.uid); err != nil {
		t.Fatal(err)
	}
	h := e.handler(func(string) bool { return true })
	wild := strings.Repeat("_", 36)
	w := calcPostForm(h.ConnectionQueryDeleteWeb, e.uid, "/x", url.Values{"with_mappings": {"1"}}, map[string]string{"id": e.imp, "qid": wild})
	if w.Code != http.StatusNotFound {
		t.Errorf("Platzhalter-Kennung: %d %s", w.Code, location(w))
	}
	if !e.exists(t, "import_mappings", col) {
		t.Fatal("der genutzte Eingangswert wurde über Platzhalter gelöscht")
	}
}

// Minor 3: Kann der Schutz nicht prüfen, meldet er einen Fehler statt „keine Nutzer“.
func TestGuardFailsClosed(t *testing.T) {
	e := newCalcWebEnv(t)
	pool, err := pgxpool.New(context.Background(), os.Getenv("PDH_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	h := &Handler{db: pool}
	if _, err := h.calcInputUsers(context.Background(), "mapping", e.a); err == nil {
		t.Error("ohne Datenbank muss der Schutz einen Fehler melden")
	}
}

// Minor 10: Vorlagen und das allgemeine Löschen von Zuordnungen greifen an der
// eingebauten Verbindung nicht.
func TestBuiltinConnectionGenericRoutes(t *testing.T) {
	e := newCalcWebEnv(t)
	ctx := context.Background()
	conn, _ := e.st.ConnectionID(ctx)
	_, id := e.sumValue(t)
	h := e.handler(func(string) bool { return true })
	if w := calcPostForm(h.ImportMappingDeleteWeb, e.uid, "/x", url.Values{"back": {"page"}}, map[string]string{"id": conn, "mappingId": id}); !strings.Contains(location(w)+w.Body.String(), errCalcOwnPage) {
		t.Errorf("Zuordnung an der eingebauten Verbindung löschen: %d %s %s", w.Code, location(w), w.Body.String())
	}
	if !e.exists(t, "import_mappings", id) {
		t.Fatal("der berechnete Wert wurde über die allgemeine Route gelöscht")
	}
	if w := calcPostForm(h.ImportConnectionTemplateSaveWeb, e.uid, "/x", url.Values{"name": {"Vorlage " + e.sfx}}, map[string]string{"id": conn}); !strings.Contains(location(w)+w.Body.String(), errCalcBuiltin) {
		t.Errorf("Vorlage speichern: %d %s", w.Code, location(w))
	}
	if w := calcPostForm(h.ImportConnectionTemplateApplyWeb, e.uid, "/x", url.Values{}, map[string]string{"id": conn, "tid": "00000000-0000-7000-8000-000000000001"}); !strings.Contains(location(w)+w.Body.String(), errCalcBuiltin) {
		t.Errorf("Vorlage anwenden: %d %s", w.Code, location(w))
	}
}

// I5: „Neue Fassung“ vom Copilot dreht Handänderungen am Wert nicht zurück,
// die zwischen Vorschlag und Übernehmen gemacht wurden.
func TestCalcProposalKeepsManualChanges(t *testing.T) {
	e := newCalcWebEnv(t)
	ctx := context.Background()
	d, conn := e.calcCopilot(t, func(string) bool { return true })
	in, id := e.sumValue(t)
	from := localDate(time.Now()).AddDate(0, 0, 7)
	prop := `{"summary":"x","rows":[{"op":"change","value_id":"` + id + `","formula":"A * 3","valid_from":"` + from.Format("2006-01-02") + `",
		"inputs":[{"short":"A","mapping_id":"` + e.a + `"}]}]}`
	if _, err := d.exec(ctx, "propose_calc", []byte(prop)); err != nil {
		t.Fatal(err)
	}
	// von Hand umbenennen, verschieben, Einheit und Höchstalter ändern
	in.Name, in.InfraID, in.Unit, in.MaxAgeMinutes = "Umbenannt "+e.sfx, e.assetB, "MW", nil
	if _, err := e.st.Save(ctx, in, e.uid); err != nil {
		t.Fatal(err)
	}
	p, _ := e.h.loadProposal(ctx, conn.Proposal.ID, e.uid)
	if res := e.h.applyProposal(ctx, p, []int{0}, e.uid, func(string) bool { return true }); res.Changed != 1 || len(res.Errors) != 0 {
		t.Fatalf("übernehmen: %+v", res)
	}
	v, err := e.st.Get(ctx, id)
	if err != nil || v.Name != "Umbenannt "+e.sfx || v.InfraID != e.assetB || v.Unit != "MW" || v.MaxAgeMinutes != nil || v.Versions[0].Formula != "A * 3" {
		t.Errorf("Handänderung zurückgedreht: %+v %v", v, err)
	}
}
