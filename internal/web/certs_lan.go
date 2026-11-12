package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"

	"pdh/internal/integrations/unifi"
)

// Namen im Haus über die UniFi UDM Pro (optional, Recht system.unifi).
//
//	POST /admin/server-config/certs/unifi          Adresse, API-Schlüssel, Standort speichern; zeigt den Fingerabdruck
//	POST /admin/server-config/certs/unifi/confirm  genau diesen Fingerabdruck bestätigen
//	POST /admin/server-config/certs/lan            Name im Haus eintragen (name, overwrite=1)
//
// Ohne gespeicherte Adresse und Schlüssel geht keine Anfrage an eine UniFi-Adresse – weder beim
// Start noch in der täglichen Prüfung noch beim Zeichnen der Karte. Zertifikate holen, erneuern
// und entfernen hängen nie von der UDM Pro ab.

// Merker je Name (app_settings): Hinweis der täglichen Prüfung bzw. Adresse eines fremden Eintrags.
const (
	keyUnifiHintPrefix  = "unifi.hint."
	keyUnifiOtherPrefix = "unifi.other."
)

var errUnifiOff = errors.New("UniFi ist nicht eingerichtet. Unter „Namen im Haus (UniFi)“ Adresse und API-Schlüssel speichern – oder den Eintrag am DNS-Server im Haus von Hand anlegen")

// certLANClient: was das PDH von der UDM Pro braucht (unifi.Client; in Tests im Speicher).
type certLANClient interface {
	Records(ctx context.Context) ([]unifi.Record, error)
	Create(ctx context.Context, name, ip string) (string, error)
	Update(ctx context.Context, id, name, ip string) error
	Delete(ctx context.Context, id string) error
}

// unifiPanel: Abschnitt „Namen im Haus (UniFi)“. Der Schlüssel selbst ist nie darin.
type unifiPanel struct {
	URL, Site, Pin, SeenPin, Err string
	HasKey, Confirmed            bool
}

// unifiPanelFrom liest nur die Einstellungen – keine Anfrage an die UDM Pro.
func unifiPanelFrom(get func(key string) string, seen, perr string) unifiPanel {
	p := unifiPanel{URL: get(keyUnifiURL), Site: get(keyUnifiSite), HasKey: get(keyUnifiKey) != "", SeenPin: seen, Err: perr}
	if pin := get(keyUnifiPin); pin != "" {
		p.Pin, p.Confirmed = unifi.FormatPin(pin), true
	}
	return p
}

// LANReady: Knopf „Im Haus eintragen“ zeigen?
func (p certPanel) LANReady() bool {
	return p.CanUnifi && p.Unifi.URL != "" && p.Unifi.HasKey && p.Unifi.Confirmed
}

