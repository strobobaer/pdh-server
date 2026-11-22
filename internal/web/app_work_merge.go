package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Zusammenführen von Schritten, die das Telefon ohne Verbindung gesammelt hat
// (Paket 3, Spec 9.3). Die App schickt dazu im Körper den Stand mit, auf dem der
// Schritt beruht: "base": {"status", "updated_at"}. Ohne base gilt alles wie in
// Paket 2. Mit base:
//
//   - start/finish: Status im PDH gilt. Weicht er von base.status ab, entfällt der
//     Statuswechsel ohne Rückfrage; ein Abschlusstext wird angehängt (Maßnahme, wenn
//     der Vorgang noch offen ist und Maßnahmen annimmt, sonst Verlaufseintrag
//     „Nachtrag aus der App: …“). completionFinish läuft dann nie.
//   - actions/comments auf einem inzwischen geschlossenen Vorgang: Text als
//     Verlaufseintrag „Nachtrag aus der App: …“ (statt 403).
//   - steps auf einem geschlossenen Vorgang: nichts speichern, nur Hinweis.
//   - Ein fehlendes Recht an einem offenen Vorgang bleibt 403; Betrachter und nicht
//     sichtbare Vorgänge bleiben 403/404.
//
// Jede Abweichung ergibt genau einen Hinweis (hint) in der Antwort, der zugleich im
// Verlauf steht (Ausnahme: steps – dort wird nichts geschrieben).

// Schreibende Wege an einem Vorgang (für appWorkWrite und das Zusammenführen).
const (
	appOpStart   = "start"
	appOpAction  = "action"
	appOpComment = "comment"
	appOpStep    = "step"
	appOpFinish  = "finish"
)

// appBase: der Stand des Vorgangs, auf dem ein Schritt aus der App beruht.
type appBase struct {
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at"`
}

// appParseBase liest "base" aus dem Körper; nil, wenn es fehlt, leer ist oder der
// Körper kein JSON ist (dann meldet der Weg selbst den Eingabefehler wie bisher).
func appParseBase(body []byte) *appBase {
	var in struct {
		Base *appBase `json:"base"`
	}
	if len(strings.TrimSpace(string(body))) == 0 || json.Unmarshal(body, &in) != nil || in.Base == nil {
		return nil
	}
	if in.Base.Status = strings.TrimSpace(in.Base.Status); in.Base.Status == "" {
		return nil
	}
	return in.Base
}

// appResolveID: gibt es keinen Datensatz id dieser Art, aber einen Alias der App
// (app_id_aliases, target_type = kind), gilt dessen Ziel; sonst bleibt id.
func (h *Handler) appResolveID(ctx context.Context, kind, id string) (string, error) {
	k, ok := completionKinds[kind]
	if !ok || h.db == nil || !bareUUIDRe.MatchString(id) {
		return id, nil
	}
	var target string
	err := h.db.QueryRow(ctx, `SELECT COALESCE((SELECT a.target_id::text FROM app_id_aliases a
		WHERE a.id = $1::uuid AND a.target_type = $2 AND NOT EXISTS (SELECT 1 FROM `+k.Table+` WHERE id = $1::uuid)), '')`, id, kind).Scan(&target)
	if err != nil {
		return id, err
	}
	if target != "" {
		return target, nil
	}
	return id, nil
}

// Hinweistexte (wörtlich, siehe global-constraints).
const (
	appHintAppended      = " Dein Eintrag wurde angehängt."
	appHintNachtrag      = "Nachtrag aus der App: "
	appHintClosedEntry   = "„%s“ ist inzwischen abgeschlossen. Dein Eintrag wurde im Verlauf angehängt."
	appHintClosedStep    = "„%s“ ist inzwischen abgeschlossen. Der Prüfpunkt wurde nicht mehr gespeichert."
	appHintStatusBy      = "„%s“ wurde inzwischen von %s auf „%s“ gesetzt."
	appHintStatusNoName  = "„%s“ wurde inzwischen auf „%s“ gesetzt."
	appHintHistoryAction = "update" // Vorgang im Verlauf für Hinweis und Nachtrag (wie S2)
)

// appResultStepSkipped: result_type in app_steps, wenn ein Prüfpunkt an einem
// geschlossenen Vorgang nicht gespeichert wurde (result_id = Kennung des Punkts).
const appResultStepSkipped = "step_skipped"

