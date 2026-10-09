package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// Push-Benachrichtigungen aufs Handy (migrations/116, Verschluesselung in webpush.go).
//
//	Mein Konto → „Push auf diesem Gerät einschalten“ (Service Worker /sw.js)
//	Core-Einstellungen → Push: ein/aus, Benutzergruppen je Art (Störung, Ticket),
//	  Wiederholung und Höchstdauer für „Anlage steht“
//
// Der Push-Dienst schaut alle 5 Sekunden nach neuen Stoerungen und Tickets
// (unabhaengig vom Anlegeweg) und benachrichtigt die Mitglieder der gewaehlten
// Gruppen – mit Inhalt und den Knoepfen „Annehmen“ / „Ansehen“. Steht die Anlage
// (plant_stopped), wiederholt sich der Alarm, bis der Erste annimmt; in der
// PDH-App erscheint er bildschirmfuellend. Wer zuerst annimmt, wird zustaendig;
// bei allen anderen verschwindet der Alarm.

const (
	keyPushEnabled     = "push_enabled"
	keyPushEnabledAt   = "push_enabled_at"
	keyPushGroupsFault = "push_groups_fault"
	keyPushGroupsTick  = "push_groups_ticket"
	keyPushRepeat      = "push_repeat_seconds"
	keyPushMaxMinutes  = "push_max_minutes"
	keyPushVAPIDPriv   = "push_vapid_private"
	keyPushVAPIDPub    = "push_vapid_public"

	pushDefaultRepeat = 60
	pushDefaultMax    = 120
	pushSettle        = 4 * time.Second // Nacharbeiten beim Anlegen (Gruppe, „Anlage steht“) abwarten
)

type pushSettings struct {
	Enabled                   bool
	EnabledAt                 time.Time
	GroupsFault, GroupsTicket []string
	RepeatSeconds, MaxMinutes int
}

func splitIDs(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); uuidInPathRe.MatchString(p) && len(p) == 36 {
			out = append(out, p)
		}
	}
	return out
}

func pushClamp(v, lo, hi, def int) int {
	if v == 0 {
		return def
	}
	return max(lo, min(hi, v))
}

func (h *Handler) pushSettings(ctx context.Context) pushSettings {
	s := pushSettings{RepeatSeconds: pushDefaultRepeat, MaxMinutes: pushDefaultMax}
	if h.db == nil {
		return s
	}
	s.Enabled = h.getUpdateSetting(ctx, keyPushEnabled, "0") == "1"
	s.EnabledAt, _ = time.Parse(time.RFC3339, h.getUpdateSetting(ctx, keyPushEnabledAt, ""))
	s.GroupsFault = splitIDs(h.getUpdateSetting(ctx, keyPushGroupsFault, ""))
	s.GroupsTicket = splitIDs(h.getUpdateSetting(ctx, keyPushGroupsTick, ""))
	rep, _ := strconv.Atoi(h.getUpdateSetting(ctx, keyPushRepeat, ""))
	s.RepeatSeconds = pushClamp(rep, 30, 600, pushDefaultRepeat)
	mx, _ := strconv.Atoi(h.getUpdateSetting(ctx, keyPushMaxMinutes, ""))
	s.MaxMinutes = pushClamp(mx, 5, 720, pushDefaultMax)
	return s
}

// pushVAPIDKeys: Absenderschluessel, beim ersten Gebrauch erzeugt.
func (h *Handler) pushVAPIDKeys(ctx context.Context) (pushVAPID, error) {
	v := pushVAPID{Private: h.getUpdateSetting(ctx, keyPushVAPIDPriv, ""), Public: h.getUpdateSetting(ctx, keyPushVAPIDPub, "")}
	if v.Private == "" || v.Public == "" {
		priv, pub, err := generateVAPIDKeys()
		if err != nil {
			return v, err
		}
		// nur setzen, wenn noch keiner da ist (mehrere Instanzen)
		if _, err := h.db.Exec(ctx, `INSERT INTO app_settings (key, value, updated_at) VALUES ($1, $2, NOW()), ($3, $4, NOW()) ON CONFLICT (key) DO NOTHING`,
			keyPushVAPIDPriv, priv, keyPushVAPIDPub, pub); err != nil {
			return v, err
		}
		v.Private, v.Public = h.getUpdateSetting(ctx, keyPushVAPIDPriv, ""), h.getUpdateSetting(ctx, keyPushVAPIDPub, "")
	}
	v.Subject = "mailto:pdh@localhost"
	if pub := strings.TrimRight(strings.TrimSpace(h.mailCfg.PublicURL), "/"); strings.HasPrefix(pub, "https://") {
		v.Subject = pub
	}
	return v, nil
}

