//go:build integration

// Berechnete Werte als Quelle einer Messstelle.
// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run 'TestSamplerSourceEmpty|TestSaveCalcSource' ./internal/modules/energie/
package energie

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Wird die Quelle leer (ein berechneter Wert hat gerade keinen Wert: Zeitpunkt
// gesetzt, Wert NULL), legt die Messstelle für diese Zeit nichts ab – auch
// nicht die ältere Lesung, die noch frisch genug wäre.
func TestSamplerSourceEmptyNoFallback(t *testing.T) {
	pool, s, sm, infra := samplerEnv(t)
	id, mapID := source(t, pool, s, infra, KindInstant, 1, "4", m0.Add(10*time.Second))
	setReading(t, pool, mapID, "5", m0.Add(30*time.Second)) // als Lesung festgehalten
	run(t, sm, m0)
	wantValue(t, pool, id, m0, 5)

	next := m0.Add(time.Minute)
	_, err := pool.Exec(context.Background(), `UPDATE import_mappings SET last_value = NULL, last_received_at = $2 WHERE id = $1::uuid`,
		mapID, next.Add(20*time.Second))
	must(t, err)
	run(t, sm, next)
	wantNoValue(t, pool, id, next)

	// kommt der Wert zurück, wird wieder abgelegt
	after := next.Add(time.Minute)
	setReading(t, pool, mapID, "6", after.Add(10*time.Second))
	run(t, sm, after)
	wantValue(t, pool, id, after, 6)
}

// Ein berechneter Wert wird zusammengefasst geschrieben – als Quelle einer
// Verbrauchs-Messstelle (jede Lesung wird addiert) ergäbe das falsche Summen.
func TestSaveCalcSourceNotConsumption(t *testing.T) {
	pool := testPool(t)
	s := NewStore(pool)
	ctx := context.Background()
	infra := newInfra(t, pool, "")
	var mapID string
	must(t, pool.QueryRow(ctx, `INSERT INTO import_mappings (connection_id, source_ref, infrastructure_id, name)
		SELECT id, $1, $2::uuid, 'Berechnet' FROM import_export_connections WHERE kind = 'calc' RETURNING id::text`,
		uniq("calc:test"), infra).Scan(&mapID))
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM energy_meters WHERE import_mapping_id = $1`, mapID)
		_, _ = pool.Exec(c, `DELETE FROM import_mappings WHERE id = $1`, mapID)
	})

	_, err := s.Save(ctx, &Meter{Key: uniq("VB"), InfraID: infra, Kind: KindConsumption, SourceID: mapID, Factor: 1}, "")
	if !errors.Is(err, ErrCalcConsumption) {
		t.Fatalf("Verbrauch mit berechnetem Wert als Quelle: %v", err)
	}
	for _, kind := range []string{KindInstant, KindCounter} {
		if _, err := s.Save(ctx, &Meter{Key: uniq("OK"), InfraID: infra, Kind: kind, SourceID: mapID, Factor: 1}, ""); err != nil {
			t.Errorf("%s mit berechnetem Wert als Quelle: %v", kind, err)
		}
	}
	// eine gewöhnliche Quelle bleibt für Verbrauch erlaubt
	_, plain := newMapping(t, pool, infra)
	if _, err := s.Save(ctx, &Meter{Key: uniq("VC"), InfraID: infra, Kind: KindConsumption, SourceID: plain, Factor: 1}, ""); err != nil {
		t.Errorf("Verbrauch mit gewöhnlicher Quelle: %v", err)
	}
}
