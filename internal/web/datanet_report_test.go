package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pdh"
	"pdh/internal/datanet"
)

func TestDataNetReportAPI(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()
	h := &Handler{db: pool}
	suffix := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	name, name2 := "testreport-"+suffix, "testreport2-"+suffix
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM datanet_events WHERE client_name IN ($1, $2)`, name, name2)
		pool.Exec(context.Background(), `DELETE FROM datanet_clients WHERE name IN ($1, $2)`, name, name2)
	})
	c, token, err := h.dataNetCreate(ctx, name, "")
	if err != nil {
		t.Fatal(err)
	}
	c2, token2, err := h.dataNetCreate(ctx, name2, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.dataNetRevoke(ctx, c2.ID, ""); err != nil {
		t.Fatal(err)
	}
	handler := h.dataNetReportHandler()

	post := func(method, bearer string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, datanet.ReportPath, bytes.NewReader(body))
		req.RemoteAddr = "100.64.0.9:5555"
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	body := func(id string) []byte {
		b, _ := json.Marshal(datanet.Report{ClientID: id, Version: "1.2.3", OS: "linux", Hostname: "falsch",
			Targets: []datanet.TargetStatus{{Port: 1433, OK: true, CheckedAt: time.Now()}}})
		return b
	}

	if rec := post(http.MethodPost, "", body(c.ID)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("ohne Token: %d", rec.Code)
	}
	if rec := post(http.MethodPost, token+"x", body(c.ID)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("falsches Token: %d", rec.Code)
	}
	if rec := post(http.MethodPost, token2, body(c2.ID)); rec.Code != http.StatusForbidden {
		t.Fatalf("gesperrt: %d", rec.Code)
	}
	if rec := post(http.MethodPost, token, []byte(`{"client_id":"`+c.ID+`","hostname":"`+strings.Repeat("a", datanet.MaxReportBytes)+`"}`)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("zu groß: %d", rec.Code)
	}
	if rec := post(http.MethodGet, token, nil); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", rec.Code)
	}
	if rec := post(http.MethodPost, token, []byte(`{"client_id":"`+c.ID+`","unbekannt":1}`)); rec.Code != http.StatusBadRequest {
		t.Fatalf("unbekanntes Feld: %d", rec.Code)
	}
	many := datanet.Report{ClientID: c.ID, Targets: make([]datanet.TargetStatus, datanet.MaxTargets+1)}
	mb, _ := json.Marshal(many)
	if rec := post(http.MethodPost, token, mb); rec.Code != http.StatusBadRequest {
		t.Fatalf("zu viele Ziele: %d", rec.Code)
	}

	rec := post(http.MethodPost, token, body(c.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("gültig: %d %s", rec.Code, rec.Body.String())
	}
	var resp datanet.ReportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.IntervalSeconds != 60 || resp.PDHVersion != pdh.Version() {
		t.Fatalf("Antwort %+v", resp)
	}
	// Die Antwort nennt die PDH-Ziele (mindestens den eigenen Server) mit Meldeport.
	if len(resp.PDH) == 0 || !strings.HasPrefix(resp.PDH[0], "pdh-"+h.dataNetServerName()+":8443") {
		t.Fatalf("PDH-Ziele %v", resp.PDH)
	}
	got, err := h.dataNetGet(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Addr != "100.64.0.9" {
		t.Fatalf("addr %q (aus RemoteAddr erwartet)", got.Addr)
	}
	if got.Version != "1.2.3" || len(got.Targets) != 1 || got.LastSeen.IsZero() {
		t.Fatalf("Meldung nicht gespeichert: %+v", got)
	}
}
