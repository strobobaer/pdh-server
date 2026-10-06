package web

import (
	"context"
	"net/http/httptest"
	"testing"
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
