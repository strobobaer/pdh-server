package web

// „PDH öffnen“ am Anlagenterminal (0.82.28): Die per Karte angemeldete Person öffnet das normale PDH im
// Browser des Terminals, angemeldet als sie selbst und mit ihren eigenen Rechten.
//
// Ablauf (zwei POST-Schritte, nichts davon in einer Adresse):
//
//  1. POST /terminal/pdh      Gerätesitzung plus aktive Kartenanmeldung nötig. Legt ein Einmal-Ticket an
//     (nur der Hash wird gespeichert, 60 s gültig) und antwortet mit einer kleinen Seite, die das Ticket
//     per POST weiterreicht.
//  2. POST /terminal/pdh/open Löst das Ticket genau einmal ein (Gerätesitzung und dieselbe Kartenperson
//     weiterhin nötig) und setzt die Cookies: pdh_token = Sitzung der Person (JWT, höchstens 15 Minuten,
//     keine Verlängerung, Claims tsess/tdev), pdh_return_token = die Geräte-Sitzung des Terminals.
//
// Die Sitzung ist serverseitig widerrufbar (Tabelle terminal_pdh_sessions) und an das Gerät gebunden:
// sie gilt nur zusammen mit dem Rückkehr-Cookie desselben Geräts (HttpOnly). Beendet wird sie bei
// Kartenabmeldung, Kartenwechsel, Abmelden/Rückkehr in PDH und bei jedem Aufruf einer /terminal-Seite
// (Ruhezeit-Rückkehr, Zurück-Knopf). Der normale PDH-Anmeldeweg bleibt unberührt.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"html"
	"net/http"
	"strings"
	"time"
)

const (
	pdhHandoffTicketTTL  = 60 * time.Second
	pdhHandoffSessionTTL = 15 * time.Minute
	pdhHandoffMaxNext    = 300
)

func hashPDHTicket(t string) string {
	sum := sha256.Sum256([]byte("pdh-terminal-handoff:" + t))
	return hex.EncodeToString(sum[:])
}

func newPDHTicket() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// pdhHandoffNext: Ziel nach dem Öffnen; nur Pfade in PDH (nie /terminal, nie /login).
func pdhHandoffNext(next string) string {
	n := loginNext(strings.TrimSpace(next))
	if n == "/terminal" || strings.HasPrefix(n, "/terminal/") || len(n) > pdhHandoffMaxNext {
		return "/"
	}
	return n
}

// pdhHandoffIssue legt das Einmal-Ticket an. Offene, noch nicht eingelöste Tickets des Terminals werden verworfen.
func (h *Handler) pdhHandoffIssue(r *http.Request, terminalID, personID, deviceID, next string) (string, error) {
	ctx := r.Context()
	ticket, err := newPDHTicket()
	if err != nil {
		return "", err
	}
	if _, err := h.db.Exec(ctx, `UPDATE terminal_pdh_sessions SET revoked_at = now()
		WHERE terminal_user_id = $1::uuid AND redeemed_at IS NULL AND revoked_at IS NULL`, terminalID); err != nil {
		return "", err
	}
	_, _ = h.db.Exec(ctx, `DELETE FROM terminal_pdh_sessions WHERE created_at < now() - interval '2 days'`)
	_, err = h.db.Exec(ctx, `INSERT INTO terminal_pdh_sessions (terminal_user_id, person_id, device_id, next_path, ticket_sha256, ticket_expires_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, now() + make_interval(secs => $6))`,
		terminalID, personID, deviceID, next, hashPDHTicket(ticket), pdhHandoffTicketTTL.Seconds())
	return ticket, err
}

// pdhHandoffRedeem löst das Ticket atomar und genau einmal ein; ok=false: unbekannt, benutzt, abgelaufen,
// widerrufen oder für ein anderes Gerät/eine andere Person.
func (h *Handler) pdhHandoffRedeem(r *http.Request, ticket, terminalID, personID, deviceID string) (id, next string, ok bool, err error) {
	rows, err := h.db.Query(r.Context(), `UPDATE terminal_pdh_sessions SET redeemed_at = now(), expires_at = now() + make_interval(secs => $5)
		WHERE ticket_sha256 = $1 AND redeemed_at IS NULL AND revoked_at IS NULL AND ticket_expires_at > now()
		  AND terminal_user_id = $2::uuid AND person_id = $3::uuid AND device_id = $4::uuid
		RETURNING id::text, next_path`, hashPDHTicket(ticket), terminalID, personID, deviceID, pdhHandoffSessionTTL.Seconds())
	if err != nil {
		return "", "", false, err
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&id, &next); err != nil {
			return "", "", false, err
		}
		ok = true
	}
	return id, next, ok, rows.Err()
}

