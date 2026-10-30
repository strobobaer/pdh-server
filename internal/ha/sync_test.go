package ha

import "testing"

func syncIn(mode Mode) SyncIn {
	return SyncIn{Mode: mode, Now: 100 * sec, Settings: DefaultSettings(), Sync: true, Streaming: true, LastStreaming: 100 * sec}
}

func TestSyncStepTrioNeedsAck(t *testing.T) {
	in := syncIn(Trio)
	in.Streaming, in.LastStreaming = false, 40*sec
	for now := 41 * sec; now <= 100*sec; now += sec {
		in.Now = now
		out := SyncStep(in)
		if !out.WantSync || out.MarkStale {
			t.Fatalf("%v: ohne Bestätigung asynchron: %+v", now, out)
		}
		if want := now-in.LastStreaming >= in.Settings.AsyncAfter; out.SendStale != want {
			t.Fatalf("%v: SendStale %v, erwartet %v", now, out.SendStale, want)
		}
	}
}

func TestSyncStepTrioAcked(t *testing.T) {
	in := syncIn(Trio)
	in.Streaming, in.LastStreaming, in.StaleAcked = false, 80*sec, true
	out := SyncStep(in)
	if out.WantSync || !out.MarkStale || out.SendStale {
		t.Fatalf("nach Bestätigung asynchron erwartet: %+v", out)
	}
}

func TestSyncStepDuo(t *testing.T) {
	in := syncIn(Duo)
	in.Streaming, in.LastStreaming = false, 80*sec
	out := SyncStep(in)
	if out.WantSync || !out.MarkStale || out.SendStale {
		t.Fatalf("Duo ohne Zeuge asynchron erwartet: %+v", out)
	}
	in.LastStreaming = 95 * sec
	if out := SyncStep(in); !out.WantSync || out.MarkStale {
		t.Fatalf("vor Ablauf synchron bleiben: %+v", out)
	}
}

func TestSyncStepBackToSync(t *testing.T) {
	in := syncIn(Trio)
	in.Sync, in.PartnerStale = false, true
	in.LagBytes = in.Settings.LagLimit + 1
	if out := SyncStep(in); out.WantSync {
		t.Fatalf("zu großer Rückstand, noch nicht synchron: %+v", out)
	}
	in.LagBytes = in.Settings.LagLimit
	if out := SyncStep(in); !out.WantSync || out.SendCurrent {
		t.Fatalf("erst synchron schalten, dann melden: %+v", out)
	}
	in.Sync = true
	if out := SyncStep(in); !out.WantSync || !out.SendCurrent || out.ClearStale {
		t.Fatalf("Trio: current an den Zeugen: %+v", out)
	}
	in.Mode = Duo
	if out := SyncStep(in); !out.ClearStale || out.SendCurrent {
		t.Fatalf("Duo: Vermerk selbst löschen: %+v", out)
	}
}

func TestDataCurrent(t *testing.T) {
	act := func() *PeerObs { return &PeerObs{ID: "a", Seen: true, PG: "primary", Sync: true} }
	limit := int64(16 << 20)
	if !DataCurrent(true, 0, limit, act()) {
		t.Fatal("alles erfüllt → aktuell")
	}
	cases := map[string]func() (bool, int64, *PeerObs){
		"nicht streaming": func() (bool, int64, *PeerObs) { return false, 0, act() },
		"rückstand":       func() (bool, int64, *PeerObs) { return true, limit + 1, act() },
		"kein aktiver":    func() (bool, int64, *PeerObs) { return true, 0, nil },
		"nicht gesehen":   func() (bool, int64, *PeerObs) { a := act(); a.Seen = false; return true, 0, a },
		"kein primary":    func() (bool, int64, *PeerObs) { a := act(); a.PG = "standby"; return true, 0, a },
		"asynchron":       func() (bool, int64, *PeerObs) { a := act(); a.Sync = false; return true, 0, a },
		"vermerkt":        func() (bool, int64, *PeerObs) { a := act(); a.Stale = true; return true, 0, a },
	}
	for name, c := range cases {
		s, lag, a := c()
		if DataCurrent(s, lag, limit, a) {
			t.Errorf("%s: darf nicht aktuell sein", name)
		}
	}
}

func TestSyncForceAsync(t *testing.T) {
	in := syncIn(Duo)
	in.Sync, in.Streaming, in.ForceAsync = false, true, true
	if out := SyncStep(in); out.WantSync {
		t.Fatalf("ForceAsync: Sync-Ziel muss leer bleiben: %+v", out)
	}
	in.Sync = true
	if out := SyncStep(in); out.WantSync || out.MarkStale {
		t.Fatalf("ForceAsync mit gesetztem Sync: %+v", out)
	}
}
