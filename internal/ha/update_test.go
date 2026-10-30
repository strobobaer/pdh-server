package ha

import (
	"reflect"
	"testing"
	"time"
)

// Testzeitpunkt: 30.10.2026 02:00 – mitten im Wartungsfenster.
var upT0 = time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)

const (
	upNew   = "c-neu"
	upOldA  = "c-alt-a"
	upOldB  = "c-alt-b"
	upToVer = "0.77.9"
)

// upJob: Auftrag im Verbund aus a und b. rolling: b führt X zuerst aus
// (First), a folgt; sequential: a zuerst, b folgt.
func upJob(kind UpdateKind, phase UpdatePhase) UpdateJob {
	j := UpdateJob{
		ID: "j1", Kind: kind, Source: "knopf",
		Target: upNew, TargetVersion: upToVer, OldVersion: "0.77.8",
		Old:   map[string]string{"a": upOldA, "b": upOldB},
		First: "b", Second: "a",
		Phase: phase, PhaseAt: upT0, StartedAt: upT0, Seq: 5,
	}
	switch kind {
	case UpdateSequential:
		j.First, j.Second = "a", "b"
	case UpdateSwap:
		j.Target, j.TargetVersion = "", ""
	}
	return j
}

// upObs: Sicht von self eine Minute nach upT0 mit den Standardfristen.
func upObs(self string, role Role) UpdateObs {
	partner := "b"
	if self == "b" {
		partner = "a"
	}
	return UpdateObs{
		Now: upT0.Add(time.Minute), Self: self, SelfRole: role,
		PartnerID: partner, Partner: PeerObs{ID: partner, Kind: "node", Seen: true},
		Countdown: 2 * time.Minute, LockMax: 30 * time.Minute, Verify: 10 * time.Minute,
	}
}

func nodeOpA(node, op, commit string) UpdateAction {
	return UpdateAction{Kind: UpNodeOp, Node: node, Op: op, Commit: commit}
}
func localOpA(node, op, commit string) UpdateAction {
	return UpdateAction{Kind: UpLocalOp, Node: node, Op: op, Commit: commit}
}
func cmdA(c CommandKind) UpdateAction { return UpdateAction{Kind: UpCommand, Command: c} }
func evA(kind, text string) UpdateAction {
	return UpdateAction{Kind: UpEvent, EventKind: kind, Text: text}
}

// waiting: Auftrag wartet auf das Ergebnis von op auf node (verschickt mit seq).
func waiting(j UpdateJob, op, node string, seq uint64) UpdateJob {
	j.WaitOp, j.WaitNode, j.WaitSeq = op, node, seq
	return j
}

func ack(op, node string, ok bool, seq uint64) *UpdateAck {
	a := &UpdateAck{JobID: "j1", Op: op, Node: node, OK: ok, Seq: seq}
	if !ok {
		a.Err = "Bau fehlgeschlagen"
	}
	return a
}

// locked: Auftrag in einer Phase nach dem Countdown, Sperre seit lockedAgo.
func locked(kind UpdateKind, phase UpdatePhase, lockedAgo, phaseAgo time.Duration) UpdateJob {
	j := upJob(kind, phase)
	now := upT0.Add(time.Minute)
	j.LockedAt = now.Add(-lockedAgo)
	j.PhaseAt = now.Add(-phaseAgo)
	return j
}

type upCase struct {
	name  string
	job   UpdateJob
	obs   UpdateObs
	phase UpdatePhase
	acts  []UpdateAction
	check func(t *testing.T, j UpdateJob)
}