// pdhHandoffCheck: gilt die PDH-Sitzung tsess der Person sub noch? Verlangt, dass sie nicht widerrufen und nicht
// abgelaufen ist und dass die Anfrage vom Gerät tdev kommt (Rückkehr-Cookie mit der Gerätesitzung, Gerät nicht gesperrt).
func (h *Handler) pdhHandoffCheck(r *http.Request, tsess, sub, tdev string) bool {
	if h.db == nil || sub == "" || !slideUUIDRe.MatchString(tsess) || !slideUUIDRe.MatchString(tdev) {
		return false
	}
	rc, err := r.Cookie("pdh_return_token")
	if err != nil || rc.Value == "" {
		return false // anderer Browser/Client: ohne die Gerätesitzung des Terminals gilt sie nicht
	}
	dev := h.parseSessionToken(rc.Value)
	if dev.problem != "" || dev.device == "" || !strings.EqualFold(dev.device, tdev) {
		return false
	}
	var one int
	err = h.db.QueryRow(r.Context(), `SELECT 1 FROM terminal_pdh_sessions
		WHERE id = $1::uuid AND person_id = $2::uuid AND device_id = $3::uuid AND terminal_user_id = $4::uuid
		  AND redeemed_at IS NOT NULL AND revoked_at IS NULL AND expires_at > now()`, tsess, sub, tdev, dev.sub).Scan(&one)
	if err != nil {
		return false // auch Datenbankfehler: nicht gültig
	}
	return terminalDevices.active(r.Context(), dev.device, dev.sub, h.deviceByID)
}

// pdhHandoffRevoke beendet eine Sitzung serverseitig.
func (h *Handler) pdhHandoffRevoke(r *http.Request, id string) {
	if h.db == nil || !slideUUIDRe.MatchString(id) {
		return
	}
	if _, err := h.db.Exec(r.Context(), `UPDATE terminal_pdh_sessions SET revoked_at = now() WHERE id = $1::uuid AND revoked_at IS NULL`, id); err != nil {
		componentLog("terminal").Error().Err(err).Msg("pdh-sitzung widerrufen")
	}
}

// pdhHandoffRevokeCookie beendet die im Cookie pdh_token genannte PDH-Sitzung (Abmelden, Rückkehr zum Terminal).
func (h *Handler) pdhHandoffRevokeCookie(r *http.Request) {
	if c, err := r.Cookie("pdh_token"); err == nil && c.Value != "" {
		if s := h.parseSessionToken(c.Value); s.tsess != "" {
			h.pdhHandoffRevoke(r, s.tsess)
		}
	}
}

// pdhHandoffRevokeTerminal beendet alle PDH-Sitzungen des Terminals (Kartenabmeldung); keepPerson: Sitzungen
// dieser Person bleiben (Karte derselben Person erneut vorgehalten), "" = alle.
func (h *Handler) pdhHandoffRevokeTerminal(ctx context.Context, terminalID, keepPerson string) error {
	_, err := h.db.Exec(ctx, `UPDATE terminal_pdh_sessions SET revoked_at = now()
		WHERE terminal_user_id = $1::uuid AND revoked_at IS NULL AND ($2 = '' OR person_id <> $2::uuid)`, terminalID, keepPerson)
	return err
}

// pdhRestoreDevice stellt die Gerätesitzung des Terminals aus dem Rückkehr-Cookie wieder her und leitet auf
// target um. false: kein gültiges Geräte-Token im Rückkehr-Cookie (nichts geschrieben).
func (h *Handler) pdhRestoreDevice(w http.ResponseWriter, r *http.Request) bool {
	return h.pdhRestoreDeviceTo(w, r, "/terminal")
}

func (h *Handler) pdhRestoreDeviceTo(w http.ResponseWriter, r *http.Request, target string) bool {
	rc, err := r.Cookie("pdh_return_token")
	if err != nil || rc.Value == "" || h.tokenDevice(rc.Value) == "" {
		return false
	}
	s := h.parseSessionToken(rc.Value)
	if s.sub == "" {
		return false
	}
	http.SetCookie(w, &http.Cookie{Name: "pdh_token", Value: rc.Value, Path: "/", MaxAge: deviceCookieMaxAge, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "pdh_user_id", Value: s.sub, Path: "/", MaxAge: deviceCookieMaxAge, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "pdh_return_token", Value: "", Path: "/", MaxAge: -1})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusSeeOther)
	return true
}

// pdhHandoffLeave: Jeder Aufruf einer /terminal-Seite mit einer PDH-Sitzung des Terminals beendet diese
// (serverseitig) und stellt die Gerätesitzung wieder her. true: Antwort ist gegeben.
func (h *Handler) pdhHandoffLeave(w http.ResponseWriter, r *http.Request) bool {
	if p := r.URL.Path; p != "/terminal" && !strings.HasPrefix(p, "/terminal/") {
		return false
	}
	c, err := r.Cookie("pdh_token")
	if err != nil || c.Value == "" {
		return false
	}
	s := h.parseSessionToken(c.Value)
	if s.tsess == "" {
		return false
	}
	h.pdhHandoffRevoke(r, s.tsess)
	if h.pdhRestoreDeviceTo(w, r, r.URL.RequestURI()) {
		return true
	}
	clearSessionCookies(w)
	http.Redirect(w, r, "/global/", http.StatusSeeOther)
	return true
}