// pushEnabledFor: Push eingeschaltet (fuer BaseData – Alarm-Fenster in der App).
func (h *Handler) pushEnabledFor(ctx context.Context) bool {
	return h.db != nil && h.getUpdateSetting(ctx, keyPushEnabled, "0") == "1"
}

// ── Geraete anmelden ─────────────────────────────────────────

// PushKeyWeb: GET /push/key → {enabled, key, devices}
func (h *Handler) PushKeyWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := map[string]any{"enabled": h.pushEnabledFor(ctx)}
	if out["enabled"] == true {
		v, err := h.pushVAPIDKeys(ctx)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Schlüssel nicht verfügbar"})
			return
		}
		out["key"] = v.Public
	}
	var n int
	_ = h.db.QueryRow(ctx, `SELECT COUNT(*) FROM push_subscriptions WHERE user_id = $1::uuid`, getUser(r).ID).Scan(&n)
	out["devices"] = n
	writeJSON(w, http.StatusOK, out)
}

type pushSubscribeIn struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// pushEndpointOK: nur https-Endpunkte (Push-Dienste der Browser), keine internen Adressen.
func pushEndpointOK(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || len(endpoint) > 1000 {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host != "localhost" && !strings.HasSuffix(host, ".local") && !strings.HasPrefix(host, "127.") && !strings.HasPrefix(host, "10.") &&
		!strings.HasPrefix(host, "192.168.") && !strings.HasPrefix(host, "169.254.") && !strings.Contains(host, ":")
}

// PushSubscribeWeb: POST /push/subscribe (JSON aus PushSubscription.toJSON())
func (h *Handler) PushSubscribeWeb(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var in pushSubscribeIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || !pushEndpointOK(in.Endpoint) || in.Keys.P256dh == "" || in.Keys.Auth == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "ungültiges Abonnement"})
		return
	}
	if p, err := b64Decode(in.Keys.P256dh); err != nil || len(p) != 65 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "ungültiger Schlüssel"})
		return
	}
	ua := r.UserAgent()
	if len(ua) > 300 {
		ua = ua[:300]
	}
	// ein Geraet gehoert immer der Person, die zuletzt eingeschaltet hat
	_, err := h.db.Exec(r.Context(), `INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth, user_agent) VALUES ($1::uuid, $2, $3, $4, $5)
		ON CONFLICT (endpoint) DO UPDATE SET user_id = EXCLUDED.user_id, p256dh = EXCLUDED.p256dh, auth = EXCLUDED.auth, user_agent = EXCLUDED.user_agent, failures = 0`,
		getUser(r).ID, in.Endpoint, in.Keys.P256dh, in.Keys.Auth, ua)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "nicht gespeichert"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// PushUnsubscribeWeb: POST /push/unsubscribe {endpoint}
func (h *Handler) PushUnsubscribeWeb(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var in pushSubscribeIn
	_ = json.NewDecoder(r.Body).Decode(&in)
	_, _ = h.db.Exec(r.Context(), `DELETE FROM push_subscriptions WHERE endpoint = $1 AND user_id = $2::uuid`, in.Endpoint, getUser(r).ID)
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// PushTestWeb: POST /push/test – Probe an alle eigenen Geraete.
func (h *Handler) PushTestWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !h.pushEnabledFor(ctx) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Push ist ausgeschaltet (Core-Einstellungen)"})
		return
	}
	payload, _ := json.Marshal(map[string]any{"type": "test", "tag": "pdh-test", "title": "PDH – Probe", "body": "Push-Benachrichtigungen kommen auf diesem Gerät an.", "url": "/users/me"})
	ok, total := h.pushToUsers(ctx, []string{getUser(r).ID}, payload, time.Hour, "normal", "pdh-test")
	if total == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Auf keinem Gerät eingeschaltet"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": ok > 0, "sent": ok, "devices": total})
}

// ── Versand ──────────────────────────────────────────────────

