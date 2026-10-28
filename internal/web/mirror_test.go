package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"pdh/pkg/database"
)

// mirrorTestHandler: Spiegel-Middleware vor einem Handler, der „ok“ schreibt.
func mirrorTestHandler(on bool) (http.Handler, *int) {
	h := &Handler{}
	h.SetMirror(MirrorInfo{On: on})
	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte("ok"))
	})
	return h.MirrorMiddleware(next), &calls
}

func mirrorServe(hd http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	hd.ServeHTTP(rec, req)
	return rec
}

func TestReadOnlyBlocksWrites(t *testing.T) {
	hd, calls := mirrorTestHandler(true)
	rec := mirrorServe(hd, http.MethodPost, "/tickets/x/comment", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), mirrorReadOnlyMsg) {
		t.Fatalf("POST: %d %q", rec.Code, rec.Body.String())
	}
	if *calls != 0 {
		t.Fatal("POST darf den Handler nicht erreichen")
	}
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if rec := mirrorServe(hd, m, "/", nil); rec.Code != http.StatusOK {
			t.Fatalf("%s /: %d", m, rec.Code)
		}
	}
	if *calls != 3 {
		t.Fatalf("GET/HEAD/OPTIONS müssen durch: %d", *calls)
	}
	// Schnittstelle: JSON wie response.Error
	rec = mirrorServe(hd, http.MethodDelete, "/api/v1/tickets/abc", nil)
	var body struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if rec.Code != http.StatusConflict || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Error != mirrorReadOnlyMsg || body.Success {
		t.Fatalf("API: %d %q", rec.Code, rec.Body.String())
	}
}

func TestReadOnlyAllowsAuth(t *testing.T) {
	hd, calls := mirrorTestHandler(true)
	cases := []struct{ method, path string }{
		{http.MethodPost, "/login"},
		{http.MethodPost, "/login/rfid"},
		{http.MethodGet, "/logout"},
		{http.MethodPost, "/logout"},
		{http.MethodPost, "/lang"},
		{http.MethodPost, "/admin/server-config/ha/command"},
	}
	for _, c := range cases {
		if rec := mirrorServe(hd, c.method, c.path, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s %s: %d", c.method, c.path, rec.Code)
		}
	}
	if *calls != len(cases) {
		t.Fatalf("durchgereicht: %d", *calls)
	}
	for _, p := range []string{"/loginx", "/admin/server-config", "/admin/server-config/hax", "/language"} {
		if rec := mirrorServe(hd, http.MethodPost, p, nil); rec.Code != http.StatusConflict {
			t.Fatalf("POST %s muss gesperrt sein: %d", p, rec.Code)
		}
	}
}

func TestReadOnlyBlocksHTMX(t *testing.T) {
	hd, _ := mirrorTestHandler(true)
	rec := mirrorServe(hd, http.MethodPost, "/tickets/x/comment", map[string]string{"HX-Request": "true"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Header().Get("X-PDH-Spiegel") != "1" || rec.Header().Get("HX-Reswap") != "none" {
		t.Fatalf("Kopfzeilen: %v", rec.Header())
	}
	if !strings.Contains(rec.Body.String(), mirrorReadOnlyMsg) || strings.Contains(rec.Body.String(), "<") {
		t.Fatalf("Text: %q", rec.Body.String())
	}
}

func TestReadOnlyOffPassesAll(t *testing.T) {
	hd, calls := mirrorTestHandler(false)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodGet} {
		if rec := mirrorServe(hd, m, "/tickets/x/comment", nil); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", m, rec.Code)
		}
	}
	if *calls != 4 {
		t.Fatalf("calls %d", *calls)
	}
}

// writeIn25006: Handler, der wie ein Schreibzugriff am Standby scheitert
// (Tracer markiert die Anfrage) und dann 500 meldet.
func writeIn25006(fail bool) http.Handler {
	tr := database.NewReadOnlyTracer()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			tr.TraceQueryEnd(r.Context(), nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "25006"}})
		}
		w.Header().Set("HX-Redirect", "/irgendwo")
		http.Error(w, "interner Fehler", http.StatusInternalServerError)
	})
}

func TestReadOnlyTracerConverts500(t *testing.T) {
	h := &Handler{}
	h.SetMirror(MirrorInfo{On: true})

	// GET-Seite, die nebenbei schreibt
	rec := mirrorServe(h.MirrorMiddleware(writeIn25006(true)), http.MethodGet, "/tickets/x", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), mirrorReadOnlyMsg) || strings.Contains(rec.Body.String(), "interner Fehler") {
		t.Fatalf("mit Marke: %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Redirect") != "" {
		t.Fatal("htmx-Kopfzeilen des Handlers müssen verworfen werden")
	}
	// erlaubter Schreibpfad (z. B. Passwort vergessen) schreibt trotzdem
	rec = mirrorServe(h.MirrorMiddleware(writeIn25006(true)), http.MethodPost, "/login/forgot", map[string]string{"HX-Request": "true"})
	if rec.Code != http.StatusConflict || rec.Header().Get("X-PDH-Spiegel") != "1" {
		t.Fatalf("htmx mit Marke: %d %v", rec.Code, rec.Header())
	}
	// ohne Marke bleibt 500
	rec = mirrorServe(h.MirrorMiddleware(writeIn25006(false)), http.MethodGet, "/tickets/x", nil)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "interner Fehler") {
		t.Fatalf("ohne Marke: %d %q", rec.Code, rec.Body.String())
	}
	// Spiegel aus: keine Marke im Kontext, keine Umwandlung
	off := &Handler{}
	rec = mirrorServe(off.MirrorMiddleware(writeIn25006(true)), http.MethodGet, "/tickets/x", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("Spiegel aus: %d", rec.Code)
	}
}

// Ereignisströme (SSE) und lange Downloads laufen ungepuffert durch.
func TestReadOnlyStreamsPassThrough(t *testing.T) {
	h := &Handler{}
	h.SetMirror(MirrorInfo{On: true})
	flushed := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: 1\n\n"))
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("Flusher fehlt")
			return
		}
		f.Flush()
		flushed = true
		if _, ok := w.(http.Hijacker); !ok {
			t.Error("Hijacker fehlt")
		}
	})
	rec := httptest.NewRecorder()
	h.MirrorMiddleware(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/chat/stream", nil))
	if !flushed || !rec.Flushed || rec.Body.String() != "data: 1\n\n" {
		t.Fatalf("SSE: flushed=%v %q", rec.Flushed, rec.Body.String())
	}
}

func TestPresenceSkippedInMirror(t *testing.T) {
	h := &Handler{}
	h.SetMirror(MirrorInfo{On: true})
	uid := "presence-mirror-test"
	presence.mu.Lock()
	delete(presence.saved, uid)
	presence.mu.Unlock()
	h.presenceTouch(uid)
	presence.mu.Lock()
	_, saved := presence.saved[uid]
	delete(presence.seen, uid)
	presence.mu.Unlock()
	if saved {
		t.Fatal("im Spiegel darf presenceTouch nichts speichern")
	}
}
