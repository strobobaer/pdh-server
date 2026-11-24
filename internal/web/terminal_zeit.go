package web

// Zeiterfassung am Terminal (0.82.51): Seite „Zeiterfassung“ mit Funktionsschalter zeiterfassung (Grundwert aus).
//
//	GET  /terminal/zeit                 Stand (wer ist da / in Pause / arbeitet woran) und Bedienung
//	                                    ?wer=<id> Person aus der Auswahl (ohne Karte), ?arbeit=1 Fenster „Arbeit beginnen“
//	POST /terminal/zeit/buchen          kind=kommen|gehen|pause_beginn|pause_ende, who=<id> (nur ohne Karte)
//	POST /terminal/zeit/arbeit/start    type=fault|ticket|task|maintenance|plant, id, who[] (nur ohne Karte)
//	POST /terminal/zeit/arbeit/stop     who[] (nur ohne Karte)
//
// Wer bucht: Mit Karte nur die Kartenperson für sich. Ohne Karte nur in der Betriebsart „Bearbeiten am Terminal“ =
// ohne_karte (dieselbe Einstellung wie bei den Vorgängen) und nur für Personen aus der Personenauswahl des Terminals
// (terminal_pick.go); eine fehlende Person (422), eine Person außerhalb der Auswahl (422) und nur_karte ohne Karte
// (403) werden abgewiesen. Alle Schreibwege verlangen die Gerätesitzung. Buchungen ohne Karte tragen die Herkunft
// „am Terminal gewählt, nicht selbst angemeldet“ (terminal_gewaehlt); ihre Zeiteinträge an Vorgängen sind unbestätigt.
//
// Das Flag „Freier Terminal-Benutzer“ gilt wie bei den übrigen Terminalfunktionen: Es ersetzt die Rechte des
// Terminal-Benutzers am Vorgang (terminalFreePerms, vorgCan) – die Anwesenheit selbst braucht kein Recht, nur
// Funktionsschalter, Gerätesitzung und die Personenauswahl.
//
// Datenschutz: Der öffentliche Bildschirm zeigt nur den Stand (anwesend, in Pause, arbeitet an …) ohne Uhrzeiten
// einzelner Personen. Die eigenen Zeiten sieht nur die Person nach der Kartenanmeldung.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

)

// Buchungsarten.
const (
	attKommen      = "kommen"
	attGehen       = "gehen"
	attPauseBegin  = "pause_beginn"
	attPauseEnde   = "pause_ende"
	zeitViaPDH     = "pdh"
	attStaleAfter  = 18 * time.Hour // eine so lange offene Anwesenheit oder Arbeit gilt am Terminal als vergessen
	zeitPickMax    = 50
	zeitPlantsMax  = 100
	zeitWhoMax     = 50
	attKindsPrompt = "kommen, gehen, pause_beginn, pause_ende"
)

var attKinds = []string{attKommen, attGehen, attPauseBegin, attPauseEnde}

var attKindLabel = map[string]string{attKommen: "Kommen", attGehen: "Gehen", attPauseBegin: "Pause Beginn", attPauseEnde: "Pause Ende"}

func attKindOK(k string) bool { _, ok := attKindLabel[k]; return ok }

// attState: Anwesenheitszustand einer Person.
type attState string

const (
	attAbwesend attState = "abwesend"
	attAnwesend attState = "anwesend"
	attPause    attState = "pause"
)

// attNext prüft eine Buchung gegen den Zustand und liefert den neuen Zustand oder eine deutsche Meldung.
func attNext(cur attState, kind string) (attState, string) {
	switch kind {
	case attKommen:
		switch cur {
		case attAbwesend:
			return attAnwesend, ""
		case attAnwesend:
			return cur, "Kommen ist schon gebucht – bitte erst Gehen buchen."
		default:
			return cur, "Kommen ist schon gebucht, gerade ist Pause – bitte erst die Pause beenden."
		}
	case attGehen:
		switch cur {
		case attAnwesend:
			return attAbwesend, ""
		case attPause:
			return cur, "Gehen geht erst nach der Pause – bitte zuerst Pause Ende buchen."
		default:
			return cur, "Gehen ohne Kommen geht nicht – bitte zuerst Kommen buchen."
		}
	case attPauseBegin:
		switch cur {
		case attAnwesend:
			return attPause, ""
		case attPause:
			return cur, "Die Pause läuft schon."
		default:
			return cur, "Eine Pause gibt es nur während der Anwesenheit – bitte zuerst Kommen buchen."
		}
	case attPauseEnde:
		switch cur {
		case attPause:
			return attAnwesend, ""
		case attAnwesend:
			return cur, "Es läuft keine Pause."
		default:
			return cur, "Es läuft keine Pause – du bist nicht angemeldet."
		}
	}
	return cur, "Unbekannte Buchung."
}

