//go:build integration

// Rechenwerk der berechneten Werte gegen eine echte PostgreSQL.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run 'TestPass|TestProbe' ./internal/modules/calc/
package calc

import (
	"context"
	"strings"
	"testing"
	"time"
)

func str(s string) *string { return &s }

// state liest Wert, Zeitpunkt und Grund eines berechneten Werts.
func (e *env) state(t *testing.T, id string) (value *string, at *time.Time, reason string) {
	t.Helper()
	if err := e.pool.QueryRow(context.Background(), `
		SELECT m.last_value, m.last_received_at, cv.reason FROM import_mappings m JOIN calc_values cv ON cv.mapping_id = m.id
		WHERE m.id = $1::uuid`, id).Scan(&value, &at, &reason); err != nil {
		t.Fatal(err)
	}
	return value, at, reason
}

func (e *env) pass(t *testing.T, eng *Engine) int {
	t.Helper()
	n, err := eng.Pass(context.Background())
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	return n
}

func (e *env) wantValue(t *testing.T, id, want string) {
	t.Helper()
	v, _, reason := e.state(t, id)
	if v == nil || *v != want || reason != "" {
		got := "<kein Wert>"
		if v != nil {
			got = *v
		}
		t.Errorf("Wert %q (Grund %q), erwartet %q", got, reason, want)
	}
}

func (e *env) wantReason(t *testing.T, id, want string) {
	t.Helper()
	v, at, reason := e.state(t, id)
	if v != nil || reason != want {
		t.Errorf("Wert %v, Grund %q; erwartet kein Wert mit Grund %q", v, reason, want)
	}
	if at == nil {
		t.Errorf("Zeitpunkt fehlt bei „kein Wert“ (%s)", want)
	}
}

// one legt einen berechneten Wert mit einem Eingangswert an.
func (e *env) one(t *testing.T, name, formulaSrc string, maxAge *int, inputs ...InputRef) string {
	t.Helper()
	return e.save(t, SaveInput{Name: name + " " + e.sfx, InfraID: e.assetA, Decimals: 2, MaxAgeMinutes: maxAge, Formula: formulaSrc, Inputs: inputs}).ID
}

func TestPassFollowsInput(t *testing.T) {
	e := calcEnv(t)
	eng := NewEngine(e.s, nil)
	a, _, in, res := e.sum(t) // A = 12,4 (vor 2 s), B = 11.9 (vor 3 s), eine Nachkommastelle
	in.Decimals = 2
	e.save(t, in)
	if n := e.pass(t, eng); n != 1 {
		t.Fatalf("erster Lauf: %d geändert", n)
	}
	e.wantValue(t, res.ID, "24.30")
	_, at, _ := e.state(t, res.ID)
	if at == nil || !at.Equal(e.now.Add(-2*time.Second)) {
		t.Errorf("Zeitpunkt: %v, erwartet den jüngeren Eingangswert %v", at, e.now.Add(-2*time.Second))
	}
	if n := e.pass(t, eng); n != 0 {
		t.Errorf("zweiter Lauf ohne Änderung: %d geändert", n)
	}
	e.now = e.now.Add(5 * time.Second)
	e.set(t, a, str("20"), e.now)
	if n := e.pass(t, eng); n != 1 {
		t.Errorf("nach neuem Wert: %d geändert", n)
	}
	e.wantValue(t, res.ID, "31.90")
}

