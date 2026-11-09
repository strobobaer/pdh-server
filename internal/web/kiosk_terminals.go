package web

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Kiosk-Terminals: Verwaltung (Core-Einstellungen → Kiosk-Terminals),
// Zuordnung je Anlage (Stammdaten) und Fernbefehl „Am Terminal zeigen“
// aus dem HMI-Reiter. Client und Prüfungen: kiosk_client.go.
//
// Rechte: Terminals gehören zu keiner Abteilung, deshalb verlangt ihre
// Verwaltung hmi.manage OHNE Abteilungsbegrenzung (rbac.Scope „all“).
// Alles an einer Anlage (Befehl, Zuordnung, Knöpfe) prüft das Recht für
// genau diese Anlage (hasPermRec, Abteilung zählt).

func (h *Handler) kioskRoutes(r chi.Router) {
	r.Get("/core/settings/kiosk-terminals", h.KioskTerminalsPage)
	r.Post("/core/settings/kiosk-terminals", h.KioskTerminalSaveWeb)
	r.Post("/core/settings/kiosk-terminals/{tid}/delete", h.KioskTerminalDeleteWeb)
	r.Post("/core/settings/kiosk-terminals/{tid}/check", h.KioskTerminalCheckWeb)
	r.Post("/infrastructure/{id}/hmis/{hmiID}/kiosk", h.HMIKioskOpen)
}

// kioskPerm: Recht für eine Anlage (Abteilung zählt) bzw. mit infraID ""
// das Recht ohne jede Abteilungsbegrenzung.
func (h *Handler) kioskPerm(r *http.Request, perm, infraID string) bool {
	if h.kioskPermHook != nil {
		return h.kioskPermHook(r, perm, infraID)
	}
	if h.rbac == nil {
		return false
	}
	if infraID == "" {
		u := getUser(r)
		all, _ := h.rbac.Scope(u.ID, string(u.Role), perm)
		return all
	}
	return h.hasPermRec(r, perm, "infrastructure", infraID)
}

// canManageKiosk: Terminals einrichten – hmi.manage ohne Abteilungsbegrenzung.
func (h *Handler) canManageKiosk(r *http.Request) bool { return h.kioskPerm(r, permHMIManage, "") }

// kioskKey: eigener Schlüssel für die Terminal-Schlüssel (wie hmiKey).
func (h *Handler) kioskKey() []byte {
	secret := h.mailCfg.CredentialsKey
	if secret == "" {
		secret = h.jwtSecret
	}
	sum := sha256.Sum256([]byte("pdh-kiosk-terminal:" + secret))
	return sum[:]
}

// kioskSameOrigin: Schutz vor fremden Seiten zusätzlich zum SameSite-Cookie
// (wie maint_lock.go): nur same-origin, none oder ohne Kopfzeile.
func kioskSameOrigin(r *http.Request) bool {
	sfs := r.Header.Get("Sec-Fetch-Site")
	return sfs == "" || sfs == "same-origin" || sfs == "none"
}

// kioskMessages: Meldungen der Verwaltungsseite. In der Adresse
// (?notice=/?err=) stehen nur diese Codes, nie Freitext.
var kioskMessages = map[string]string{
	"saved":          "Terminal gespeichert – jetzt „Verbindung prüfen“.",
	"deleted":        "Terminal gelöscht.",
	"checked":        "Verbindung geprüft – das Ergebnis steht beim Terminal.",
	"not_found":      "Terminal nicht gefunden.",
	"name_invalid":   "Name: 1 bis 120 Zeichen, keine Steuerzeichen.",
	"name_taken":     "Ein Terminal mit diesem Namen gibt es schon.",
	"url_invalid":    "Adresse: https://<Terminal>:<Port>, z. B. https://10.1.2.3:8443.",
	"key_missing":    "Bitte den Schlüssel eintragen (Terminal: Admin-Seite → Netzwerk → Fernsteuerung (PDH)).",
	"key_invalid":    "Schlüssel ungültig: beginnt mit hmik_ und hat danach 43 Zeichen – bitte vollständig kopieren.",
	"key_required":   "Adresse oder Fingerabdruck wurden geändert: Der bisherige Schlüssel gilt dafür nicht. Bitte am Terminal einen neuen Schlüssel erzeugen und hier eintragen.",
	"key_unreadable": "Schlüssel des Terminals nicht lesbar oder nicht gesetzt – bitte neu eintragen.",
	"fp_invalid":     "Fingerabdruck: 64 Hex-Zeichen, mit oder ohne Doppelpunkte (wie auf der Admin-Seite des Terminals).",
	"save_failed":    "Terminal konnte nicht gespeichert werden.",
	"delete_failed":  "Terminal konnte nicht gelöscht werden.",
	"load_failed":    "Kiosk-Terminals konnten nicht geladen werden.",
	"busy":           "An dieses Terminal läuft gerade ein Befehl – bitte kurz warten.",
}

