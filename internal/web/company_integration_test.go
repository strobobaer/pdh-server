//go:build integration

package web

// Aufruf: PDH_TEST_DSN=postgres://... go test -tags integration -run Company ./internal/web/

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/pkg/database"
)

func TestCompanyProfileRoundTrip(t *testing.T) {
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close) // nach dem Zurücksetzen (Cleanup läuft rückwärts)
	if err := database.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	var before companyProfile
	h := &Handler{db: pool}
	before = h.company(ctx)
	t.Cleanup(func() {
		if err := h.saveCompany(context.Background(), before, ""); err != nil {
			t.Errorf("zurücksetzen: %v", err)
		}
	})

	var uid string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM users ORDER BY created_at LIMIT 1`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	p := companyProfile{Name: "Muster GmbH", City: "Augsburg", Country: "Deutschland", IBAN: "DE89370400440532013000"}
	if err := h.saveCompany(ctx, p, uid); err != nil {
		t.Fatal(err)
	}
	got := h.company(ctx)
	if got.Name != "Muster GmbH" || got.City != "Augsburg" || got.IBAN != p.IBAN || got.UpdatedBy != uid || got.UpdatedAt.IsZero() {
		t.Errorf("gelesen: %+v", got)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM permissions WHERE key='company.edit'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("Recht company.edit fehlt (%d, %v)", n, err)
	}
}