// attStateOf: Zustand nach der letzten Buchung; was länger als attStaleAfter offen ist, gilt als vergessen (abwesend).
func attStateOf(kind string, at, now time.Time) attState {
	var s attState
	switch kind {
	case attKommen, attPauseEnde:
		s = attAnwesend
	case attPauseBegin:
		s = attPause
	default:
		return attAbwesend
	}
	if now.Sub(at) > attStaleAfter {
		return attAbwesend
	}
	return s
}

// zeitErr: Meldung für die Person (nicht protokolliert).
type zeitErr struct {
	Code int
	Msg  string
}

func (e *zeitErr) Error() string { return e.Msg }

type pgxQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// attLast: letzte Buchung der Person.
func attLast(ctx context.Context, q pgxQuerier, uid string) (kind string, at time.Time, found bool, err error) {
	err = q.QueryRow(ctx, `SELECT kind, at FROM attendance_events WHERE user_id = $1::uuid ORDER BY at DESC, created_at DESC, id DESC LIMIT 1`, uid).Scan(&kind, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, false, nil
	}
	return kind, at, err == nil, err
}

// attStateNow: aktueller Zustand der Person.
func attStateNow(ctx context.Context, q pgxQuerier, uid string, now time.Time) (attState, error) {
	k, at, found, err := attLast(ctx, q, uid)
	if err != nil || !found {
		return attAbwesend, err
	}
	return attStateOf(k, at, now), nil
}

