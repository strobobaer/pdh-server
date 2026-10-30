package ha

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// Regressionstests der Korrekturrunde nach dem Gesamtreview (C1–C3, I2, I7, Minors).

// C1: Ohne Zielversion übernimmt der Auftrag die Version, die pdh auf B nach
// dem Einspielen meldet; vor der Sperre zählt am Spiegel nur 200 + Version
// (ein eingeschränkter Spiegel wartet erwartungsgemäß auf die Migration).
func TestUpdateAdoptsTargetVersionFromPartner(t *testing.T) {
	j := upJob(UpdateRolling, UpVerifyPrep)
	j.TargetVersion = ""
	o := upObs("a", Active)
	o.Partner.Commit, o.PartnerPDHVersion, o.MirrorSync = upNew, upToVer, true
	o.PartnerPDHOK = false // migrations_ok=false am Spiegel
	got, _ := UpdateStep(j, o)
	if got.TargetVersion != upToVer {
		t.Fatalf("TargetVersion = %q, will %q", got.TargetVersion, upToVer)
	}
	if got.Phase != UpCountdown {
		t.Errorf("Phase = %q, will %q (eingeschränkter Spiegel mit passender Version genügt)", got.Phase, UpCountdown)
	}
	if got.Seq != j.Seq+1 {
		t.Errorf("Seq = %d, will %d", got.Seq, j.Seq+1)
	}
}

// Ohne Zielversion und ohne Antwort von B: nichts übernehmen, warten.
func TestUpdateNoTargetVersionWaits(t *testing.T) {
	j := upJob(UpdateRolling, UpVerifyPrep)
	j.TargetVersion = ""
	o := upObs("a", Active)
	o.Partner.Commit, o.MirrorSync = upNew, true
	got, acts := UpdateStep(j, o)
	if got.Phase != UpVerifyPrep || got.TargetVersion != "" || len(acts) != 0 || got.Seq != j.Seq {
		t.Errorf("Phase %q Version %q Aktionen %+v Seq %d", got.Phase, got.TargetVersion, acts, got.Seq)
	}
}

// Vor der Sperre: falsche Version am Spiegel reicht nicht.
func TestUpdateVerifyPrepWrongVersion(t *testing.T) {
	j := upJob(UpdateRolling, UpVerifyPrep)
	o := upObs("a", Active)
	o.Partner.Commit, o.PartnerPDHVersion, o.MirrorSync = upNew, "0.77.8", true
	if got, _ := UpdateStep(j, o); got.Phase != UpVerifyPrep {
		t.Errorf("Phase = %q, will %q", got.Phase, UpVerifyPrep)
	}
}

// Prepare rolling: Ergebnis verloren (pdh-ha auf B neu gestartet), B auf X
// mit eingeschränktem Spiegel → weiter zur Prüfung, Version übernommen.
func TestUpdatePrepareLostAckRestrictedMirror(t *testing.T) {
	j := waiting(upJob(UpdateRolling, UpPrepare), OpInstall, "b", 5)
	j.TargetVersion = ""
	o := upObs("a", Active)
	o.Partner.Commit, o.PartnerPDHVersion = upNew, upToVer
	got, _ := UpdateStep(j, o)
	if got.Phase != UpVerifyPrep || got.TargetVersion != upToVer {
		t.Errorf("Phase %q Version %q", got.Phase, got.TargetVersion)
	}
}

// sequential: A übernimmt die eigene Version nach dem Neustart mit X.
func TestUpdateAdoptsTargetVersionFromSelf(t *testing.T) {
	j := locked(UpdateSequential, UpVerifyNew, time.Minute, 10*time.Second)
	j.TargetVersion, j.FreezeAcked, j.FrozenSynced = "", true, true
	o := upObs("a", Active)
	o.SelfCommit, o.SelfPDHVersion = upNew, upToVer
	got, _ := UpdateStep(j, o)
	if got.TargetVersion != upToVer {
		t.Errorf("TargetVersion = %q, will %q", got.TargetVersion, upToVer)
	}
}

// Abschlussmeldung nennt die übernommene Version, nicht die Kurz-SHA.
func TestUpdateDoneNamesVersion(t *testing.T) {
	j := waiting(upJob(UpdateRolling, UpFollow), OpInstall, "a", 5)
	j.Target, j.TargetVersion = "0123456789abcdef0123456789abcdef01234567", ""
	o := upObs("b", Active)
	o.Partner.Commit, o.PartnerPDHOK, o.PartnerPDHVersion = j.Target, true, upToVer
	got, acts := UpdateStep(j, o)
	if got.Phase != UpDone || got.Result != "Update auf "+upToVer+" abgeschlossen" {
		t.Errorf("Phase %q Ergebnis %q %+v", got.Phase, got.Result, acts)
	}
}

