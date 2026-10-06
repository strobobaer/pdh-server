package web

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"pdh"
)

// HMI-Fernzugriff (VNC) aus der Anlage. Ablauf:
//   Anlage → Reiter „HMI“ → Ansehen/Bedienen → Viewer-Seite (noVNC im Browser)
//   → WebSocket /infrastructure/{id}/hmi/{hmiID}/ws → PDH-Server → TCP zum HMI.
// Der Server verbindet nur zu eingerichteten HMIs (kein offener Proxy),
// meldet sich selbst an (hmi_rfb.go) und erzwingt „nur ansehen“.

const (
	permHMIView    = "hmi.view"
	permHMIControl = "hmi.control"
	permHMIManage  = "hmi.manage"
)

// hmiPerms: Stufen der angemeldeten Person (fuer Vorlagen und Pruefungen).
type hmiPerms struct {
	View, Control, Manage bool
}

func (h *Handler) hmiPermsFor(r *http.Request) hmiPerms {
	p := hmiPerms{View: h.hasPerm(r, permHMIView), Control: h.hasPerm(r, permHMIControl), Manage: h.hasPerm(r, permHMIManage)}
	if p.Control { // bedienen schliesst ansehen ein
		p.View = true
	}
	return p
}

type hmiView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Host         string `json:"host,omitempty"` // nur fuer „einrichten“
	Port         int    `json:"port,omitempty"`
	HasPassword  bool   `json:"has_password"`
	AllowControl bool   `json:"allow_control"`
	Notes        string `json:"notes"`
	CanControl   bool   `json:"can_control"` // Stufe der Person und Freigabe am HMI
}

