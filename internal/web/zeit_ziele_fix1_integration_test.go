//go:build integration

// Zeit ohne Vorgang, Nachbesserung 1: Abteilungsebene des Rechts, Anlage vom Client, CSV ohne Formeln.
package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"pdh/internal/modules/timetracking"
	"pdh/pkg/middleware"
)

func TestZeitZieleAbteilungDesRechts(t *testing.T) {
	z := newZeitEnv(t, true)
	mux := z.timeRouter()
	mine := z.newAsset("Abt-Eigen", z.sub)
	foreign := z.newAsset("Abt-Fremd", z.foreign)
	z.e.exec(`UPDATE infrastructure SET department_id = $2::uuid WHERE id = $1::uuid`, z.foreign, z.otherDept)
	person := z.e.user("zielabt", "viewer", false)
	z.e.grant(person, "it.view", z.dept, true) // Recht nur für eine Abteilung (Rolle ohne Abteilung)
	z.e.warm()
	t.Cleanup(func() {
		z.e.pool.Exec(context.Background(), `DELETE FROM time_entries WHERE user_id = $1::uuid`, person)
	})
	c := z.pdhCookie(person, "viewer")
	rec := z.timeReq(mux, "GET", "/time/it-assets?q=Abt-", nil, c)
	var out struct{ Data []timeAssetOption }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || len(out.Data) != 1 || out.Data[0].ID != mine {
		t.Errorf("Suche mit Abteilungsrecht: %d %+v", rec.Code, out.Data)
	}
	if rec := z.timeReq(mux, "POST", "/time/start-web", url.Values{"target_kind": {"it_asset"}, "it_asset_id": {foreign}}, c); rec.Code != 403 {
		t.Errorf("fremdes Asset direkt: %d", rec.Code)
	}
	if rec := z.timeReq(mux, "POST", "/time/start-web", url.Values{"target_kind": {"it_asset"}, "it_asset_id": {mine}}, c); rec.Code != 204 {
		t.Errorf("eigenes Asset: %d %s", rec.Code, rec.Body.String())
	}
	if z.count(`time_entries WHERE user_id = $1::uuid AND it_asset_id = $2::uuid`, person, foreign) != "0" {
		t.Error("fremdes Asset wurde gebucht")
	}
}

func TestZeitZieleClientAnlageGeprueft(t *testing.T) {
	z := newZeitEnv(t, true)
	mux := z.timeRouter()
	loose := z.newAsset("Ohne-Anlage", "")
	admin := z.e.user("zielinfra", "admin", false)
	z.e.warm()
	t.Cleanup(func() {
		z.e.pool.Exec(context.Background(), `DELETE FROM time_entries WHERE user_id = $1::uuid`, admin)
	})
	c := z.pdhCookie(admin, "admin")
	post := func(f url.Values) *httptest.ResponseRecorder {
		f.Set("started_at", time.Now().Add(-2*time.Hour).Format("2006-01-02T15:04"))
		f.Set("ended_at", time.Now().Add(-1*time.Hour).Format("2006-01-02T15:04"))
		return z.timeReq(mux, "POST", "/time/manual-web", f, c)
	}
	for _, bad := range []string{"00000000-0000-4000-8000-000000000000", "kein-uuid"} {
		rec := post(url.Values{"target_kind": {"other"}, "description": {"x"}, "infrastructure_id": {bad}})
		if rec.Code != 400 || strings.Contains(strings.ToLower(rec.Body.String()), "violates") || !strings.Contains(rec.Body.String(), "nicht gefunden") {
			t.Errorf("Sonstiges mit Anlage %q: %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	if rec := post(url.Values{"target_kind": {"it_asset"}, "it_asset_id": {loose}, "infrastructure_id": {z.plant}}); rec.Code != 204 {
		t.Fatalf("Asset ohne Anlage: %d %s", rec.Code, rec.Body.String())
	}
	if e := z.lastEntry(admin); e.Infra != "" || e.Asset != loose {
		t.Errorf("Anlage vom Client übernommen: %+v", e)
	}
}

func TestZeitZieleCSVOhneFormeln(t *testing.T) {
	z := newZeitEnv(t, true)
	z.h.time = timetracking.NewService(timetracking.NewRepository(z.e.pool))
	admin := z.e.user("zielcsv", "admin", false)
	z.e.warm()
	t.Cleanup(func() {
		z.e.pool.Exec(context.Background(), `DELETE FROM time_entries WHERE user_id = $1::uuid`, admin)
	})
	z.e.exec(`INSERT INTO time_entries (id, user_id, ref_type, ref_id, description, started_at, ended_at, duration_min) VALUES (pdh_uuid_v7(), $1::uuid, 'production', pdh_uuid_v7(), '=HYPERLINK("http://x")', now() - interval '2 minutes', now() - interval '1 minute', 1)`, admin)
	api := timetracking.NewHandler(z.h.time)
	now := time.Now()
	req := httptest.NewRequest("GET", "/export.csv?year="+now.Format("2006")+"&month="+now.Format("1"), nil)
	ctx := context.WithValue(req.Context(), middleware.UserIDKey, admin)
	rec := httptest.NewRecorder()
	api.ExportCSV(rec, req.WithContext(ctx))
	if b := rec.Body.String(); !strings.Contains(b, "'=HYPERLINK") || strings.Contains(b, ",=HYPERLINK") || strings.Contains(b, ",\"=HYPERLINK") {
		t.Errorf("CSV mit Formel:\n%s", b)
	}
}