func TestUpdateStepTable(t *testing.T) {
	now := upT0.Add(time.Minute)
	cases := []upCase{
		// Prepare (rolling), Eintritt → node_op install X an First
		func() upCase {
			j := upJob(UpdateRolling, UpPrepare)
			j.PhaseAt, j.StartedAt = time.Time{}, time.Time{}
			return upCase{name: "vorbereiten rolling Eintritt", job: j, obs: upObs("a", Active),
				phase: UpPrepare, acts: []UpdateAction{nodeOpA("b", OpInstall, upNew)},
				check: func(t *testing.T, j UpdateJob) {
					if !j.PhaseAt.Equal(now) || !j.StartedAt.Equal(now) {
						t.Errorf("PhaseAt/StartedAt = %v/%v, will %v", j.PhaseAt, j.StartedAt, now)
					}
					if j.WaitOp != OpInstall || j.WaitNode != "b" || j.WaitSeq != 6 {
						t.Errorf("Warten = %q/%q/%d", j.WaitOp, j.WaitNode, j.WaitSeq)
					}
				}}
		}(),
		// Prepare (sequential), Eintritt → local_op install_norestart X
		func() upCase {
			j := upJob(UpdateSequential, UpPrepare)
			j.PhaseAt = time.Time{}
			return upCase{name: "vorbereiten sequential Eintritt", job: j, obs: upObs("a", Active),
				phase: UpPrepare, acts: []UpdateAction{localOpA("a", OpInstallNoRestart, upNew)}}
		}(),
		// Prepare: Ack ok → VerifyPrep
		{name: "vorbereiten Ack ok", job: waiting(upJob(UpdateRolling, UpPrepare), OpInstall, "b", 5),
			obs:   func() UpdateObs { o := upObs("a", Active); o.Ack = ack(OpInstall, "b", true, 5); return o }(),
			phase: UpVerifyPrep},
		// Prepare: veraltetes Ack (kleinere Seq) wird übergangen
		{name: "vorbereiten Ack veraltet", job: waiting(upJob(UpdateRolling, UpPrepare), OpInstall, "b", 5),
			obs:   func() UpdateObs { o := upObs("a", Active); o.Ack = ack(OpInstall, "b", true, 4); return o }(),
			phase: UpPrepare},
		// Prepare: Ack fehlerhaft → Failed, B zurück, X fehlerhaft
		{name: "vorbereiten Ack fehlerhaft", job: waiting(upJob(UpdateRolling, UpPrepare), OpInstall, "b", 5),
			obs:   func() UpdateObs { o := upObs("a", Active); o.Ack = ack(OpInstall, "b", false, 5); return o }(),
			phase: UpFailed,
			acts: []UpdateAction{nodeOpA("b", OpRollback, upOldB),
				evA("alarm", "Update auf 0.77.9 abgebrochen: Einspielen auf b fehlgeschlagen: Bau fehlgeschlagen; Version als fehlerhaft gemerkt")},
			check: func(t *testing.T, j UpdateJob) {
				if !j.MarkFaulty {
					t.Error("MarkFaulty fehlt")
				}
			}},
		// VerifyPrep: Frist überschritten → Failed
		{name: "pruefen vorher Frist", job: upJob(UpdateRolling, UpVerifyPrep),
			obs:   func() UpdateObs { o := upObs("a", Active); o.Now = upT0.Add(11 * time.Minute); return o }(),
			phase: UpFailed,
			acts: []UpdateAction{nodeOpA("b", OpRollback, upOldB),
				evA("alarm", "Update auf 0.77.9 abgebrochen: Prüffrist überschritten; Version als fehlerhaft gemerkt")},
			check: func(t *testing.T, j UpdateJob) {
				if !j.MarkFaulty {
					t.Error("MarkFaulty fehlt")
				}
			}},
		// VerifyPrep rolling: Partner auf X und pdh ok → Countdown
		{name: "pruefen vorher rolling ok", job: upJob(UpdateRolling, UpVerifyPrep),
			obs: func() UpdateObs {
				o := upObs("a", Active)
				o.Partner.Commit, o.PartnerPDHOK, o.MirrorSync = upNew, true, true
				return o
			}(),
			phase: UpCountdown,
			check: func(t *testing.T, j UpdateJob) {
				if want := now.Add(2 * time.Minute); !j.CountdownUntil.Equal(want) {
					t.Errorf("CountdownUntil = %v, will %v", j.CountdownUntil, want)
				}
			}},
		// VerifyPrep rolling: Partner noch nicht auf X → warten
		{name: "pruefen vorher rolling wartet", job: upJob(UpdateRolling, UpVerifyPrep),
			obs: upObs("a", Active), phase: UpVerifyPrep},
		// VerifyPrep sequential: sofort Countdown
		{name: "pruefen vorher sequential", job: upJob(UpdateSequential, UpVerifyPrep),
			obs: upObs("a", Active), phase: UpCountdown},
		// Countdown läuft noch
		func() upCase {
			j := upJob(UpdateRolling, UpCountdown)
			j.CountdownUntil = now.Add(time.Second)
			return upCase{name: "countdown laeuft", job: j, obs: upObs("a", Active), phase: UpCountdown}
		}(),
		// Countdown vorbei, rolling → Locked, Tausch mit Einfrieren
		func() upCase {
			j := upJob(UpdateRolling, UpCountdown)
			j.CountdownUntil = now
			return upCase{name: "countdown rolling", job: j, obs: upObs("a", Active), phase: UpLocked,
				acts: []UpdateAction{cmdA(CmdSwapFreeze)},
				check: func(t *testing.T, j UpdateJob) {
					if !j.LockedAt.Equal(now) || !j.Locked() {
						t.Errorf("LockedAt = %v, Locked %v", j.LockedAt, j.Locked())
					}
				}}
		}(),
		// Countdown vorbei, sequential → Locked, erst B einfrieren
		func() upCase {
			j := upJob(UpdateSequential, UpCountdown)
			j.CountdownUntil = now
			return upCase{name: "countdown sequential", job: j, obs: upObs("a", Active), phase: UpLocked,
				acts: []UpdateAction{nodeOpA("b", OpFreeze, "")},
				check: func(t *testing.T, j UpdateJob) {
					if j.FreezeAcked || j.WaitOp != OpFreeze {
						t.Errorf("FreezeAcked %v WaitOp %q", j.FreezeAcked, j.WaitOp)
					}
				}}
		}(),
		// Locked sequential: Ack für freeze → lokal X einspielen
		{name: "gesperrt sequential freeze ok",
			job:   waiting(locked(UpdateSequential, UpLocked, time.Minute, time.Minute), OpFreeze, "b", 5),
			obs:   func() UpdateObs { o := upObs("a", Active); o.Ack = ack(OpFreeze, "b", true, 5); return o }(),
			phase: UpLocked, acts: []UpdateAction{localOpA("a", OpInstall, upNew)},
			check: func(t *testing.T, j UpdateJob) {
				if !j.FreezeAcked {
					t.Error("FreezeAcked fehlt")
				}
			}},
		// Locked rolling: B ist aktiv → VerifyNew
		{name: "gesperrt rolling neuer aktiv", job: locked(UpdateRolling, UpLocked, time.Minute, time.Minute),
			obs: upObs("b", Active), phase: UpVerifyNew},
		// Locked sequential: eigener Commit X → VerifyNew
		func() upCase {
			j := locked(UpdateSequential, UpLocked, time.Minute, time.Minute)
			j.FreezeAcked = true
			o := upObs("a", Active)
			o.SelfCommit = upNew
			return upCase{name: "gesperrt sequential neu gestartet", job: j, obs: o, phase: UpVerifyNew}
		}(),
		// VerifyNew: pdh ok → Release, B (bzw. A) auftauen
		{name: "pruefen neu ok", job: locked(UpdateRolling, UpVerifyNew, 2*time.Minute, time.Minute),
			obs:   func() UpdateObs { o := upObs("b", Active); o.SelfPDHOK = true; return o }(),
			phase: UpRelease, acts: []UpdateAction{nodeOpA("a", OpThaw, "")}},
		// VerifyNew: Frist überschritten → Rollback
		{name: "pruefen neu Frist", job: locked(UpdateRolling, UpVerifyNew, 12*time.Minute, 11*time.Minute),
			obs:   upObs("b", Active),
			phase: UpRollback,
			acts: []UpdateAction{cmdA(CmdSwapAbandon),
				evA("alarm", "Update auf 0.77.9 wird zurückgesetzt: Prüfung der neuen Version fehlgeschlagen")},
			check: func(t *testing.T, j UpdateJob) {
				if !j.MarkFaulty {
					t.Error("MarkFaulty fehlt")
				}
			}},
		// Release: Spiegel aufgeholt → Follow, Sperre aus, Partner zieht nach
		{name: "freigabe rolling", job: locked(UpdateRolling, UpRelease, 3*time.Minute, time.Minute),
			obs:   func() UpdateObs { o := upObs("b", Active); o.MirrorSync = true; return o }(),
			phase: UpFollow, acts: []UpdateAction{nodeOpA("a", OpInstall, upNew)},
			check: func(t *testing.T, j UpdateJob) {
				if !j.ReleasedAt.Equal(now) || j.Locked() {
					t.Errorf("ReleasedAt = %v, Locked %v", j.ReleasedAt, j.Locked())
				}
			}},
		{name: "freigabe sequential", job: locked(UpdateSequential, UpRelease, 3*time.Minute, time.Minute),
			obs:   func() UpdateObs { o := upObs("a", Active); o.MirrorSync = true; return o }(),
			phase: UpFollow, acts: []UpdateAction{nodeOpA("b", OpInstall, upNew)}},
		{name: "freigabe wartet auf Spiegel", job: locked(UpdateRolling, UpRelease, 3*time.Minute, time.Minute),
			obs: upObs("b", Active), phase: UpRelease},
		// Follow: Ack ok und Partner auf X → Done
		{name: "nachziehen fertig", job: waiting(upJob(UpdateRolling, UpFollow), OpInstall, "a", 5),
			obs: func() UpdateObs {
				o := upObs("b", Active)
				o.Ack = ack(OpInstall, "a", true, 5)
				o.Partner.Commit, o.PartnerPDHOK = upNew, true
				return o
			}(),
			phase: UpDone, acts: []UpdateAction{evA("info", "Update auf 0.77.9 abgeschlossen")}},
		// Follow: Ack ok, pdh auf dem Partner noch nicht bereit → warten
		{name: "nachziehen Ack ok wartet", job: waiting(upJob(UpdateRolling, UpFollow), OpInstall, "a", 5),
			obs:   func() UpdateObs { o := upObs("b", Active); o.Ack = ack(OpInstall, "a", true, 5); return o }(),
			phase: UpFollow,
			check: func(t *testing.T, j UpdateJob) {
				if !j.WaitOK {
					t.Error("WaitOK fehlt")
				}
			}},
		// Follow: erster Fehler → zweiter Versuch
		{name: "nachziehen zweiter Versuch", job: waiting(upJob(UpdateRolling, UpFollow), OpInstall, "a", 5),
			obs:   func() UpdateObs { o := upObs("b", Active); o.Ack = ack(OpInstall, "a", false, 5); return o }(),
			phase: UpFollow, acts: []UpdateAction{nodeOpA("a", OpInstall, upNew)},
			check: func(t *testing.T, j UpdateJob) {
				if j.Attempts != 1 || j.WaitSeq != 6 {
					t.Errorf("Attempts %d WaitSeq %d", j.Attempts, j.WaitSeq)
				}
			}},
		// Follow: zweiter Fehler → Partner neu aufsetzen, Done
		func() upCase {
			j := waiting(upJob(UpdateRolling, UpFollow), OpInstall, "a", 5)
			j.Attempts = 1
			o := upObs("b", Active)
			o.Ack = ack(OpInstall, "a", false, 5)
			return upCase{name: "nachziehen neu aufsetzen", job: j, obs: o, phase: UpDone,
				acts: []UpdateAction{nodeOpA("a", OpReinit, upNew),
					evA("alarm", "Update auf 0.77.9: a konnte die neue Version nicht einspielen und wird neu aufgesetzt")}}
		}(),
		// Rollback: Self (A) nach dem Rücktausch aktiv → Partner auf Alt
		func() upCase {
			j := locked(UpdateRolling, UpRollback, 12*time.Minute, time.Minute)
			j.Result = "Prüfung der neuen Version fehlgeschlagen"
			return upCase{name: "zuruecksetzen Partner", job: j, obs: upObs("a", Active), phase: UpRollback,
				acts: []UpdateAction{nodeOpA("b", OpRollback, upOldB)},
				check: func(t *testing.T, j UpdateJob) {
					if !j.RollbackSent {
						t.Error("RollbackSent fehlt")
					}
				}}
		}(),
		// Rollback: B (neu) wartet nach CmdSwapAbandon auf den Rücktausch
		{name: "zuruecksetzen wartet auf Ruecktausch", job: locked(UpdateRolling, UpRollback, 12*time.Minute, time.Minute),
			obs: upObs("b", Active), phase: UpRollback},
		// Rollback: Ack ok und Spiegel → Failed, Sperre aus
		func() upCase {
			j := waiting(locked(UpdateRolling, UpRollback, 12*time.Minute, time.Minute), OpRollback, "b", 5)
			j.RollbackSent = true
			j.Result = "Prüfung der neuen Version fehlgeschlagen"
			o := upObs("a", Active)
			o.Ack = ack(OpRollback, "b", true, 5)
			o.MirrorSync = true
			return upCase{name: "zuruecksetzen fertig", job: j, obs: o, phase: UpFailed,
				acts: []UpdateAction{evA("alarm", "Update auf 0.77.9 zurückgesetzt: Prüfung der neuen Version fehlgeschlagen")},
				check: func(t *testing.T, j UpdateJob) {
					if !j.ReleasedAt.Equal(now) || j.Locked() {
						t.Errorf("ReleasedAt = %v", j.ReleasedAt)
					}
				}}
		}(),
		// Höchstdauer in VerifyNew → Rollback
		{name: "hoechstdauer pruefen neu", job: locked(UpdateRolling, UpVerifyNew, 31*time.Minute, 5*time.Minute),
			obs:   upObs("b", Active),
			phase: UpRollback,
			acts: []UpdateAction{cmdA(CmdSwapAbandon),
				evA("alarm", "Update auf 0.77.9 wird zurückgesetzt: Höchstdauer überschritten")},
			check: func(t *testing.T, j UpdateJob) {
				if !j.MarkFaulty {
					t.Error("MarkFaulty fehlt")
				}
			}},
		// Höchstdauer in Locked, Tausch ist nie passiert (A noch aktiv) → X nicht fehlerhaft
		{name: "hoechstdauer gesperrt ohne Tausch", job: locked(UpdateRolling, UpLocked, 31*time.Minute, 31*time.Minute),
			obs:   upObs("a", Active),
			phase: UpRollback,
			acts:  []UpdateAction{evA("alarm", "Update auf 0.77.9 wird zurückgesetzt: Höchstdauer überschritten")},
			check: func(t *testing.T, j UpdateJob) {
				if j.MarkFaulty {
					t.Error("MarkFaulty gesetzt, obwohl der Tausch nicht stattfand")
				}
			}},
		// Höchstdauer im Rollback → Failed, Sperre endet, dringender Alarm
		{name: "hoechstdauer zuruecksetzen", job: locked(UpdateRolling, UpRollback, 45*time.Minute, 11*time.Minute),
			obs:   upObs("a", Active),
			phase: UpFailed,
			acts: []UpdateAction{evA("alarm",
				"Dringend: Update auf 0.77.9 – Höchstdauer überschritten, Zurücksetzen nicht abgeschlossen; Sperre aufgehoben, bitte von Hand prüfen")},
			check: func(t *testing.T, j UpdateJob) {
				if !j.ReleasedAt.Equal(now) || j.Locked() {
					t.Errorf("ReleasedAt = %v", j.ReleasedAt)
				}
			}},
		// Abbruch vor der Sperre → Failed, B zurück
		func() upCase {
			j := upJob(UpdateRolling, UpCountdown)
			j.CountdownUntil = now.Add(time.Minute)
			o := upObs("a", Active)
			o.Abort = "Admin"
			return upCase{name: "abbruch vor sperre", job: j, obs: o, phase: UpFailed,
				acts: []UpdateAction{nodeOpA("b", OpRollback, upOldB),
					evA("info", "Update auf 0.77.9 abgebrochen von Admin")},
				check: func(t *testing.T, j UpdateJob) {
					if j.AbortedBy != "Admin" || j.MarkFaulty {
						t.Errorf("AbortedBy %q MarkFaulty %v", j.AbortedBy, j.MarkFaulty)
					}
				}}
		}(),
		// Abbruch in der Sperre → Rollback, X nicht fehlerhaft
		func() upCase {
			o := upObs("b", Active)
			o.Abort = "Admin"
			return upCase{name: "abbruch in sperre", job: locked(UpdateRolling, UpVerifyNew, 2*time.Minute, time.Minute),
				obs: o, phase: UpRollback,
				acts: []UpdateAction{cmdA(CmdSwapAbandon),
					evA("alarm", "Update auf 0.77.9 wird zurückgesetzt: abgebrochen von Admin")},
				check: func(t *testing.T, j UpdateJob) {
					if j.AbortedBy != "Admin" || j.MarkFaulty {
						t.Errorf("AbortedBy %q MarkFaulty %v", j.AbortedBy, j.MarkFaulty)
					}
				}}
		}(),
		// Übernahme → Failed, Sperre endet
		func() upCase {
			o := upObs("b", Active)
			o.Takeover = true
			return upCase{name: "uebernahme", job: locked(UpdateRolling, UpVerifyNew, 2*time.Minute, time.Minute),
				obs: o, phase: UpFailed,
				acts: []UpdateAction{evA("alarm", "Auftrag abgebrochen: Übernahme")},
				check: func(t *testing.T, j UpdateJob) {
					if j.Locked() {
						t.Error("Sperre nach Übernahme noch an")
					}
				}}
		}(),
		// Kind swap: Eintritt → direkt Countdown
		func() upCase {
			j := upJob(UpdateSwap, UpPrepare)
			j.PhaseAt = time.Time{}
			return upCase{name: "tausch Eintritt", job: j, obs: upObs("a", Active), phase: UpCountdown,
				check: func(t *testing.T, j UpdateJob) {
					if want := now.Add(2 * time.Minute); !j.CountdownUntil.Equal(want) {
						t.Errorf("CountdownUntil = %v", j.CountdownUntil)
					}
				}}
		}(),
		// Kind swap: Countdown → Locked mit CmdSwap
		func() upCase {
			j := upJob(UpdateSwap, UpCountdown)
			j.CountdownUntil = now
			return upCase{name: "tausch countdown", job: j, obs: upObs("a", Active), phase: UpLocked,
				acts: []UpdateAction{cmdA(CmdSwap)}}
		}(),
		// Kind swap: neuer Aktiver ok → Done, Sperre aus
		{name: "tausch fertig", job: locked(UpdateSwap, UpVerifyNew, 2*time.Minute, time.Minute),
			obs:   func() UpdateObs { o := upObs("b", Active); o.SelfPDHOK = true; return o }(),
			phase: UpDone, acts: []UpdateAction{evA("info", "Rollentausch abgeschlossen")},
			check: func(t *testing.T, j UpdateJob) {
				if j.Locked() || !j.ReleasedAt.Equal(now) {
					t.Errorf("ReleasedAt = %v", j.ReleasedAt)
				}
			}},
		// Abgeschlossene Aufträge bleiben unverändert
		{name: "fertig bleibt", job: upJob(UpdateRolling, UpDone), obs: upObs("a", Active), phase: UpDone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := c.job
			got, acts := UpdateStep(c.job, c.obs)
			if got.Phase != c.phase {
				t.Errorf("Phase = %q, will %q", got.Phase, c.phase)
			}
			if !reflect.DeepEqual(acts, c.acts) {
				t.Errorf("Aktionen =\n  %+v\nwill\n  %+v", acts, c.acts)
			}
			if got.Phase != before.Phase && !got.PhaseAt.Equal(c.obs.Now) {
				t.Errorf("PhaseAt = %v, will %v beim Phasenwechsel", got.PhaseAt, c.obs.Now)
			}
			changed := !reflect.DeepEqual(withSeq(got, 0), withSeq(before, 0))
			switch {
			case changed && got.Seq != before.Seq+1:
				t.Errorf("Seq = %d, will %d nach Änderung", got.Seq, before.Seq+1)
			case !changed && got.Seq != before.Seq:
				t.Errorf("Seq = %d ohne Änderung, will %d", got.Seq, before.Seq)
			}
			if c.check != nil {
				c.check(t, got)
			}
		})
	}
}

