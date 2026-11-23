//go:build integration

// Befunde der Schlussprüfung (Rechenwerk, Speicher).
package calc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// I3: Ein fester Wert (berechneter Wert ohne Eingangswerte, z. B. ein Tarif)
// wird als Eingangswert nie „zu alt“.
func TestPassFixedValueInput(t *testing.T) {
	e := calcEnv(t)
	eng := NewEngine(e.s, nil)
	a := e.src(t, e.assetA, "A", "100", e.now)
	tarif := e.one(t, "Tarif", "0,32", ptr(10))
	cost := e.one(t, "Kosten", "A * T", ptr(10), InputRef{Short: "A", MappingID: a}, InputRef{Short: "T", MappingID: tarif})
	e.pass(t, eng)
	e.wantValue(t, cost, "32.00")
	e.now = e.now.Add(30 * time.Minute)
	e.set(t, a, str("200"), e.now)
	e.pass(t, eng)
	e.wantValue(t, tarif, "0.32")
	e.wantValue(t, cost, "64.00")
}

// I4: Rechnet ein Folgewert aus einem Wert ohne Altersprüfung, zählt für sein
// Höchstalter der älteste wirklich benutzte Eingangswert – nicht der jüngste
// Zeitpunkt des Zwischenwerts.
func TestPassChainInheritsAge(t *testing.T) {
	e := calcEnv(t)
	eng := NewEngine(e.s, nil)
	x := e.src(t, e.assetA, "X", "1", e.now.Add(-time.Second))
	y := e.src(t, e.assetA, "Y", "100", e.now.Add(-72*time.Hour))
	sum := e.one(t, "SummeAlt", "X + Y", nil, InputRef{Short: "X", MappingID: x}, InputRef{Short: "Y", MappingID: y})
	next := e.one(t, "Folge", "S * 2", ptr(10), InputRef{Short: "S", MappingID: sum})
	e.pass(t, eng)
	e.wantValue(t, sum, "101.00")
	_, _, reason := e.state(t, next)
	if !strings.HasPrefix(reason, "S „Presse A "+e.sfx+" › SummeAlt "+e.sfx+"“ ist älter als 10 Minuten") {
		t.Errorf("Folgewert muss das Alter der benutzten Werte prüfen: %q", reason)
	}
	// nutzt der Zwischenwert nur frische Werte, ist der Folgewert da
	e.set(t, y, str("100"), e.now)
	e.pass(t, eng)
	e.wantValue(t, next, "202.00")
}

// Minor 2: Kennungen in Großbuchstaben umgehen die Kreisprüfung nicht.
func TestCycleUpperCaseID(t *testing.T) {
	e := calcEnv(t)
	a := e.src(t, e.assetA, "A", "1", e.now)
	v1 := e.save(t, SaveInput{Name: "Eins " + e.sfx, InfraID: e.assetA, Formula: "A", Inputs: []InputRef{{Short: "A", MappingID: a}}})
	v2 := e.save(t, SaveInput{Name: "Zwei " + e.sfx, InfraID: e.assetA, Formula: "E", Inputs: []InputRef{{Short: "E", MappingID: v1.ID}}})
	_, err := e.s.Save(context.Background(), SaveInput{ID: strings.ToUpper(v1.ID), Name: "Eins " + e.sfx, InfraID: e.assetA, Formula: "Z",
		Inputs: []InputRef{{Short: "Z", MappingID: strings.ToUpper(v2.ID)}}}, e.uid)
	var ce *CycleError
	if !errors.As(err, &ce) {
		t.Fatalf("Kreis mit Großbuchstaben: %v", err)
	}
}
