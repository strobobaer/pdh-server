package web

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"pdh/internal/modules/energie"
	"pdh/internal/modules/tickets"
	"pdh/pkg/database"
)

// alarmEnv: Labor-Datenbank mit Anlage (Elternteil mit Verantwortlichem, Kind
// ohne eigenen), Gruppe mit Mitglied, Benutzer und Messstelle an der Kind-Anlage.
type alarmEnv struct {
	t                         *testing.T
	ctx                       context.Context
	pool                      *pgxpool.Pool
	h                         *Handler
	sfx                       string
	user, member, infraOwner  string
	admin                     string
	group                     string
	parent, child, orphan     string
	meterID, meterKey         string
	thresholdName, unitString string
}

func newAlarmEnv(t *testing.T) *alarmEnv {
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
	e := &alarmEnv{t: t, ctx: ctx, pool: pool, sfx: strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")}
	e.h = &Handler{db: pool, jwtSecret: "t", tickets: tickets.NewService(tickets.NewRepository(pool))}
	newUser := func(n string) string {
		var id string
		e.must(pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, first_name, last_name, role, department, phone)
			VALUES ($1::text, $1::text || '@x', 'x', 'Al', $1::text, 'technician', '', '') RETURNING id::text`, n+e.sfx).Scan(&id))
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1::uuid`, id) })
		return id
	}
	e.user, e.member, e.infraOwner, e.admin = newUser("alu"), newUser("alm"), newUser("alo"), newUser("ala")
	e.must(pool.QueryRow(ctx, `INSERT INTO user_groups (name) VALUES ($1) RETURNING id::text`, "AlarmTeam "+e.sfx).Scan(&e.group))
	_, err = pool.Exec(ctx, `INSERT INTO user_group_members (group_id, user_id) VALUES ($1::uuid, $2::uuid)`, e.group, e.member)
	e.must(err)
	_, err = pool.Exec(ctx, `INSERT INTO user_permissions (user_id, permission_id, granted)
		SELECT $1::uuid, id, true FROM permissions WHERE key = 'energy.admin'`, e.admin)
	e.must(err)
	e.must(pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type, responsible_to) VALUES ($1, 'building', $2::uuid) RETURNING id::text`,
		"AlarmWeb-Halle "+e.sfx, e.infraOwner).Scan(&e.parent))
	e.must(pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type, parent_id) VALUES ($1, 'line', $2::uuid) RETURNING id::text`,
		"AlarmWeb-Linie "+e.sfx, e.parent).Scan(&e.child))
	e.must(pool.QueryRow(ctx, `INSERT INTO infrastructure (name, type) VALUES ($1, 'building') RETURNING id::text`,
		"AlarmWeb-Waise "+e.sfx).Scan(&e.orphan))
	st := energie.NewStore(pool)
	m := &energie.Meter{Key: "ALW" + e.sfx, Name: "Presse 4 Strom", InfraID: e.child, Unit: "kWh", Kind: energie.KindCounter, Active: true}
	if _, err := st.Save(ctx, m, ""); err != nil {
		t.Fatal(err)
	}
	e.meterID, e.meterKey = m.ID, m.Key
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM tickets WHERE infrastructure_id IN ($1::uuid, $2::uuid, $3::uuid)`, e.parent, e.child, e.orphan)
		_, _ = pool.Exec(c, `DELETE FROM energy_meters WHERE id = $1::uuid`, e.meterID)
		_, _ = pool.Exec(c, `DELETE FROM infrastructure WHERE id IN ($1::uuid, $2::uuid, $3::uuid)`, e.child, e.parent, e.orphan)
		_, _ = pool.Exec(c, `DELETE FROM user_groups WHERE id = $1::uuid`, e.group)
	})
	return e
}

func (e *alarmEnv) must(err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}

// threshold: gespeicherter Schwellwert „über 50 kWh in 15 min“ an der Messstelle.
func (e *alarmEnv) threshold(mod func(*energie.Threshold)) energie.Threshold {
	e.t.Helper()
	st := energie.NewStore(e.pool)
	th := &energie.Threshold{MeterID: e.meterID, Name: "Spitze " + e.sfx, Condition: energie.CondAbove, Limit: 50,
		WindowMinutes: 15, ResetMinutes: 15, Priority: "high", Tag: "Energie-Alarm", Active: true}
	if mod != nil {
		mod(th)
	}
	e.must(st.SaveThreshold(e.ctx, th))
	list, err := st.Thresholds(e.ctx, e.meterID)
	e.must(err)
	for _, x := range list {
		if x.ID == th.ID {
			return x
		}
	}
	e.t.Fatal("Schwellwert nicht gefunden")
	return energie.Threshold{}
}

// open legt über die Senke ein Ticket an (Transaktion, Commit, danach after).
func (e *alarmEnv) open(ev energie.AlarmEvent) string {
	e.t.Helper()
	tx, err := e.pool.Begin(e.ctx)
	e.must(err)
	defer tx.Rollback(e.ctx)
	id, after, err := energyAlarmSink{h: e.h}.OpenTicket(e.ctx, tx, ev)
	e.must(err)
	e.must(tx.Commit(e.ctx))
	if after != nil {
		after()
	}
	return id
}

// systemMessages: Nachrichten von PDH-System an den Benutzer, die den Text enthalten.
func (e *alarmEnv) systemMessages(userID, text string) int {
	e.t.Helper()
	var n int
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM chat_messages m
		JOIN chat_members cm ON cm.conversation_id = m.conversation_id AND cm.user_id = $1::uuid
		WHERE m.user_id = $2::uuid AND m.body LIKE '%' || $3 || '%'`, userID, pdhSystemUserID, text).Scan(&n))
	return n
}