// C2: Zurückdrehen vor der Sperre – das Ergebnis geht verloren (pdh-ha auf
// B startet beim Zurückdrehen neu). B auf Alt mit alter Version = Erfolg,
// kein „braucht Hilfe“.
func TestUpdateUndoLostAckPartnerOnOld(t *testing.T) {
	j := waiting(upJob(UpdateRolling, UpRollback), OpRollback, "b", 5)
	j.RollbackSent, j.Result = true, "Prüffrist überschritten"
	o := upObs("a", Active)
	o.Partner.Commit, o.PartnerPDHOK = upOldB, true
	got, acts := UpdateStep(j, o)
	if got.Phase != UpFailed || len(acts) != 0 {
		t.Errorf("Phase %q Aktionen %+v", got.Phase, acts)
	}
	// Partner noch auf X: weiter warten
	o.Partner.Commit = upNew
	if got, _ := UpdateStep(j, o); got.Phase != UpRollback {
		t.Errorf("Partner auf X: Phase %q", got.Phase)
	}
}

// C2: Zurücksetzen in der Sperre – Ergebnis verloren, Partner auf Alt mit
// alter Version → wie Ergebnis ok; mit Spiegel und eigenem pdh fertig.
func TestUpdateRollbackLostAckPartnerOnOld(t *testing.T) {
	j := waiting(locked(UpdateRolling, UpRollback, 5*time.Minute, 3*time.Minute), OpRollback, "b", 5)
	j.RollbackSent, j.Result, j.FrozenSynced = true, "Prüfung der neuen Version fehlgeschlagen", true
	o := upObs("a", Active)
	o.Partner.Commit, o.PartnerPDHOK, o.MirrorSync, o.SelfPDHOK = upOldB, true, true, true
	got, acts := UpdateStep(j, o)
	if got.Phase != UpFailed || got.Locked() {
		t.Errorf("Phase %q Locked %v %+v", got.Phase, got.Locked(), acts)
	}
}

// C3: nacheinander – erst A abschotten und Gleichstand abwarten, dann B einfrieren.
func TestUpdateSequentialSealBeforeFreeze(t *testing.T) {
	now := upT0.Add(time.Minute)
	j := upJob(UpdateSequential, UpCountdown)
	j.CountdownUntil = now
	o := upObs("a", Active)
	got, acts := UpdateStep(j, o)
	if got.Phase != UpLocked || !reflect.DeepEqual(acts, []UpdateAction{localOpA("a", OpSeal, "")}) || !got.Sealed {
		t.Fatalf("Countdown-Ende: Phase %q Aktionen %+v Sealed %v", got.Phase, acts, got.Sealed)
	}
	// Gleichstand bestätigt → B einfrieren
	o.Ack = ack(OpSeal, "a", true, got.WaitSeq)
	got2, acts := UpdateStep(got, o)
	if !reflect.DeepEqual(acts, []UpdateAction{nodeOpA("b", OpFreeze, "")}) || got2.FreezeAcked {
		t.Fatalf("nach Abschotten: %+v FreezeAcked %v", acts, got2.FreezeAcked)
	}
	// Einfrieren bestätigt → Gleichstand festgehalten, X einspielen
	o.Ack = ack(OpFreeze, "b", true, got2.WaitSeq)
	got3, acts := UpdateStep(got2, o)
	if !got3.FreezeAcked || !got3.FrozenSynced || !reflect.DeepEqual(acts, []UpdateAction{localOpA("a", OpInstall, upNew)}) {
		t.Fatalf("nach Einfrieren: FreezeAcked %v FrozenSynced %v %+v", got3.FreezeAcked, got3.FrozenSynced, acts)
	}
}

