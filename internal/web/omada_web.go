package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"pdh/internal/integrations/omada"
	"pdh/internal/integrations/unifi"
)

// Reiter „Omada“ der Server-Einstellungen (optional, Recht system.omada) – gebaut nach dem
// Muster des Reiters „UniFi“ (certs_lan.go).
//
//	POST /admin/server-config/omada           Adresse, Omada-ID, Client-ID, Client-Secret, Standort speichern; zeigt den Fingerabdruck
//	POST /admin/server-config/omada/confirm   genau diesen Fingerabdruck bestätigen
//	POST /admin/server-config/omada/check     Verbindung prüfen (anmelden, Standort suchen, Geräte zählen)
//
// Beim Zeichnen des Reiters geht keine Anfrage an den Controller. Das Client-Secret steht
// nie in einer Antwort, einer Meldung oder im Protokoll.
//
// Stand 0.80.21: nach der Dokumentation des Herstellers gebaut und noch nicht an einem
// echten Controller erprobt.

var errOmadaBlocked = errors.New("Am Spiegelserver und während der Wartung lässt sich die Omada-Anbindung nicht ändern")

// omadaKeys: alle Einstellungen der Anbindung (beim Entfernen geleert; Secret und Pin zuerst).
var omadaKeys = []string{keyOmadaSecret, keyOmadaPin, keyOmadaURL, keyOmadaID, keyOmadaClient, keyOmadaSite}

// omadaPanel: Einstellungen der Omada-Anbindung. Das Client-Secret selbst ist nie darin.
type omadaPanel struct {
	URL, ID, Client, Site, Pin, SeenPin, Err string
	HasSecret, Confirmed                     bool
}

// Ready: alles hinterlegt und bestätigt – „Verbindung prüfen“ anbieten?
func (p omadaPanel) Ready() bool {
	return p.URL != "" && p.ID != "" && p.Client != "" && p.HasSecret && p.Confirmed
}

// omadaPanelFrom liest nur die Einstellungen – keine Anfrage an den Controller.
func omadaPanelFrom(get func(key string) string) omadaPanel {
	p := omadaPanel{URL: get(keyOmadaURL), ID: get(keyOmadaID), Client: get(keyOmadaClient), Site: get(keyOmadaSite),
		HasSecret: get(keyOmadaSecret) != ""}
	if pin := get(keyOmadaPin); pin != "" {
		p.Pin, p.Confirmed = unifi.FormatPin(pin), true
	}
	return p
}

// omadaTab: Reiter „Omada“ (widgets/omada.gohtml).
type omadaTab struct {
	Can      bool // system.omada
	Msg, Err string
	Omada    omadaPanel
}

// omadaResult: was die Aktionen dem Reiter zusätzlich zeigen.
type omadaResult struct {
	Seen string // gesehener Fingerabdruck (zur Bestätigung)
	Warn string // z. B. Controller gerade nicht erreichbar
}

// omadaDeps: was die Aktionen brauchen (in Tests im Speicher, ohne Netz und Datenbank).
type omadaDeps struct {
	setting     func(ctx context.Context, key string) string
	putSetting  func(ctx context.Context, key, value string) error
	blocked     func() bool                                            // Spiegelserver oder Wartung
	fingerprint func(ctx context.Context, base string) (string, error) // nur TLS, ohne Zugangsdaten
	seal        func(plain string) (string, error)                     // Secret verschlüsseln
	check       func(ctx context.Context) (int, error)                 // anmelden und Geräte zählen
	forget      func()                                                 // gemerkten Client samt Token verwerfen
	notify      func(ctx context.Context, userID, msg string)          // Meldung an die Administratoren
}

func omadaSite(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) > 64 || strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || r == '/' || r == '\\' }) >= 0 {
		return "", errors.New("Standort: bitte den Namen des Standorts aus dem Omada-Controller eintragen (oder leer lassen)")
	}
	return s, nil
}