// pushToUsers schickt einen Inhalt an alle Geraete der Personen; liefert (zugestellt, Geraete).
func (h *Handler) pushToUsers(ctx context.Context, userIDs []string, payload []byte, ttl time.Duration, urgency, topic string) (int, int) {
	if len(userIDs) == 0 {
		return 0, 0
	}
	v, err := h.pushVAPIDKeys(ctx)
	if err != nil {
		componentLog("push").Error().Err(err).Msg("vapid")
		return 0, 0
	}
	rows, err := h.db.Query(ctx, `SELECT id::text, user_id::text, endpoint, p256dh, auth FROM push_subscriptions WHERE user_id::text = ANY($1::text[])`, userIDs)
	if err != nil {
		return 0, 0
	}
	var subs []pushSubscription
	for rows.Next() {
		var s pushSubscription
		if rows.Scan(&s.ID, &s.UserID, &s.Endpoint, &s.P256dh, &s.Auth) == nil {
			subs = append(subs, s)
		}
	}
	rows.Close()
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	ok := 0
	for _, s := range subs {
		wg.Add(1)
		sem <- struct{}{}
		go func(s pushSubscription) {
			defer wg.Done()
			defer func() { <-sem }()
			err := sendPush(ctx, v, s, payload, ttl, urgency, topic)
			switch {
			case err == nil:
				_, _ = h.db.Exec(ctx, `UPDATE push_subscriptions SET last_ok_at = NOW(), failures = 0 WHERE id = $1::uuid`, s.ID)
				mu.Lock()
				ok++
				mu.Unlock()
			case errors.Is(err, errPushGone):
				_, _ = h.db.Exec(ctx, `DELETE FROM push_subscriptions WHERE id = $1::uuid`, s.ID)
			default:
				componentLog("push").Warn().Err(err).Str("benutzer", s.UserID).Msg("zustellung")
				_, _ = h.db.Exec(ctx, `UPDATE push_subscriptions SET failures = failures + 1 WHERE id = $1::uuid`, s.ID)
				_, _ = h.db.Exec(ctx, `DELETE FROM push_subscriptions WHERE id = $1::uuid AND failures >= 20`, s.ID)
			}
		}(s)
	}
	wg.Wait()
	return ok, len(subs)
}

type pushAlert struct {
	ID, RefType, RefID, Title, Body string
	Urgent                          bool
	Recipients                      []string
}

func pushTopic(id string) string { return "a" + strings.ReplaceAll(id, "-", "")[:31] }

func (a pushAlert) payload(repeat int) []byte {
	b, _ := json.Marshal(map[string]any{
		"type": "alert", "id": a.ID, "tag": "pdh-alert-" + a.ID, "title": a.Title, "body": a.Body, "urgent": a.Urgent,
		"url": "/push/alert/" + a.ID, "accept": "/push/alert/" + a.ID + "/accept", "repeat": repeat,
	})
	return b
}

func (h *Handler) sendAlert(ctx context.Context, a pushAlert, repeat int) {
	ttl, urgency := time.Hour, "normal"
	if a.Urgent {
		ttl, urgency = 5*time.Minute, "high"
	}
	h.pushToUsers(ctx, a.Recipients, a.payload(repeat), ttl, urgency, pushTopic(a.ID))
	_, _ = h.db.Exec(ctx, `UPDATE push_alerts SET last_sent_at = NOW(), sends = sends + 1 WHERE id = $1::uuid`, a.ID)
}

// closeAlertPush: Alarm auf allen Geraeten wegnehmen (Service Worker schliesst die Nachricht).
func (h *Handler) closeAlertPush(ctx context.Context, id string, recipients []string, note string) {
	b, _ := json.Marshal(map[string]any{"type": "close", "id": id, "tag": "pdh-alert-" + id, "note": note})
	h.pushToUsers(ctx, recipients, b, 10*time.Minute, "high", pushTopic(id))
}

// pushAlertText: Titel und Inhalt der Nachricht.
func pushAlertText(refType, title, desc, prio, infra string, urgent bool) (string, string) {
	label := map[string]string{"fault": "Neue Störung", "ticket": "Neues Ticket"}[refType]
	head := label + ": " + title
	if urgent {
		head = "🚨 Anlage steht – " + title
	}
	var parts []string
	if infra != "" {
		parts = append(parts, infra)
	}
	if p := priorityLabels[prio]; p != "" {
		parts = append(parts, "Priorität "+p)
	}
	body := strings.Join(parts, " · ")
	if d := strings.TrimSpace(desc); d != "" {
		if r := []rune(d); len(r) > 240 {
			d = string(r[:237]) + " …"
		}
		if body != "" {
			body += "\n"
		}
		body += d
	}
	if r := []rune(head); len(r) > 120 {
		head = string(r[:117]) + " …"
	}
	return head, body
}