// pdhHandoffGate: Voraussetzungen beider Schritte – Gerätesitzung, Kartenanmeldung. ok=false: Antwort ist gegeben.
func (h *Handler) pdhHandoffGate(w http.ResponseWriter, r *http.Request) (term, personID, deviceID string, ok bool) {
	if terminalForeignPost(w, r) {
		return
	}
	term, ok = h.cardTerminal(w, r)
	if !ok {
		return
	}
	ok = false
	_, _, deviceID = h.sessionToken(r)
	if h.db == nil || deviceID == "" {
		http.Error(w, "Diese Seite ist nur für Terminal-Benutzer (Systembenutzer) bestimmt.", http.StatusForbidden)
		return
	}
	p, err := h.cardSessionPerson(r.Context(), term)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("kartensitzung lesen")
		http.Error(w, "Daten nicht verfügbar", http.StatusInternalServerError)
		return
	}
	if p == nil {
		http.Error(w, "Bitte zuerst mit der Karte anmelden.", http.StatusForbidden)
		return
	}
	// wie die Kartenanmeldung an der PDH-Anmeldeseite: Spiegel-Regel C
	if h.mirrorLoginRefused(w, r, p, "rfid-terminal", "PDH öffnen") {
		return
	}
	return term, p.ID, deviceID, true
}

// TerminalPDHStart: POST /terminal/pdh (optional next = Ziel in PDH)
func (h *Handler) TerminalPDHStart(w http.ResponseWriter, r *http.Request) {
	term, person, dev, ok := h.pdhHandoffGate(w, r)
	if !ok {
		return
	}
	_ = r.ParseForm()
	ticket, err := h.pdhHandoffIssue(r, term, person, dev, pdhHandoffNext(r.PostFormValue("next")))
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("pdh-ticket anlegen")
		http.Error(w, "Daten nicht verfügbar", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="de"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1">` +
		`<meta name="referrer" content="no-referrer"><title>PDH öffnen</title></head><body style="background:#0f1117;color:#f1f3fb;font-family:system-ui,sans-serif">` +
		`<form method="post" action="/terminal/pdh/open"><input type="hidden" name="ticket" value="` + html.EscapeString(ticket) + `">` +
		`<noscript><button type="submit" style="font-size:2em;padding:1em">PDH öffnen</button></noscript></form>` +
		`<script>document.forms[0].submit()</script></body></html>`))
}

// TerminalPDHOpen: POST /terminal/pdh/open (ticket) – löst das Ticket ein und setzt die Cookies.
func (h *Handler) TerminalPDHOpen(w http.ResponseWriter, r *http.Request) {
	term, person, dev, ok := h.pdhHandoffGate(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	_ = r.ParseForm()
	ticket := strings.TrimSpace(r.PostFormValue("ticket"))
	deny := func() {
		authLog(r, false, "terminal-pdh", "", person, "", "ticket ungültig, benutzt oder abgelaufen")
		http.Error(w, "Das Öffnen ist nicht mehr gültig. Bitte am Terminal erneut „PDH öffnen“ wählen.", http.StatusForbidden)
	}
	if ticket == "" || len(ticket) > 100 {
		deny()
		return
	}
	id, next, ok, err := h.pdhHandoffRedeem(r, ticket, term, person, dev)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("pdh-ticket einlösen")
		http.Error(w, "Daten nicht verfügbar", http.StatusInternalServerError)
		return
	}
	if !ok {
		deny()
		return
	}
	u, err := h.userByID(r.Context(), person)
	if err != nil || u == nil || !u.Active {
		h.pdhHandoffRevoke(r, id)
		deny()
		return
	}
	token, err := h.users.IssueToken(u, pdhHandoffSessionTTL, map[string]interface{}{"override": true, "tsess": id, "tdev": dev})
	if err != nil {
		h.pdhHandoffRevoke(r, id)
		componentLog("terminal").Error().Err(err).Msg("pdh-sitzung ausstellen")
		http.Error(w, "Anmeldung derzeit nicht möglich.", http.StatusInternalServerError)
		return
	}
	devCookie, _ := r.Cookie("pdh_token") // Gerätesitzung: Rückkehr-Cookie
	maxAge := int(pdhHandoffSessionTTL.Seconds())
	http.SetCookie(w, &http.Cookie{Name: "pdh_return_token", Value: devCookie.Value, Path: "/", MaxAge: deviceCookieMaxAge, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "pdh_token", Value: token, Path: "/", MaxAge: maxAge, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "pdh_user_id", Value: u.ID, Path: "/", MaxAge: maxAge, SameSite: http.SameSiteLaxMode})
	authLog(r, true, "terminal-pdh", "PDH öffnen", u.ID, strings.TrimSpace(u.FirstName+" "+u.LastName), "")
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, loginNext(next), http.StatusSeeOther)
}