var alarmAt = time.Date(2026, 11, 15, 10, 42, 0, 0, time.Local)

func TestAlarmRecipients(t *testing.T) {
	e := newAlarmEnv(t)
	base := e.threshold(nil)

	// Benutzer am Schwellwert
	th := base
	th.ResponsibleUserID = &e.user
	ids, au, ag, err := e.h.alarmRecipients(e.ctx, th)
	e.must(err)
	if !slices.Equal(ids, []string{e.user}) || au == nil || *au != e.user || ag != nil {
		t.Errorf("Benutzer: %v %v %v", ids, au, ag)
	}

	// Gruppe am Schwellwert: Mitglieder, Ticket an die Gruppe
	th = base
	th.ResponsibleGroupID = &e.group
	ids, au, ag, err = e.h.alarmRecipients(e.ctx, th)
	e.must(err)
	if !slices.Equal(ids, []string{e.member}) || au != nil || ag == nil || *ag != e.group {
		t.Errorf("Gruppe: %v %v %v", ids, au, ag)
	}

	// niemand am Schwellwert: Verantwortlicher der Anlage (vom Elternteil geerbt)
	ids, au, ag, err = e.h.alarmRecipients(e.ctx, base)
	e.must(err)
	if !slices.Equal(ids, []string{e.infraOwner}) || au == nil || *au != e.infraOwner || ag != nil {
		t.Errorf("Anlage geerbt: %v %v %v", ids, au, ag)
	}

	// niemand ermittelbar: Inhaber von energy.admin, ohne Zuständigen
	th = base
	th.InfrastructureID = e.orphan
	ids, au, ag, err = e.h.alarmRecipients(e.ctx, th)
	e.must(err)
	if !slices.Contains(ids, e.admin) || slices.Contains(ids, pdhSystemUserID) || au != nil || ag != nil {
		t.Errorf("energy.admin: %v %v %v", ids, au, ag)
	}

	// inaktiver Benutzer am Schwellwert zählt nicht: weiter mit der Anlage
	_, err = e.pool.Exec(e.ctx, `UPDATE users SET active = false WHERE id = $1::uuid`, e.user)
	e.must(err)
	th = base
	th.ResponsibleUserID = &e.user
	ids, au, _, err = e.h.alarmRecipients(e.ctx, th)
	e.must(err)
	if !slices.Equal(ids, []string{e.infraOwner}) || au == nil || *au != e.infraOwner {
		t.Errorf("inaktiver Benutzer: %v %v", ids, au)
	}
}