// pushRecipients: aktive Mitglieder der Gruppen (ohne Systemkonten und ohne den Ersteller).
func (h *Handler) pushRecipients(ctx context.Context, groups []string, exclude string) []string {
	if len(groups) == 0 {
		return nil
	}
	rows, err := h.db.Query(ctx, `SELECT DISTINCT u.id::text FROM user_group_members m
		JOIN user_groups g ON g.id = m.group_id AND g.active
		JOIN users u ON u.id = m.user_id AND u.active AND NOT u.is_system_user
		WHERE m.group_id::text = ANY($1::text[])`, groups)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && id != exclude {
			out = append(out, id)
		}
	}
	return out
}

// ── Dienst ───────────────────────────────────────────────────

// StartPushNotifier prueft alle 5 Sekunden auf neue Vorgaenge und offene Alarme.
func (h *Handler) StartPushNotifier(ctx context.Context) {
	if h.db == nil {
		return
	}
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				runCtx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
				h.runPush(runCtx)
				cancel()
			}
		}
	}()
}

var pushRunning sync.Mutex

func (h *Handler) runPush(ctx context.Context) {
	if !pushRunning.TryLock() {
		return // voriger Durchlauf (langsame Zustellung) noch nicht fertig
	}
	defer pushRunning.Unlock()
	s := h.pushSettings(ctx)
	if s.Enabled {
		h.pushNewRecords(ctx, s)
	}
	h.pushMaintain(ctx, s)
}

const pushOpenFault = `('detected','analyzing','in_progress','pending')`
const pushOpenTicket = `('open','in_progress','pending')`

// pushNewRecords: neue Stoerungen/Tickets (seit dem Einschalten, hoechstens 10 Minuten alt).
func (h *Handler) pushNewRecords(ctx context.Context, s pushSettings) {
	since := s.EnabledAt
	if since.IsZero() {
		since = time.Now().Add(-time.Minute)
	}
	rows, err := h.db.Query(ctx, `
		SELECT 'fault', f.id::text, f.title, COALESCE(f.description, ''), COALESCE(f.severity::text, ''), f.plant_stopped,
		       COALESCE(f.created_by::text, ''), COALESCE(f.assigned_to::text, ''), COALESCE(i.name, '')
		FROM faults f LEFT JOIN infrastructure i ON i.id = f.infrastructure_id
		WHERE f.created_at > GREATEST(NOW() - INTERVAL '10 minutes', $1::timestamptz) AND f.created_at < NOW() - $2::interval
		  AND f.status IN `+pushOpenFault+` AND f.archived_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM push_alerts a WHERE a.ref_type = 'fault' AND a.ref_id = f.id)
		UNION ALL
		SELECT 'ticket', t.id::text, t.title, COALESCE(t.description, ''), COALESCE(t.priority::text, ''), t.plant_stopped,
		       COALESCE(t.created_by::text, ''), COALESCE(t.assigned_to::text, ''), COALESCE(i.name, '')
		FROM tickets t LEFT JOIN infrastructure i ON i.id = t.infrastructure_id
		WHERE t.created_at > GREATEST(NOW() - INTERVAL '10 minutes', $1::timestamptz) AND t.created_at < NOW() - $2::interval
		  AND t.status IN `+pushOpenTicket+` AND t.archived_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM push_alerts a WHERE a.ref_type = 'ticket' AND a.ref_id = t.id)`,
		since, fmt.Sprintf("%d seconds", int(pushSettle.Seconds())))
	if err != nil {
		componentLog("push").Warn().Err(err).Msg("neue vorgänge")
		return
	}
	type rec struct {
		typ, id, title, desc, prio, creator, assignee, infra string
		urgent                                               bool
	}
	var recs []rec
	for rows.Next() {
		var x rec
		if rows.Scan(&x.typ, &x.id, &x.title, &x.desc, &x.prio, &x.urgent, &x.creator, &x.assignee, &x.infra) == nil {
			recs = append(recs, x)
		}
	}
	rows.Close()
	for _, x := range recs {
		groups := s.GroupsFault
		if x.typ == "ticket" {
			groups = s.GroupsTicket
		}
		recipients := h.pushRecipients(ctx, groups, x.creator)
		title, body := pushAlertText(x.typ, x.title, x.desc, x.prio, x.infra, x.urgent)
		var id string
		// auch ohne Empfaenger eintragen – sonst wuerde der Vorgang jede Runde neu geprueft
		err := h.db.QueryRow(ctx, `INSERT INTO push_alerts (ref_type, ref_id, title, body, urgent, recipients, initial_assignee, closed_at, close_reason)
			VALUES ($1, $2::uuid, $3, $4, $5, $6::uuid[], NULLIF($7, '')::uuid, CASE WHEN cardinality($6::uuid[]) = 0 THEN NOW() END,
			        CASE WHEN cardinality($6::uuid[]) = 0 THEN 'keine Empfänger' ELSE '' END)
			ON CONFLICT (ref_type, ref_id) DO NOTHING RETURNING id::text`,
			x.typ, x.id, title, body, x.urgent, recipients, x.assignee).Scan(&id)
		if err != nil || len(recipients) == 0 {
			continue // schon von einer anderen Instanz verschickt oder niemand zu benachrichtigen
		}
		h.sendAlert(ctx, pushAlert{ID: id, RefType: x.typ, RefID: x.id, Title: title, Body: body, Urgent: x.urgent, Recipients: recipients}, 0)
		h.addHistory(ctx, x.typ, x.id, "push", "push", "", fmt.Sprintf("%d Personen", len(recipients)), pushHistoryNote(x.urgent), "")
	}
}