func certLANNorm(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

// certLANMatch: was für name zu tun ist, und der betroffene Eintrag (eigener bzw. fremder).
// Nur Einträge Typ A gleichen Namens zählen; Groß/Klein und Punkt am Ende sind egal.
func certLANMatch(recs []unifi.Record, name, ip, ownID string) (string, unifi.Record) {
	n := certLANNorm(name)
	var same []unifi.Record
	for _, r := range recs {
		if strings.EqualFold(r.Type, "A") && certLANNorm(r.Name) == n {
			same = append(same, r)
		}
	}
	if ownID != "" {
		for _, r := range same {
			if r.ID == ownID {
				if strings.TrimSpace(r.Value) == ip {
					return "stimmt", r
				}
				return "eigenen-aendern", r
			}
		}
	}
	for _, r := range same {
		if strings.TrimSpace(r.Value) == ip {
			return "stimmt", r
		}
	}
	if len(same) > 0 {
		return "fremd", same[0]
	}
	return "anlegen", unifi.Record{}
}

// certLANPlan: "anlegen", "stimmt", "eigenen-aendern" (Kennung == ownID) oder "fremd"
// (anderer Eintrag gleichen Namens mit anderer Adresse; other = dessen Adresse).
func certLANPlan(recs []unifi.Record, name, ip, ownID string) (action string, other string) {
	action, r := certLANMatch(recs, name, ip, ownID)
	if action == "fremd" {
		other = r.Value
	}
	return action, other
}

func certLANClearMarks(ctx context.Context, put func(ctx context.Context, key, value string) error, name string) {
	_ = put(ctx, keyUnifiHintPrefix+name, "")
	_ = put(ctx, keyUnifiOtherPrefix+name, "")
}

// certDoLAN trägt einen geführten Namen an der UDM Pro ein (Typ A auf die mitwandernde
// Adresse). Einen fremden Eintrag ändert es nur mit overwrite und übernimmt ihn nie als
// eigenen – beim Entfernen des Namens bleibt er stehen.
func certDoLAN(ctx context.Context, d certActDeps, raw string, overwrite bool) (string, error) {
	if d.blocked() {
		return "", errCertBlocked
	}
	row, err := certCheckName(ctx, d, raw)
	if err != nil {
		return "", err
	}
	if d.lan == nil || d.lanTarget == nil {
		return "", errUnifiOff
	}
	c, err := d.lan(ctx)
	if err != nil {
		return "", err
	}
	ip, err := d.lanTarget(ctx)
	if err != nil {
		return "", err
	}
	recs, err := c.Records(ctx)
	if err != nil {
		return "", err
	}
	lg := componentLog("zertifikate")
	action, rec := certLANMatch(recs, row.Name, ip, row.LANRecordID)
	switch action {
	case "stimmt":
		certLANClearMarks(ctx, d.putSetting, row.Name)
		return row.Name + " zeigt im Haus schon auf " + ip + ".", nil
	case "eigenen-aendern":
		if err := c.Update(ctx, rec.ID, row.Name, ip); err != nil {
			return "", err
		}
		lg.Info().Str("name", row.Name).Str("adresse", ip).Str("alt", rec.Value).Str("kennung", rec.ID).Msg("eintrag im haus geändert")
		certLANClearMarks(ctx, d.putSetting, row.Name)
		return row.Name + " zeigt im Haus jetzt auf " + ip + " (Eintrag an der UDM Pro geändert, vorher " + rec.Value + ").", nil
	case "fremd":
		if !overwrite {
			_ = d.putSetting(ctx, keyUnifiOtherPrefix+row.Name, rec.Value)
			return "", fmt.Errorf("An der UDM Pro steht für %s schon ein Eintrag auf %s, den nicht das PDH angelegt hat. Nichts geändert. Zum Ersetzen in der Zeile „Überschreiben“ drücken", row.Name, rec.Value)
		}
		if err := c.Update(ctx, rec.ID, row.Name, ip); err != nil {
			return "", err
		}
		lg.Info().Str("name", row.Name).Str("adresse", ip).Str("alt", rec.Value).Str("kennung", rec.ID).Msg("fremden eintrag im haus überschrieben (nicht übernommen)")
		certLANClearMarks(ctx, d.putSetting, row.Name)
		return row.Name + " zeigt im Haus jetzt auf " + ip + " (vorhandener Eintrag geändert, vorher " + rec.Value +
			"). Der Eintrag bleibt beim Entfernen des Namens an der UDM Pro stehen, weil das PDH ihn nicht angelegt hat.", nil
	}
	id, err := c.Create(ctx, row.Name, ip)
	if err != nil {
		return "", err
	}
	lg.Info().Str("name", row.Name).Str("adresse", ip).Str("kennung", id).Msg("eintrag im haus angelegt")
	if err := d.store.certSetLANRecord(ctx, row.Name, id); err != nil {
		lg.Warn().Str("name", row.Name).Str("kennung", id).Str("fehler", certSafeText(err.Error())).Msg("kennung des eintrags im haus nicht gemerkt")
		return "", fmt.Errorf("Eintrag an der UDM Pro angelegt (Kennung %s), aber nicht gemerkt: %s. Beim Entfernen des Namens bitte von Hand löschen", id, certSafeText(err.Error()))
	}
	certLANClearMarks(ctx, d.putSetting, row.Name)
	return row.Name + " zeigt im Haus jetzt auf " + ip + " (Eintrag an der UDM Pro angelegt).", nil
}

// certLANRemove löscht beim Entfernen eines Namens nur den Eintrag, den das PDH selbst
// angelegt hat (gemerkte Kennung). Scheitert das, bleibt das Zertifikat trotzdem entfernt;
// die Meldung nennt die Kennung zum Löschen von Hand.
func certLANRemove(ctx context.Context, d certActDeps, row certRow) string {
	_ = d.putSetting(ctx, keyUnifiHintPrefix+row.Name, "")
	_ = d.putSetting(ctx, keyUnifiOtherPrefix+row.Name, "")
	id := row.LANRecordID
	if id == "" {
		return ""
	}
	lg := componentLog("zertifikate")
	manual := func(why string) string {
		lg.Warn().Str("name", row.Name).Str("kennung", id).Str("fehler", why).Msg("eintrag im haus nicht gelöscht")
		return " Der Eintrag im Haus an der UDM Pro (Kennung " + id + ") ist nicht gelöscht: " + why + ". Bitte dort von Hand löschen."
	}
	if d.lan == nil {
		return manual(errUnifiOff.Error())
	}
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	c, err := d.lan(lctx)
	if err != nil {
		return manual(certSafeText(err.Error()))
	}
	if err := c.Delete(lctx, id); err != nil {
		return manual(certSafeText(err.Error()))
	}
	lg.Info().Str("name", row.Name).Str("kennung", id).Msg("eintrag im haus gelöscht")
	return " Der Eintrag im Haus an der UDM Pro ist gelöscht."
}

// certLANDaily: nur lesen und Abweichungen als Hinweis merken – nie ändern, nie Alarm.
func certLANDaily(ctx context.Context, d certDeps, rows []certRow) {
	if d.lanCheck == nil || len(rows) == 0 {
		return
	}
	lg := componentLog("zertifikate")
	c, ip, err := d.lanCheck(ctx)
	if errors.Is(err, errUnifiOff) {
		return
	}
	if err != nil {
		lg.Warn().Str("fehler", certSafeText(err.Error())).Msg("namen im haus nicht geprüft")
		return
	}
	recs, err := c.Records(ctx)
	if err != nil {
		lg.Warn().Str("fehler", certSafeText(err.Error())).Msg("namen im haus nicht geprüft")
		return
	}
	for _, r := range rows {
		action, other := certLANPlan(recs, r.Name, ip, r.LANRecordID)
		hint, oth := "", ""
		switch action {
		case "anlegen":
			if r.LANRecordID != "" {
				hint = "Der Eintrag im Haus, den das PDH angelegt hatte, fehlt an der UDM Pro. „Im Haus eintragen“ drücken."
			} else {
				hint = "An der UDM Pro fehlt der Eintrag im Haus auf " + ip + "."
			}
		case "eigenen-aendern":
			hint = "Der Eintrag im Haus an der UDM Pro zeigt nicht auf " + ip + ". „Im Haus eintragen“ drücken."
		case "fremd":
			oth = other
		}
		_ = d.setSetting(ctx, keyUnifiHintPrefix+r.Name, hint)
		_ = d.setSetting(ctx, keyUnifiOtherPrefix+r.Name, oth)
		if hint != "" || oth != "" {
			lg.Info().Str("name", r.Name).Str("adresse", ip).Str("plan", action).Str("vorhanden", other).Msg("eintrag im haus weicht ab")
		}
	}
}

// unifiResult: was die Einstellungs-Aktionen der Karte zusätzlich zeigen.
type unifiResult struct {
	Seen string // gesehener Fingerabdruck (zur Bestätigung)
	Warn string // z. B. UDM Pro gerade nicht erreichbar
}

func certUnifiSite(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) > 64 || strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || r == '/' || r == '\\' }) >= 0 {
		return "", errors.New("Standort: bitte den Namen des Standorts aus UniFi Network eintragen (oder leer lassen)")
	}
	return s, nil
}

