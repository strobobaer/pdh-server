package main

import (
	"testing"
	"time"

	"pdh/internal/ha"
)

// Minor (Runde 4): Die Sendeseite von up_lead. Ohne das Feld im Herzschlag wäre der Handweg aus
// R53 am Partner tot: Ein offener Auftrag sendet seine Kennung, sonst „-“.
func TestHeartbeatUpLeadSent(t *testing.T) {
	m := newMDT(t, ha.Active, true, message{Role: "passiv"})
	m.tick()
	m.d.lock()
	hb := m.d.heartbeatLocked(false, nil, time.Now())
	m.d.unlock()
	if hb.UpLead != "-" || hb.UpJob {
		t.Fatalf("ohne Auftrag: up_lead=%q upjob=%v", hb.UpLead, hb.UpJob)
	}
	m.d.lock()
	m.d.up.job = ha.UpdateJob{ID: "auftrag-7", Phase: ha.UpPrepare}
	hb = m.d.heartbeatLocked(false, nil, time.Now())
	m.d.unlock()
	if hb.UpLead != "auftrag-7" || !hb.UpJob {
		t.Fatalf("mit Auftrag: up_lead=%q upjob=%v", hb.UpLead, hb.UpJob)
	}
	m.d.lock()
	m.d.up.job.Phase = ha.UpDone
	hb = m.d.heartbeatLocked(false, nil, time.Now())
	m.d.unlock()
	if hb.UpLead != "-" || hb.UpJob {
		t.Fatalf("beendeter Auftrag: up_lead=%q upjob=%v", hb.UpLead, hb.UpJob)
	}
}