func pushHistoryNote(urgent bool) string {
	if urgent {
		return "Push-Alarm „Anlage steht“ verschickt – wiederholt sich bis zur Annahme"
	}
	return "Push-Benachrichtigung verschickt"
}

// pushDecision: was mit einem offenen Alarm passiert.
func pushDecision(open bool, assignee, initial string, urgent bool, age, sinceLast time.Duration, s pushSettings) (closeReason string, resend bool) {
	switch {
	case !open:
		return "Vorgang erledigt", false
	case assignee != "" && assignee != initial:
		return "anderweitig zugewiesen", false
	case urgent && age > time.Duration(s.MaxMinutes)*time.Minute:
		return "ohne Annahme abgelaufen", false
	case !urgent && age > 24*time.Hour:
		return "abgelaufen", false
	case urgent && sinceLast >= time.Duration(s.RepeatSeconds)*time.Second:
		return "", true
	}
	return "", false
}

// pushMaintain: Alarme wiederholen bzw. schliessen.
func (h *Handler) pushMaintain(ctx context.Context, s pushSettings) {
	rows, err := h.db.Query(ctx, `
		SELECT a.id::text, a.ref_type, a.ref_id::text, a.title, a.body, a.urgent, a.recipients::text[], a.sends,
		       EXTRACT(EPOCH FROM NOW() - a.created_at)::bigint, EXTRACT(EPOCH FROM NOW() - COALESCE(a.last_sent_at, a.created_at))::bigint,
		       COALESCE(a.initial_assignee::text, ''), COALESCE(r.open, false), COALESCE(r.assignee, '')
		FROM push_alerts a
		LEFT JOIN LATERAL (
			SELECT f.status IN `+pushOpenFault+` AND f.archived_at IS NULL AS open, COALESCE(f.assigned_to::text, '') AS assignee
			  FROM faults f WHERE a.ref_type = 'fault' AND f.id = a.ref_id
			UNION ALL
			SELECT t.status IN `+pushOpenTicket+` AND t.archived_at IS NULL, COALESCE(t.assigned_to::text, '')
			  FROM tickets t WHERE a.ref_type = 'ticket' AND t.id = a.ref_id
		) r ON true
		WHERE a.closed_at IS NULL`)
	if err != nil {
		componentLog("push").Warn().Err(err).Msg("offene alarme")
		return
	}
	type row struct {
		a                 pushAlert
		sends             int
		age, since        int64
		initial, assignee string
		open              bool
	}
	var list []row
	for rows.Next() {
		var x row
		if rows.Scan(&x.a.ID, &x.a.RefType, &x.a.RefID, &x.a.Title, &x.a.Body, &x.a.Urgent, &x.a.Recipients, &x.sends,
			&x.age, &x.since, &x.initial, &x.open, &x.assignee) == nil {
			list = append(list, x)
		}
	}
	rows.Close()
	for _, x := range list {
		reason, resend := pushDecision(x.open, x.assignee, x.initial, x.a.Urgent, time.Duration(x.age)*time.Second, time.Duration(x.since)*time.Second, s)
		if reason != "" {
			tag, err := h.db.Exec(ctx, `UPDATE push_alerts SET closed_at = NOW(), close_reason = $2 WHERE id = $1::uuid AND closed_at IS NULL`, x.a.ID, reason)
			if err == nil && tag.RowsAffected() == 1 && s.Enabled {
				h.closeAlertPush(ctx, x.a.ID, x.a.Recipients, reason)
			}
			continue
		}
		if resend && s.Enabled {
			h.sendAlert(ctx, x.a, x.sends)
		}
	}
}