func omadaIDField(label, raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", fmt.Errorf("Bitte die %s eintragen (Controller → Einstellungen → Platform Integration → Open API)", label)
	}
	if !omada.ValidID(v) {
		return "", fmt.Errorf("%s: nur Buchstaben, Ziffern, „-“ und „_“ – bitte unverändert vom Controller übernehmen", label)
	}
	return v, nil
}

// omadaDoSettings speichert Adresse, Omada-ID, Client-ID, Standort und – nur wenn neu
// eingegeben – das Client-Secret (verschlüsselt). Eine geänderte Adresse verlangt ein neu
// eingegebenes Secret und löscht den bestätigten Fingerabdruck. Danach liest es den
// Fingerabdruck des Controllers (nur TLS, ohne Zugangsdaten) zur Bestätigung.
// Eine leere Adresse entfernt die Anbindung.
func omadaDoSettings(ctx context.Context, d omadaDeps, rawURL, id, client, secret, site string) (omadaResult, string, error) {
	var res omadaResult
	if d.blocked() {
		return res, "", errOmadaBlocked
	}
	if strings.TrimSpace(rawURL) == "" {
		for _, k := range omadaKeys {
			if err := d.putSetting(ctx, k, ""); err != nil {
				return res, "", err
			}
		}
		if d.forget != nil {
			d.forget()
		}
		return res, "Omada-Anbindung entfernt. Am Controller ändert sich nichts.", nil
	}
	base, err := omada.ValidateBase(rawURL)
	if err != nil {
		return res, "", err
	}
	if id, err = omadaIDField("Omada-ID", id); err != nil {
		return res, "", err
	}
	if client, err = omadaIDField("Client-ID", client); err != nil {
		return res, "", err
	}
	if site, err = omadaSite(site); err != nil {
		return res, "", err
	}
	secret = strings.TrimSpace(secret)
	if len(secret) > 1024 || strings.IndexFunc(secret, unicode.IsControl) >= 0 {
		return res, "", errors.New("Client-Secret: bitte das Secret aus dem Omada-Controller unverändert einfügen")
	}
	// Das gespeicherte Secret gilt nur für die Adresse, unter der es eingetragen wurde: Bei
	// neuer Adresse muss es neu eingegeben werden, sonst wird nichts gespeichert. So kann
	// niemand das hinterlegte Secret an ein anderes Gerät schicken lassen.
	old := d.setting(ctx, keyOmadaURL)
	urlChanged := old != base
	if secret == "" && urlChanged && old != "" {
		return res, "", errors.New("Bei neuer Adresse das Client-Secret neu eintragen. Nichts gespeichert")
	}
	if secret == "" && (urlChanged || d.setting(ctx, keyOmadaSecret) == "") {
		return res, "", errors.New("Bitte das Client-Secret aus dem Omada-Controller eintragen")
	}
	enc := ""
	if secret != "" {
		if d.seal == nil {
			return res, "", errors.New("Client-Secret lässt sich nicht verschlüsseln")
		}
		if enc, err = d.seal(secret); err != nil || enc == "" {
			return res, "", errors.New("Client-Secret lässt sich nicht verschlüsseln")
		}
	}
	// Zuerst den Fingerabdruck löschen: Bricht das Speichern mittendrin ab, gibt es ohne
	// bestätigten Fingerabdruck keine Verbindung – weder zum alten noch zum neuen Gerät.
	if urlChanged {
		if err := d.putSetting(ctx, keyOmadaPin, ""); err != nil {
			return res, "", err
		}
	}
	if enc != "" {
		if err := d.putSetting(ctx, keyOmadaSecret, enc); err != nil {
			return res, "", err
		}
	}
	for _, kv := range [][2]string{{keyOmadaURL, base}, {keyOmadaID, id}, {keyOmadaClient, client}, {keyOmadaSite, site}} {
		if err := d.putSetting(ctx, kv[0], kv[1]); err != nil {
			return res, "", err
		}
	}
	what := "Omada-Einstellungen gespeichert (" + base
	if site != "" {
		what += ", Standort " + site
	}
	what += ")."
	if enc != "" {
		what += " Neues Client-Secret hinterlegt."
	}
	if d.fingerprint == nil {
		return res, what, nil
	}
	fp, err := d.fingerprint(ctx, base)
	if err != nil {
		res.Warn = "Der Omada-Controller antwortet gerade nicht: " + certSafeText(err.Error()) + ". Adresse und Port prüfen und erneut speichern."
		return res, what, nil
	}
	if fp == unifi.NormalizePin(d.setting(ctx, keyOmadaPin)) {
		return res, what + " Fingerabdruck unverändert.", nil
	}
	res.Seen = unifi.FormatPin(fp)
	return res, what + " Jetzt den Fingerabdruck prüfen und bestätigen.", nil
}

