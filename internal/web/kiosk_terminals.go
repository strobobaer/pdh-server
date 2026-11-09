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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Kiosk-Terminals: Verwaltung (Core-Einstellungen → Kiosk-Terminals),
// Zuordnung je Anlage (Stammdaten) und Fernbefehl „Am Terminal zeigen“
// aus dem HMI-Reiter. Client und Prüfungen: kiosk_client.go.

func (h *Handler) kioskRoutes(r chi.Router) {
	r.Get("/core/settings/kiosk-terminals", h.KioskTerminalsPage)
	r.Post("/core/settings/kiosk-terminals", h.KioskTerminalSaveWeb)
	r.Post("/core/settings/kiosk-terminals/{tid}/delete", h.KioskTerminalDeleteWeb)
	r.Post("/core/settings/kiosk-terminals/{tid}/check", h.KioskTerminalCheckWeb)
	r.Post("/infrastructure/{id}/hmis/{hmiID}/kiosk", h.HMIKioskOpen)
}

// canManageKiosk: Terminals einrichten wie HMIs einrichten (hmi.manage).
func (h *Handler) canManageKiosk(r *http.Request) bool { return h.hasPerm(r, permHMIManage) }

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

type kioskTerminalView struct {
	ID, Name, BaseURL, Fingerprint, FingerprintColons string
	HasKey, Active                                    bool
	Plants                                            int
	UpdatedAt                                         string
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
		Notice:    r.URL.Query().Get("notice"), Err: r.URL.Query().Get("err"),
	}
	if err != nil {
		d.Err = "Kiosk-Terminals konnten nicht geladen werden"
	}
	h.render(w, "kiosk_terminals", d)
}

func kioskBack(w http.ResponseWriter, r *http.Request, notice, errMsg, anchor string) {
	p := "/core/settings/kiosk-terminals?"
	if notice != "" {
		p += "notice=" + url.QueryEscape(notice) + "&"
	}
	if errMsg != "" {
		p += "err=" + url.QueryEscape(errMsg) + "&"
	}
	p = strings.TrimRight(p, "&?")
	if anchor != "" {
		p += "#" + url.PathEscape(anchor)
	}
	http.Redirect(w, r, p, http.StatusSeeOther)
}

// KioskTerminalSaveWeb: POST /core/settings/kiosk-terminals – anlegen oder (terminal_id) ändern.
func (h *Handler) KioskTerminalSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageKiosk(r) || !kioskSameOrigin(r) {
		http.Error(w, "keine Berechtigung (HMI einrichten)", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	in, err := parseKioskTerminalForm(r.PostForm)
	if err != nil {
		kioskBack(w, r, "", err.Error(), "")
		return
	}
	keyEnc := ""
	if in.APIKey != "" {
		if keyEnc, err = encryptSecret(h.kioskKey(), in.APIKey); err != nil {
			kioskBack(w, r, "", "Schlüssel konnte nicht gespeichert werden", "")
			return
		}
	}
	ctx := r.Context()
	if in.ID == "" {
		err = h.db.QueryRow(ctx, `INSERT INTO kiosk_terminals (name, base_url, api_key_enc, pubkey_sha256, active)
			VALUES ($1, $2, $3, $4, $5) RETURNING id::text`, in.Name, in.BaseURL, keyEnc, in.Fingerprint, in.Active).Scan(&in.ID)
	} else {
		var tag pgconn.CommandTag
		tag, err = h.db.Exec(ctx, `UPDATE kiosk_terminals SET name = $2, base_url = $3, pubkey_sha256 = $4, active = $5,
				api_key_enc = CASE WHEN $6 <> '' THEN $6 ELSE api_key_enc END, updated_at = NOW()
			WHERE id = $1::uuid`, in.ID, in.Name, in.BaseURL, in.Fingerprint, in.Active, keyEnc)
		if err == nil && tag.RowsAffected() == 0 {
			kioskBack(w, r, "", "Terminal nicht gefunden", "")
			return
		}
	}
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			kioskBack(w, r, "", "Ein Terminal mit dem Namen „"+in.Name+"“ gibt es schon", "")
			return
		}
		kioskBack(w, r, "", "Terminal konnte nicht gespeichert werden", "")
		return
	}
	componentLog("kiosk").Info().Str("terminal", in.ID).Str("user", getUser(r).ID).Bool("schluessel_neu", in.APIKey != "").Msg("kiosk-terminal gespeichert")
	kioskBack(w, r, "Terminal „"+in.Name+"“ gespeichert – jetzt „Verbindung prüfen“", "", "k-"+in.ID)
}