func zeitLock(ctx context.Context, tx pgx.Tx, uid string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('zeit:' || $1::text, 0))`, uid)
	return err
}

// zeitStopped: eine beendete Arbeit (für Verlauf und Meldung nach dem Speichern).
type zeitStopped struct {
	RefType, RefID, Title, Via, Name string
	Minutes                          int
	Infra                            string
}

// workStopTx beendet die laufende Arbeit der Person (falls vorhanden) und schreibt den Zeiteintrag am Vorgang.
func (h *Handler) workStopTx(ctx context.Context, tx pgx.Tx, uid string, now time.Time) (*zeitStopped, error) {
	var id, via, termUID string
	s := &zeitStopped{}
	var started time.Time
	var infra *string
	err := tx.QueryRow(ctx, `SELECT w.id::text, w.ref_type, w.ref_id::text, w.title, w.origin_via, w.started_at, COALESCE(w.terminal_user_id::text, ''), w.infrastructure_id::text,
			(SELECT TRIM(u.first_name || ' ' || u.last_name) FROM users u WHERE u.id = w.user_id)
		FROM work_sessions w WHERE w.user_id = $1::uuid AND w.ended_at IS NULL FOR UPDATE`, uid).
		Scan(&id, &s.RefType, &s.RefID, &s.Title, &via, &started, &termUID, &infra, &s.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if infra != nil {
		s.Infra = *infra
	}
	s.Via = via
	if now.Before(started) {
		now = started
	}
	s.Minutes = max(1, int(now.Sub(started).Minutes()))
	var entryID *string
	if s.RefType != "plant" {
		kind := zeitKindOf(s.RefType)
		desc := "Arbeit am Terminal: " + kind.Label + ": " + s.Title
		if n := vorgViaLabel[via]; n != "" {
			desc += " – " + n
		}
		var eid string
		err := tx.QueryRow(ctx, `INSERT INTO time_entries (id, user_id, ref_type, ref_id, description, started_at, ended_at, duration_min, pending, pending_from, infrastructure_id, origin_via)
			VALUES (pdh_uuid_v7(), $1::uuid, $2::time_ref_type, $3::uuid, $4, $5, $6, $7, $8, NULLIF($9, '')::uuid, NULLIF($10, '')::uuid, $11) RETURNING id::text`,
			uid, s.RefType, s.RefID, desc, started, now, s.Minutes, via == vorgViaGewaehlt, termUID, s.Infra, via).Scan(&eid)
		if err != nil {
			return nil, err
		}
		entryID = &eid
	}
	if _, err := tx.Exec(ctx, `UPDATE work_sessions SET ended_at = $2, time_entry_id = $3::uuid WHERE id = $1::uuid`, id, now, entryID); err != nil {
		return nil, err
	}
	return s, nil
}

func zeitKindOf(refType string) vorgKind {
	k, _ := vorgKindOf(refType)
	return k
}

// zeitStoppedHistory schreibt den Verlauf des Vorgangs zur beendeten Arbeit.
func (h *Handler) zeitStoppedHistory(ctx context.Context, s *zeitStopped, personID, by, place string) {
	if s == nil || s.RefType == "plant" {
		return
	}
	h.vorgHistory(ctx, historyRef(s.RefType), s.RefID, "time", "Arbeitszeit", "", fmt.Sprintf("%d min", s.Minutes),
		fmt.Sprintf("Arbeitszeit am Terminal erfasst: %s, %d min – %s (%s)", s.Name, s.Minutes, place, vorgViaLabel[s.Via]), by, s.Via, place, []string{personID})
}

// ---- Gate ----

type zeitCtx struct {
	P        terminalPage
	Act      vorgActor
	Tree     terminalPlantTree
	Device   bool
	TermName string
}

// zeitGate: Seite und Anlage (404), bei POST fremde Seite und Gerätesitzung (403).
func (h *Handler) zeitGate(w http.ResponseWriter, r *http.Request) (*zeitCtx, bool) {
	post := r.Method == http.MethodPost
	if post && terminalForeignPost(w, r) {
		return nil, false
	}
	p, uid, ok := h.terminalSetupGate(w, r, "zeit")
	if !ok {
		return nil, false
	}
	c := &zeitCtx{P: p, Device: h.terminalDeviceSession(r)}
	if post && !c.Device {
		http.Error(w, "Die Zeiterfassung bedient nur das Terminal selbst.", http.StatusForbidden)
		return nil, false
	}
	act, err := h.vorgActorFor(r, uid)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("kartensitzung lesen")
		http.Error(w, "Daten nicht verfügbar", http.StatusInternalServerError)
		return nil, false
	}
	c.Act = act
	c.Tree = h.terminalPlantTree(r, p.InfraID, p.Name)
	var first, last, uname string
	_ = h.db.QueryRow(r.Context(), `SELECT first_name, last_name, username FROM users WHERE id = $1::uuid`, uid).Scan(&first, &last, &uname)
	c.TermName = displayUserName(first, last, uname)
	return c, true
}

func (c *zeitCtx) via() string { return c.Act.via() }

// canAct: darf an diesem Gerät gebucht werden (Gerätesitzung; Karte oder Betriebsart ohne_karte)?
func (c *zeitCtx) canAct() bool { return c.Device && c.Act.canWrite(true) }

// zeitWho löst die Personen eines Schreibwegs auf. Mit Karte nur die Kartenperson; ohne Karte nur in ohne_karte und
// nur aus der Personenauswahl des Terminals.
func (h *Handler) zeitWho(ctx context.Context, c *zeitCtx, ids []string) ([]terminalPerson, *zeitErr) {
	if c.Act.Card != nil {
		self := terminalPerson{ID: c.Act.Card.ID, Name: strings.TrimSpace(c.Act.Card.FirstName + " " + c.Act.Card.LastName)}
		for _, id := range ids {
			if id = strings.TrimSpace(id); id != "" && id != self.ID {
				return nil, &zeitErr{http.StatusUnprocessableEntity, "Mit Karte bucht jede Person nur für sich."}
			}
		}
		return []terminalPerson{self}, nil
	}
	if c.Act.Modus != vorgModusOhneKarte {
		return nil, &zeitErr{http.StatusForbidden, "Dieses Terminal erfasst Zeiten nur mit Karte – bitte Karte vorhalten."}
	}
	var chosen []string
	seen := map[string]bool{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			chosen = append(chosen, id)
		}
	}
	if len(chosen) == 0 {
		return nil, &zeitErr{http.StatusUnprocessableEntity, "Bitte eine Person wählen oder die Karte vorhalten."}
	}
	if len(chosen) > zeitWhoMax {
		return nil, &zeitErr{http.StatusBadRequest, "Zu viele Personen gewählt."}
	}
	people, err := h.terminalPickPeople(ctx, c.Act.TermUID)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("mitarbeitende lesen")
		return nil, &zeitErr{http.StatusInternalServerError, "Daten nicht verfügbar"}
	}
	byID := map[string]terminalPerson{}
	for _, p := range people {
		byID[p.ID] = p
	}
	var out []terminalPerson
	for _, id := range chosen {
		p, ok := byID[id]
		if !ok {
			return nil, &zeitErr{http.StatusUnprocessableEntity, "Diese Person steht an diesem Terminal nicht zur Auswahl."}
		}
		out = append(out, p)
	}
	return out, nil
}

// ---- Buchungen ----

// zeitBook bucht eine Anwesenheit; beim Gehen und beim Pausenbeginn endet die laufende Arbeit (mit Meldung).
func (h *Handler) zeitBook(ctx context.Context, c *zeitCtx, p terminalPerson, kind string, now time.Time) (string, *zeitErr) {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return "", &zeitErr{http.StatusInternalServerError, "Daten nicht verfügbar"}
	}
	defer tx.Rollback(ctx)
	if err := zeitLock(ctx, tx, p.ID); err != nil {
		return "", &zeitErr{http.StatusInternalServerError, "Daten nicht verfügbar"}
	}
	cur, err := attStateNow(ctx, tx, p.ID, now)
	if err != nil {
		return "", &zeitErr{http.StatusInternalServerError, "Daten nicht verfügbar"}
	}
	if _, msg := attNext(cur, kind); msg != "" {
		return "", &zeitErr{http.StatusConflict, msg}
	}
	var stopped *zeitStopped
	if kind == attGehen || kind == attPauseBegin {
		if stopped, err = h.workStopTx(ctx, tx, p.ID, now); err != nil {
			componentLog("terminal").Error().Err(err).Msg("arbeit beenden")
			return "", &zeitErr{http.StatusInternalServerError, "Daten nicht verfügbar"}
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO attendance_events (user_id, kind, at, terminal_user_id, origin_via, created_by) VALUES ($1::uuid, $2, $3, $4::uuid, $5, $6::uuid)`,
		p.ID, kind, now, c.Act.TermUID, c.via(), c.Act.TermUID); err != nil {
		componentLog("terminal").Error().Err(err).Msg("anwesenheit buchen")
		return "", &zeitErr{http.StatusInternalServerError, "Daten nicht verfügbar"}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", &zeitErr{http.StatusInternalServerError, "Daten nicht verfügbar"}
	}
	msg := attKindLabel[kind] + " gebucht."
	if stopped != nil {
		h.zeitStoppedHistory(ctx, stopped, p.ID, c.Act.TermUID, c.TermName)
		msg += " Die laufende Arbeit „" + stopped.Title + "“ wurde beendet."
	}
	return msg, nil
}