func withSeq(j UpdateJob, s uint64) UpdateJob { j.Seq = s; return j }

// Ein Neustart von pdh-ha lädt den Auftrag aus update-job.json; die Frist
// läuft ab dem gespeicherten PhaseAt weiter.
func TestUpdateStepResumeAfterRestart(t *testing.T) {
	j := locked(UpdateRolling, UpVerifyNew, 12*time.Minute, 11*time.Minute)
	o := upObs("b", Active) // frisch gestartet: pdh noch nicht bereit
	got, acts := UpdateStep(j, o)
	if got.Phase != UpRollback {
		t.Fatalf("Phase = %q, will %q", got.Phase, UpRollback)
	}
	if len(acts) == 0 || acts[0] != cmdA(CmdSwapAbandon) {
		t.Errorf("Aktionen = %+v, will zuerst %v", acts, CmdSwapAbandon)
	}
	// Innerhalb der Frist geht es in derselben Phase weiter.
	j = locked(UpdateRolling, UpVerifyNew, 6*time.Minute, 5*time.Minute)
	got, acts = UpdateStep(j, o)
	if got.Phase != UpVerifyNew || len(acts) != 0 || got.Seq != j.Seq {
		t.Errorf("innerhalb der Frist: Phase %q, Aktionen %+v, Seq %d", got.Phase, acts, got.Seq)
	}
	// Im Countdown gestartet, Countdown inzwischen abgelaufen: Sperre setzen,
	// aber nur der abgebende Server (Second) gibt den Tausch-Befehl.
	cj := upJob(UpdateRolling, UpCountdown)
	cj.CountdownUntil = upT0
	got, acts = UpdateStep(cj, upObs("b", Active)) // b ist schon aktiv: Tausch lief bereits
	if got.Phase != UpLocked || len(acts) != 0 {
		t.Errorf("Countdown auf First: Phase %q, Aktionen %+v", got.Phase, acts)
	}
}

