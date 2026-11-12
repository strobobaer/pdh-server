//go:build integration

package web

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTerminalLoginSettingsDefaultAndSave(t *testing.T) {
	e := newATEnv(t)
	uid := e.user("tls", "admin", true)
	h := &Handler{db: e.pool}

	got, err := h.terminalLoginSettings(e.ctx, uid)
	e.must(err)
	if got != (terminalLoginSettings{ManualLogin: loginCardPIN, PasswordButton: true}) {
		t.Fatalf("Standard ohne Zeile: %+v", got)
	}

	// Rückkehrzeit setzen; sie darf beim Speichern der Anmeldeart nicht verloren gehen.
	e.must(h.saveTerminalIdle(e.ctx, uid, "300"))
	for _, kind := range []string{loginCardPIN, loginPassword, loginCardOnly, loginNone} {
		for _, btn := range []bool{true, false} {
			want := terminalLoginSettings{ManualLogin: kind, PasswordButton: btn}
			e.must(h.saveTerminalLoginSettings(e.ctx, uid, want))
			got, err := h.terminalLoginSettings(e.ctx, uid)
			e.must(err)
			if got != want {
				t.Fatalf("gespeichert %+v, gelesen %+v", want, got)
			}
		}
	}
	if err := h.saveTerminalLoginSettings(e.ctx, uid, terminalLoginSettings{ManualLogin: "foo"}); err == nil {
		t.Fatal("unbekannte Art muss einen Fehler liefern")
	}
	if got, _ := h.terminalLoginSettings(e.ctx, uid); got.ManualLogin != loginNone {
		t.Fatalf("Fehler darf nichts ändern: %+v", got)
	}
	if v := h.terminalIdleField(e.ctx, uid); v != "300" {
		t.Fatalf("Rückkehrzeit geändert: %q", v)
	}
}

func TestTerminalPINLifecycle(t *testing.T) {
	e := newATEnv(t)
	uid := e.user("pin", "admin", true)
	h := &Handler{db: e.pool}
	now := time.Now()

	check := func(pin string, at time.Time) bool {
		t.Helper()
		ok, err := h.checkTerminalPIN(e.ctx, uid, pin, at)
		e.must(err)
		return ok
	}
	failures := func() int {
		return mustInt(e, `SELECT terminal_pin_failures FROM users WHERE id = $1::uuid`, uid)
	}

	if check("4711", now) {
		t.Fatal("ohne PIN muss false kommen")
	}
	if has, lk, err := h.terminalPINState(e.ctx, uid, now); err != nil || has || lk != nil {
		t.Fatalf("Zustand ohne PIN: %v %v %v", has, lk, err)
	}

	e.must(h.setTerminalPIN(e.ctx, uid, "4711"))
	if err := h.setTerminalPIN(e.ctx, uid, "1234"); err == nil {
		t.Fatal("ungültige PIN muss abgelehnt werden")
	}
	if !check("4711", now) || check("1111", now) {
		t.Fatal("richtig/falsch")
	}

	// 5 Fehlversuche -> gesperrt, auch die richtige PIN wird abgelehnt.
	for i := 0; i < 5; i++ {
		check("9999", now)
	}
	has, lk, err := h.terminalPINState(e.ctx, uid, now)
	e.must(err)
	if !has || lk == nil || lk.Sub(now.Add(15*time.Minute)).Abs() > time.Second {
		t.Fatalf("Sperre erwartet ca. now+15min, ist %v", lk)
	}
	if check("4711", now) {
		t.Fatal("gesperrt: richtige PIN darf nicht gelten")
	}
	// Nach Ablauf der Sperre.
	later := now.Add(16 * time.Minute)
	if _, lk, _ := h.terminalPINState(e.ctx, uid, later); lk != nil {
		t.Fatalf("abgelaufene Sperre muss nil sein: %v", lk)
	}
	if !check("4711", later) || failures() != 0 {
		t.Fatal("nach Ablauf muss die richtige PIN gelten, Zähler 0")
	}

	// 4 falsch, 1 richtig, 4 falsch -> nicht gesperrt.
	for i := 0; i < 4; i++ {
		check("9999", now)
	}
	if !check("4711", now) {
		t.Fatal("richtige PIN")
	}
	for i := 0; i < 4; i++ {
		check("9999", now)
	}
	if _, lk, _ := h.terminalPINState(e.ctx, uid, now); lk != nil {
		t.Fatalf("nicht gesperrt erwartet: %v", lk)
	}

	// clearTerminalPIN hebt Sperre und PIN auf.
	check("9999", now) // 5. Fehlversuch -> gesperrt
	if _, lk, _ := h.terminalPINState(e.ctx, uid, now); lk == nil {
		t.Fatal("Sperre erwartet")
	}
	e.must(h.clearTerminalPIN(e.ctx, uid))
	if has, lk, _ := h.terminalPINState(e.ctx, uid, now); has || lk != nil || failures() != 0 {
		t.Fatalf("clear: %v %v", has, lk)
	}
	if check("4711", now) {
		t.Fatal("nach clear keine PIN")
	}
}