func TestPassReasons(t *testing.T) {
	e := calcEnv(t)
	eng := NewEngine(e.s, nil)
	pa := "Presse A " + e.sfx + " › "
	old := e.now.Add(-11 * time.Minute)

	never := e.src(t, e.assetA, "Nie", "", time.Time{})
	text := e.src(t, e.assetA, "Text", "12,5 kW", e.now)
	empty := e.src(t, e.assetA, "Leer", "", e.now)
	stale := e.src(t, e.assetA, "Alt", "5", old)
	gone := e.src(t, e.assetA, "Weg", "5", e.now)
	zero := e.src(t, e.assetA, "Null", "0", e.now)
	good := e.src(t, e.assetA, "Gut", "9", e.now)

	vNever := e.one(t, "nie", "A", ptr(10), InputRef{Short: "A", MappingID: never})
	vText := e.one(t, "text", "A", ptr(10), InputRef{Short: "A", MappingID: text})
	vEmpty := e.one(t, "leer", "A", ptr(10), InputRef{Short: "A", MappingID: empty})
	vStale := e.one(t, "alt", "A + B", ptr(10), InputRef{Short: "A", MappingID: good}, InputRef{Short: "B", MappingID: stale})
	vGone := e.one(t, "weg", "x * 2", ptr(10), InputRef{Short: "x", MappingID: gone})
	vDiv := e.one(t, "teilen", "A / B", ptr(10), InputRef{Short: "A", MappingID: good}, InputRef{Short: "B", MappingID: zero})
	vRoot := e.one(t, "wurzel", "WURZEL(0 - A)", ptr(10), InputRef{Short: "A", MappingID: good})
	vLater := e.save(t, SaveInput{Name: "später " + e.sfx, InfraID: e.assetA, Formula: "A", ValidFrom: day(24),
		Inputs: []InputRef{{Short: "A", MappingID: good}}}).ID
	if _, err := e.pool.Exec(context.Background(), `DELETE FROM import_mappings WHERE id = $1::uuid`, gone); err != nil {
		t.Fatal(err)
	}

	e.pass(t, eng)
	e.wantReason(t, vNever, "A „"+pa+"Nie "+e.sfx+"“ hat noch nie einen Wert geliefert")
	e.wantReason(t, vText, "A „"+pa+"Text "+e.sfx+"“ ist keine Zahl („12,5 kW“)")
	e.wantReason(t, vEmpty, "A „"+pa+"Leer "+e.sfx+"“ ist keine Zahl („“)")
	e.wantReason(t, vStale, "B „"+pa+"Alt "+e.sfx+"“ ist älter als 10 Minuten (letzter Wert "+old.Local().Format("02.01. 15:04")+")")
	e.wantReason(t, vGone, "x: den Eingangswert „"+pa+"Weg "+e.sfx+"“ gibt es nicht mehr")
	e.wantReason(t, vDiv, "Teilen durch 0")
	e.wantReason(t, vRoot, "Ergebnis ist keine Zahl")
	e.wantReason(t, vLater, "Die erste Fassung gilt ab 24.11.2026")

	// ein überlanger Text wird in der Meldung gekürzt
	e.set(t, text, str(strings.Repeat("x", 80)), e.now)
	e.pass(t, eng)
	if _, _, reason := e.state(t, vText); !strings.Contains(reason, "„"+strings.Repeat("x", 30)+"…“") {
		t.Errorf("gekürzter Text: %q", reason)
	}
}

func TestPassMaxAgeOff(t *testing.T) {
	e := calcEnv(t)
	eng := NewEngine(e.s, nil)
	a := e.src(t, e.assetA, "Alt", "7", e.now.Add(-40*24*time.Hour))
	id := e.one(t, "ohne Alter", "A", nil, InputRef{Short: "A", MappingID: a})
	e.pass(t, eng)
	e.wantValue(t, id, "7.00")
}

func TestPassLazyBranch(t *testing.T) {
	e := calcEnv(t)
	eng := NewEngine(e.s, nil)
	a := e.src(t, e.assetA, "A", "8", e.now)
	b := e.src(t, e.assetA, "B", "0", e.now)
	c := e.src(t, e.assetA, "C", "1", e.now.Add(-2*time.Hour))
	safe := e.one(t, "sicher", "WENN(B > 0; A / B; 0)", ptr(10), InputRef{Short: "A", MappingID: a}, InputRef{Short: "B", MappingID: b})
	skip := e.one(t, "Zweig", "WENN(A > 0; A; C)", ptr(10), InputRef{Short: "A", MappingID: a}, InputRef{Short: "C", MappingID: c})
	e.pass(t, eng)
	e.wantValue(t, safe, "0.00")
	e.wantValue(t, skip, "8.00") // das veraltete C steht im nicht gerechneten Zweig
}

// Kette: V2 rechnet aus V1. Wird V1 leer, ist V2 im selben Lauf leer und
// nennt V1; kommt V1 zurück, ist V2 im selben Lauf wieder da.
func TestPassChain(t *testing.T) {
	e := calcEnv(t)
	eng := NewEngine(e.s, nil)
	a := e.src(t, e.assetA, "A", "4", e.now)
	v1 := e.one(t, "Eins", "A", ptr(10), InputRef{Short: "A", MappingID: a})
	// Name so gewählt, dass V2 in der Liste vor V1 steht – die Reihenfolge muss aus den Bezügen kommen
	v2 := e.one(t, "Anfang", "V1 * 2", ptr(10), InputRef{Short: "V1", MappingID: v1})
	if n := e.pass(t, eng); n != 2 {
		t.Fatalf("ein Lauf rechnet beide: %d", n)
	}
	e.wantValue(t, v1, "4.00")
	e.wantValue(t, v2, "8.00")

	e.now = e.now.Add(time.Minute)
	e.set(t, a, str("x"), e.now)
	if n := e.pass(t, eng); n != 2 {
		t.Fatalf("beide werden im selben Lauf leer: %d", n)
	}
	e.wantReason(t, v1, "A „Presse A "+e.sfx+" › A "+e.sfx+"“ ist keine Zahl („x“)")
	e.wantReason(t, v2, "V1 „Presse A "+e.sfx+" › Eins "+e.sfx+"“ hat selbst keinen Wert")

	e.now = e.now.Add(time.Minute)
	e.set(t, a, str("5"), e.now)
	if n := e.pass(t, eng); n != 2 {
		t.Fatalf("beide kommen im selben Lauf zurück: %d", n)
	}
	e.wantValue(t, v1, "5.00")
	e.wantValue(t, v2, "10.00")
	if _, at, _ := e.state(t, v2); at == nil || !at.Equal(e.now) {
		t.Errorf("Zeitpunkt der Kette: %v, erwartet %v", at, e.now)
	}
}

