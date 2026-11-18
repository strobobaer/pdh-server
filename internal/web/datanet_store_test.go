package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/datanet"
	"pdh/pkg/database"
)

func dataNetTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PDH_TEST_DSN")
	if dsn == "" {
		t.Skip("PDH_TEST_DSN nicht gesetzt")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestDataNetStore(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()
	h := &Handler{db: pool}
	name := "teststore-" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM datanet_events WHERE client_name = $1`, name)
		pool.Exec(context.Background(), `DELETE FROM datanet_clients WHERE name = $1`, name)
	})

	c, token, err := h.dataNetCreate(ctx, name, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(token) < 43 {
		t.Fatalf("Token zu kurz: %d", len(token))
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM datanet_clients WHERE id = $1`, c.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(token))
	if stored != hex.EncodeToString(sum[:]) {
		t.Fatalf("gespeichert ist nicht sha256(token): %q", stored)
	}
	if _, _, err := h.dataNetCreate(ctx, name, ""); err == nil {
		t.Error("zweiter aktiver Client mit gleichem Namen muss scheitern")
	}

	if _, err := h.dataNetAuth(ctx, c.ID, token+"x"); !errors.Is(err, errDataNetToken) {
		t.Errorf("falscher Token: %v", err)
	}
	if _, err := h.dataNetAuth(ctx, c.ID, token); err != nil {
		t.Errorf("richtiger Token: %v", err)
	}
	if _, err := h.dataNetAuth(ctx, "00000000-0000-0000-0000-000000000000", token); !errors.Is(err, errDataNetToken) {
		t.Errorf("unbekannter Client: %v", err)
	}

	rep := datanet.Report{ClientID: c.ID, Version: "1.2.3", OS: "linux", Hostname: "h1",
		Targets: []datanet.TargetStatus{{Port: 5432, OK: true, CheckedAt: time.Now()}}}
	if err := h.dataNetSaveReport(ctx, c.ID, "10.0.0.5", rep); err != nil {
		t.Fatal(err)
	}
	got, err := h.dataNetGet(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Addr != "10.0.0.5" || got.Version != "1.2.3" || got.Hostname != "h1" || got.LastSeen.IsZero() || len(got.Targets) != 1 || got.Targets[0].Port != 5432 {
		t.Errorf("Meldung nicht gespeichert: %+v", got)
	}
	list, err := h.dataNetList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, x := range list {
		found = found || x.ID == c.ID
	}
	if !found {
		t.Error("Client fehlt in der Liste")
	}

	if err := h.dataNetRevoke(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.dataNetAuth(ctx, c.ID, token); !errors.Is(err, errDataNetRevoked) {
		t.Errorf("gesperrt: %v", err)
	}
	if _, err := h.dataNetAuth(ctx, c.ID, token+"x"); !errors.Is(err, errDataNetToken) {
		t.Errorf("gesperrt mit falschem Token darf nichts verraten: %v", err)
	}
	if err := h.dataNetDelete(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.dataNetGet(ctx, c.ID); err == nil {
		t.Error("gelöschter Client darf nicht mehr lesbar sein")
	}

	rows, err := pool.Query(ctx, `SELECT action FROM datanet_events WHERE client_name = $1 ORDER BY id`, name)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, a)
	}
	if strings.Join(actions, ",") != "create,revoke,delete" {
		t.Errorf("Ereignisse: %v", actions)
	}
}

func TestDataNetState(t *testing.T) {
	now := time.Now()
	iv := 60 * time.Second
	ok := []datanet.TargetStatus{{Port: 1, OK: true}}
	bad := []datanet.TargetStatus{{Port: 1, OK: true}, {Port: 2, OK: false}}
	seen := func(d time.Duration) time.Time { return now.Add(-d) }
	for _, c := range []struct {
		name string
		c    dataNetClient
		want string
	}{
		{"nie gemeldet", dataNetClient{}, "new"},
		{"vor 179 s", dataNetClient{LastSeen: seen(179 * time.Second), Targets: ok}, "ok"},
		{"vor 181 s", dataNetClient{LastSeen: seen(181 * time.Second), Targets: ok}, "offline"},
		{"Ziel down", dataNetClient{LastSeen: seen(time.Second), Targets: bad}, "db_down"},
		{"offline vor db_down", dataNetClient{LastSeen: seen(time.Hour), Targets: bad}, "offline"},
		{"gesperrt hat Vorrang", dataNetClient{Revoked: true}, "revoked"},
		{"gesperrt und gemeldet", dataNetClient{Revoked: true, LastSeen: seen(time.Second), Targets: bad}, "revoked"},
	} {
		if got := dataNetState(c.c, now, iv); got != c.want {
			t.Errorf("%s: %q, erwartet %q", c.name, got, c.want)
		}
	}
}