func TestUpdateReady(t *testing.T) {
	green := func() ReadyObs {
		return ReadyObs{
			Mode: Duo, SelfRole: Active, PartnerSeen: true, PartnerRole: Passive, SameTerm: true,
			SelfCommit: "c1", PartnerCommit: "c1",
			MirrorSync: true, LagBytes: 0, LagLimit: 16 << 20,
			BackupConfigured: true, LastBaseOK: true, LastFilesOK: true,
			FreeSelf: 10 << 30, FreePartner: 10 << 30,
			WitnessExpected: true, WitnessSeen: true,
		}
	}
	cases := []struct {
		name   string
		mod    func(*ReadyObs)
		ok     bool
		reason string
		hints  []string
	}{
		{"alles grün", func(o *ReadyObs) {}, true, "", nil},
		{"Partner nicht gesehen", func(o *ReadyObs) { o.PartnerSeen = false }, false, "Partner nicht erreichbar", nil},
		{"beide aktiv", func(o *ReadyObs) { o.PartnerRole = Active }, false, "Rollen nicht eindeutig (aktiv/aktiv)", nil},
		{"Amtszeit verschieden", func(o *ReadyObs) { o.SameTerm = false }, false, "Amtszeit unterschiedlich", nil},
		{"Spiegel nicht synchron", func(o *ReadyObs) { o.MirrorSync = false }, false, "Spiegel nicht synchron", nil},
		{"Verzögerung zu groß", func(o *ReadyObs) { o.LagBytes = 20 << 20 }, false, "Verzögerung der Spiegelung zu groß", nil},
		{"Wartung", func(o *ReadyObs) { o.Maint = true }, false, "Wartung eingeschaltet", nil},
		{"anderer Vorgang", func(o *ReadyObs) { o.Busy = true }, false, "Ein anderer Vorgang läuft", nil},
		{"Auftrag offen", func(o *ReadyObs) { o.JobOpen = true }, false, "Ein Auftrag läuft bereits", nil},
		{"Stand verschieden", func(o *ReadyObs) { o.PartnerCommit = "c0" }, false, "Server auf unterschiedlichem Stand", nil},
		{"Stand verschieden, Fortsetzen", func(o *ReadyObs) { o.PartnerCommit = "c0"; o.Resume = true; o.JobOpen = true }, true, "", nil},
		{"Stand unbekannt", func(o *ReadyObs) { o.PartnerCommit = "" }, false, "Stand des Quelltexts unbekannt", nil},
		{"Grundsicherung fehlgeschlagen", func(o *ReadyObs) { o.LastBaseOK = false }, false, "Letzte Grundsicherung nicht erfolgreich", nil},
		{"Dateiabgleich fehlgeschlagen", func(o *ReadyObs) { o.LastFilesOK = false }, false, "Letzter Dateiabgleich nicht erfolgreich", nil},
		{"keine Sicherung eingerichtet", func(o *ReadyObs) { o.BackupConfigured, o.LastBaseOK, o.LastFilesOK = false, false, false }, true, "", nil},
		{"wenig Platz hier", func(o *ReadyObs) { o.FreeSelf = 1 << 30 }, false, "Weniger als 2 GB frei auf diesem Server", nil},
		{"wenig Platz Partner", func(o *ReadyObs) { o.FreePartner = 1 << 30 }, false, "Weniger als 2 GB frei auf dem Partner", nil},
		{"Zeuge fehlt", func(o *ReadyObs) { o.WitnessSeen = false }, true, "", []string{"Zeuge nicht erreichbar"}},
		{"mehrere Gründe", func(o *ReadyObs) { o.Maint = true; o.FreeSelf = 0; o.WitnessSeen = false }, false,
			"Wartung eingeschaltet", []string{"Weniger als 2 GB frei auf diesem Server", "Zeuge nicht erreichbar"}},
		{"Einzelbetrieb: nur Platz und Grundsicherung", func(o *ReadyObs) {
			*o = ReadyObs{Mode: Single, SelfRole: Active, BackupConfigured: true, LastBaseOK: true, FreeSelf: 3 << 30}
		}, true, "", nil},
		{"Einzelbetrieb wenig Platz", func(o *ReadyObs) {
			*o = ReadyObs{Mode: Single, FreeSelf: 1 << 30}
		}, false, "Weniger als 2 GB frei auf diesem Server", nil},
		{"Einzelbetrieb Grundsicherung fehlgeschlagen", func(o *ReadyObs) {
			*o = ReadyObs{Mode: Single, BackupConfigured: true, FreeSelf: 3 << 30}
		}, false, "Letzte Grundsicherung nicht erfolgreich", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := green()
			c.mod(&o)
			ok, reason, hints := UpdateReady(o)
			if ok != c.ok || reason != c.reason || !reflect.DeepEqual(hints, c.hints) {
				t.Errorf("UpdateReady = %v, %q, %q; will %v, %q, %q", ok, reason, hints, c.ok, c.reason, c.hints)
			}
		})
	}
}