// C3: Gleichstand gescheitert → Sperre zurück, nur Checkout zurückdrehen,
// X nicht fehlerhaft, klare Meldung; danach fertig ohne Zurücksetzen.
func TestUpdateSequentialSealFails(t *testing.T) {
	j := waiting(locked(UpdateSequential, UpLocked, 30*time.Second, 30*time.Second), OpSeal, "a", 5)
	j.Sealed = true
	o := upObs("a", Active)
	o.Ack = ack(OpSeal, "a", false, 5)
	got, acts := UpdateStep(j, o)
	if got.Phase != UpRollback || got.MarkFaulty || !got.LockedAt.IsZero() || got.Locked() {
		t.Fatalf("Phase %q MarkFaulty %v LockedAt %v", got.Phase, got.MarkFaulty, got.LockedAt)
	}
	const msg = "Spiegel nicht auf Gleichstand – Update abgebrochen, nichts geändert"
	if got.Result != msg {
		t.Errorf("Ergebnis %q", got.Result)
	}
	if len(acts) != 2 || acts[0] != localOpA("a", OpRollbackNoRestart, upOldA) ||
		acts[1].Kind != UpEvent || acts[1].EventKind != "alarm" || !strings.Contains(acts[1].Text, msg) {
		t.Fatalf("Aktionen %+v", acts)
	}
	o.Ack = ack(OpRollbackNoRestart, "a", true, got.WaitSeq)
	fin, acts := UpdateStep(got, o)
	if fin.Phase != UpFailed || fin.Result != msg || fin.MarkFaulty || len(acts) != 0 {
		t.Errorf("Ende: %q %q %v %+v", fin.Phase, fin.Result, fin.MarkFaulty, acts)
	}
}

// C3: Einfrieren gescheitert (A abgeschottet) → Zurücksetzen: erst A
// freigeben (Checkout zurück), dann B auftauen.
func TestUpdateSequentialFreezeFailsUnseals(t *testing.T) {
	j := waiting(locked(UpdateSequential, UpLocked, time.Minute, time.Minute), OpFreeze, "b", 5)
	j.Sealed = true
	o := upObs("a", Active)
	o.Ack = ack(OpFreeze, "b", false, 5)
	got, _ := UpdateStep(j, o)
	if got.Phase != UpRollback {
		t.Fatalf("Phase %q", got.Phase)
	}
	o.Ack = nil
	o.SelfDBPending = true // abgeschottet: Datenbank nur lesend
	got, acts := UpdateStep(got, o)
	if !reflect.DeepEqual(acts, []UpdateAction{localOpA("a", OpRollbackNoRestart, upOldA)}) {
		t.Fatalf("erst A freigeben: %+v", acts)
	}
	o.Ack = ack(OpRollbackNoRestart, "a", true, got.WaitSeq)
	o.SelfDBPending = false
	got, acts = UpdateStep(got, o)
	if got.Sealed || !reflect.DeepEqual(acts, []UpdateAction{nodeOpA("b", OpThaw, "")}) {
		t.Fatalf("dann B auftauen: Sealed %v %+v", got.Sealed, acts)
	}
}

// C3 Verteidigung: eingefrorene Kopie ohne bestätigten Gleichstand wird nie
// hochgestuft – kein Rücktausch, Dringend-Alarm, Sperre aus.
func TestUpdateRollbackWithoutFrozenSyncNoSwapBack(t *testing.T) {
	j := locked(UpdateSequential, UpVerifyNew, 12*time.Minute, 11*time.Minute)
	j.FreezeAcked, j.FrozenSynced = true, false
	o := upObs("a", Active)
	got, acts := UpdateStep(j, o)
	if got.Phase != UpFailed || got.Locked() {
		t.Fatalf("Phase %q Locked %v", got.Phase, got.Locked())
	}
	for _, a := range acts {
		if a.Kind == UpCommand {
			t.Fatalf("Rücktausch trotz fehlendem Gleichstand: %+v", acts)
		}
	}
	if len(acts) == 0 || acts[len(acts)-1].EventKind != "alarm" || !strings.Contains(acts[len(acts)-1].Text, "Dringend") {
		t.Errorf("kein Dringend-Alarm: %+v", acts)
	}
	// Mit bestätigtem Gleichstand: Rücktausch wie bisher
	j.FrozenSynced = true
	if _, acts := UpdateStep(j, o); len(acts) == 0 || acts[0] != cmdA(CmdSwapAbandon) {
		t.Errorf("mit Gleichstand: %+v", acts)
	}
}

// rolling: Übernahme durch First nach demoteFreeze hält den Gleichstand fest.
func TestUpdateRollingSwapMarksFrozenSync(t *testing.T) {
	j := locked(UpdateRolling, UpLocked, time.Minute, time.Minute)
	got, _ := UpdateStep(j, upObs("b", Active))
	if got.Phase != UpVerifyNew || !got.FrozenSynced {
		t.Errorf("Phase %q FrozenSynced %v", got.Phase, got.FrozenSynced)
	}
}