func TestPassEmptyKeepsTimestamp(t *testing.T) {
	e := calcEnv(t)
	eng := NewEngine(e.s, nil)
	a := e.src(t, e.assetA, "A", "4", e.now)
	id := e.one(t, "Zeit", "A", ptr(10), InputRef{Short: "A", MappingID: a})
	e.pass(t, eng)
	e.now = e.now.Add(time.Minute)
	e.set(t, a, str("kaputt"), e.now.Add(-time.Second))
	e.pass(t, eng)
	_, at, _ := e.state(t, id)
	if at == nil || !at.Equal(e.now) {
		t.Fatalf("Zeitpunkt, zu dem der Wert leer wurde: %v, erwartet %v", at, e.now)
	}
	e.now = e.now.Add(time.Minute)
	if n := e.pass(t, eng); n != 0 {
		t.Errorf("weiterer Lauf ohne Änderung: %d geändert", n)
	}
	if _, at2, _ := e.state(t, id); at2 == nil || !at2.Equal(*at) {
		t.Errorf("Zeitpunkt wandert: %v → %v", at, at2)
	}
	// nur der Grund ändert sich: der Wert bleibt leer, der Zeitpunkt bleibt
	e.set(t, a, str("anders kaputt"), e.now)
	e.pass(t, eng)
	if _, at3, reason := e.state(t, id); at3 == nil || !at3.Equal(*at) || !strings.Contains(reason, "anders kaputt") {
		t.Errorf("neuer Grund: %q, Zeitpunkt %v", reason, at3)
	}
}

func TestPassVersionSwitch(t *testing.T) {
	e := calcEnv(t)
	eng := NewEngine(e.s, nil)
	a := e.src(t, e.assetA, "A", "4", e.now)
	in := SaveInput{Name: "Stichtag " + e.sfx, InfraID: e.assetA, Decimals: 0, Formula: "A", Inputs: []InputRef{{Short: "A", MappingID: a}}}
	in.ID = e.save(t, in).ID
	in.Formula, in.ValidFrom = "A * 2", day(24)
	e.save(t, in)
	e.pass(t, eng)
	e.wantValue(t, in.ID, "4")
	e.today = day(24)
	if n := e.pass(t, eng); n != 1 {
		t.Errorf("am Stichtag: %d geändert", n)
	}
	e.wantValue(t, in.ID, "8")
}

func TestPassSkip(t *testing.T) {
	e := calcEnv(t)
	skip := true
	eng := NewEngine(e.s, func() bool { return skip })
	a := e.src(t, e.assetA, "A", "4", e.now)
	id := e.one(t, "Wartung", "A", ptr(10), InputRef{Short: "A", MappingID: a})
	if n := e.pass(t, eng); n != 0 {
		t.Fatalf("in der Wartung: %d geändert", n)
	}
	if v, at, _ := e.state(t, id); v != nil || at != nil {
		t.Fatalf("in der Wartung wurde geschrieben: %v %v", v, at)
	}
	skip = false
	if n := e.pass(t, eng); n != 1 {
		t.Errorf("nach der Wartung: %d geändert", n)
	}
}

func TestProbe(t *testing.T) {
	e := calcEnv(t)
	ctx := context.Background()
	a := e.src(t, e.assetA, "A", "12,4", e.now)
	b := e.src(t, e.assetA, "B", "11.9", e.now.Add(-20*time.Minute))
	inputs := []InputRef{{Short: "A", MappingID: a}, {Short: "B", MappingID: b}}
	value, reason, err := e.s.Probe(ctx, "a + B", inputs, 2, nil)
	if err != nil || value != "24.30" || reason != "" {
		t.Fatalf("Probe: %q %q %v", value, reason, err)
	}
	value, reason, err = e.s.Probe(ctx, "A + B", inputs, 2, ptr(10))
	if err != nil || value != "" || !strings.HasPrefix(reason, "B „Presse A "+e.sfx+" › B "+e.sfx+"“ ist älter als 10 Minuten") {
		t.Errorf("Probe mit altem Wert: %q %q %v", value, reason, err)
	}
	if _, _, err := e.s.Probe(ctx, "A +", inputs, 2, nil); err == nil {
		t.Error("Schreibfehler muss gemeldet werden")
	}
	if n := e.values(t); n != 0 {
		t.Errorf("Probe hat gespeichert (%d)", n)
	}
}