// kioskNotices: Meldungen aus der Adresse – nur bekannte Codes.
func kioskNotices(q url.Values) (notice, errMsg string) {
	return kioskMessages[q.Get("notice")], kioskMessages[q.Get("err")]
}

// kioskCheckResult: letztes Ergebnis von „Verbindung prüfen“ je Terminal
// (nur im Speicher; nach einem Neustart leer).
type kioskCheckResult struct {
	OK      bool
	Message string
	At      time.Time
}

var kioskChecks sync.Map // Terminal-ID → kioskCheckResult

func kioskLastCheck(tid string) (kioskCheckResult, bool) {
	v, ok := kioskChecks.Load(tid)
	if !ok {
		return kioskCheckResult{}, false
	}
	return v.(kioskCheckResult), true
}

// kioskInflight: höchstens ein Befehl je Terminal gleichzeitig (Schutz vor
// Fluten des Terminals und des Zugriffsprotokolls).
var kioskInflight sync.Map

func kioskAcquire(tid string) (release func(), ok bool) {
	if _, busy := kioskInflight.LoadOrStore(tid, struct{}{}); busy {
		return nil, false
	}
	return func() { kioskInflight.Delete(tid) }, true
}

type kioskTerminalView struct {
	ID, Name, BaseURL, Fingerprint, FingerprintColons string
	HasKey, Active                                    bool
	Plants                                            int
	UpdatedAt                                         string
	Check                                             *kioskCheckResult
	CheckAt                                           string
}

func kioskColons(hexs string) string {
	var b strings.Builder
	for i := 0; i+1 < len(hexs); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(hexs[i : i+2])
	}
	return b.String()
}