func TestUpdateViewLocked(t *testing.T) {
	now := upT0.Add(10 * time.Minute)
	j := upJob(UpdateRolling, UpLocked)
	j.LockedAt = now.Add(-5 * time.Minute)
	j.CountdownUntil = j.LockedAt
	v := j.View(now, 30*time.Minute, []string{"c-kaputt"})
	want := UpdateView{
		Active: true, Kind: "rolling", Phase: "gesperrt",
		Steps: []string{"Abmelden", "Rollentausch", "Datenbank aktualisieren", "Prüfen", "Freigabe"},
		Step:  1, Percent: 20, Text: "Rollentausch läuft", RemainingS: 25 * 60,
		Locked: true, CountdownUntil: j.LockedAt,
		FromVersion: "0.77.8", ToVersion: upToVer, Faulty: []string{"c-kaputt"},
	}
	if !reflect.DeepEqual(v, want) {
		t.Errorf("View =\n  %+v\nwill\n  %+v", v, want)
	}
	// Höchstdauer überschritten: Restzeit nie negativ.
	if v := j.View(now.Add(time.Hour), 30*time.Minute, nil); v.RemainingS != 0 || v.Faulty == nil {
		t.Errorf("RemainingS = %d, Faulty = %v", v.RemainingS, v.Faulty)
	}
}