// omadaDoConfirm speichert den Fingerabdruck nur, wenn der Controller genau diesen zeigt.
func omadaDoConfirm(ctx context.Context, d omadaDeps, posted string) (omadaResult, string, error) {
	var res omadaResult
	if d.blocked() {
		return res, "", errOmadaBlocked
	}
	base := d.setting(ctx, keyOmadaURL)
	if base == "" {
		return res, "", errors.New("Zuerst die Adresse des Omada-Controllers speichern")
	}
	want := unifi.NormalizePin(posted)
	if len(want) != 64 || strings.Trim(want, "0123456789abcdef") != "" {
		return res, "", errors.New("Fingerabdruck unvollständig – bitte erneut speichern und bestätigen")
	}
	if d.fingerprint == nil {
		return res, "", errOmadaOff
	}
	fp, err := d.fingerprint(ctx, base)
	if err != nil {
		return res, "", err
	}
	if fp != want {
		res.Seen = unifi.FormatPin(fp)
		return res, "", errors.New("Der Omada-Controller zeigt inzwischen einen anderen Fingerabdruck. Nicht bestätigt – bitte den neu angezeigten prüfen")
	}
	if err := d.putSetting(ctx, keyOmadaPin, fp); err != nil {
		return res, "", err
	}
	return res, "Fingerabdruck des Omada-Controllers bestätigt (" + unifi.FormatPin(fp) + "). Jetzt „Verbindung prüfen“ drücken.", nil
}

// omadaDoCheck meldet sich am Controller an und zählt die Geräte am Standort. Es ändert
// nichts – weder am Controller noch an den Einstellungen.
func omadaDoCheck(ctx context.Context, d omadaDeps) (omadaResult, string, error) {
	var res omadaResult
	if d.check == nil {
		return res, "", errOmadaOff
	}
	n, err := d.check(ctx)
	if err != nil {
		return res, "", err
	}
	if n == 1 {
		return res, "Anmeldung in Ordnung – 1 Gerät am Standort gefunden.", nil
	}
	return res, fmt.Sprintf("Anmeldung in Ordnung – %d Geräte am Standort gefunden.", n), nil
}

// ── Betrieb ──────────────────────────────────────────────────

func (h *Handler) omadaDepsLive() omadaDeps {
	return omadaDeps{
		setting:    h.certSetting,
		putSetting: h.certPutSetting,
		blocked:    func() bool { return h.Mirror().On || h.MaintLocked() },
		fingerprint: func(ctx context.Context, base string) (string, error) {
			return omada.Fingerprint(ctx, base)
		},
		seal: func(plain string) (string, error) { return encryptSecret(h.pkiKey(), plain) },
		check: func(ctx context.Context) (int, error) {
			c, err := h.omadaAPI(ctx)
			if err != nil {
				return 0, err
			}
			return c.Check(ctx)
		},
		forget: func() {
			h.omadaC.mu.Lock()
			h.omadaC.client, h.omadaC.key = nil, ""
			h.omadaC.mu.Unlock()
		},
		notify: func(ctx context.Context, userID, msg string) {
			if h.db == nil {
				return
			}
			h.notifyAdmins(ctx, "📡 **Omada-Anbindung geändert** von "+h.chatUserName(ctx, userID)+": "+msg)
		},
	}
}