// I2: Tausch-Befehl wiederholen, solange Second aktiv und keine Übergabe läuft.
func TestUpdateLockedRepeatsSwapCommand(t *testing.T) {
	now := upT0.Add(time.Minute)
	j := locked(UpdateRolling, UpLocked, 40*time.Second, 40*time.Second)
	j.CmdAt = now.Add(-40 * time.Second)
	o := upObs("a", Active)
	got, acts := UpdateStep(j, o)
	if !reflect.DeepEqual(acts, []UpdateAction{cmdA(CmdSwapFreeze)}) || !got.CmdAt.Equal(now) {
		t.Fatalf("Wiederholung: %+v CmdAt %v", acts, got.CmdAt)
	}
	// Übergabe läuft: nicht wiederholen
	o.Handover = true
	if _, acts := UpdateStep(j, o); len(acts) != 0 {
		t.Errorf("bei laufender Übergabe: %+v", acts)
	}
	// gerade erst gesendet: nicht wiederholen
	o.Handover = false
	j.CmdAt = now.Add(-5 * time.Second)
	if _, acts := UpdateStep(j, o); len(acts) != 0 {
		t.Errorf("kurz nach dem Befehl: %+v", acts)
	}
}

// I2: eigene Tauschfrist (3 min) → Zurücksetzen; X nicht fehlerhaft.
func TestUpdateLockedSwapDeadline(t *testing.T) {
	j := locked(UpdateRolling, UpLocked, 3*time.Minute+time.Second, 3*time.Minute+time.Second)
	j.CmdAt = upT0
	o := upObs("a", Active)
	got, acts := UpdateStep(j, o)
	if got.Phase != UpRollback || got.MarkFaulty || !strings.Contains(got.Result, "Rollentausch") {
		t.Fatalf("Phase %q MarkFaulty %v Ergebnis %q %+v", got.Phase, got.MarkFaulty, got.Result, acts)
	}
	// Läuft gerade eine Übergabe, zählt die Frist noch nicht.
	o.Handover = true
	if got, _ := UpdateStep(j, o); got.Phase != UpLocked {
		t.Errorf("mit laufender Übergabe: %q", got.Phase)
	}
}

// I2: Rücktausch im Zurücksetzen wiederholen, solange der neue Server noch aktiv ist.
func TestUpdateRollbackRepeatsSwapBack(t *testing.T) {
	now := upT0.Add(time.Minute)
	j := locked(UpdateRolling, UpRollback, 5*time.Minute, 40*time.Second)
	j.FrozenSynced, j.CmdAt = true, now.Add(-30*time.Second)
	o := upObs("b", Active) // b führt X aus und muss abgeben
	got, acts := UpdateStep(j, o)
	if !reflect.DeepEqual(acts, []UpdateAction{cmdA(CmdSwapAbandon)}) || !got.CmdAt.Equal(now) {
		t.Fatalf("Rücktausch nicht wiederholt: %+v", acts)
	}
	o.Handover = true
	if _, acts := UpdateStep(j, o); len(acts) != 0 {
		t.Errorf("bei laufender Übergabe: %+v", acts)
	}
}

// Minor: Auftauen gescheitert, A spielt X trotzdem ein → Warnung statt Info.
func TestUpdateDoneUnprotectedWarns(t *testing.T) {
	j := waiting(locked(UpdateRolling, UpRelease, 3*time.Minute, time.Minute), OpThaw, "a", 5)
	o := upObs("b", Active)
	o.Ack = ack(OpThaw, "a", false, 5)
	got, _ := UpdateStep(j, o)
	if got.Phase != UpFollow || !got.Unprotected {
		t.Fatalf("Phase %q Unprotected %v", got.Phase, got.Unprotected)
	}
	o = upObs("b", Active)
	o.Partner.Commit, o.PartnerPDHOK = upNew, true
	fin, acts := UpdateStep(got, o)
	want := "Update auf 0.77.9 abgeschlossen – Spiegel ungeschützt, bitte prüfen"
	if fin.Phase != UpDone || fin.Result != want ||
		!reflect.DeepEqual(acts, []UpdateAction{evA("alarm", want)}) {
		t.Errorf("Ende: %q %q %+v", fin.Phase, fin.Result, acts)
	}
}