// ── Annehmen und Alarmseite ──────────────────────────────────

type pushAlertView struct {
	ID, RefType, RefID, Title, Body, Kind, DetailURL string
	Urgent, Closed, Recipient                        bool
	AcceptedBy, AcceptedAt, Created, CloseReason     string
}

func (h *Handler) loadPushAlert(ctx context.Context, id, userID string) (pushAlertView, error) {
	var v pushAlertView
	var created time.Time
	var acceptedAt *time.Time
	err := h.db.QueryRow(ctx, `SELECT a.id::text, a.ref_type, a.ref_id::text, a.title, a.body, a.urgent, a.closed_at IS NOT NULL,
			$2::uuid = ANY(a.recipients), COALESCE(NULLIF(TRIM(u.first_name || ' ' || u.last_name), ''), ''), a.accepted_at, a.created_at, a.close_reason
		FROM push_alerts a LEFT JOIN users u ON u.id = a.accepted_by WHERE a.id = $1::uuid`, id, userID).
		Scan(&v.ID, &v.RefType, &v.RefID, &v.Title, &v.Body, &v.Urgent, &v.Closed, &v.Recipient, &v.AcceptedBy, &acceptedAt, &created, &v.CloseReason)
	if err != nil {
		return v, err
	}
	v.Kind = map[string]string{"fault": "Störung", "ticket": "Ticket"}[v.RefType]
	v.DetailURL = map[string]string{"fault": "/faults/", "ticket": "/tickets/"}[v.RefType] + v.RefID
	v.Created = created.Local().Format("02.01. 15:04")
	if acceptedAt != nil {
		v.AcceptedAt = acceptedAt.Local().Format("15:04")
	}
	return v, nil
}

// pushMayView: Empfaenger oder wer die Art ansehen darf.
func (h *Handler) pushMayView(r *http.Request, v pushAlertView) bool {
	return v.Recipient || h.hasPerm(r, map[string]string{"fault": "faults.view", "ticket": "tickets.view"}[v.RefType])
}

// PushAlertPage: GET /push/alert/{id} – Vollbild-Alarm mit „Annehmen“.
func (h *Handler) PushAlertPage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	v, err := h.loadPushAlert(r.Context(), id, getUser(r).ID)
	if err != nil || !h.pushMayView(r, v) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	t, err := h.tmpl.Clone()
	if err == nil {
		t = bindLang(t, h.requestLang(r))
		if _, err = t.ParseFiles("web/templates/push_alert.gohtml"); err == nil {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			err = t.ExecuteTemplate(w, "push_alert.gohtml", v)
		}
	}
	if err != nil {
		componentLog("push").Error().Err(err).Msg("alarmseite")
	}
}

// PushAlertStateWeb: GET /push/alert/{id}/state – fuer die Alarmseite (wer hat angenommen?).
func (h *Handler) PushAlertStateWeb(w http.ResponseWriter, r *http.Request) {
	v, err := h.loadPushAlert(r.Context(), chi.URLParam(r, "id"), getUser(r).ID)
	if err != nil || !h.pushMayView(r, v) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "nicht gefunden"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"closed": v.Closed, "accepted_by": v.AcceptedBy, "accepted_at": v.AcceptedAt, "reason": v.CloseReason, "url": v.DetailURL})
}