// certDoUnifiSettings speichert Adresse, Standort und – nur wenn neu eingegeben – den
// Schlüssel (verschlüsselt). Eine geänderte Adresse löscht den bestätigten Fingerabdruck.
// Danach liest es den Fingerabdruck des Geräts (nur TLS, ohne Schlüssel) zur Bestätigung.
// Eine leere Adresse entfernt die Anbindung.
func certDoUnifiSettings(ctx context.Context, d certActDeps, rawURL, key, site string) (unifiResult, string, error) {
	var res unifiResult
	if d.blocked() {
		return res, "", errCertBlocked
	}
	if strings.TrimSpace(rawURL) == "" {
		for _, k := range []string{keyUnifiURL, keyUnifiKey, keyUnifiSite, keyUnifiPin} {
			if err := d.putSetting(ctx, k, ""); err != nil {
				return res, "", err
			}
		}
		return res, "UniFi-Anbindung entfernt. Vorhandene Einträge an der UDM Pro bleiben, wie sie sind.", nil
	}
	base, err := unifi.ValidateBase(rawURL)
	if err != nil {
		return res, "", err
	}
	site, err = certUnifiSite(site)
	if err != nil {
		return res, "", err
	}
	key = strings.TrimSpace(key)
	if len(key) > 1024 || strings.IndexFunc(key, unicode.IsControl) >= 0 {
		return res, "", errors.New("API-Schlüssel: bitte den Schlüssel aus UniFi Network unverändert einfügen")
	}
	if key == "" && d.setting(ctx, keyUnifiKey) == "" {
		return res, "", errors.New("Bitte den API-Schlüssel aus UniFi Network eintragen")
	}
	if key != "" {
		if d.seal == nil {
			return res, "", errors.New("Schlüssel lässt sich nicht verschlüsseln")
		}
		enc, err := d.seal(key)
		if err != nil {
			return res, "", errors.New("Schlüssel lässt sich nicht verschlüsseln")
		}
		if err := d.putSetting(ctx, keyUnifiKey, enc); err != nil {
			return res, "", err
		}
	}
	urlChanged := d.setting(ctx, keyUnifiURL) != base
	if err := d.putSetting(ctx, keyUnifiURL, base); err != nil {
		return res, "", err
	}
	if err := d.putSetting(ctx, keyUnifiSite, site); err != nil {
		return res, "", err
	}
	if urlChanged {
		if err := d.putSetting(ctx, keyUnifiPin, ""); err != nil {
			return res, "", err
		}
	}
	what := "UniFi-Einstellungen gespeichert (" + base
	if site != "" {
		what += ", Standort " + site
	}
	what += ")."
	if key != "" {
		what += " Neuer API-Schlüssel hinterlegt."
	}
	if d.fingerprint == nil {
		return res, what, nil
	}
	fp, err := d.fingerprint(ctx, base)
	if err != nil {
		res.Warn = "Die UDM Pro antwortet gerade nicht: " + certSafeText(err.Error()) + ". Adresse prüfen und erneut speichern."
		return res, what, nil
	}
	if fp == unifi.NormalizePin(d.setting(ctx, keyUnifiPin)) {
		return res, what + " Fingerabdruck unverändert.", nil
	}
	res.Seen = unifi.FormatPin(fp)
	return res, what + " Jetzt den Fingerabdruck prüfen und bestätigen.", nil
}