type hmiSessionView struct {
	User      string     `json:"user"`
	Mode      string     `json:"mode"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// hmiKey: eigener Schluessel fuer VNC-Passwoerter (wie Partner-Zugangsdaten).
func (h *Handler) hmiKey() []byte {
	secret := h.mailCfg.CredentialsKey
	if secret == "" {
		secret = h.jwtSecret
	}
	sum := sha256.Sum256([]byte("pdh-hmi-vnc:" + secret))
	return sum[:]
}

func (h *Handler) hmiRoutes(r chi.Router) {
	r.Get("/infrastructure/{id}/hmis", h.HMIList)
	r.Post("/infrastructure/{id}/hmis", h.HMISave)
	r.Post("/infrastructure/{id}/hmis/{hmiID}", h.HMISave)
	r.Post("/infrastructure/{id}/hmis/{hmiID}/delete", h.HMIDelete)
	r.Get("/infrastructure/{id}/hmis/{hmiID}/sessions", h.HMISessions)
	r.Get("/infrastructure/{id}/hmi/{hmiID}", h.HMIViewerPage)
	r.Get("/infrastructure/{id}/hmi/{hmiID}/ws", h.HMIWebSocket)
	r.Get("/vendor/novnc/rfb.min.js", h.NoVNCScript)
}

// NoVNCScript liefert den eingebetteten noVNC-Client (web/static/vendor/novnc).
func (h *Handler) NoVNCScript(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(pdh.Static, "web/static/vendor/novnc/rfb.min.js")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(b)
}

func (h *Handler) HMIList(w http.ResponseWriter, r *http.Request) {
	p := h.hmiPermsFor(r)
	if !p.View && !p.Manage {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "keine Berechtigung (HMI ansehen)"})
		return
	}
	rows, err := h.db.Query(r.Context(), `SELECT id::text, name, host, port, password_enc <> '', allow_control, notes
		FROM infrastructure_hmis WHERE infrastructure_id = $1::uuid ORDER BY sort_order, lower(name)`, chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "HMIs konnten nicht geladen werden"})
		return
	}
	defer rows.Close()
	out := []hmiView{}
	for rows.Next() {
		var v hmiView
		if err := rows.Scan(&v.ID, &v.Name, &v.Host, &v.Port, &v.HasPassword, &v.AllowControl, &v.Notes); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "HMIs konnten nicht gelesen werden"})
			return
		}
		v.CanControl = p.Control && v.AllowControl
		if !p.Manage {
			v.Host, v.Port = "", 0
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"hmis": out, "perms": p})
}

var hmiHostRe = regexp.MustCompile(`^[A-Za-z0-9._:\-\[\]]{1,253}$`)

// HMISave: anlegen (ohne hmiID) oder aendern. Leeres Passwort = unveraendert,
// clear_password = entfernen.
func (h *Handler) HMISave(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permHMIManage) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "keine Berechtigung (HMI einrichten)"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var in struct {
		Name          string `json:"name"`
		Host          string `json:"host"`
		Port          int    `json:"port"`
		Password      string `json:"password"`
		ClearPassword bool   `json:"clear_password"`
		AllowControl  bool   `json:"allow_control"`
		Notes         string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ungültige Eingabe"})
		return
	}
	in.Name, in.Host, in.Notes = strings.TrimSpace(in.Name), strings.TrimSpace(in.Host), strings.TrimSpace(in.Notes)
	if in.Port == 0 {
		in.Port = 5900
	}
	switch {
	case in.Name == "" || len([]rune(in.Name)) > 120:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Bitte einen Namen angeben (höchstens 120 Zeichen)"})
		return
	case !hmiHostRe.MatchString(in.Host):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Bitte eine gültige Adresse angeben (IP oder Rechnername)"})
		return
	case in.Port < 1 || in.Port > 65535:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Port muss zwischen 1 und 65535 liegen"})
		return
	case len(in.Password) > 64:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Passwort zu lang"})
		return
	}
	pwEnc := ""
	if in.Password != "" {
		enc, err := encryptSecret(h.hmiKey(), in.Password)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Passwort konnte nicht gespeichert werden"})
			return
		}
		pwEnc = enc
	}
	ctx, infraID, hmiID := r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "hmiID")
	var err error
	if hmiID == "" {
		err = h.db.QueryRow(ctx, `INSERT INTO infrastructure_hmis (infrastructure_id, name, host, port, password_enc, allow_control, notes, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, NULLIF($8,'')::uuid) RETURNING id::text`,
			infraID, in.Name, in.Host, in.Port, pwEnc, in.AllowControl, in.Notes, getUser(r).ID).Scan(&hmiID)
	} else {
		var tag interface{ RowsAffected() int64 }
		tag, err = h.db.Exec(ctx, `UPDATE infrastructure_hmis SET name=$3, host=$4, port=$5, allow_control=$6, notes=$7,
				password_enc = CASE WHEN $9 THEN '' WHEN $8 <> '' THEN $8 ELSE password_enc END, updated_at=NOW()
			WHERE id=$2::uuid AND infrastructure_id=$1::uuid`,
			infraID, hmiID, in.Name, in.Host, in.Port, in.AllowControl, in.Notes, pwEnc, in.ClearPassword)
		if err == nil && tag.RowsAffected() == 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "HMI nicht gefunden"})
			return
		}
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "HMI konnte nicht gespeichert werden"})
		return
	}
	componentLog("hmi").Info().Str("infra", infraID).Str("hmi", hmiID).Str("user", getUser(r).ID).Msg("hmi gespeichert")
	writeJSON(w, http.StatusOK, map[string]string{"id": hmiID})
}

func (h *Handler) HMIDelete(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permHMIManage) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "keine Berechtigung (HMI einrichten)"})
		return
	}
	if _, err := h.db.Exec(r.Context(), `DELETE FROM infrastructure_hmis WHERE id=$2::uuid AND infrastructure_id=$1::uuid`,
		chi.URLParam(r, "id"), chi.URLParam(r, "hmiID")); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "HMI konnte nicht gelöscht werden"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "gelöscht"})
}

// HMISessions: die letzten Zugriffe (wer, wann, wie) – fuer alle mit HMI-Rechten.
func (h *Handler) HMISessions(w http.ResponseWriter, r *http.Request) {
	p := h.hmiPermsFor(r)
	if !p.View && !p.Manage {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "keine Berechtigung"})
		return
	}
	rows, err := h.db.Query(r.Context(), `SELECT COALESCE(TRIM(u.first_name || ' ' || u.last_name), ''), s.mode, s.started_at, s.ended_at, s.error
		FROM infrastructure_hmi_sessions s
		JOIN infrastructure_hmis x ON x.id = s.hmi_id AND x.infrastructure_id = $1::uuid
		LEFT JOIN users u ON u.id = s.user_id
		WHERE s.hmi_id = $2::uuid ORDER BY s.started_at DESC LIMIT 20`, chi.URLParam(r, "id"), chi.URLParam(r, "hmiID"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Zugriffe konnten nicht geladen werden"})
		return
	}
	defer rows.Close()
	out := []hmiSessionView{}
	for rows.Next() {
		var v hmiSessionView
		if err := rows.Scan(&v.User, &v.Mode, &v.StartedAt, &v.EndedAt, &v.Error); err != nil {
			break
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

type hmiTarget struct {
	ID, InfraID, InfraName, Name, Host, PasswordEnc string
	Port                                            int
	AllowControl                                    bool
}

func (h *Handler) loadHMI(ctx context.Context, infraID, hmiID string) (*hmiTarget, error) {
	t := &hmiTarget{}
	err := h.db.QueryRow(ctx, `SELECT x.id::text, x.infrastructure_id::text, i.name, x.name, x.host, x.port, x.password_enc, x.allow_control
		FROM infrastructure_hmis x JOIN infrastructure i ON i.id = x.infrastructure_id
		WHERE x.id = $2::uuid AND x.infrastructure_id = $1::uuid`, infraID, hmiID).
		Scan(&t.ID, &t.InfraID, &t.InfraName, &t.Name, &t.Host, &t.Port, &t.PasswordEnc, &t.AllowControl)
	return t, err
}

// hmiMode: gewuenschter Modus, begrenzt auf die Stufe der Person und die Freigabe am HMI.
func hmiMode(requested string, p hmiPerms, t *hmiTarget) string {
	if requested == "control" && p.Control && t.AllowControl {
		return "control"
	}
	return "view"
}

// HMIViewerPage: Vollbild-Viewer (noVNC) fuer ein HMI.
func (h *Handler) HMIViewerPage(w http.ResponseWriter, r *http.Request) {
	p := h.hmiPermsFor(r)
	if !p.View {
		http.Error(w, "keine Berechtigung (HMI ansehen)", http.StatusForbidden)
		return
	}
	t, err := h.loadHMI(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "hmiID"))
	if err != nil {
		http.Redirect(w, r, "/infrastructure/"+chi.URLParam(r, "id")+"?tab=hmi", http.StatusFound)
		return
	}
	mode := hmiMode(r.URL.Query().Get("mode"), p, t)
	data := struct {
		BaseData
		HMI        *hmiTarget
		Mode       string
		CanControl bool
	}{
		BaseData:   h.baseData(r, "hmi", t.Name, "HMI"),
		HMI:        t,
		Mode:       mode,
		CanControl: p.Control && t.AllowControl,
	}
	h.render(w, "hmi_viewer", data)
}

var hmiUpgrader = websocket.Upgrader{
	ReadBufferSize:  32 << 10,
	WriteBufferSize: 32 << 10,
	Subprotocols:    []string{"binary"},
	// CheckOrigin: Standard (Origin muss zum Host passen) – kein fremder Seitenzugriff
}

// HMIWebSocket: WebSocket ↔ TCP zum HMI mit serverseitiger Anmeldung.
func (h *Handler) HMIWebSocket(w http.ResponseWriter, r *http.Request) {
	p := h.hmiPermsFor(r)
	if !p.View {
		http.Error(w, "keine Berechtigung (HMI ansehen)", http.StatusForbidden)
		return
	}
	t, err := h.loadHMI(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "hmiID"))
	if err != nil {
		http.Error(w, "HMI nicht gefunden", http.StatusNotFound)
		return
	}
	mode := hmiMode(r.URL.Query().Get("mode"), p, t)
	password, err := decryptSecret(h.hmiKey(), t.PasswordEnc)
	if err != nil {
		http.Error(w, "VNC-Passwort nicht lesbar – bitte am HMI neu eingeben", http.StatusInternalServerError)
		return
	}
	ws, err := hmiUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade schreibt die Antwort selbst
	}
	defer ws.Close()

	user := getUser(r)
	log := componentLog("hmi").With().Str("hmi", t.ID).Str("infra", t.InfraID).Str("user", user.ID).Str("mode", mode).Logger()
	var sessionID string
	_ = h.db.QueryRow(context.Background(), `INSERT INTO infrastructure_hmi_sessions (hmi_id, user_id, mode) VALUES ($1::uuid, NULLIF($2,'')::uuid, $3) RETURNING id::text`,
		t.ID, user.ID, mode).Scan(&sessionID)
	finish := func(errText string) {
		if sessionID != "" {
			_, _ = h.db.Exec(context.Background(), `UPDATE infrastructure_hmi_sessions SET ended_at = NOW(), error = $2 WHERE id = $1::uuid`, sessionID, errText)
		}
	}
	fail := func(msg string, err error) {
		log.Warn().Err(err).Msg(msg)
		text := msg
		if err != nil {
			text += ": " + err.Error()
		}
		finish(text)
		_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4000, hmiCloseReason(text)), time.Now().Add(time.Second))
	}

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(t.Host, strconv.Itoa(t.Port)), 8*time.Second)
	if err != nil {
		fail("HMI nicht erreichbar", err)
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if err := rfbServerHandshake(conn, password); err != nil {
		fail("Anmeldung am HMI fehlgeschlagen", err)
		return
	}
	_ = conn.SetDeadline(time.Time{})

	wsc := &wsConn{ws: ws}
	if err := rfbClientHandshake(wsc); err != nil {
		fail("Browser-Verbindung fehlgeschlagen", err)
		return
	}
	log.Info().Msg("hmi-sitzung gestartet")

	var once sync.Once
	done := make(chan string, 2)
	stop := func(reason string) { once.Do(func() { done <- reason; conn.Close(); ws.Close() }) }
	go func() { // HMI → Browser
		_, err := io.Copy(wsc, conn)
		stop(errText(err, "HMI hat die Verbindung beendet"))
	}()
	go func() { // Browser → HMI (bei „ansehen“ gefiltert)
		err := rfbForwardClient(conn, wsc, mode == "view")
		stop(errText(err, ""))
	}()
	reason := <-done
	finish(reason)
	log.Info().Str("grund", reason).Msg("hmi-sitzung beendet")
}

func errText(err error, fallback string) string {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		return fallback
	}
	return err.Error()
}

// hmiCloseReason: Close-Grund ist auf 123 Bytes begrenzt.
func hmiCloseReason(s string) string {
	for len(s) > 120 {
		r := []rune(s)
		s = string(r[:len(r)-1])
	}
	return s
}

// wsConn: WebSocket als Byte-Strom (noVNC schickt RFB in Binaer-Nachrichten).
type wsConn struct {
	ws  *websocket.Conn
	r   io.Reader
	wmu sync.Mutex
}

func (c *wsConn) Read(p []byte) (int, error) {
	for {
		if c.r == nil {
			typ, r, err := c.ws.NextReader()
			if err != nil {
				return 0, err
			}
			if typ != websocket.BinaryMessage {
				continue
			}
			c.r = r
		}
		n, err := c.r.Read(p)
		if errors.Is(err, io.EOF) {
			c.r = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *wsConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := c.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}