// PushAlertsOpenWeb: GET /push/alerts/open – eigene offene „Anlage steht“-Alarme (Vollbild in der App).
func (h *Handler) PushAlertsOpenWeb(w http.ResponseWriter, r *http.Request) {
	out := make([]map[string]any, 0)
	rows, err := h.db.Query(r.Context(), `SELECT id::text, title, body, ref_type FROM push_alerts
		WHERE closed_at IS NULL AND urgent AND $1::uuid = ANY(recipients) ORDER BY created_at`, getUser(r).ID)
	if err == nil {
		for rows.Next() {
			var id, title, body, typ string
			if rows.Scan(&id, &title, &body, &typ) == nil {
				out = append(out, map[string]any{"id": id, "title": title, "body": body, "kind": map[string]string{"fault": "Störung", "ticket": "Ticket"}[typ],
					"url": "/push/alert/" + id, "accept": "/push/alert/" + id + "/accept"})
			}
		}
		rows.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": out})
}

// PushAlertAcceptWeb: POST /push/alert/{id}/accept – wer zuerst annimmt, wird zustaendig.
func (h *Handler) PushAlertAcceptWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := getUser(r)
	id := chi.URLParam(r, "id")
	var refType, refID string
	var recipients []string
	err := h.db.QueryRow(ctx, `UPDATE push_alerts SET accepted_by = $2::uuid, accepted_at = NOW(), closed_at = NOW(), close_reason = 'angenommen'
		WHERE id = $1::uuid AND closed_at IS NULL AND $2::uuid = ANY(recipients) RETURNING ref_type, ref_id::text, recipients::text[]`, id, u.ID).
		Scan(&refType, &refID, &recipients)
	if err != nil {
		v, lerr := h.loadPushAlert(ctx, id, u.ID)
		switch {
		case lerr != nil:
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "Benachrichtigung nicht gefunden"})
		case v.AcceptedBy != "":
			writeJSON(w, http.StatusConflict, map[string]any{"error": "Schon angenommen von " + v.AcceptedBy, "accepted_by": v.AcceptedBy, "url": v.DetailURL})
		case v.Closed:
			writeJSON(w, http.StatusConflict, map[string]any{"error": "Nicht mehr offen (" + v.CloseReason + ")", "url": v.DetailURL})
		default:
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "Diese Benachrichtigung ging nicht an dich"})
		}
		return
	}
	// zustaendig werden und in Arbeit setzen (wie „Annehmen“ im Leitstand)
	q := `UPDATE faults SET assigned_to = $1::uuid, status = 'in_progress', updated_at = NOW() WHERE id = $2::uuid AND status IN ` + pushOpenFault
	if refType == "ticket" {
		q = `UPDATE tickets SET assigned_to = $1::uuid, status = 'in_progress', updated_at = NOW() WHERE id = $2::uuid AND status IN ` + pushOpenTicket
	}
	if _, err := h.db.Exec(ctx, q, u.ID, refID); err != nil {
		componentLog("push").Error().Err(err).Msg("annehmen")
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	h.addHistory(ctx, refType, refID, "update", "assigned_to", "", name, "Per Push-Benachrichtigung angenommen", u.ID)
	var others []string
	for _, rid := range recipients {
		if rid != u.ID {
			others = append(others, rid)
		}
	}
	go func() {
		bctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		h.closeAlertPush(bctx, id, others, "angenommen von "+name)
	}()
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "url": map[string]string{"fault": "/faults/", "ticket": "/tickets/"}[refType] + refID})
}

// ── „Anlage steht“ ───────────────────────────────────────────

// markPlantStopped: „Anlage steht“ beim Anlegen (Leitstand, Easy-Mode) setzen.
func (h *Handler) markPlantStopped(ctx context.Context, refType, id, userID string) {
	table := map[string]string{"fault": "faults", "ticket": "tickets"}[refType]
	if table == "" || id == "" {
		return
	}
	if _, err := h.db.Exec(ctx, `UPDATE `+table+` SET plant_stopped = true WHERE id = $1::uuid`, id); err != nil {
		componentLog("push").Warn().Err(err).Msg("anlage steht")
		return
	}
	h.addHistory(ctx, refType, id, "update", "plant_stopped", "", "ja", "Anlage steht", userID)
}