// certDoUnifiConfirm speichert den Fingerabdruck nur, wenn das Gerät genau diesen zeigt.
func certDoUnifiConfirm(ctx context.Context, d certActDeps, posted string) (unifiResult, string, error) {
	var res unifiResult
	if d.blocked() {
		return res, "", errCertBlocked
	}
	base := d.setting(ctx, keyUnifiURL)
	if base == "" {
		return res, "", errors.New("Zuerst die Adresse der UDM Pro speichern")
	}
	want := unifi.NormalizePin(posted)
	if len(want) != 64 || strings.Trim(want, "0123456789abcdef") != "" {
		return res, "", errors.New("Fingerabdruck unvollständig – bitte erneut speichern und bestätigen")
	}
	if d.fingerprint == nil {
		return res, "", errUnifiOff
	}
	fp, err := d.fingerprint(ctx, base)
	if err != nil {
		return res, "", err
	}
	if fp != want {
		res.Seen = unifi.FormatPin(fp)
		return res, "", errors.New("Die UDM Pro zeigt inzwischen einen anderen Fingerabdruck. Nicht bestätigt – bitte den neu angezeigten prüfen")
	}
	if err := d.putSetting(ctx, keyUnifiPin, fp); err != nil {
		return res, "", err
	}
	componentLog("zertifikate").Info().Str("udm", base).Str("fingerabdruck", fp).Msg("fingerabdruck der udm pro bestätigt")
	return res, "Fingerabdruck der UDM Pro bestätigt (" + unifi.FormatPin(fp) + "). Jetzt je Name „Im Haus eintragen“ drücken.", nil
}

// ── Betrieb ──────────────────────────────────────────────────

// unifiClient: nur mit gespeicherter Adresse und Schlüssel; sonst errUnifiOff ohne Anfrage.
func (h *Handler) unifiClient(ctx context.Context) (certLANClient, error) {
	base, enc := h.certSetting(ctx, keyUnifiURL), h.certSetting(ctx, keyUnifiKey)
	if base == "" || enc == "" {
		return nil, errUnifiOff
	}
	key, err := decryptSecret(h.pkiKey(), enc)
	if err != nil || key == "" {
		return nil, errors.New("Der UniFi-Schlüssel ist nicht lesbar – bitte unter „Namen im Haus (UniFi)“ neu eintragen")
	}
	pin := h.certSetting(ctx, keyUnifiPin)
	if pin == "" {
		return nil, unifi.ErrNoPin
	}
	return unifi.New(base, key, h.certSetting(ctx, keyUnifiSite), pin), nil
}