func TestAlarmTicketContent(t *testing.T) {
	e := newAlarmEnv(t)
	th := e.threshold(func(x *energie.Threshold) { x.ResponsibleGroupID = &e.group })
	old := "00000000-0000-4000-8000-0000000000aa" // altes, geschlossenes Ticket: wird nicht verwendet
	th.TicketID = &old
	id := e.open(energie.AlarmEvent{Threshold: th, Value: 63.5, At: alarmAt})
	if id == "" || id == old {
		t.Fatalf("Ticket-ID %q", id)
	}
	tk, err := e.h.tickets.GetByID(e.ctx, id)
	e.must(err)
	wantTitle := "Schwellwert „Spitze " + e.sfx + "“ überschritten: Presse 4 Strom"
	if tk.Title != wantTitle {
		t.Errorf("Titel %q, erwartet %q", tk.Title, wantTitle)
	}
	for _, s := range []string{"63,5 kWh", "50 kWh", "Menge in 15 min", "15.11.2026 10:42", "/energy/meters?q=" + e.meterKey} {
		if !strings.Contains(tk.Description, s) {
			t.Errorf("Beschreibung ohne %q:\n%s", s, tk.Description)
		}
	}
	if !slices.Equal(tk.Tags, []string{"Energie-Alarm"}) {
		t.Errorf("Tags %v", tk.Tags)
	}
	if tk.InfrastructureID == nil || *tk.InfrastructureID != e.child {
		t.Errorf("Anlage %v", tk.InfrastructureID)
	}
	if tk.Priority != tickets.PriorityHigh || !tk.RequiresRootCause || tk.CreatedBy != pdhSystemUserID {
		t.Errorf("Priorität %s, Ursache-Pflicht %v, Ersteller %s", tk.Priority, tk.RequiresRootCause, tk.CreatedBy)
	}
	// Gruppe: keine Person zugewiesen; verantwortlich ist wie bei jedem Ticket
	// die Person der Anlage (Vorgabe aus Migration 129).
	if tk.AssignedGroupID == nil || *tk.AssignedGroupID != e.group || tk.AssignedTo != nil ||
		tk.ResponsibleTo == nil || *tk.ResponsibleTo != e.infraOwner {
		t.Errorf("Gruppe %v, zugewiesen %v, verantwortlich %v", tk.AssignedGroupID, tk.AssignedTo, tk.ResponsibleTo)
	}

	// Benutzer am Schwellwert, „unter“, ohne Tag, Momentanwert
	th2 := e.threshold(func(x *energie.Threshold) {
		x.Name, x.Condition, x.Tag, x.Priority, x.WindowMinutes = "Leerlauf "+e.sfx, energie.CondBelow, "", "low", 1
		x.ResponsibleUserID = &e.user
	})
	th2.Kind = energie.KindInstant
	id2 := e.open(energie.AlarmEvent{Threshold: th2, Value: 2, At: alarmAt})
	tk2, err := e.h.tickets.GetByID(e.ctx, id2)
	e.must(err)
	if !strings.Contains(tk2.Title, "unterschritten") || !strings.Contains(tk2.Description, "Mittel über 1 min") {
		t.Errorf("unter: %q / %q", tk2.Title, tk2.Description)
	}
	if len(tk2.Tags) != 0 || tk2.Priority != tickets.PriorityLow {
		t.Errorf("Tags %v, Priorität %s", tk2.Tags, tk2.Priority)
	}
	if tk2.AssignedTo == nil || *tk2.AssignedTo != e.user || tk2.ResponsibleTo == nil || *tk2.ResponsibleTo != e.user || tk2.AssignedGroupID != nil {
		t.Errorf("Benutzer: zugewiesen %v, verantwortlich %v, Gruppe %v", tk2.AssignedTo, tk2.ResponsibleTo, tk2.AssignedGroupID)
	}
}

