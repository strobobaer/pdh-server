package web

// Herkunft von Tickets und Störungen (0.82.31): Migration 201 mit Nachtrag, Anlegen setzt sie.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"pdh/internal/modules/faults"
	"pdh/internal/modules/tickets"
)

func originMigration(t *testing.T) string {
	t.Helper()
	m, _ := filepath.Glob("../../migrations/*_record_origin.up.sql")
	if len(m) != 1 {
		t.Fatalf("Migration record_origin: %v", m)
	}
	b, err := os.ReadFile(m[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func originOf(t *testing.T, ctx context.Context, h *Handler, table, id string) (origin, label string) {
	t.Helper()
	if err := h.db.QueryRow(ctx, `SELECT origin, origin_label FROM `+table+` WHERE id = $1::uuid`, id).Scan(&origin, &label); err != nil {
		t.Fatal(err)
	}
	return origin, label
}

// Vorhandene Datensätze bekommen ihre Herkunft nachträglich, soweit erkennbar.
func TestRecordOriginBackfill(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()
	h := &Handler{db: pool}
	var ids []string
	mk := func(table, q string, args ...any) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, q, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		ids = append(ids, table+":"+id)
		return id
	}
	t.Cleanup(func() {
		for _, x := range ids {
			table, id := x[:len(x)-37], x[len(x)-36:]
			_, _ = pool.Exec(context.Background(), "DEL"+"ETE FROM "+table+" WHERE id = $1::uuid", id)
		}
	})
	sys := pdhSystemUserID
	easy := mk("faults", `INSERT INTO faults (title, description, severity, status, created_by) VALUES ('Easy', 'x'||chr(10)||'Gemeldet per QR-Code (Easy-Mode)', 'medium', 'detected', $1::uuid) RETURNING id::text`, sys)
	term := mk("tickets", `INSERT INTO tickets (title, description, priority, status, created_by) VALUES ('Terminal', 'x'||chr(10)||'Gemeldet am Anlagenterminal', 'medium', 'open', $1::uuid) RETURNING id::text`, sys)
	alarmIT := mk("tickets", `INSERT INTO tickets (title, description, priority, status, created_by, requires_root_cause) VALUES ('Netzgerät offline: Switch 3', 'x', 'medium', 'open', $1::uuid, true) RETURNING id::text`, sys)
	alarmE := mk("tickets", `INSERT INTO tickets (title, description, priority, status, created_by, requires_root_cause) VALUES ('Schwellwert „Strom“ überschritten: Presse', 'x', 'medium', 'open', $1::uuid, true) RETURNING id::text`, sys)
	fw := mk("tickets", `INSERT INTO tickets (title, description, priority, status, created_by, tags) VALUES ('Firewall-Freigabe', 'x', 'medium', 'open', $1::uuid, '["esppdh-freigabe:abc"]'::jsonb) RETURNING id::text`, sys)
	// Migration erneut ausführen (wiederholbar) – erst danach steht die Herkunft da
	if _, err := pool.Exec(ctx, `UPDATE faults SET origin = '' WHERE id = $1::uuid`, easy); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tickets SET origin = '', origin_label = '' WHERE id = ANY($1::uuid[])`, []string{term, alarmIT, alarmE, fw}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, originMigration(t)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ table, id, origin, label string }{
		{"faults", easy, "easy", ""}, {"tickets", term, "terminal", ""},
		{"tickets", alarmIT, "alarm", "IT-Netz"}, {"tickets", alarmE, "alarm", "Energiedaten"},
		{"tickets", fw, "system", "Firewall-Freigabe"},
	} {
		if o, l := originOf(t, ctx, h, c.table, c.id); o != c.origin || l != c.label {
			t.Errorf("%s %s: %q/%q, erwartet %q/%q", c.table, c.id, o, l, c.origin, c.label)
		}
	}
}

// Easy-Modus und Anlagenterminal legen über reportCreate an – mit ihrer Herkunft.
func TestReportCreateSetsOrigin(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()
	h := &Handler{db: pool, tickets: tickets.NewService(tickets.NewRepository(pool)), faults: faults.NewService(faults.NewRepository(pool), nil)}
	var infra string
	if err := pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ('Herkunft Anlage', 'plant') RETURNING id::text`).Scan(&infra); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, "DEL"+"ETE FROM tickets WHERE infrastructure_id = $1::uuid", infra)
		_, _ = pool.Exec(c, "DEL"+"ETE FROM faults WHERE infrastructure_id = $1::uuid", infra)
		_, _ = pool.Exec(c, "DEL"+"ETE FROM infrastructure WHERE id = $1::uuid", infra)
	})
	fid, err := h.reportCreate(ctx, "fault", "", "Easy-Störung", "x", []string{}, infra, pdhSystemUserID, "easy", "")
	if err != nil {
		t.Fatal(err)
	}
	tid, err := h.reportCreate(ctx, "ticket", "", "Terminal-Ticket", "x", []string{}, infra, pdhSystemUserID, "terminal", "Terminal Halle 2")
	if err != nil {
		t.Fatal(err)
	}
	if o, _ := originOf(t, ctx, h, "faults", fid); o != "easy" {
		t.Errorf("Easy-Störung: %q", o)
	}
	if o, l := originOf(t, ctx, h, "tickets", tid); o != "terminal" || l != "Terminal Halle 2" {
		t.Errorf("Terminal-Ticket: %q/%q", o, l)
	}
}

// Anlegen über die Dienste übernimmt Herkunft und Zusatz.
func TestCreateSetsOrigin(t *testing.T) {
	pool := dataNetTestPool(t)
	ctx := context.Background()
	h := &Handler{db: pool, tickets: tickets.NewService(tickets.NewRepository(pool)), faults: faults.NewService(faults.NewRepository(pool), nil)}
	tk, err := h.tickets.Create(ctx, &tickets.CreateInput{Title: "Herkunft Ticket", Priority: "medium", Tags: []string{}, Origin: "terminal", OriginLabel: "Presse 3"}, pdhSystemUserID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DEL"+"ETE FROM tickets WHERE id = $1::uuid", tk.ID) })
	if o, l := originOf(t, ctx, h, "tickets", tk.ID); o != "terminal" || l != "Presse 3" {
		t.Errorf("Ticket: %q/%q", o, l)
	}
	f, err := h.faults.Create(ctx, &faults.CreateFaultInput{Title: "Herkunft Störung", Severity: "medium", Origin: "app"}, pdhSystemUserID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DEL"+"ETE FROM faults WHERE id = $1::uuid", f.ID) })
	if o, _ := originOf(t, ctx, h, "faults", f.ID); o != "app" {
		t.Errorf("Störung: %q", o)
	}
	// unbekannte Herkunft wird nicht gespeichert (bleibt leer)
	tk2, err := h.tickets.Create(ctx, &tickets.CreateInput{Title: "Herkunft kaputt", Priority: "medium", Tags: []string{}, Origin: "erfunden"}, pdhSystemUserID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DEL"+"ETE FROM tickets WHERE id = $1::uuid", tk2.ID) })
	if o, _ := originOf(t, ctx, h, "tickets", tk2.ID); o != "" {
		t.Errorf("unbekannte Herkunft gespeichert: %q", o)
	}
}