// ── HTTP ─────────────────────────────────────────────────────

func (h *Handler) omadaTabFor(ctx context.Context, r *http.Request) omadaTab {
	return h.omadaTabFrom(r, func(k string) string { return h.certSetting(ctx, k) })
}

func (h *Handler) omadaTabFrom(r *http.Request, get func(key string) string) omadaTab {
	t := omadaTab{Can: h.hasPerm(r, permOmada)}
	if t.Can {
		t.Omada = omadaPanelFrom(get)
	}
	return t
}

func (h *Handler) omadaRender(w http.ResponseWriter, t omadaTab) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tm, err := h.tmpl.Clone()
	if err == nil {
		err = tm.ExecuteTemplate(w, "omada-panel", t)
	}
	if err != nil {
		fmt.Fprintf(w, `<div id="omada-panel" class="sc-sub" style="color:var(--red)">Omada: %s</div>`, esc(err.Error()))
	}
}

// omadaAct: Recht prüfen, Aktion ausführen, protokollieren, melden, Reiter-Karte neu zeichnen.
// changes=false (Verbindung prüfen) wird nur protokolliert – es hat sich nichts geändert.
func (h *Handler) omadaAct(w http.ResponseWriter, r *http.Request, d omadaDeps, what string, changes bool,
	do func(ctx context.Context, d omadaDeps) (omadaResult, string, error)) {
	if !h.hasPerm(r, permOmada) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	res, msg, err := do(ctx, d)
	uid := ""
	if u := getUser(r); u != nil {
		uid = u.ID
	}
	lg := componentLog("omada")
	if err != nil {
		lg.Warn().Str("von", uid).Str("aktion", what).Str("fehler", certSafeText(err.Error())).Msg("omada")
	} else {
		lg.Info().Str("von", uid).Str("aktion", what).Str("meldung", msg).Msg("omada")
		if changes && d.notify != nil {
			d.notify(ctx, uid, msg)
		}
	}
	t := h.omadaTabFrom(r, func(k string) string { return d.setting(ctx, k) })
	t.Msg = msg
	if err != nil {
		t.Err = certSafeText(err.Error())
	}
	t.Omada.SeenPin = res.Seen
	if res.Warn != "" {
		t.Omada.Err = res.Warn
	}
	h.omadaRender(w, t)
}

// OmadaWeb: POST /admin/server-config/omada (url, omada_id, client_id, secret, site)
func (h *Handler) OmadaWeb(w http.ResponseWriter, r *http.Request) {
	h.omadaAct(w, r, h.omadaDepsLive(), "omada-einstellungen", true, func(ctx context.Context, d omadaDeps) (omadaResult, string, error) {
		return omadaDoSettings(ctx, d, r.FormValue("url"), r.FormValue("omada_id"), r.FormValue("client_id"), r.FormValue("secret"), r.FormValue("site"))
	})
}

// OmadaConfirmWeb: POST /admin/server-config/omada/confirm (fingerprint)
func (h *Handler) OmadaConfirmWeb(w http.ResponseWriter, r *http.Request) {
	h.omadaAct(w, r, h.omadaDepsLive(), "omada-fingerabdruck", true, func(ctx context.Context, d omadaDeps) (omadaResult, string, error) {
		return omadaDoConfirm(ctx, d, r.FormValue("fingerprint"))
	})
}

// OmadaCheckWeb: POST /admin/server-config/omada/check
func (h *Handler) OmadaCheckWeb(w http.ResponseWriter, r *http.Request) {
	h.omadaAct(w, r, h.omadaDepsLive(), "omada-pruefen", false, func(ctx context.Context, d omadaDeps) (omadaResult, string, error) {
		return omadaDoCheck(ctx, d)
	})
}