// appMergeHistoryWrite schreibt Verlaufseinträge (action appHintHistoryAction) in einer
// Transaktion mit geprüften Fehlern – anders als addHistory, das Fehler verschluckt.
// Variable, damit ein Test den Fehlerfall erzwingen kann.
var appMergeHistoryWrite = func(ctx context.Context, h *Handler, ref, id, uid string, msgs []string) error {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, m := range msgs {
		if _, err := tx.Exec(ctx, `INSERT INTO record_history (ref_type, ref_id, action, field_name, old_value, new_value, message, created_by)
			VALUES ($1, $2, $3, '', '', '', $4, $5)`, ref, id, appHintHistoryAction, m, nullID(uid)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// appStatusHint: Hinweis „… wurde inzwischen von <Name> auf „<Status>“ gesetzt.“ Der
// Name stammt aus dem jüngsten Verlaufseintrag, der den Status geändert hat
// (field_name = 'status'), und nur, wenn dieser auf den aktuellen Status lautet –
// sonst die Variante ohne Namen.
func (h *Handler) appStatusHint(ctx context.Context, kind, id, title, status string) (string, error) {
	var name, value string
	err := h.db.QueryRow(ctx, `SELECT COALESCE(TRIM(u.first_name || ' ' || u.last_name), ''), COALESCE(rh.new_value, '')
		FROM record_history rh LEFT JOIN users u ON u.id = rh.created_by
		WHERE rh.ref_type = $1 AND rh.ref_id = $2::uuid AND rh.field_name = 'status'
		ORDER BY rh.created_at DESC, rh.id DESC LIMIT 1`, historyRef(kind), id).Scan(&name, &value)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if value != status {
		name = ""
	}
	if name != "" {
		return fmt.Sprintf(appHintStatusBy, title, name, statusLabel(status)), nil
	}
	return fmt.Sprintf(appHintStatusNoName, title, statusLabel(status)), nil
}

// appMergeText liest und prüft den Text des Schritts (Meldungen wie die Wege selbst).
func appMergeText(op string, body []byte) (string, error) {
	var in struct {
		Text string `json:"text"`
	}
	if err := appDecode(body, &in); err != nil {
		return "", err
	}
	switch op {
	case appOpAction:
		return appText(in.Text, "Bitte eine Maßnahme eingeben.", "Die Maßnahme darf höchstens %d Zeichen haben.")
	case appOpComment:
		return appText(in.Text, "Bitte einen Kommentar eingeben.", "Der Kommentar darf höchstens %d Zeichen haben.")
	}
	return appText(in.Text, "", "Der Text darf höchstens %d Zeichen haben.")
}

// appWorkMerge führt einen Schritt mit base zusammen. false: keine Abweichung,
// der Weg läuft wie in Paket 2 weiter. true: beantwortet (Hinweis, 403, 422 oder 503).
func (h *Handler) appWorkMerge(w http.ResponseWriter, r *http.Request, op, kind, id string, p appWorkPerms, base *appBase, body []byte) bool {
	ctx := r.Context()
	k := completionKinds[kind]
	var title, status string
	if err := h.db.QueryRow(ctx, `SELECT title::text, status::text FROM `+k.Table+` WHERE id = $1::uuid`, id).Scan(&title, &status); err != nil {
		appUnavailable(w, err, kind, id, "app: vorgang lesen")
		return true
	}
	switch op {
	case appOpStart, appOpFinish:
		if status == base.Status {
			return false
		}
	default:
		if !p.Closed {
			return false
		}
	}
	// Abweichung. Offen: das Recht des Wegs gilt (Regel 6). Geschlossen: wer den
	// Vorgang sieht (p.See, schon geprüft) und kein Betrachter ist, darf anhängen.
	var ok bool
	switch {
	case p.Closed:
		u := getUser(r)
		ok = u != nil && u.Role != "viewer"
	case op == appOpStart:
		ok = p.Start
	case op == appOpFinish:
		ok = p.Finish
	}
	if !ok {
		appJSONErr(w, http.StatusForbidden, "app_no_permission", "app_no_permission")
		return true
	}
	if op == appOpStep { // nichts schreiben, nur Hinweis; das Auslassen merkt sich app_steps
		appSetResult(r, appResultStepSkipped, chi.URLParam(r, "stepID"))
		h.appWorkRespondHint(w, r, kind, id, p, fmt.Sprintf(appHintClosedStep, title))
		return true
	}
	uid := getUser(r).ID
	text, err := appMergeText(op, body)
	if err != nil {
		h.appWorkRunErr(w, err, kind, id)
		return true
	}
	var hint string
	if op == appOpAction || op == appOpComment {
		hint = fmt.Sprintf(appHintClosedEntry, title)
	} else if hint, err = h.appStatusHint(ctx, kind, id, title, status); err != nil {
		appUnavailable(w, err, kind, id, "app: hinweis bilden")
		return true
	}
	asAction := text != "" && !p.Closed && p.Action // noch offen und nimmt Maßnahmen an: als Maßnahme
	if text != "" && op == appOpFinish {
		hint += appHintAppended
	}
	if asAction {
		if !h.completionService(k.Type) {
			appUnavailable(w, errNoService, kind, id, "app: nachtrag")
			return true
		}
		if err := h.completionAddAction(ctx, k, id, text, uid); err != nil {
			h.appWorkRunErr(w, err, kind, id)
			return true
		}
		appKeepOnce(r) // Text steht als Maßnahme: ab jetzt ein Replay
		// Der Hinweis ist hier nur Zusatz – scheitert er, bleibt die Maßnahme (kein 503,
		// sonst würde der Schritt sie beim nächsten Versuch doppelt eintragen).
		if err := appMergeHistoryWrite(ctx, h, historyRef(kind), id, uid, []string{hint}); err != nil {
			componentLog("app").Error().Err(err).Str("art", kind).Str("vorgang", id).Msg("app: hinweis in den verlauf")
		}
	} else {
		// Nachtrag (der Text lebt nur im Verlauf) und Hinweis gemeinsam und geprüft: scheitert
		// es, 503 ohne appKeepOnce – der Schritt bleibt in der Warteschlange des Telefons.
		msgs := []string{hint}
		if text != "" {
			msgs = []string{appHintNachtrag + text, hint}
		}
		if err := appMergeHistoryWrite(ctx, h, historyRef(kind), id, uid, msgs); err != nil {
			appUnavailable(w, err, kind, id, "app: nachtrag in den verlauf")
			return true
		}
		appKeepOnce(r) // geschrieben: ein erneuter Versuch ist ab jetzt ein Replay
	}
	if p, err = h.appWorkCan(r, kind, id); err != nil {
		appUnavailable(w, err, kind, id, "vorgang prüfen")
		return true
	}
	if !appWorkDenied(w, p) {
		h.appWorkRespondHint(w, r, kind, id, p, hint)
	}
	return true
}

// appReplayHint: bei einer Wiederholung eines Schritts mit base denselben Hinweis
// wie beim ersten Mal. Der Hinweis steht im Verlauf (Eintrag des Benutzers mit „…“
// am Anfang). Zum Schritt gehört der erste solche Eintrag nach dem Eintrag der Once
// in app_steps, sofern dazwischen kein anderer Schritt desselben Benutzers begann;
// sonst kein Hinweis. steps schreiben keinen Verlauf: dort gilt das gespeicherte
// Ergebnis des ersten Laufs (appResultStepSkipped) – nur dann „nicht mehr
// gespeichert“; wurde der Punkt damals gespeichert, gibt es keinen Hinweis, auch wenn
// der Vorgang inzwischen geschlossen ist.
func (h *Handler) appReplayHint(r *http.Request, op, kind, id string) string {
	ctx := r.Context()
	if op == appOpStep {
		if typ, _ := appStepResult(r); typ != appResultStepSkipped {
			return ""
		}
		var title string
		if err := h.db.QueryRow(ctx, `SELECT title::text FROM `+completionKinds[kind].Table+` WHERE id = $1::uuid`, id).Scan(&title); err != nil {
			return ""
		}
		return fmt.Sprintf(appHintClosedStep, title)
	}
	var hint string
	err := h.db.QueryRow(ctx, `SELECT rh.message FROM record_history rh JOIN app_steps s ON s.id = $3::uuid
		WHERE rh.ref_type = $1 AND rh.ref_id = $2::uuid AND rh.created_by = s.user_id AND rh.action = $4
		  AND rh.message LIKE '„%' AND rh.created_at >= s.created_at
		  AND NOT EXISTS (SELECT 1 FROM app_steps s2 WHERE s2.user_id = s.user_id AND s2.id <> s.id
		                  AND s2.created_at > s.created_at AND s2.created_at <= rh.created_at)
		ORDER BY rh.created_at, rh.id LIMIT 1`, historyRef(kind), id, appOnce(r), appHintHistoryAction).Scan(&hint)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			componentLog("app").Error().Err(err).Str("art", kind).Str("vorgang", id).Msg("app: hinweis der wiederholung")
		}
		return ""
	}
	return hint
}