// RecordPlantStoppedWeb: POST /records/{refType}/{id}/plant-stopped (value=1|0) –
// Ersteller (in den ersten 10 Minuten) oder wer die Art bearbeiten darf.
func (h *Handler) RecordPlantStoppedWeb(w http.ResponseWriter, r *http.Request) {
	refType, id := chi.URLParam(r, "refType"), chi.URLParam(r, "id")
	table := map[string]string{"fault": "faults", "ticket": "tickets"}[refType]
	if table == "" || !uuidInPathRe.MatchString(id) {
		http.Error(w, "unbekannter vorgang", http.StatusBadRequest)
		return
	}
	r.ParseForm()
	on := r.FormValue("value") == "1"
	u := getUser(r)
	editPerm := map[string]string{"fault": "faults.edit", "ticket": "tickets.edit"}[refType]
	tag, err := h.db.Exec(r.Context(), `UPDATE `+table+` SET plant_stopped = $2 WHERE id = $1::uuid
		AND ($4 OR (created_by = $3::uuid AND created_at > NOW() - INTERVAL '10 minutes'))`, id, on, nullID(u.ID), h.hasPerm(r, editPerm))
	if err != nil || tag.RowsAffected() == 0 {
		http.Error(w, "nicht erlaubt oder nicht gefunden", http.StatusForbidden)
		return
	}
	if on {
		h.addHistory(r.Context(), refType, id, "update", "plant_stopped", "", "ja", "Anlage steht", u.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Einstellungen ────────────────────────────────────────────

type pushGroupOption struct {
	ID, Name            string
	Members             int
	ForFault, ForTicket bool
}

type pushSettingsView struct {
	pushSettings
	Groups  []pushGroupOption
	Devices int
	HTTPS   bool
}

func (h *Handler) pushSettingsView(ctx context.Context) pushSettingsView {
	v := pushSettingsView{pushSettings: h.pushSettings(ctx)}
	if h.db == nil {
		return v
	}
	in := func(list []string, id string) bool {
		for _, x := range list {
			if x == id {
				return true
			}
		}
		return false
	}
	rows, err := h.db.Query(ctx, `SELECT g.id::text, g.name, (SELECT COUNT(*) FROM user_group_members m WHERE m.group_id = g.id)
		FROM user_groups g WHERE g.active ORDER BY lower(g.name)`)
	if err == nil {
		for rows.Next() {
			var g pushGroupOption
			if rows.Scan(&g.ID, &g.Name, &g.Members) == nil {
				g.ForFault, g.ForTicket = in(v.GroupsFault, g.ID), in(v.GroupsTicket, g.ID)
				v.Groups = append(v.Groups, g)
			}
		}
		rows.Close()
	}
	_ = h.db.QueryRow(ctx, `SELECT COUNT(*) FROM push_subscriptions`).Scan(&v.Devices)
	v.HTTPS = strings.HasPrefix(strings.TrimSpace(h.mailCfg.PublicURL), "https://")
	return v
}

// PushSettingsWeb: POST /core/settings/push – Core-Einstellungen.
func (h *Handler) PushSettingsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	ctx := r.Context()
	on := r.FormValue("enabled") == "on"
	was := h.pushSettings(ctx).Enabled
	rep, _ := strconv.Atoi(r.FormValue("repeat_seconds"))
	mx, _ := strconv.Atoi(r.FormValue("max_minutes"))
	set := map[string]string{
		keyPushEnabled:     map[bool]string{true: "1", false: "0"}[on],
		keyPushGroupsFault: strings.Join(splitIDs(strings.Join(r.Form["groups_fault"], ",")), ","),
		keyPushGroupsTick:  strings.Join(splitIDs(strings.Join(r.Form["groups_ticket"], ",")), ","),
		keyPushRepeat:      strconv.Itoa(pushClamp(rep, 30, 600, pushDefaultRepeat)),
		keyPushMaxMinutes:  strconv.Itoa(pushClamp(mx, 5, 720, pushDefaultMax)),
	}
	if on && !was {
		// erst ab jetzt angelegte Vorgaenge – keine Nachrichten fuer Altbestand
		set[keyPushEnabledAt] = time.Now().UTC().Format(time.RFC3339)
	}
	for k, val := range set {
		if err := h.setUpdateSetting(ctx, k, val); err != nil {
			http.Error(w, "Einstellung konnte nicht gespeichert werden", http.StatusInternalServerError)
			return
		}
	}
	if on {
		if _, err := h.pushVAPIDKeys(ctx); err != nil {
			componentLog("push").Error().Err(err).Msg("vapid erzeugen")
		}
	}
	msg := "Push-Benachrichtigungen ausgeschaltet"
	if on {
		msg = "Push-Benachrichtigungen gespeichert"
	}
	http.Redirect(w, r, "/core/settings?notice="+url.QueryEscape(msg)+"#push", http.StatusSeeOther)
}