// Review Focus 3: parallele Fehlversuche dürfen den Zähler nie über 5 treiben.
func TestTerminalPINConcurrentFailures(t *testing.T) {
	e := newATEnv(t)
	uid := e.user("pinc", "admin", true)
	h := &Handler{db: e.pool}
	e.must(h.setTerminalPIN(e.ctx, uid, "4711"))
	now := time.Now()

	stop := make(chan struct{})
	maxSeen := make(chan int, 1)
	go func() {
		m := 0
		for {
			select {
			case <-stop:
				maxSeen <- m
				return
			default:
				var n int
				if e.pool.QueryRow(e.ctx, `SELECT terminal_pin_failures FROM users WHERE id = $1::uuid`, uid).Scan(&n) == nil && n > m {
					m = n
				}
			}
		}
	}()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, err := h.checkTerminalPIN(e.ctx, uid, "9999", now); ok || err != nil {
				t.Errorf("ok=%v err=%v", ok, err)
			}
		}()
	}
	wg.Wait()
	close(stop)
	if m := <-maxSeen; m > 5 {
		t.Fatalf("Zähler stieg auf %d", m)
	}
	if _, lk, _ := h.terminalPINState(e.ctx, uid, now); lk == nil {
		t.Fatal("nach 10 Fehlversuchen muss gesperrt sein")
	}
	if n := mustInt(e, `SELECT terminal_pin_failures FROM users WHERE id = $1::uuid`, uid); n > 5 {
		t.Fatalf("Zähler %d", n)
	}
}

func mustInt(e *atEnv, q string, args ...any) int {
	e.t.Helper()
	var n int
	e.must(e.pool.QueryRow(e.ctx, q, args...).Scan(&n))
	return n
}

// I1: Sperrprüfung, Vergleich und Zählen sind ein atomarer Schritt je Benutzer;
// auch bei vielen parallelen Anfragen gibt es höchstens 5 Vergleiche je Sperrfenster.
func TestTerminalPINConcurrentLimitsComparisons(t *testing.T) {
	e := newATEnv(t)
	uid := e.user("pinl", "admin", true)
	h := &Handler{db: e.pool}
	e.must(h.setTerminalPIN(e.ctx, uid, "4711"))
	now := time.Now()

	var compares atomic.Int32
	orig := terminalPINCompare
	terminalPINCompare = func(hash, pw []byte) error {
		compares.Add(1)
		return orig(hash, pw)
	}
	t.Cleanup(func() { terminalPINCompare = orig })

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, err := h.checkTerminalPIN(e.ctx, uid, "9999", now); ok || err != nil {
				t.Errorf("ok=%v err=%v", ok, err)
			}
		}()
	}
	wg.Wait()
	if n := compares.Load(); n != terminalPINMaxFailures {
		t.Fatalf("Vergleiche je Sperrfenster: %d, erwartet %d", n, terminalPINMaxFailures)
	}
	_, lk, _ := h.terminalPINState(e.ctx, uid, now)
	if lk == nil {
		t.Fatal("nach 20 Fehlversuchen muss gesperrt sein")
	}

	// Gesperrt: parallele richtige und falsche Eingaben vergleichen nichts,
	// melden nie Erfolg und löschen die Sperre nicht.
	compares.Store(0)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pin := "4711"
			if i%2 == 1 {
				pin = "9999"
			}
			if ok, err := h.checkTerminalPIN(e.ctx, uid, pin, now); ok || err != nil {
				t.Errorf("gesperrt: ok=%v err=%v", ok, err)
			}
		}(i)
	}
	wg.Wait()
	if n := compares.Load(); n != 0 {
		t.Fatalf("gesperrt: %d Vergleiche", n)
	}
	if _, lk2, _ := h.terminalPINState(e.ctx, uid, now); lk2 == nil || !lk2.Equal(*lk) {
		t.Fatalf("Sperre verändert: %v -> %v", lk, lk2)
	}
}
