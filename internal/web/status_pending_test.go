package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// „Wartet“ ist ein eigener, sichtbarer Status – auch am Leitstand.
func TestPendingStatusLabels(t *testing.T) {
	if statusLabel("pending") != "Wartet" || statusClass("pending") != "b-amber" {
		t.Fatalf("pending: %q / %q", statusLabel("pending"), statusClass("pending"))
	}
	if globalStatusLabel("pending") != "Wartet" {
		t.Fatalf("Leitstand: %q", globalStatusLabel("pending"))
	}
}

// Fertigmeldung per Karte am Leitstand darf abschließen, auch ohne Abschluss-Recht der Rolle.
func TestCanFinishViaBoardCard(t *testing.T) {
	h := &Handler{}
	k := completionKinds["maintenance"]
	req := httptest.NewRequest("POST", "/complete/maintenance/x", nil)
	if h.canFinish(req, k) {
		t.Fatal("ohne Recht und ohne Karte darf nicht abgeschlossen werden")
	}
	req = req.WithContext(context.WithValue(req.Context(), boardCompleteKey{}, true))
	if !h.canFinish(req, k) {
		t.Fatal("Fertigmeldung per Karte am Leitstand muss abschließen dürfen")
	}
}

// Am Leitstand braucht der Assistent auch kein Bearbeiten-Recht der Rolle
// (sonst lassen sich Wartungen dort weder laden noch abschließen).
func TestCompletionKindViaBoardCard(t *testing.T) {
	h := &Handler{}
	const id = "11111111-1111-1111-1111-111111111111"
	route := func(r *http.Request) *http.Request {
		rc := chi.NewRouteContext()
		rc.URLParams.Add("type", "maintenance")
		rc.URLParams.Add("id", id)
		return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
	}
	req := route(httptest.NewRequest("GET", "/complete/maintenance/"+id, nil))
	rec := httptest.NewRecorder()
	if _, _, ok := h.completionKind(rec, req); ok || rec.Code != http.StatusForbidden {
		t.Fatalf("ohne Recht und ohne Karte: ok=%v, %d", ok, rec.Code)
	}
	req = req.WithContext(context.WithValue(req.Context(), boardCompleteKey{}, true))
	rec = httptest.NewRecorder()
	if _, got, ok := h.completionKind(rec, req); !ok || got != id {
		t.Fatalf("per Karte am Leitstand abgelehnt: %d %s", rec.Code, rec.Body.String())
	}
}