// Das Anlegen läuft in der Transaktion des Wächters: ohne Commit kein Ticket
// und keine Nachricht.
func TestAlarmTicketInTx(t *testing.T) {
	e := newAlarmEnv(t)
	th := e.threshold(func(x *energie.Threshold) { x.ResponsibleUserID = &e.user })
	tx, err := e.pool.Begin(e.ctx)
	e.must(err)
	id, after, err := energyAlarmSink{h: e.h}.OpenTicket(e.ctx, tx, energie.AlarmEvent{Threshold: th, Value: 60, At: alarmAt})
	e.must(err)
	e.must(tx.Rollback(e.ctx))
	if after == nil {
		t.Fatal("keine Nachricht vorgesehen")
	}
	var n int
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM tickets WHERE id = $1::uuid`, id).Scan(&n))
	if n != 0 {
		t.Error("Ticket trotz Rollback")
	}
	if e.systemMessages(e.user, id) != 0 {
		t.Error("Nachricht vor dem Commit")
	}
}

// Alarm-Tickets: genau eine Nachricht (vom Wächter), nicht zusätzlich der
// allgemeine Hinweis „Neu für dich“ und keine Verteilung an Broker.
func TestAlarmNoDoubleNotify(t *testing.T) {
	e := newAlarmEnv(t)
	th := e.threshold(func(x *energie.Threshold) { x.ResponsibleUserID = &e.user })
	id := e.open(energie.AlarmEvent{Threshold: th, Value: 60, At: alarmAt})
	if got := e.systemMessages(e.user, "/tickets/"+id); got != 1 {
		t.Errorf("Nachrichten an den Verantwortlichen: %d, erwartet 1", got)
	}
	var queued int
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM record_change_queue WHERE module = 'ticket' AND record_id = $1::uuid AND kind = 'created'`, id).Scan(&queued))
	if queued != 0 {
		t.Errorf("Anlege-Hinweis in der Warteschlange: %d", queued)
	}
	// Normale Tickets behalten den Anlege-Hinweis.
	tk, err := e.h.tickets.Create(e.ctx, &tickets.CreateInput{Title: "Normal " + e.sfx, Priority: tickets.PriorityLow, InfrastructureID: &e.child}, e.user)
	e.must(err)
	e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM record_change_queue WHERE module = 'ticket' AND record_id = $1::uuid AND kind = 'created'`, tk.ID).Scan(&queued))
	if queued != 1 {
		t.Errorf("normales Ticket ohne Anlege-Hinweis: %d", queued)
	}
	_, _ = e.pool.Exec(e.ctx, `DELETE FROM record_change_queue WHERE record_id = $1::uuid`, tk.ID)
}

func TestAlarmRepeatComment(t *testing.T) {
	e := newAlarmEnv(t)
	th := e.threshold(func(x *energie.Threshold) { x.ResponsibleUserID = &e.user })
	id := e.open(energie.AlarmEvent{Threshold: th, Value: 60, At: alarmAt})
	sink := energyAlarmSink{h: e.h}

	tx, err := e.pool.Begin(e.ctx)
	e.must(err)
	open, err := sink.TicketOpen(e.ctx, tx, id)
	e.must(err)
	if !open {
		t.Fatal("neues Ticket gilt nicht als offen")
	}
	// unbekanntes Ticket: nicht offen, kein Fehler (Transaktion bleibt nutzbar)
	open, err = sink.TicketOpen(e.ctx, tx, "00000000-0000-4000-8000-0000000000ab")
	if err != nil || open {
		t.Errorf("unbekanntes Ticket: %v %v", open, err)
	}
	at := time.Date(2026, 11, 15, 11, 5, 0, 0, time.Local)
	after, err := sink.Repeat(e.ctx, tx, energie.AlarmEvent{Threshold: th, Value: 63, At: at, TicketID: id})
	e.must(err)
	if e.systemMessages(e.user, "Erneut") != 0 {
		t.Error("Nachricht vor dem Commit")
	}
	e.must(tx.Commit(e.ctx))
	after()

	comments, err := e.h.tickets.ListComments(e.ctx, id)
	e.must(err)
	want := "Erneut überschritten am 15.11.2026 11:05 – Wert 63 kWh, Grenze 50 kWh"
	if len(comments) != 1 || comments[0].Text != want || comments[0].UserID != pdhSystemUserID {
		t.Fatalf("Kommentare %+v, erwartet %q", comments, want)
	}
	if got := e.systemMessages(e.user, "/tickets/"+id); got != 2 {
		t.Errorf("Nachrichten an den Verantwortlichen: %d, erwartet 2 (Alarm + erneut)", got)
	}
	// kein zusätzlicher Änderungshinweis „Ticket … wurde geändert“ zum Kommentar
	commentQueued := func() int {
		var n int
		e.must(e.pool.QueryRow(e.ctx, `SELECT count(*) FROM record_change_queue
			WHERE module = 'ticket' AND record_id = $1::uuid AND kind = 'comment'`, id).Scan(&n))
		return n
	}
	if n := commentQueued(); n != 0 {
		t.Errorf("Änderungshinweis zum Wächter-Kommentar: %d", n)
	}
	// ein Kommentar einer Person bleibt ein Änderungshinweis
	_, err = e.h.tickets.AddComment(e.ctx, id, e.user, "Schaue ich mir an")
	e.must(err)
	if n := commentQueued(); n != 1 {
		t.Errorf("Änderungshinweis zum Kommentar einer Person: %d, erwartet 1", n)
	}
	_, _ = e.pool.Exec(e.ctx, `DELETE FROM record_change_queue WHERE record_id = $1::uuid`, id)

	// Rollback: kein Kommentar
	tx, err = e.pool.Begin(e.ctx)
	e.must(err)
	_, err = sink.Repeat(e.ctx, tx, energie.AlarmEvent{Threshold: th, Value: 70, At: at, TicketID: id})
	e.must(err)
	e.must(tx.Rollback(e.ctx))
	comments, _ = e.h.tickets.ListComments(e.ctx, id)
	if len(comments) != 2 {
		t.Errorf("Kommentar trotz Rollback: %d", len(comments))
	}

	// erledigt, geschlossen oder archiviert: nicht mehr offen
	// (ein offener Status hebt die Archivierung auf, Migration 108)
	for _, q := range []string{
		`UPDATE tickets SET status = 'resolved' WHERE id = $1::uuid`,
		`UPDATE tickets SET status = 'closed' WHERE id = $1::uuid`,
		`UPDATE tickets SET archived_at = NOW() WHERE id = $1::uuid`,
	} {
		_, err := e.pool.Exec(e.ctx, q, id)
		e.must(err)
		tx, err := e.pool.Begin(e.ctx)
		e.must(err)
		open, err := sink.TicketOpen(e.ctx, tx, id)
		_ = tx.Rollback(e.ctx)
		if err != nil || open {
			t.Errorf("%s: offen %v, Fehler %v", q, open, err)
		}
	}
}

// Der Wächter startet nur am aktiven Server und ruht in der Wartungssperre.
// Kein echter Durchlauf: der Wächter würde alle Schwellwerte der Labor-DB prüfen.
func TestStartEnergyAlarmsMirror(t *testing.T) {
	e := newAlarmEnv(t)

	e.h.mirror = MirrorInfo{On: true}
	if w := e.h.energyAlarmWatch(); w != nil {
		t.Error("Wächter auf dem Spiegel gebaut")
	}
	if e.h.startEnergyAlarms(context.Background()) {
		t.Error("Wächter auf dem Spiegel gestartet")
	}
	e.h.mirror = MirrorInfo{}
	if (&Handler{}).energyAlarmWatch() != nil {
		t.Error("Wächter ohne Datenbank gebaut")
	}
	if (&Handler{db: e.pool}).energyAlarmWatch() != nil {
		t.Error("Wächter ohne Ticketdienst gebaut")
	}

	w := e.h.energyAlarmWatch()
	if w == nil || w.Store == nil || w.Skip == nil {
		t.Fatalf("Wächter unvollständig: %+v", w)
	}
	if sink, ok := w.Sink.(energyAlarmSink); !ok || sink.h != e.h {
		t.Errorf("Senke %T", w.Sink)
	}
	if w.Skip() {
		t.Error("Skip ohne Wartungssperre")
	}
	e.h.mlock.mu.Lock()
	e.h.mlock.locked = true
	e.h.mlock.mu.Unlock()
	if !w.Skip() {
		t.Error("Skip folgt nicht der Wartungssperre")
	}
	e.h.mlock.mu.Lock()
	e.h.mlock.locked = false
	e.h.mlock.mu.Unlock()

	// aktiver Server: startet (beendeter Kontext – der Durchlauf bricht sofort ab)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !e.h.startEnergyAlarms(ctx) {
		t.Error("Wächter am aktiven Server nicht gestartet")
	}
}