func (h *Handler) loadKioskTerminals(ctx context.Context, onlyActive bool) ([]kioskTerminalView, error) {
	q := `SELECT k.id::text, k.name, k.base_url, k.pubkey_sha256, k.api_key_enc <> '', k.active,
		(SELECT COUNT(*) FROM infrastructure i WHERE i.kiosk_terminal_id = k.id),
		to_char(k.updated_at, 'DD.MM.YYYY HH24:MI')
		FROM kiosk_terminals k`
	if onlyActive {
		q += ` WHERE k.active`
	}
	rows, err := h.db.Query(ctx, q+` ORDER BY lower(k.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []kioskTerminalView
	for rows.Next() {
		var v kioskTerminalView
		if err := rows.Scan(&v.ID, &v.Name, &v.BaseURL, &v.Fingerprint, &v.HasKey, &v.Active, &v.Plants, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.FingerprintColons = kioskColons(v.Fingerprint)
		if c, ok := kioskLastCheck(v.ID); ok {
			v.Check, v.CheckAt = &c, c.At.Local().Format(kioskTimeDisplay)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

type KioskTerminalsPageData struct {
	BaseData
	Terminals   []kioskTerminalView
	Notice, Err string
}

// KioskTerminalsPage: GET /core/settings/kiosk-terminals
func (h *Handler) KioskTerminalsPage(w http.ResponseWriter, r *http.Request) {
	if !h.canManageKiosk(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	list, err := h.loadKioskTerminals(r.Context(), false)
	d := KioskTerminalsPageData{
		BaseData:  h.baseData(r, "kiosk-terminals", "Kiosk-Terminals", "Systemverwaltung"),
		Terminals: list,
	}
	d.Notice, d.Err = kioskNotices(r.URL.Query())
	if err != nil {
		d.Err = kioskMessages["load_failed"]
	}
	h.render(w, "kiosk_terminals", d)
}

// kioskBack: zurück zur Verwaltung mit Meldungscodes (siehe kioskMessages).
func kioskBack(w http.ResponseWriter, r *http.Request, noticeCode, errCode, anchor string) {
	q := url.Values{}
	if noticeCode != "" {
		q.Set("notice", noticeCode)
	}
	if errCode != "" {
		q.Set("err", errCode)
	}
	p := "/core/settings/kiosk-terminals"
	if len(q) > 0 {
		p += "?" + q.Encode()
	}
	if anchor != "" {
		p += "#" + url.PathEscape(anchor)
	}
	http.Redirect(w, r, p, http.StatusSeeOther)
}

func kioskErrCode(err error) string {
	var fe kioskFormError
	if errors.As(err, &fe) {
		return fe.Code
	}
	return "save_failed"
}

// kioskUpdateSQL: Ändern. Der Schlüssel bleibt nur, wenn Adresse und
// Fingerabdruck gleich bleiben (zweite Sicherung neben kioskKeyRequired, auch
// bei gleichzeitigen Änderungen); sonst wird er verworfen.
const kioskUpdateSQL = `UPDATE kiosk_terminals SET name = $2, base_url = $3, pubkey_sha256 = $4, active = $5,
		api_key_enc = CASE WHEN $6 <> '' THEN $6 WHEN base_url = $3 AND pubkey_sha256 = $4 THEN api_key_enc ELSE '' END,
		updated_at = NOW()
	WHERE id = $1::uuid`

// KioskTerminalSaveWeb: POST /core/settings/kiosk-terminals – anlegen oder (terminal_id) ändern.
func (h *Handler) KioskTerminalSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageKiosk(r) || !kioskSameOrigin(r) {
		http.Error(w, "keine Berechtigung (HMI einrichten, ohne Abteilungsbegrenzung)", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	in, err := parseKioskTerminalForm(r.PostForm)
	if err != nil {
		kioskBack(w, r, "", kioskErrCode(err), "")
		return
	}
	ctx := r.Context()
	var oldURL, oldPin string
	if in.ID != "" {
		if err := h.db.QueryRow(ctx, `SELECT base_url, pubkey_sha256 FROM kiosk_terminals WHERE id = $1::uuid`, in.ID).Scan(&oldURL, &oldPin); err != nil {
			kioskBack(w, r, "", "not_found", "")
			return
		}
		if err := kioskKeyRequired(oldURL, oldPin, in); err != nil {
			kioskBack(w, r, "", kioskErrCode(err), "k-"+in.ID)
			return
		}
	}
	keyEnc := ""
	if in.APIKey != "" {
		if keyEnc, err = encryptSecret(h.kioskKey(), in.APIKey); err != nil {
			kioskBack(w, r, "", "save_failed", "")
			return
		}
	}
	if in.ID == "" {
		err = h.db.QueryRow(ctx, `INSERT INTO kiosk_terminals (name, base_url, api_key_enc, pubkey_sha256, active)
			VALUES ($1, $2, $3, $4, $5) RETURNING id::text`, in.Name, in.BaseURL, keyEnc, in.Fingerprint, in.Active).Scan(&in.ID)
	} else {
		var tag pgconn.CommandTag
		tag, err = h.db.Exec(ctx, kioskUpdateSQL, in.ID, in.Name, in.BaseURL, in.Fingerprint, in.Active, keyEnc)
		if err == nil && tag.RowsAffected() == 0 {
			kioskBack(w, r, "", "not_found", "")
			return
		}
	}
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			kioskBack(w, r, "", "name_taken", "")
			return
		}
		kioskBack(w, r, "", "save_failed", "")
		return
	}
	kioskChecks.Delete(in.ID) // altes Prüfergebnis gilt nicht mehr
	componentLog("kiosk").Info().Str("terminal", in.ID).Str("user", getUser(r).ID).
		Str("adresse_alt", oldURL).Str("adresse", in.BaseURL).
		Str("fingerabdruck_alt", oldPin).Str("fingerabdruck", in.Fingerprint).
		Bool("schluessel_neu", in.APIKey != "").Msg("kiosk-terminal gespeichert")
	kioskBack(w, r, "saved", "", "k-"+in.ID)
}

// KioskTerminalDeleteWeb: Anlagen verlieren die Zuordnung (ON DELETE SET NULL).
func (h *Handler) KioskTerminalDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageKiosk(r) || !kioskSameOrigin(r) {
		http.Error(w, "keine Berechtigung (HMI einrichten, ohne Abteilungsbegrenzung)", http.StatusForbidden)
		return
	}
	tid := chi.URLParam(r, "tid")
	if _, err := h.db.Exec(r.Context(), `DELETE FROM kiosk_terminals WHERE id::text = $1`, tid); err != nil {
		kioskBack(w, r, "", "delete_failed", "")
		return
	}
	kioskChecks.Delete(tid)
	componentLog("kiosk").Info().Str("terminal", tid).Str("user", getUser(r).ID).Msg("kiosk-terminal gelöscht")
	kioskBack(w, r, "deleted", "", "")
}

type kioskTarget struct {
	ID, Name, BaseURL, KeyEnc, Fingerprint string
	Active                                 bool
}

func (h *Handler) loadKioskTerminal(ctx context.Context, id string) (*kioskTarget, error) {
	t := &kioskTarget{}
	err := h.db.QueryRow(ctx, `SELECT id::text, name, base_url, api_key_enc, pubkey_sha256, active FROM kiosk_terminals WHERE id::text = $1`, id).
		Scan(&t.ID, &t.Name, &t.BaseURL, &t.KeyEnc, &t.Fingerprint, &t.Active)
	return t, err
}

// KioskTerminalCheckWeb: „Verbindung prüfen“ – GET /api/remote/v1/status.
// Die Meldung bleibt ausführlich (abgelehnt, Zeitüberschreitung,
// Fingerabdruck): sie sehen nur Personen mit hmi.manage ohne
// Abteilungsbegrenzung, die die Adresse ohnehin selbst eintragen.
func (h *Handler) KioskTerminalCheckWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageKiosk(r) || !kioskSameOrigin(r) {
		http.Error(w, "keine Berechtigung (HMI einrichten, ohne Abteilungsbegrenzung)", http.StatusForbidden)
		return
	}
	t, err := h.loadKioskTerminal(r.Context(), chi.URLParam(r, "tid"))
	if err != nil {
		kioskBack(w, r, "", "not_found", "")
		return
	}
	key, err := decryptSecret(h.kioskKey(), t.KeyEnc)
	if err != nil || key == "" {
		kioskBack(w, r, "", "key_unreadable", "k-"+t.ID)
		return
	}
	release, ok := kioskAcquire(t.ID)
	if !ok {
		kioskBack(w, r, "", "busy", "k-"+t.ID)
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), kioskTimeout)
	defer cancel()
	msg, ok := kioskStatusMessage(newKioskClient(t.BaseURL, key, t.Fingerprint).status(ctx))
	kioskChecks.Store(t.ID, kioskCheckResult{OK: ok, Message: msg, At: time.Now()})
	componentLog("kiosk").Info().Str("terminal", t.ID).Bool("ok", ok).Msg("kiosk-terminal geprüft")
	kioskBack(w, r, "checked", "", "k-"+t.ID)
}

// ── Zuordnung an der Anlage ──────────────────────────────────

type kioskTerminalOption struct {
	ID, Label string
}

// kioskTerminalOptions: aktive Terminals; ein bereits zugeordnetes inaktives
// bleibt sichtbar (sonst ginge die Zuordnung beim Speichern still verloren).
func (h *Handler) kioskTerminalOptions(ctx context.Context, current string) []kioskTerminalOption {
	rows, err := h.db.Query(ctx, `SELECT id::text, name, active FROM kiosk_terminals
		WHERE active OR id::text = $1 ORDER BY lower(name)`, current)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []kioskTerminalOption
	for rows.Next() {
		var o kioskTerminalOption
		var active bool
		if rows.Scan(&o.ID, &o.Label, &active) == nil {
			if !active {
				o.Label += " (inaktiv)"
			}
			out = append(out, o)
		}
	}
	return out
}

// saveInfraKioskTerminal: Feld „Kiosk-Terminal“ der Stammdaten (hmi.manage für diese Anlage).
func (h *Handler) saveInfraKioskTerminal(ctx context.Context, r *http.Request, infraID string) error {
	if _, ok := r.Form["kiosk_terminal_id"]; !ok || !h.kioskPerm(r, permHMIManage, infraID) {
		return nil
	}
	tid := strings.TrimSpace(r.FormValue("kiosk_terminal_id"))
	if tid == "" {
		_, err := h.db.Exec(ctx, `UPDATE infrastructure SET kiosk_terminal_id = NULL WHERE id = $1::uuid`, infraID)
		return err
	}
	if !kioskUUIDRe.MatchString(tid) {
		return errors.New("Unbekanntes Kiosk-Terminal")
	}
	tag, err := h.db.Exec(ctx, `UPDATE infrastructure SET kiosk_terminal_id = $2::uuid WHERE id = $1::uuid
		AND (kiosk_terminal_id = $2::uuid OR EXISTS (SELECT 1 FROM kiosk_terminals WHERE id = $2::uuid AND active))`, infraID, tid)
	if err == nil && tag.RowsAffected() == 0 {
		return errors.New("Kiosk-Terminal nicht gefunden oder inaktiv")
	}
	return err
}

// ── Fernbefehl „Am Terminal zeigen / bedienen“ ───────────────

// HMIKioskOpen: POST /infrastructure/{id}/hmis/{hmiID}/kiosk {"mode":"view"|"control"}.
// Der Server entschlüsselt das VNC-Passwort und den Terminal-Schlüssel und
// schickt den Befehl; beides verlässt den Server nur zum Terminal (TLS, gepinnt)
// und wird nie protokolliert.
func (h *Handler) HMIKioskOpen(w http.ResponseWriter, r *http.Request) {
	fail := func(code int, msg string) { writeJSON(w, code, map[string]string{"error": msg}) }
	if !kioskSameOrigin(r) {
		fail(http.StatusForbidden, "Anfrage von fremder Seite abgewiesen")
		return
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		fail(http.StatusUnsupportedMediaType, "JSON erwartet")
		return
	}
	var in struct {
		Mode string `json:"mode"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || (in.Mode != "view" && in.Mode != "control") {
		fail(http.StatusBadRequest, "ungültige Eingabe")
		return
	}
	ctx, infraID, hmiID := r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "hmiID")
	if !kioskUUIDRe.MatchString(infraID) || !kioskUUIDRe.MatchString(hmiID) {
		fail(http.StatusNotFound, "HMI nicht gefunden")
		return
	}
	// Rechte für genau diese Anlage (Abteilung zählt)
	if !h.kioskPerm(r, permHMIKiosk, infraID) {
		fail(http.StatusForbidden, "keine Berechtigung (HMI am Kiosk-Terminal zeigen)")
		return
	}
	if in.Mode == "control" && !h.kioskPerm(r, permHMIControl, infraID) {
		fail(http.StatusForbidden, "keine Berechtigung (HMI bedienen)")
		return
	}
	// Anlage: aktiv, zugeordnetes Terminal, Pfad für die Leiste
	var termID, plantPath string
	err := h.db.QueryRow(ctx, `SELECT COALESCE(kiosk_terminal_id::text, ''), COALESCE(`+assetPathExpr("$1::uuid")+`, name)
		FROM infrastructure WHERE id = $1::uuid AND active`, infraID).Scan(&termID, &plantPath)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(http.StatusNotFound, "Anlage nicht gefunden oder archiviert")
		return
	}
	if err != nil {
		componentLog("kiosk").Warn().Err(err).Str("infra", infraID).Msg("anlage laden")
		fail(http.StatusInternalServerError, "Anlage konnte nicht geladen werden")
		return
	}
	// HMI nur unter seiner eigenen Anlage (loadHMI filtert nach infrastructure_id)
	t, err := h.loadHMI(ctx, infraID, hmiID)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(http.StatusNotFound, "HMI nicht gefunden")
		return
	}
	if err != nil {
		fail(http.StatusInternalServerError, "HMI konnte nicht geladen werden")
		return
	}
	if in.Mode == "control" && !t.AllowControl {
		fail(http.StatusForbidden, "An diesem HMI ist Bedienen nicht erlaubt")
		return
	}
	if termID == "" {
		fail(http.StatusConflict, "Dieser Anlage ist kein Kiosk-Terminal zugeordnet")
		return
	}
	term, err := h.loadKioskTerminal(ctx, termID)
	if err != nil || !term.Active {
		fail(http.StatusConflict, "Das zugeordnete Kiosk-Terminal ist inaktiv")
		return
	}
	release, ok := kioskAcquire(term.ID)
	if !ok {
		fail(http.StatusTooManyRequests, "An das Terminal „"+term.Name+"“ läuft gerade ein Befehl – bitte kurz warten und noch einmal versuchen.")
		return
	}
	defer release()
	user := getUser(r)
	logSession := func(errText string) {
		_, _ = h.db.Exec(context.Background(), `INSERT INTO infrastructure_hmi_sessions
			(hmi_id, user_id, mode, kiosk_terminal_id, kiosk_terminal_name, ended_at, error)
			VALUES ($1::uuid, NULLIF($2,'')::uuid, $3, $4::uuid, $5, NOW(), $6)`, t.ID, user.ID, in.Mode, term.ID, term.Name, errText)
	}
	log := componentLog("kiosk").With().Str("hmi", t.ID).Str("infra", infraID).Str("terminal", term.ID).Str("user", user.ID).Str("mode", in.Mode).Logger()
	password, err := decryptSecret(h.hmiKey(), t.PasswordEnc)
	if err != nil {
		logSession("VNC-Passwort nicht lesbar")
		fail(http.StatusInternalServerError, "VNC-Passwort nicht lesbar – bitte am HMI neu eingeben")
		return
	}
	key, err := decryptSecret(h.kioskKey(), term.KeyEnc)
	if err != nil || key == "" {
		logSession("Terminal-Schlüssel nicht lesbar oder nicht gesetzt")
		fail(http.StatusInternalServerError, "Schlüssel des Terminals nicht lesbar oder nicht gesetzt – bitte unter Kiosk-Terminals neu eintragen")
		return
	}
	if plantPath == "" {
		plantPath = t.InfraName
	}
	req := kioskOpenRequest{
		Host: t.Host, Port: t.Port, Password: password, Mode: in.Mode,
		Label: kioskLabel(plantPath, t.Name), User: kioskUserName(user.FirstName, user.LastName, user.Username),
	}
	cctx, cancel := context.WithTimeout(ctx, kioskTimeout+2*time.Second)
	defer cancel()
	out := newKioskClient(term.BaseURL, key, term.Fingerprint).open(cctx, req, term.Name)
	if out.OK {
		logSession("")
		log.Info().Msg("hmi am kiosk-terminal geöffnet")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": out.Message})
		return
	}
	logSession(out.Message)
	log.Warn().Str("ergebnis", out.Kind).Int("http", out.HTTPCode).Msg("kiosk-terminal: befehl nicht ausgeführt")
	code := http.StatusBadGateway
	if out.Kind == "busy" {
		code = http.StatusConflict
	}
	writeJSON(w, code, map[string]any{"error": out.Message, "kind": out.Kind, "hmi": out.HMI, "since": out.Since})
}
