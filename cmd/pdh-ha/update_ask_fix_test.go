package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"pdh/internal/ha"
)

// askDaemon: aktiver Server mit offener Nachfrage (Frist in der Zukunft).
func askDaemon(t *testing.T) *daemon {
	t.Helper()
	d, _ := upActive(t)
	j := rollingJob(ha.UpAsk, 2)
	j.Source = srcZeitplan
	j.PhaseAt = d.wall()
	j.AskUntil = j.PhaseAt.Add(ha.UpdateAskWait)
	d.mu.Lock()
	d.setJobLocked(j)
	d.mu.Unlock()
	return d
}

func TestAskNewJobDropsOldConfirm(t *testing.T) {
	d := askDaemon(t)
	d.mu.Lock()
	d.up.confirm, d.up.confirmBlocked = "Anna", "Spiegel nicht synchron"
	d.up.job.Phase, d.up.job.Result = ha.UpDone, "fertig"
	j := rollingJob(ha.UpAsk, 5)
	j.ID = "j2"
	j.PhaseAt = d.wall()
	j.AskUntil = j.PhaseAt.Add(ha.UpdateAskWait)
	err := d.startUpdateLocked(j)
	conf, blocked := d.up.confirm, d.up.confirmBlocked
	d.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if conf != "" || blocked != "" {
		t.Fatalf("alte Bestätigung blieb: %q %q", conf, blocked)
	}
	d.tick(context.Background())
	if d.up.job.Phase != ha.UpAsk {
		t.Fatalf("neue Nachfrage darf im ersten Takt nicht entschieden werden: %+v", d.up.job)
	}
}

func TestConfirmAfterDeadline(t *testing.T) {
	d := askDaemon(t)
	d.mu.Lock()
	d.up.job.AskUntil = d.wall().Add(-time.Second)
	d.mu.Unlock()
	if err := d.confirmUpdate(updateConfirmReq{By: "Anna"}); err == nil || !strings.Contains(err.Error(), "Keine Nachfrage offen") {
		t.Fatalf("nach Fristende: %v", err)
	}
}

func TestConfirmAfterAbort(t *testing.T) {
	d := askDaemon(t)
	d.mu.Lock()
	d.up.abort = "Berta"
	d.mu.Unlock()
	if err := d.confirmUpdate(updateConfirmReq{By: "Anna"}); err == nil || !strings.Contains(err.Error(), "bereits abgesagt") {
		t.Fatalf("nach Absage: %v", err)
	}
	if d.up.confirm != "" {
		t.Fatalf("Bestätigung darf nicht vorgemerkt sein: %q", d.up.confirm)
	}
}

func TestMaintenanceDuringAsk(t *testing.T) {
	d := askDaemon(t)
	if err := d.setMaintenance(true); err == nil || !strings.Contains(err.Error(), "Nachfrage offen") {
		t.Fatalf("Wartung in der Nachfrage: %v", err)
	}
	d.mu.Lock()
	d.up.job.Phase = ha.UpPrepare
	d.mu.Unlock()
	if err := d.setMaintenance(true); err == nil || !strings.Contains(err.Error(), "pdh-ha update abort") {
		t.Fatalf("Wartung im Update: %v", err)
	}
}

func TestUpdateAskExpiredWhileDown(t *testing.T) {
	st := store{etc: t.TempDir(), state: t.TempDir()}
	for _, err := range []error{
		st.saveNode(nodeConfig{ID: "a", Kind: "node", CAHolder: true}), st.saveCluster(duoCluster()),
		st.saveState(ha.Persisted{Role: ha.Active, Term: 1, PartnerVersion: "test"}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	j := rollingJob(ha.UpAsk, 2)
	j.Source = srcZeitplan
	j.PhaseAt = time.Now().Add(-10 * time.Minute)
	j.AskUntil = j.PhaseAt.Add(ha.UpdateAskWait)
	if err := st.saveJob(j); err != nil {
		t.Fatal(err)
	}
	fn := newFakeNet()
	fn.setView("b", upPeerView("", ""))
	d := loadFakeDaemon(t, st, fn)
	d.booting = false
	d.tick(context.Background())
	if d.up.job.Phase != ha.UpFailed || d.up.job.Result != "Update entfallen: nicht bestätigt" {
		t.Fatalf("Nachfrage nach Ablauf: %+v", d.up.job)
	}
}