// Schritt, Prozent und Text je Phase.
func TestUpdateViewPhases(t *testing.T) {
	now := upT0.Add(10 * time.Minute)
	cases := []struct {
		kind    UpdateKind
		phase   UpdatePhase
		step    int
		percent int
		text    string
		active  bool
		back    bool
	}{
		{UpdateRolling, UpPrepare, 0, 0, "Neue Version wird auf dem Partner eingespielt", true, false},
		{UpdateSequential, UpPrepare, 0, 0, "Neue Version wird im Hintergrund gebaut", true, false},
		{UpdateRolling, UpVerifyPrep, 0, 3, "Partner wird geprüft", true, false},
		{UpdateSequential, UpVerifyPrep, 0, 3, "Vorbereitung wird geprüft", true, false},
		{UpdateRolling, UpCountdown, 0, 5, "Abmeldung in Kürze – bitte Arbeit speichern", true, false},
		{UpdateSequential, UpLocked, 1, 20, "Server startet mit der neuen Version neu", true, false},
		{UpdateRolling, UpVerifyNew, 3, 60, "Neue Version wird geprüft", true, false},
		{UpdateRolling, UpRelease, 4, 90, "Freigabe – Spiegel holt auf", true, false},
		{UpdateRolling, UpFollow, 4, 100, "Partner zieht die neue Version nach", true, false},
		{UpdateRolling, UpRollback, 3, 60, "Update wird zurückgesetzt", true, true},
		{UpdateRolling, UpDone, 4, 100, "Update abgeschlossen", false, false},
		{UpdateRolling, UpFailed, 4, 100, "Update abgebrochen", false, false},
		{UpdateSwap, UpVerifyNew, 2, 60, "Neuer aktiver Server wird geprüft", true, false},
		{UpdateSwap, UpDone, 3, 100, "Rollentausch abgeschlossen", false, false},
	}
	for _, c := range cases {
		j := upJob(c.kind, c.phase)
		v := j.View(now, 30*time.Minute, nil)
		if v.Step != c.step || v.Percent != c.percent || v.Text != c.text || v.Active != c.active || v.RollingBack != c.back {
			t.Errorf("%s/%s: Step %d Percent %d Text %q Active %v RollingBack %v; will %d %d %q %v %v",
				c.kind, c.phase, v.Step, v.Percent, v.Text, v.Active, v.RollingBack, c.step, c.percent, c.text, c.active, c.back)
		}
	}
	seq := upJob(UpdateSequential, UpLocked).View(now, 30*time.Minute, nil)
	if seq.Steps[1] != "Neustart" {
		t.Errorf("sequential Schritte = %v", seq.Steps)
	}
	sw := upJob(UpdateSwap, UpLocked).View(now, 30*time.Minute, nil)
	if !reflect.DeepEqual(sw.Steps, []string{"Abmelden", "Rollentausch", "Prüfen", "Freigabe"}) {
		t.Errorf("swap Schritte = %v", sw.Steps)
	}
	if v := (UpdateJob{}).View(now, 30*time.Minute, nil); v.Active || v.Locked {
		t.Errorf("leerer Auftrag: %+v", v)
	}
}

