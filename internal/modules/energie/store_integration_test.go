package energie

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var seq atomic.Int64

// newInfra legt eine Anlage an (optional unter parent) und räumt sie samt
// Messstellen wieder auf.
func newInfra(t *testing.T, pool *pgxpool.Pool, parent string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	must(t, pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type, parent_id)
		VALUES ($1, 'plant', NULLIF($2, '')::uuid) RETURNING id::text`,
		fmt.Sprintf("EnergieTest-%d", seq.Add(1)), parent).Scan(&id))
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM energy_meters WHERE infrastructure_id = $1`, id)
		_, _ = pool.Exec(c, `DELETE FROM infrastructure WHERE id = $1`, id)
	})
	return id
}

// uniq macht eine Messstellen-ID für diesen Lauf eindeutig.
func uniq(name string) string {
	return fmt.Sprintf("%s-%d-%d", name, time.Now().UnixNano()%1000000, seq.Add(1))
}

// newMapping legt eine Verbindung mit einer Zuordnung an.
func newMapping(t *testing.T, pool *pgxpool.Pool, infra string) (connID, mapID string) {
	t.Helper()
	ctx := context.Background()
	must(t, pool.QueryRow(ctx, `INSERT INTO import_export_connections (direction, kind, name)
		VALUES ('import', 'mqtt', $1) RETURNING id::text`, uniq("energie-test")).Scan(&connID))
	must(t, pool.QueryRow(ctx, `INSERT INTO import_mappings (connection_id, source_ref, infrastructure_id, name)
		VALUES ($1, $2, $3, 'Quelle') RETURNING id::text`, connID, uniq("e/t"), infra).Scan(&mapID))
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM energy_meters WHERE import_mapping_id = $1`, mapID)
		_, _ = pool.Exec(c, `DELETE FROM import_mappings WHERE id = $1`, mapID)
		_, _ = pool.Exec(c, `DELETE FROM import_export_connections WHERE id = $1`, connID)
	})
	return
}

func save(t *testing.T, s *Store, m *Meter) {
	t.Helper()
	if m.Kind == "" {
		m.Kind = KindCounter
	}
	m.Active, m.Export = true, true
	if _, err := s.Save(context.Background(), m, ""); err != nil {
		t.Fatalf("Save %s: %v", m.Key, err)
	}
}

func TestSaveCreatesForInfra(t *testing.T) {
	pool := testPool(t)
	s := NewStore(pool)
	ctx := context.Background()
	a, b := newInfra(t, pool, ""), newInfra(t, pool, "")
	key := uniq("S1")

	m := &Meter{Key: key, Name: "Strom", InfraID: a, Kind: KindCounter, Unit: "kWh", Active: true}
	created, err := s.Save(ctx, m, "")
	if err != nil || !created || m.ID == "" {
		t.Fatalf("anlegen: created=%v err=%v id=%q", created, err, m.ID)
	}
	created, err = s.Save(ctx, &Meter{Key: key, Name: "Strom neu", InfraID: a, Kind: KindCounter, Unit: "kWh", Active: true}, "")
	if err != nil || created {
		t.Fatalf("zweites Speichern: created=%v err=%v", created, err)
	}
	got, err := s.Get(ctx, m.ID)
	if err != nil || got.Name != "Strom neu" || got.Factor != 1 {
		t.Fatalf("Get: %+v %v", got, err)
	}
	if _, err = s.Save(ctx, &Meter{Key: key, Name: "x", InfraID: b, Kind: KindCounter}, ""); !errors.Is(err, ErrKeyTaken) {
		t.Fatalf("andere Anlage: %v", err)
	}
	if _, err = s.Save(ctx, &Meter{Key: uniq("S2"), Name: "x", InfraID: "", Kind: KindCounter}, ""); !errors.Is(err, ErrNoInfra) {
		t.Fatalf("ohne Anlage: %v", err)
	}
	if _, err = s.Get(ctx, "00000000-0000-0000-0000-000000000001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get fehlt: %v", err)
	}
}

func TestSaveRejectsBadKey(t *testing.T) {
	pool := testPool(t)
	s := NewStore(pool)
	infra := newInfra(t, pool, "")
	for _, k := range []string{"a;b", "[x]", strings.Repeat("a", 65), "", "  ", "a\"b", "a\nb"} {
		_, err := s.Save(context.Background(), &Meter{Key: k, Name: "x", InfraID: infra, Kind: KindCounter}, "")
		if !errors.Is(err, ErrBadKey) {
			t.Errorf("Key %q: %v", k, err)
		}
	}
	if _, err := s.Save(context.Background(), &Meter{Key: uniq("K"), Name: "x", InfraID: infra, Kind: "foo"}, ""); !errors.Is(err, ErrBadKind) {
		t.Errorf("Art: %v", err)
	}
}