// certAddrs: eigene Adressen (ohne Loopback, Link-Local) und die mitwandernde des Verbunds.
func (h *Handler) certAddrs(ctx context.Context) (own []string, vip string) {
	seen := map[string]bool{}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				ip := n.IP
				if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || seen[ip.String()] {
					continue
				}
				seen[ip.String()] = true
				own = append(own, ip.String())
			}
		}
	}
	// Verbund (pdh-ha) ist optional; nicht erreichbar heißt einfach: keine Zusatzadresse
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if st, err := h.haStatusFetch(cctx); err == nil && st.Configured && st.Access.VIPOn && st.Access.VIP != "" {
		if ip := net.ParseIP(vipAddr(st.Access.VIP)); ip != nil {
			vip = ip.String()
		}
	}
	return own, vip
}

// certLANTargetFrom: mitwandernde Adresse, ohne Verbund die erste eigene IPv4-Adresse
// (private bevorzugt). Nie eine Adresse aus der Anfrage.
func certLANTargetFrom(own []string, vip string) string {
	if ip := net.ParseIP(vip); ip != nil && ip.To4() != nil {
		return ip.String()
	}
	first := ""
	for _, o := range own {
		ip := net.ParseIP(o)
		if ip == nil || ip.To4() == nil {
			continue
		}
		if ip.IsPrivate() {
			return ip.String()
		}
		if first == "" {
			first = ip.String()
		}
	}
	return first
}

func (h *Handler) certLANTarget(ctx context.Context) (string, error) {
	own, vip := h.certAddrs(ctx)
	if t := certLANTargetFrom(own, vip); t != "" {
		return t, nil
	}
	return "", errors.New("Keine IPv4-Adresse des Servers gefunden – den Eintrag im Haus bitte von Hand anlegen")
}

// certLANCheckLive: für die tägliche Prüfung; ohne Einrichtung errUnifiOff ohne Anfrage.
func (h *Handler) certLANCheckLive(ctx context.Context) (certLANClient, string, error) {
	c, err := h.unifiClient(ctx)
	if err != nil {
		return nil, "", err
	}
	ip, err := h.certLANTarget(ctx)
	if err != nil {
		return nil, "", err
	}
	return c, ip, nil
}

// ── HTTP ─────────────────────────────────────────────────────

func (h *Handler) unifiAct(w http.ResponseWriter, r *http.Request, what string,
	do func(ctx context.Context, d certActDeps) (unifiResult, string, error)) {
	var res unifiResult
	h.certActWith(w, r, h.hasPerm(r, permUnifi), what, true, func(ctx context.Context, d certActDeps) (string, error) {
		var msg string
		var err error
		res, msg, err = do(ctx, d)
		return msg, err
	}, func(p *certPanel) {
		p.UnifiOpen = true
		p.Unifi.SeenPin = res.Seen
		if res.Warn != "" {
			p.Unifi.Err = res.Warn
		}
	})
}

// CertUnifiWeb: POST /admin/server-config/certs/unifi (url, key, site)
func (h *Handler) CertUnifiWeb(w http.ResponseWriter, r *http.Request) {
	h.unifiAct(w, r, "unifi-einstellungen", func(ctx context.Context, d certActDeps) (unifiResult, string, error) {
		return certDoUnifiSettings(ctx, d, r.FormValue("url"), r.FormValue("key"), r.FormValue("site"))
	})
}

// CertUnifiConfirmWeb: POST /admin/server-config/certs/unifi/confirm (fingerprint)
func (h *Handler) CertUnifiConfirmWeb(w http.ResponseWriter, r *http.Request) {
	h.unifiAct(w, r, "unifi-fingerabdruck", func(ctx context.Context, d certActDeps) (unifiResult, string, error) {
		return certDoUnifiConfirm(ctx, d, r.FormValue("fingerprint"))
	})
}

// CertLANWeb: POST /admin/server-config/certs/lan (name, overwrite=1)
func (h *Handler) CertLANWeb(w http.ResponseWriter, r *http.Request) {
	h.certAct(w, r, h.hasPerm(r, permUnifi), "im-haus-eintragen", true, func(ctx context.Context, d certActDeps) (string, error) {
		return certDoLAN(ctx, d, r.FormValue("name"), r.FormValue("overwrite") == "1")
	})
}