// KioskTerminalDeleteWeb: Anlagen verlieren die Zuordnung (ON DELETE SET NULL).
func (h *Handler) KioskTerminalDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageKiosk(r) || !kioskSameOrigin(r) {
		http.Error(w, "keine Berechtigung (HMI einrichten)", http.StatusForbidden)
		return
	}
	if _, err := h.db.Exec(r.Context(), `DELETE FROM kiosk_terminals WHERE id::text = $1`, chi.URLParam(r, "tid")); err != nil {
		kioskBack(w, r, "", "Terminal konnte nicht gelöscht werden", "")
		return
	}
	componentLog("kiosk").Info().Str("terminal", chi.URLParam(r, "tid")).Str("user", getUser(r).ID).Msg("kiosk-terminal gelöscht")
	kioskBack(w, r, "Terminal gelöscht", "", "")
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
func (h *Handler) KioskTerminalCheckWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageKiosk(r) || !kioskSameOrigin(r) {
		http.Error(w, "keine Berechtigung (HMI einrichten)", http.StatusForbidden)
		return
	}
	t, err := h.loadKioskTerminal(r.Context(), chi.URLParam(r, "tid"))
	if err != nil {
		kioskBack(w, r, "", "Terminal nicht gefunden", "")
		return
	}
	key, err := decryptSecret(h.kioskKey(), t.KeyEnc)
	if err != nil || key == "" {
		kioskBack(w, r, "", "„"+t.Name+"“: Schlüssel nicht lesbar – bitte neu eintragen", "k-"+t.ID)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), kioskTimeout)
	defer cancel()
	msg, ok := kioskStatusMessage(newKioskClient(t.BaseURL, key, t.Fingerprint).status(ctx))
	componentLog("kiosk").Info().Str("terminal", t.ID).Bool("ok", ok).Msg("kiosk-terminal geprüft")
	if ok {
		kioskBack(w, r, "„"+t.Name+"“: "+msg, "", "k-"+t.ID)
		return
	}
	kioskBack(w, r, "", "„"+t.Name+"“: "+msg, "k-"+t.ID)
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

// saveInfraKioskTerminal: Feld „Kiosk-Terminal“ der Stammdaten (nur mit hmi.manage).
func (h *Handler) saveInfraKioskTerminal(ctx context.Context, r *http.Request, infraID string) error {
	if _, ok := r.Form["kiosk_terminal_id"]; !ok || !h.hasPerm(r, permHMIManage) {
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
	p := h.hmiPermsFor(r)
	if !p.Kiosk {
		fail(http.StatusForbidden, "keine Berechtigung (HMI am Kiosk-Terminal zeigen)")
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
	if in.Mode == "control" && !p.Control {
		fail(http.StatusForbidden, "keine Berechtigung (HMI bedienen)")
		return
	}
	ctx, infraID, hmiID := r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "hmiID")
	if !kioskUUIDRe.MatchString(infraID) || !kioskUUIDRe.MatchString(hmiID) {
		fail(http.StatusNotFound, "HMI nicht gefunden")
		return
	}
	t, err := h.loadHMI(ctx, infraID, hmiID)
	if err != nil {
		fail(http.StatusNotFound, "HMI nicht gefunden")
		return
	}
	if in.Mode == "control" && !t.AllowControl {
		fail(http.StatusForbidden, "An diesem HMI ist Bedienen nicht erlaubt")
		return
	}
	var termID, plantPath string
	_ = h.db.QueryRow(ctx, `SELECT COALESCE(kiosk_terminal_id::text, ''), `+assetPathExpr("$1::uuid")+` FROM infrastructure WHERE id = $1::uuid`, infraID).
		Scan(&termID, &plantPath)
	if termID == "" {
		fail(http.StatusConflict, "Dieser Anlage ist kein Kiosk-Terminal zugeordnet")
		return
	}
	term, err := h.loadKioskTerminal(ctx, termID)
	if err != nil || !term.Active {
		fail(http.StatusConflict, "Das zugeordnete Kiosk-Terminal ist inaktiv")
		return
	}
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
		logSession("Terminal-Schlüssel nicht lesbar")
		fail(http.StatusInternalServerError, "Schlüssel des Terminals nicht lesbar – bitte unter Kiosk-Terminals neu eintragen")
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
