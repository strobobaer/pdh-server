package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"pdh/internal/modules/it/netsync"
	"pdh/internal/modules/tickets"
)

// Ausfall → Ticket mit Ursachepflicht, danach Wiederkehr → Kommentar „Wieder erreichbar“.
func TestITNetAlarmSinkTicketAndRecovery(t *testing.T) {
	h, pool := newITNetTestHandler(t)
	h.tickets = tickets.NewService(tickets.NewRepository(pool))
	ctx := context.Background()
	sfx := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	src := "unifitest" + sfx
	var user, asset, ticket string
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
		VALUES ($1::text, $1::text || '@x', 'x', 'It', 'S', 'technician', '', '') RETURNING id::text`, "its"+sfx).Scan(&user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		if ticket != "" {
			_, _ = pool.Exec(c, `DELETE FROM tickets WHERE id = $1::uuid`, ticket)
		}
		_, _ = pool.Exec(c, `DELETE FROM it_assets WHERE id = $1::uuid`, asset)
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id = $1::uuid`, user)
	})
	since := time.Now().Add(-time.Hour)
	if err := pool.QueryRow(ctx, `INSERT INTO it_assets (name, type, status, created_by, net_source, net_external_id, net_role, net_monitored, net_state, net_state_since, ip_address)
		VALUES ($1, 'network', 'active', $2::uuid, $3, 'x1', 'device', true, 'offline', $4, '10.9.9.9') RETURNING id::text`,
		"Switch "+sfx, user, src, since).Scan(&asset); err != nil {
		t.Fatal(err)
	}
	w := &netsync.Watch{DB: pool, Sink: itNetAlarmSink{h: h}, Source: src, Grace: func(context.Context) time.Duration { return time.Minute }}
	if err := w.Pass(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	var title string
	var rc bool
	if err := pool.QueryRow(ctx, `SELECT t.id::text, t.title, t.requires_root_cause FROM it_net_alarm_state s JOIN tickets t ON t.id = s.ticket_id
		WHERE s.asset_id = $1::uuid`, asset).Scan(&ticket, &title, &rc); err != nil {
		t.Fatalf("kein Ticket: %v", err)
	}
	if title != "Netzgerät offline: Switch "+sfx || !rc {
		t.Errorf("Ticket: %q %v", title, rc)
	}
	var queued int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM record_change_queue WHERE module = 'ticket' AND record_id = $1::uuid`, ticket).Scan(&queued)
	if queued != 0 {
		t.Errorf("Anlege-Hinweis nicht entfernt: %d", queued)
	}
	if _, err := pool.Exec(ctx, `UPDATE it_assets SET net_state = 'online', net_state_since = now() WHERE id = $1::uuid`, asset); err != nil {
		t.Fatal(err)
	}
	if err := w.Pass(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM ticket_comments WHERE ticket_id = $1::uuid AND text LIKE 'Wieder erreichbar seit %'`, ticket).Scan(&n)
	if n != 1 {
		t.Errorf("Kommentar „Wieder erreichbar“: %d", n)
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM record_change_queue WHERE module = 'ticket' AND record_id = $1::uuid AND kind = 'comment'`, ticket).Scan(&queued)
	if queued != 0 {
		t.Errorf("Kommentar-Hinweis nicht entfernt: %d", queued)
	}
}