// zeitWorkStart beginnt die Arbeit; eine laufende andere Arbeit der Person wird beendet.
func (h *Handler) zeitWorkStart(ctx context.Context, c *zeitCtx, p terminalPerson, refType, refID, infra, title string, now time.Time) (string, *zeitErr) {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return "", &zeitErr{http.StatusInternalServerError, "Daten nicht verfügbar"}
	}
	defer tx.Rollback(ctx)
	if err := zeitLock(ctx, tx, p.ID); err != nil {
		return "", &zeitErr{http.StatusInternalServerError, "Daten nicht verfügbar"}
	}
	cur, err := attStateNow(ctx, tx, p.ID, now)
	if err != nil {
		return "", &zeitErr{http.StatusInternalServerError, "Daten nicht verfügbar"}
	}
	if cur == attPause {
		return "", &zeitErr{http.StatusConflict, "In der Pause beginnt keine Arbeit – bitte zuerst Pause Ende buchen."}
	}
	var runRef string
	err = tx.QueryRow(ctx, `SELECT ref_type || ':' || ref_id::text FROM work_sessions WHERE user_id = $1::uuid AND ended_at IS NULL`, p.ID).Scan(&runRef)
	if err == nil && runRef == refType+":"+refID {
		return "", &zeitErr{http.StatusConflict, "Die Arbeit läuft schon."}
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", &zeitErr{http.StatusInternalServerError, "Daten nicht verfügbar"}
	}
	stopped, err := h.workStopTx(ctx, tx, p.ID, now)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("arbeit beenden")
		return "", &zeitErr{http.StatusInternalServerError, "Daten nicht verfügbar"}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO work_sessions (user_id, ref_type, ref_id, infrastructure_id, title, terminal_user_id, origin_via, started_at)
		VALUES ($1::uuid, $2, $3::uuid, NULLIF($4, '')::uuid, $5, $6::uuid, $7, $8)`, p.ID, refType, refID, infra, title, c.Act.TermUID, c.via(), now); err != nil {
		componentLog("terminal").Error().Err(err).Msg("arbeit beginnen")
		return "", &zeitErr{http.StatusConflict, "Es läuft schon eine Arbeit – bitte zuerst beenden."}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", &zeitErr{http.StatusInternalServerError, "Daten nicht verfügbar"}
	}
	msg := "Arbeit begonnen."
	if stopped != nil {
		h.zeitStoppedHistory(ctx, stopped, p.ID, c.Act.TermUID, c.TermName)
		msg = "Arbeit begonnen – die vorherige Arbeit „" + stopped.Title + "“ wurde beendet."
	}
	return msg, nil
}

func (h *Handler) zeitWorkStop(ctx context.Context, c *zeitCtx, p terminalPerson, now time.Time) (string, *zeitErr) {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return "", &zeitErr{http.StatusInternalServerError, "Daten nicht verfügbar"}
	}
	defer tx.Rollback(ctx)
	if err := zeitLock(ctx, tx, p.ID); err != nil {
		return "", &zeitErr{http.StatusInternalServerError, "Daten nicht verfügbar"}
	}
	stopped, err := h.workStopTx(ctx, tx, p.ID, now)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("arbeit beenden")
		return "", &zeitErr{http.StatusInternalServerError, "Daten nicht verfügbar"}
	}
	if stopped == nil {
		return "", &zeitErr{http.StatusConflict, "Es läuft keine Arbeit."}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", &zeitErr{http.StatusInternalServerError, "Daten nicht verfügbar"}
	}
	h.zeitStoppedHistory(ctx, stopped, p.ID, c.Act.TermUID, c.TermName)
	return "Arbeit beendet.", nil
}

// ---- Seite ----

type zeitRow struct{ Label, Time string }

type zeitWorkRow struct{ Name, What string }

type zeitItem struct{ Sel, Label, Title, Path string }

// terminalZeitPage: Daten der Seite /terminal/zeit.
type terminalZeitPage struct {
	Device, CanAct, Card bool
	NeedCard             bool // nur_karte ohne Karte
	Modus, Notice        string
	NoticeBad            bool
	Person, PersonID     string // Kartenperson oder gewählte Person
	Picked               bool   // gewählte Person (ohne Karte)
	State                attState
	StateLabel           string
	Can                  map[string]bool // erlaubte Buchungen der Person
	Running              string          // laufende Arbeit der Person
	Mine                 []zeitRow       // eigene Zeiten heute – nur mit Karte
	MineWork             string          // eigene Arbeitszeit heute – nur mit Karte
	Pick                 []terminalPerson
	Present, Pause       []string
	Working              []zeitWorkRow
	WorkDlg              bool
	WorkPeople           []terminalPerson
	WorkOn               map[string]bool
	Items                []zeitItem
	Plants               []zeitItem
}

func (p terminalZeitPage) CanKommen() bool { return p.Can[attKommen] }

var zeitNotices = map[string]string{
	"kommen": "Kommen gebucht.", "gehen": "Gehen gebucht.", "pause_beginn": "Pause Beginn gebucht.", "pause_ende": "Pause Ende gebucht.",
	"start": "Arbeit begonnen.", "wechsel": "Arbeit begonnen – die vorherige Arbeit wurde beendet.", "stop": "Arbeit beendet.",
	"gehen_arbeit": "Gehen gebucht – die laufende Arbeit wurde beendet.", "pause_arbeit": "Pause Beginn gebucht – die laufende Arbeit wurde beendet.",
}

func zeitStateLabel(s attState) string {
	return map[attState]string{attAbwesend: "nicht angemeldet", attAnwesend: "anwesend", attPause: "in Pause"}[s]
}

func zeitCan(s attState) map[string]bool {
	m := map[string]bool{}
	for _, k := range attKinds {
		_, msg := attNext(s, k)
		m[k] = msg == ""
	}
	return m
}

// zeitFill füllt Stand und Bedienung.
func (h *Handler) zeitFill(r *http.Request, c *zeitCtx, v *terminalZeitPage) error {
	ctx := r.Context()
	now := time.Now()
	v.Device, v.Modus, v.Card = c.Device, c.Act.Modus, c.Act.Card != nil
	v.CanAct = c.canAct()
	v.NeedCard = c.Device && c.Act.Card == nil && c.Act.Modus != vorgModusOhneKarte
	people, err := h.terminalPickPeople(ctx, c.Act.TermUID)
	if err != nil {
		return err
	}
	names := map[string]string{}
	ids := make([]string, 0, len(people))
	for _, p := range people {
		names[p.ID] = p.Name
		ids = append(ids, p.ID)
	}
	// Stand aller Personen der Auswahl: nur Zustand, keine Uhrzeiten
	rows, err := h.db.Query(ctx, `SELECT DISTINCT ON (user_id) user_id::text, kind, at FROM attendance_events WHERE user_id = ANY($1::uuid[])
		ORDER BY user_id, at DESC, created_at DESC, id DESC`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, k string
		var at time.Time
		if err := rows.Scan(&id, &k, &at); err != nil {
			rows.Close()
			return err
		}
		switch attStateOf(k, at, now) {
		case attAnwesend:
			v.Present = append(v.Present, names[id])
		case attPause:
			v.Pause = append(v.Pause, names[id])
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	sort.Strings(v.Present)
	sort.Strings(v.Pause)
	wr, err := h.db.Query(ctx, `SELECT user_id::text, ref_type, title, COALESCE(infrastructure_id::text, '') FROM work_sessions
		WHERE ended_at IS NULL AND user_id = ANY($1::uuid[]) AND started_at > $2 ORDER BY started_at`, ids, now.Add(-attStaleAfter))
	if err != nil {
		return err
	}
	for wr.Next() {
		var id, rt, title, infra string
		if err := wr.Scan(&id, &rt, &title, &infra); err != nil {
			wr.Close()
			return err
		}
		v.Working = append(v.Working, zeitWorkRow{Name: names[id], What: zeitWhat(c.Tree, rt, title, infra)})
	}
	wr.Close()
	if err := wr.Err(); err != nil {
		return err
	}
	sort.SliceStable(v.Working, func(i, j int) bool { return v.Working[i].Name < v.Working[j].Name })

	// Wer bedient gerade: Kartenperson, sonst die gewählte Person (ohne_karte)
	var who *terminalPerson
	if c.Act.Card != nil {
		who = &terminalPerson{ID: c.Act.Card.ID, Name: strings.TrimSpace(c.Act.Card.FirstName + " " + c.Act.Card.LastName)}
	} else if v.CanAct {
		v.Pick = people
		if sel := strings.TrimSpace(r.URL.Query().Get("wer")); sel != "" {
			for _, p := range people {
				if p.ID == sel {
					pp := p
					who, v.Picked = &pp, true
				}
			}
			if who == nil {
				v.Notice, v.NoticeBad = "Diese Person steht an diesem Terminal nicht zur Auswahl.", true
			}
		}
	}
	if who != nil {
		v.Person, v.PersonID = who.Name, who.ID
		if v.State, err = attStateNow(ctx, h.db, who.ID, now); err != nil {
			return err
		}
		v.StateLabel, v.Can = zeitStateLabel(v.State), zeitCan(v.State)
		var rt, title, infra string
		err := h.db.QueryRow(ctx, `SELECT ref_type, title, COALESCE(infrastructure_id::text, '') FROM work_sessions WHERE user_id = $1::uuid AND ended_at IS NULL`, who.ID).Scan(&rt, &title, &infra)
		if err == nil {
			v.Running = zeitWhat(c.Tree, rt, title, infra)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if c.Act.Card != nil { // eigene Zeiten nur mit Karte
			if err := h.zeitMine(ctx, who.ID, now, v); err != nil {
				return err
			}
		}
		if r.URL.Query().Get("arbeit") == "1" {
			v.WorkDlg = true
			v.WorkOn = map[string]bool{who.ID: true}
			if c.Act.Card != nil {
				v.WorkPeople = []terminalPerson{*who}
			} else {
				v.WorkPeople = people
			}
			if err := h.zeitItems(r, c, v); err != nil {
				return err
			}
		}
	}
	return nil
}

func zeitWhat(t terminalPlantTree, rt, title, infra string) string {
	if rt == "plant" {
		if infra != "" && t.ok(infra) {
			return "Anlage " + terminalPlantLabel(t, infra)
		}
		return "Anlage " + title
	}
	return zeitKindOf(rt).Label + ": " + title
}

// zeitMine: eigene Buchungen und Arbeitszeit des heutigen Tages.
func (h *Handler) zeitMine(ctx context.Context, uid string, now time.Time, v *terminalZeitPage) error {
	day := localDayStart(now)
	rows, err := h.db.Query(ctx, `SELECT kind, at FROM attendance_events WHERE user_id = $1::uuid AND at >= $2 AND at < $3 ORDER BY at, created_at, id`, uid, day, day.AddDate(0, 0, 1))
	if err != nil {
		return err
	}
	for rows.Next() {
		var k string
		var at time.Time
		if err := rows.Scan(&k, &at); err != nil {
			rows.Close()
			return err
		}
		v.Mine = append(v.Mine, zeitRow{Label: attKindLabel[k], Time: at.In(workLocation).Format("15:04")})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var min float64
	if err := h.db.QueryRow(ctx, `SELECT COALESCE(SUM(EXTRACT(EPOCH FROM (COALESCE(ended_at, $3) - started_at))), 0) / 60 FROM work_sessions
		WHERE user_id = $1::uuid AND started_at >= $2 AND started_at < $2 + INTERVAL '1 day'`, uid, day, now).Scan(&min); err != nil {
		return err
	}
	if min >= 1 {
		v.MineWork = workDuration(int(min))
	}
	return nil
}

// zeitItems: Vorgänge der Anlage, an denen die Person arbeiten darf, und die Anlagen des Terminals.
func (h *Handler) zeitItems(r *http.Request, c *zeitCtx, v *terminalZeitPage) error {
	ctx := r.Context()
	kinds := append(append([]vorgKind{}, vorgKinds...), vorgMaintKind)
	for _, k := range kinds {
		visible := false
		switch k.Type {
		case "fault":
			visible = c.P.CanFaults
		case "ticket":
			visible = c.P.CanTickets
		default:
			visible = h.terminalUserCan(r, c.Act.TermUID, k.ViewPerm, c.P.InfraID)
		}
		if !visible {
			continue
		}
		g, err := h.vorgLoad(ctx, c.Tree, k)
		if err != nil {
			return err
		}
		for _, it := range g.Items {
			if h.vorgCan(r, c.Act, k.EditPerm, it) {
				v.Items = append(v.Items, zeitItem{Sel: it.Sel(), Label: k.Label, Title: it.Title, Path: it.Path})
			}
		}
	}
	ids := make([]string, 0, len(c.Tree.Nodes))
	for id := range c.Tree.Nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := terminalPlantLabel(c.Tree, ids[i]), terminalPlantLabel(c.Tree, ids[j])
		if ids[i] == c.Tree.Root || ids[j] == c.Tree.Root {
			return ids[i] == c.Tree.Root
		}
		return setupPathLess(a, b)
	})
	for _, id := range ids {
		if len(v.Plants) >= zeitPlantsMax {
			break
		}
		v.Plants = append(v.Plants, zeitItem{Sel: "plant:" + id, Label: "Anlage", Title: terminalPlantLabel(c.Tree, id)})
	}
	return nil
}

// TerminalZeitPage: GET /terminal/zeit
func (h *Handler) TerminalZeitPage(w http.ResponseWriter, r *http.Request) {
	c, ok := h.zeitGate(w, r)
	if !ok {
		return
	}
	h.zeitRender(w, r, c, "", false, http.StatusOK)
}

func (h *Handler) zeitRender(w http.ResponseWriter, r *http.Request, c *zeitCtx, notice string, bad bool, status int) {
	p := c.P
	v := terminalZeitPage{}
	if err := h.zeitFill(r, c, &v); err != nil {
		componentLog("terminal").Error().Err(err).Msg("zeiterfassung lesen")
		http.Error(w, "Die Daten konnten nicht geladen werden.", http.StatusInternalServerError)
		return
	}
	if n, okN := zeitNotices[r.URL.Query().Get("n")]; okN && notice == "" {
		notice = n
	}
	if notice != "" {
		v.Notice, v.NoticeBad = notice, bad
	}
	if c.Act.Card != nil {
		p.SetupCard = true
	}
	p.Zeit = v
	if r.URL.Query().Get("fragment") == "status" {
		h.renderTerminalPart(w, p, "zeit-status")
		return
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	h.renderTerminal(w, p)
}

func zeitBack(w http.ResponseWriter, r *http.Request, code, who string) {
	dst := "/terminal/zeit?n=" + url.QueryEscape(code)
	_ = who
	http.Redirect(w, r, dst, http.StatusSeeOther)
}

// TerminalZeitBuchen: POST /terminal/zeit/buchen (kind, who)
func (h *Handler) TerminalZeitBuchen(w http.ResponseWriter, r *http.Request) {
	c, ok := h.zeitGate(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	kind := r.FormValue("kind")
	if !attKindOK(kind) {
		http.Error(w, "Unbekannte Buchung (erlaubt: "+attKindsPrompt+")", http.StatusBadRequest)
		return
	}
	if len(r.Form["who"]) > 1 {
		http.Error(w, "Anwesenheit bucht man für eine Person.", http.StatusBadRequest)
		return
	}
	people, e := h.zeitWho(r.Context(), c, r.Form["who"])
	if e != nil {
		h.zeitFail(w, r, c, e)
		return
	}
	msg, e := h.zeitBook(r.Context(), c, people[0], kind, time.Now())
	if e != nil {
		h.zeitFail(w, r, c, e)
		return
	}
	code := kind
	if strings.Contains(msg, "beendet") {
		code = map[string]string{attGehen: "gehen_arbeit", attPauseBegin: "pause_arbeit"}[kind]
	}
	zeitBack(w, r, code, "")
}

// zeitFail: Meldung als Seite (eine Weiterleitung würde den deutschen Text in die Adresse legen).
func (h *Handler) zeitFail(w http.ResponseWriter, r *http.Request, c *zeitCtx, e *zeitErr) {
	if e.Code >= 500 || e.Code == http.StatusBadRequest {
		http.Error(w, e.Msg, e.Code)
		return
	}
	h.zeitRender(w, r, c, e.Msg, true, e.Code)
}

// TerminalZeitArbeitStart: POST /terminal/zeit/arbeit/start (type, id, who[])
func (h *Handler) TerminalZeitArbeitStart(w http.ResponseWriter, r *http.Request) {
	c, ok := h.zeitGate(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	_ = r.ParseForm()
	people, e := h.zeitWho(ctx, c, r.Form["who"])
	if e != nil {
		h.zeitFail(w, r, c, e)
		return
	}
	typ, id := r.FormValue("type"), strings.TrimSpace(r.FormValue("id"))
	if sel := r.FormValue("sel"); sel != "" { // Auswahl im Fenster: "<Art>:<Kennung>"
		typ, id, _ = strings.Cut(sel, ":")
	}
	var infra, title string
	if typ == "plant" {
		if !c.Tree.ok(id) || !uuidInPathRe.MatchString(id) {
			http.Error(w, "Anlage nicht gefunden oder nicht in der Anlage dieses Terminals", http.StatusNotFound)
			return
		}
		infra, title = id, terminalPlantLabel(c.Tree, id)
	} else {
		k, known := vorgKindOf(typ)
		if !known {
			http.Error(w, "Unbekannter Vorgang", http.StatusBadRequest)
			return
		}
		it, found, err := h.vorgLoadOne(ctx, c.Tree, k.Type, id)
		if err != nil {
			componentLog("terminal").Error().Err(err).Msg("vorgang lesen")
			http.Error(w, "Daten nicht verfügbar", http.StatusInternalServerError)
			return
		}
		if !found {
			http.Error(w, "Vorgang nicht gefunden oder nicht in der Anlage dieses Terminals", http.StatusNotFound)
			return
		}
		if !h.vorgCan(r, c.Act, k.EditPerm, it) {
			http.Error(w, "Keine Berechtigung für diesen Vorgang.", http.StatusForbidden)
			return
		}
		infra, title = it.Infra, it.Title
	}
	now := time.Now()
	var errs []string
	changed := false
	for _, p := range people {
		msg, e := h.zeitWorkStart(ctx, c, p, typ, id, infra, title, now)
		if e != nil {
			errs = append(errs, p.Name+": "+e.Msg)
			continue
		}
		_ = msg
		changed = true
	}
	if len(errs) > 0 {
		h.zeitRender(w, r, c, strings.Join(errs, " "), true, http.StatusConflict)
		return
	}
	if changed {
		zeitBack(w, r, "start", "")
	}
}

// TerminalZeitArbeitStop: POST /terminal/zeit/arbeit/stop (who[])
func (h *Handler) TerminalZeitArbeitStop(w http.ResponseWriter, r *http.Request) {
	c, ok := h.zeitGate(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	people, e := h.zeitWho(r.Context(), c, r.Form["who"])
	if e != nil {
		h.zeitFail(w, r, c, e)
		return
	}
	now := time.Now()
	var errs []string
	for _, p := range people {
		if _, e := h.zeitWorkStop(r.Context(), c, p, now); e != nil {
			errs = append(errs, p.Name+": "+e.Msg)
		}
	}
	if len(errs) > 0 {
		h.zeitRender(w, r, c, strings.Join(errs, " "), true, http.StatusConflict)
		return
	}
	zeitBack(w, r, "stop", "")
}