// Wege, die nicht als eigene Zeile in der Tabelle des Plans stehen
// (Entscheidungen des Umsetzers, siehe Bericht).
func TestUpdateStepEdgeCases(t *testing.T) {
	// sequential: Einspielen auf A scheitert → A gibt ab (Rücktausch, B mit
	// eingefrorenem Stand wird aktiv), X fehlerhaft.
	j := waiting(locked(UpdateSequential, UpLocked, 2*time.Minute, 2*time.Minute), OpInstall, "a", 5)
	j.FreezeAcked = true
	o := upObs("a", Active)
	o.Ack = ack(OpInstall, "a", false, 5)
	got, acts := UpdateStep(j, o)
	want := []UpdateAction{cmdA(CmdSwapAbandon),
		evA("alarm", "Update auf 0.77.9 wird zurückgesetzt: Einspielen fehlgeschlagen: Bau fehlgeschlagen")}
	if got.Phase != UpRollback || !got.MarkFaulty || !reflect.DeepEqual(acts, want) {
		t.Errorf("sequential Einspielen fehlerhaft: %q %v %+v", got.Phase, got.MarkFaulty, acts)
	}
	// … danach dirigiert B (Second, jetzt aktiv): A zurück auf Alt.
	got, acts = UpdateStep(got, upObs("b", Active))
	if !reflect.DeepEqual(acts, []UpdateAction{nodeOpA("a", OpRollback, upOldA)}) {
		t.Errorf("sequential Rückweg über B: %+v", acts)
	}

	// sequential: Einfrieren scheitert → Zurücksetzen ohne Rücktausch, B nur auftauen.
	j = waiting(locked(UpdateSequential, UpLocked, time.Minute, time.Minute), OpFreeze, "b", 5)
	o = upObs("a", Active)
	o.Ack = ack(OpFreeze, "b", false, 5)
	got, acts = UpdateStep(j, o)
	if got.Phase != UpRollback || got.MarkFaulty || len(acts) != 1 || acts[0].Kind != UpEvent {
		t.Errorf("Einfrieren fehlerhaft: %q %v %+v", got.Phase, got.MarkFaulty, acts)
	}
	got, acts = UpdateStep(got, upObs("a", Active))
	if !reflect.DeepEqual(acts, []UpdateAction{nodeOpA("b", OpThaw, "")}) {
		t.Errorf("Einfrieren fehlerhaft, Auftauen: %+v", acts)
	}

	// Freigabe über der Höchstdauer: Sperre trotzdem aufheben und nachziehen.
	j = locked(UpdateRolling, UpRelease, 31*time.Minute, 2*time.Minute)
	got, acts = UpdateStep(j, upObs("b", Active))
	want = []UpdateAction{
		evA("alarm", "Update auf 0.77.9: Höchstdauer überschritten, Spiegel nicht aufgeholt – Sperre aufgehoben"),
		nodeOpA("a", OpInstall, upNew)}
	if got.Phase != UpFollow || got.Locked() || !reflect.DeepEqual(acts, want) {
		t.Errorf("Freigabe über Höchstdauer: %q locked %v %+v", got.Phase, got.Locked(), acts)
	}

	// Zurücksetzen auf dem Partner scheitert: Alarm, Sperre bleibt.
	j = waiting(locked(UpdateRolling, UpRollback, 12*time.Minute, time.Minute), OpRollback, "b", 5)
	j.RollbackSent = true
	o = upObs("a", Active)
	o.Ack = ack(OpRollback, "b", false, 5)
	o.MirrorSync = true
	got, acts = UpdateStep(j, o)
	if got.Phase != UpRollback || !got.Locked() || got.WaitOp != "" ||
		!reflect.DeepEqual(acts, []UpdateAction{evA("alarm", "Zurücksetzen auf b fehlgeschlagen: Bau fehlgeschlagen")}) {
		t.Errorf("Zurücksetzen fehlerhaft: %q %+v", got.Phase, acts)
	}
	// Dasselbe Ergebnis noch einmal zugestellt: keine zweite Meldung.
	if again, acts := UpdateStep(got, o); len(acts) != 0 || again.Seq != got.Seq {
		t.Errorf("doppeltes Ergebnis: %+v", acts)
	}

	// Nachziehen: Ergebnis ging verloren (pdh-ha auf dem Partner neu
	// gestartet), Partner aber auf X und bereit → fertig.
	j = waiting(upJob(UpdateRolling, UpFollow), OpInstall, "a", 5)
	o = upObs("b", Active)
	o.Partner.Commit, o.PartnerPDHOK = upNew, true
	if got, _ := UpdateStep(j, o); got.Phase != UpDone {
		t.Errorf("Nachziehen ohne Ergebnis: %q", got.Phase)
	}
	// Nachziehen: kein Ergebnis innerhalb der Frist → zweiter Versuch mit neuer Frist.
	o = upObs("b", Active)
	o.Now = upT0.Add(11 * time.Minute)
	got, acts = UpdateStep(j, o)
	if got.Attempts != 1 || !got.PhaseAt.Equal(o.Now) || !reflect.DeepEqual(acts, []UpdateAction{nodeOpA("a", OpInstall, upNew)}) {
		t.Errorf("Nachziehen Frist: Attempts %d %+v", got.Attempts, acts)
	}

	// Abbruch, solange der Eintritt noch nicht lief: nichts zurückzudrehen.
	j = upJob(UpdateRolling, UpPrepare)
	j.PhaseAt = time.Time{}
	o = upObs("a", Active)
	o.Abort = "Admin"
	if got, acts := UpdateStep(j, o); got.Phase != UpFailed || len(acts) != 1 {
		t.Errorf("Abbruch vor Eintritt: %q %+v", got.Phase, acts)
	}
}
