package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"os"
	"strings"
	"time"

	"pdh/internal/integrations/unifi"
)

// Karte „Zertifikate (Let's Encrypt)“ unter der Cloudflare-Karte (optional).
//
//	GET  /admin/server-config/certs/panel     Karte neu zeichnen (htmx, alle 5 s solange ein Auftrag läuft)
//	POST /admin/server-config/certs/settings  E-Mail für Let's Encrypt, Testumgebung
//	POST /admin/server-config/certs/add       Namen eintragen
//	POST /admin/server-config/certs/issue     Zertifikat jetzt holen bzw. erneuern (im Hintergrund)
//	POST /admin/server-config/certs/remove    Namen entfernen und Zertifikat widerrufen (Bereichsadministrator)
//	POST /admin/server-config/certs/strict    Tunnel und Domain auf Full (strict) bzw. zurück (dazu system.cloudflare)
//	POST /admin/server-config/certs/lan            Namen im Haus über die UniFi UDM Pro (system.unifi, certs_lan.go;
//	                                               die Anbindung selbst steht im Reiter „UniFi“: …/unifi, …/unifi/confirm)
//
// Die Karte lädt nur auf den Knopf „Zertifikate laden“ im Reiter Cloudflare-Tunnel – unabhängig
// vom API-Token und vom Recht system.cloudflare. Ohne Cloudflare, Update-Agent oder nginx zeigt
// sie einen Hinweis; eingetragene Namen lassen sich auch ohne API-Token entfernen.

type certLine struct {
	Name, State, Step, Error, ValidUntil, LAN string
	Staging, CanRemove                        bool

	LANRecord string // Kennung des Eintrags, den das PDH an der UDM Pro angelegt hat
	LANOther  string // Adresse eines fremden Eintrags gleichen Namens an der UDM Pro
	LANHint   string // Abweichung aus der täglichen Prüfung
	LAN6Hint  string // Hinweis des IPv6-Laufs (fremder AAAA-Eintrag, Fehler)
	LAN6Other string // Adresse eines fremden AAAA-Eintrags (Knopf „IPv6 überschreiben“)
	Note      string // Hinweis des Update-Agenten: Dateien abgelegt, nginx-Seite von Hand
}

type certPanel struct {
	CanManage, AgentOK, NginxOK, Staging, StrictReady, StrictOn bool
	Email, Msg, Err                                             string
	Lines                                                       []certLine
	Suggested                                                   []string

	CanStrict bool   // system.cloudflare: Knopf „Full (strict)“
	Zone      string // Domain des API-Tokens
	Hint      string // Cloudflare nicht eingerichtet – keine Formulare

	CanUnifi  bool       // system.unifi: Knopf „Im Haus eintragen“, Verweis auf den Reiter „UniFi“
	Unifi     unifiPanel // nur Einstellungen, nie der Schlüssel
	LANTarget string     // Zieladresse im Haus (mitwandernd bzw. erste eigene)
	LAN6      string     // mitwandernde IPv6-Adresse für die Liste von Hand ("" = kein IPv6)
	// Hinweise des IPv6-Laufs zu Namen an der UDM Pro, die kein Zertifikat haben (nur aus den
	// Einstellungen – das Zeichnen fragt nie die UDM Pro).
	Extra6 []lan6Extra
}

// Running: läuft ein Auftrag? Dann fragt die Karte alle 5 Sekunden nach.
func (p certPanel) Running() bool {
	for _, l := range p.Lines {
		if l.State == certStateLaeuft {
			return true
		}
	}
	return false
}

// certLines: Zeilen der Karte. lan: Name → "direkt" | "cloudflare" | "unbekannt".
func certLines(rows []certRow, lan map[string]string, canRemove bool) []certLine {
	out := make([]certLine, 0, len(rows))
	for _, r := range rows {
		l := certLine{Name: r.Name, State: r.State, Step: r.Step, Error: certSafeText(r.LastError),
			LAN: lan[r.Name], Staging: r.Staging, CanRemove: canRemove, LANRecord: r.LANRecordID}
		if l.LAN == "" {
			l.LAN = "unbekannt"
		}
		if !r.NotAfter.IsZero() {
			l.ValidUntil = r.NotAfter.In(time.Local).Format("02.01.2006")
		}
		out = append(out, l)
	}
	return out
}

func certStagingOn(v string) bool { return v == "1" || v == "true" || v == "on" }

// certMayRemove: Bereichsadministrator der Zertifikate (system.certs_admin).
func (h *Handler) certMayRemove(r *http.Request) bool {
	if h.rbac == nil { // nur in Tests mit hasPermHook
		return h.hasPermHook != nil && h.hasPerm(r, permCertsAdmin)
	}
	return h.isAreaAdmin(r, "certs", "")
}

// certCanManage: Karte sehen, Einstellungen, Namen eintragen, holen – mit system.certs oder
// als Bereichsadministrator der Zertifikate (system.certs_admin allein genügt).
func (h *Handler) certCanManage(r *http.Request) bool {
	return h.hasPerm(r, permCerts) || h.certMayRemove(r)
}

// certPanelFor baut die Karte. Fragt nur die Datenbank, den Update-Agenten (kurz) und das
// DNS des Servers – nie Let's Encrypt oder Cloudflare. Ein Fehler hier bricht nie die
// Cloudflare-Karte.
func (h *Handler) certPanelFor(ctx context.Context, r *http.Request, msg string, perr error) (p certPanel) {
	p = certPanel{CanManage: h.certCanManage(r), Msg: msg}
	if perr != nil {
		p.Err = certSafeText(perr.Error())
	}
	if !p.CanManage {
		return p
	}
	defer func() {
		if rec := recover(); rec != nil {
			componentLog("zertifikate").Error().Str("panik", certSafeText(fmt.Sprint(rec))).Msg("zertifikats-karte")
			p.Err = "Die Zertifikate lassen sich gerade nicht anzeigen – siehe Server-Protokoll"
		}
	}()
	p.CanStrict = h.hasPerm(r, permCloudflare)
	if a, err := h.cfAccess(ctx); err != nil {
		p.Hint = err.Error()
	} else {
		p.Zone = a.zone
	}
	var rows []certRow
	if h.db != nil {
		var err error
		if rows, err = h.certList(ctx); err != nil && p.Err == "" {
			p.Err = "Zertifikate lesen: " + certSafeText(err.Error())
		}
	}
	p.Email = h.certSetting(ctx, keyCertEmail)
	p.Staging = certStagingOn(h.certSetting(ctx, keyCertStaging))
	p.StrictOn = h.certSetting(ctx, keyCertStrictBefore) != ""
	p.StrictReady = certStrictReady(rows, time.Now())
	p.AgentOK, p.NginxOK = h.certAgentState(ctx)
	// Status des Verbunds nur einmal je Zeichnen abrufen – und nur mit eingetragenen Namen.
	var st haStatus
	stErr := errors.New("keine Namen")
	if len(rows) > 0 {
		st, stErr = h.certHAStatus(ctx)
	}
	lan, target := h.certLAN(ctx, rows, st, stErr)
	p.Lines = certLines(rows, lan, h.certMayRemove(r))
	p.LANTarget = target
	p.CanUnifi = h.hasPerm(r, permUnifi)
	p.Unifi = unifiPanelFrom(func(k string) string { return h.certSetting(ctx, k) }, "", "")
	for i := range p.Lines {
		p.Lines[i].LANHint = h.certSetting(ctx, keyUnifiHintPrefix+p.Lines[i].Name)
		p.Lines[i].LANOther = h.certSetting(ctx, keyUnifiOtherPrefix+p.Lines[i].Name)
		p.Lines[i].Note = h.certSetting(ctx, keyCertNotePrefix+p.Lines[i].Name)
		p.Lines[i].LAN6Hint = h.certSetting(ctx, keyUnifiHint6Prefix+certLANNorm(p.Lines[i].Name))
		p.Lines[i].LAN6Other = h.certSetting(ctx, keyUnifiOther6Prefix+certLANNorm(p.Lines[i].Name))
	}
	if len(rows) > 0 {
		if t := lan6TargetFetched(st, stErr); t.Wanted {
			p.LAN6 = t.V6
		}
	}
	p.Extra6 = certLAN6Extra(h.certSettingsPrefix(ctx, keyUnifiHint6Prefix), h.certSettingsPrefix(ctx, keyUnifiOther6Prefix), rows)
	if p.Zone != "" {
		host, _ := h.nextStartValue(ctx, cfHostKey)
		have := map[string]bool{}
		for _, row := range rows {
			have[row.Name] = true
		}
		for _, n := range certSuggested(host, os.Getenv("PDH_NEXTCLOUD_BASEURL")) {
			if _, err := certNameInZone(n, p.Zone); err == nil && !have[n] {
				p.Suggested = append(p.Suggested, n)
			}
		}
	}
	return p
}

// certAgentState: Update-Agent erreichbar und nginx vorhanden (systemd-Betrieb)?
func (h *Handler) certAgentState(ctx context.Context) (agentOK, nginxOK bool) {
	if !h.driveAgentConfigured() {
		return false, false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out struct {
		Nginx bool `json:"nginx"`
	}
	if err := h.agentJSON(ctx, http.MethodGet, "/v1/certs/status", nil, &out); err != nil {
		return false, false
	}
	return true, out.Nginx
}

// certLAN: Wohin zeigt jeder Name im Firmennetz (DNS des Servers)? Dazu die Zieladresse im
// Haus (mitwandernde bzw. erste eigene) für die Anleitung von Hand.
// st/stErr: Status des Verbunds, einmal je Zeichnen abgerufen (certHAStatus).
func (h *Handler) certLAN(ctx context.Context, rows []certRow, st haStatus, stErr error) (map[string]string, string) {
	out := map[string]string{}
	if len(rows) == 0 {
		return out, ""
	}
	iface, vip := certOwnAddrs(), certVIPFrom(st, stErr)
	own := append([]string(nil), iface...)
	if vip != "" {
		own = append(own, vip)
	}
	for _, r := range rows {
		lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		addrs, _ := net.DefaultResolver.LookupHost(lctx, r.Name)
		cancel()
		out[r.Name] = certLANState(addrs, own)
	}
	return out, certLANTargetFrom(iface, vip)
}

// ── Aktionen (ohne HTTP, testbar) ────────────────────────────

// certActDeps: was die Knöpfe brauchen; im Betrieb aus certActLive.
type certActDeps struct {
	store       certStore
	zone        func(ctx context.Context) (string, error)
	setting     func(ctx context.Context, key string) string
	putSetting  func(ctx context.Context, key, value string) error
	blocked     func() bool
	startIssue  func(ctx context.Context, name string) error
	agentRemove func(ctx context.Context, row certRow) error
	revoke      func(ctx context.Context, row certRow) error
	strictOn    func(ctx context.Context) error
	strictOff   func(ctx context.Context) error

	// Namen im Haus (UniFi, optional): nil oder errUnifiOff = nicht eingerichtet, keine Anfrage.
	lan         func(ctx context.Context) (certLANClient, error)
	lanTarget   func(ctx context.Context) (string, error)
	lan6        func(ctx context.Context) lan6Target // IPv6 für „Überschreiben“; nil = ohne IPv6
	fingerprint func(ctx context.Context, base string) (string, error) // nur TLS, ohne Schlüssel
	seal        func(plain string) (string, error)                     // Schlüssel verschlüsseln
}

var (
	errCertBlocked = errors.New("Am Spiegelserver und während der Wartung lassen sich Zertifikate nicht ändern")
	errCertNoAgent = errors.New("Der Update-Agent ist nicht eingerichtet")
)

// certCheckName: Name in der Domain des API-Tokens und eingetragen.
func certCheckName(ctx context.Context, d certActDeps, raw string) (certRow, error) {
	zone, err := d.zone(ctx)
	if err != nil {
		return certRow{}, err
	}
	name, err := certNameInZone(raw, zone)
	if err != nil {
		return certRow{}, err
	}
	rows, err := d.store.certList(ctx)
	if err != nil {
		return certRow{}, err
	}
	for _, r := range rows {
		if r.Name == name {
			return r, nil
		}
	}
	return certRow{}, fmt.Errorf("%s ist nicht eingetragen", name)
}

// certFindRow: eingetragener Name, ohne Prüfung der Domain – Entfernen geht auch ohne
// Cloudflare-Zugang (Token gelöscht oder abgelaufen).
func certFindRow(ctx context.Context, d certActDeps, raw string) (certRow, error) {
	name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if name == "" {
		return certRow{}, errors.New("Kein Name angegeben")
	}
	rows, err := d.store.certList(ctx)
	if err != nil {
		return certRow{}, err
	}
	for _, r := range rows {
		if r.Name == name {
			return r, nil
		}
	}
	return certRow{}, fmt.Errorf("%s ist nicht eingetragen", name)
}

func certDoSettings(ctx context.Context, d certActDeps, email string, staging bool) (string, error) {
	if d.blocked() {
		return "", errCertBlocked
	}
	email = strings.TrimSpace(email)
	if email != "" {
		a, err := mail.ParseAddress(email)
		if err != nil || a.Address != email || len(email) > 254 {
			return "", errors.New("E-Mail: bitte eine gültige Adresse eingeben, z. B. it@firma.de")
		}
	}
	st := ""
	if staging {
		st = "1"
	}
	if err := d.putSetting(ctx, keyCertEmail, email); err != nil {
		return "", err
	}
	if err := d.putSetting(ctx, keyCertStaging, st); err != nil {
		return "", err
	}
	what := "echte Umgebung"
	if staging {
		what = "Testumgebung"
	}
	return "Einstellungen gespeichert (" + what + "). Sie gelten für neu eingetragene Namen.", nil
}

func certDoAdd(ctx context.Context, d certActDeps, raw string) (string, error) {
	if d.blocked() {
		return "", errCertBlocked
	}
	zone, err := d.zone(ctx)
	if err != nil {
		return "", err
	}
	name, err := certNameInZone(raw, zone)
	if err != nil {
		return "", err
	}
	rows, err := d.store.certList(ctx)
	if err != nil {
		return "", err
	}
	for _, r := range rows {
		if r.Name == name {
			return "", fmt.Errorf("%s ist schon eingetragen", name)
		}
	}
	staging := certStagingOn(d.setting(ctx, keyCertStaging))
	if err := d.store.certAdd(ctx, name, staging); err != nil {
		return "", err
	}
	if staging {
		return name + " eingetragen (Testumgebung). Jetzt „Holen“ drücken.", nil
	}
	return name + " eingetragen. Jetzt „Holen“ drücken.", nil
}

func certDoIssue(ctx context.Context, d certActDeps, raw string) (string, error) {
	if d.blocked() {
		return "", errCertBlocked
	}
	row, err := certCheckName(ctx, d, raw)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(d.setting(ctx, keyCertEmail)) == "" {
		return "", errors.New("Zuerst oben die E-Mail für Let's Encrypt eintragen und speichern")
	}
	if row.State == certStateLaeuft {
		return errCertBusy.Error() + ".", nil
	}
	if err := d.startIssue(ctx, row.Name); err != nil {
		if errors.Is(err, errCertBusy) {
			return errCertBusy.Error() + ".", nil
		}
		return "", err
	}
	return "Zertifikat für " + row.Name + " wird geholt. Der Stand erneuert sich hier von selbst.", nil
}

// certDoRemove: erst der Agent (Seite bzw. Zeilen zurück, Dateien weg), dann Widerruf bei
// Let's Encrypt, dann der Eintrag. Lehnt der Agent ab, bleibt alles, wie es ist. Braucht
// keinen Cloudflare-Zugang; scheitert der Widerruf, wird trotzdem entfernt.
func certDoRemove(ctx context.Context, d certActDeps, raw string) (string, error) {
	if d.blocked() {
		return "", errCertBlocked
	}
	row, err := certFindRow(ctx, d, raw)
	if err != nil {
		return "", err
	}
	if row.State == certStateLaeuft {
		return "", errors.New(errCertBusy.Error() + " – erst danach entfernen")
	}
	if d.setting(ctx, keyCertStrictBefore) != "" {
		return "", errors.New("Full (strict) ist eingeschaltet. Zuerst „Zurückstellen“, dann den Namen entfernen")
	}
	revokeNote := ""
	if row.ChainPEM != "" {
		if err := d.agentRemove(ctx, row); err != nil {
			// Ohne eingerichteten Agenten gibt es keine Dateien, solange nie etwas eingerichtet war.
			if !(errors.Is(err, errCertNoAgent) && row.InstalledFP == "") {
				return "", fmt.Errorf("Nicht entfernt – der Update-Agent meldet: %s", certSafeText(err.Error()))
			}
		}
		if err := d.revoke(ctx, row); err != nil {
			componentLog("zertifikate").Warn().Str("name", row.Name).Str("fehler", certSafeText(err.Error())).Msg("widerruf gescheitert")
			revokeNote = " Der Widerruf bei Let's Encrypt ist gescheitert (" + certSafeText(err.Error()) + "); das Zertifikat läuft einfach ab."
		} else {
			revokeNote = " Das Zertifikat ist bei Let's Encrypt widerrufen."
		}
	}
	if err := d.store.certDelete(ctx, row.Name); err != nil {
		return "", err
	}
	_ = d.putSetting(ctx, keyCertPrevPrefix+row.Name, "")
	_ = d.putSetting(ctx, keyCertAlarmPrefix+row.Name, "")
	_ = d.putSetting(ctx, keyCertNotePrefix+row.Name, "")
	_ = d.putSetting(ctx, keyCertPendingPrefix+row.Name, "")
	// Eintrag im Haus: nur den eigenen löschen; scheitert das, bleibt das Zertifikat entfernt.
	lanNote := certLANRemove(ctx, d, row)
	return row.Name + " entfernt." + revokeNote + lanNote, nil
}

func certDoStrict(ctx context.Context, d certActDeps, on bool) (string, error) {
	if d.blocked() {
		return "", errCertBlocked
	}
	if on {
		if err := d.strictOn(ctx); err != nil {
			return "", err
		}
		return "Tunnel und Domain sind auf Full (strict) umgestellt.", nil
	}
	if err := d.strictOff(ctx); err != nil {
		return "", err
	}
	return "Tunnel und Domain sind auf den früheren Stand zurückgestellt.", nil
}

// ── Betrieb ──────────────────────────────────────────────────

func (h *Handler) certActLive() certActDeps {
	return certActDeps{
		store: h,
		zone: func(ctx context.Context) (string, error) {
			a, err := h.cfAccess(ctx)
			if err != nil {
				return "", err
			}
			return a.zone, nil
		},
		setting:    h.certSetting,
		putSetting: h.certPutSetting,
		blocked:    func() bool { return h.Mirror().On || h.MaintLocked() },
		startIssue: h.certStartIssue,
		agentRemove: func(ctx context.Context, row certRow) error {
			if !h.driveAgentConfigured() {
				return errCertNoAgent
			}
			ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			host, _ := h.nextStartValue(ctx, cfHostKey)
			c, k, ok := h.certPrevious(ctx, row.Name)
			body := certRemoveBody(row.Name, host, os.Getenv("PDH_NEXTCLOUD_BASEURL"), c, k, ok)
			return h.agentJSON(ctx, http.MethodPost, "/v1/certs/remove", body, nil)
		},
		revoke: func(ctx context.Context, row certRow) error {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			ca, err := h.certDepsLive().ca(row.Staging)
			if err != nil {
				return err
			}
			return ca.Revoke(ctx, []byte(row.ChainPEM))
		},
		strictOn:  h.certStrictOn,
		strictOff: h.certStrictOff,
		lan:       h.unifiClient,
		lanTarget: h.certLANTarget,
		lan6:      h.certLAN6Target,
		fingerprint: func(ctx context.Context, base string) (string, error) {
			return unifi.Fingerprint(ctx, base)
		},
		seal: func(plain string) (string, error) { return encryptSecret(h.pkiKey(), plain) },
	}
}

// certStartIssue startet den Auftrag im Hintergrund mit eigenem Kontext. Scheitert er sofort
// (läuft schon, Sperrzeit), kommt der Fehler zurück; sonst nil, sobald er angelaufen ist.
func (h *Handler) certStartIssue(ctx context.Context, name string) error {
	bg := context.WithoutCancel(ctx)
	early := make(chan error, 1)
	go func() {
		jctx, cancel := context.WithTimeout(bg, 15*time.Minute)
		defer cancel()
		err := certIssue(jctx, h.certDepsLive(), name)
		select {
		case early <- err:
		default:
		}
		lg := componentLog("zertifikate")
		switch {
		case errors.Is(err, errCertBusy):
		case err != nil:
			lg.Warn().Str("name", name).Str("fehler", certSafeText(err.Error())).Msg("zertifikat nicht geholt")
			h.notifyAdmins(bg, "🔒 **Zertifikat für "+name+" nicht geholt:** "+certSafeText(err.Error()))
		default:
			h.notifyAdmins(bg, "🔒 **Zertifikat für "+name+" geholt und eingerichtet.**")
		}
	}()
	// Kurz warten: Was sofort scheitert, sieht der Anwender gleich in der Karte.
	select {
	case err := <-early:
		return err
	case <-time.After(1500 * time.Millisecond):
		return nil // das spätere Ergebnis steht in der Karte (last_error) und im Protokoll
	}
}

// ── HTTP ─────────────────────────────────────────────────────

func (h *Handler) certRender(w http.ResponseWriter, p certPanel) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	t, err := h.tmpl.Clone()
	if err == nil {
		err = t.ExecuteTemplate(w, "cert-panel", p)
	}
	if err != nil {
		fmt.Fprintf(w, `<div id="cert-panel" class="sc-sub" style="color:var(--red)">Zertifikate: %s</div>`, esc(err.Error()))
	}
}

// certAct: Recht prüfen (may: das Recht dieser Aktion), Aktion ausführen, protokollieren,
// melden, Karte neu zeichnen.
func (h *Handler) certAct(w http.ResponseWriter, r *http.Request, may bool, what string, notify bool,
	do func(ctx context.Context, d certActDeps) (string, error)) {
	h.certActWith(w, r, may, what, notify, do, nil)
}

// certActWith: wie certAct; after ergänzt die Karte vor dem Zeichnen (z. B. Fingerabdruck).
func (h *Handler) certActWith(w http.ResponseWriter, r *http.Request, may bool, what string, notify bool,
	do func(ctx context.Context, d certActDeps) (string, error), after func(p *certPanel)) {
	if !may {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	msg, err := do(ctx, h.certActLive())
	uid := ""
	if u := getUser(r); u != nil {
		uid = u.ID
	}
	lg := componentLog("zertifikate")
	if err != nil {
		lg.Warn().Str("von", uid).Str("aktion", what).Str("fehler", certSafeText(err.Error())).Msg("zertifikate")
	} else {
		lg.Info().Str("von", uid).Str("aktion", what).Str("meldung", msg).Msg("zertifikate")
		if notify && h.db != nil {
			h.notifyAdmins(ctx, "🔒 **Zertifikate geändert** von "+h.chatUserName(ctx, uid)+": "+msg)
		}
	}
	p := h.certPanelFor(ctx, r, msg, err)
	if after != nil {
		after(&p)
	}
	h.certRender(w, p)
}

// CertPanelWeb: GET /admin/server-config/certs/panel
func (h *Handler) CertPanelWeb(w http.ResponseWriter, r *http.Request) {
	if !h.certCanManage(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	h.certRender(w, h.certPanelFor(ctx, r, "", nil))
}

// CertSettingsWeb: POST /admin/server-config/certs/settings (email, staging)
func (h *Handler) CertSettingsWeb(w http.ResponseWriter, r *http.Request) {
	h.certAct(w, r, h.certCanManage(r), "einstellungen", true, func(ctx context.Context, d certActDeps) (string, error) {
		return certDoSettings(ctx, d, r.FormValue("email"), r.FormValue("staging") == "on")
	})
}

// CertAddWeb: POST /admin/server-config/certs/add (name)
func (h *Handler) CertAddWeb(w http.ResponseWriter, r *http.Request) {
	h.certAct(w, r, h.certCanManage(r), "eintragen", true, func(ctx context.Context, d certActDeps) (string, error) {
		return certDoAdd(ctx, d, r.FormValue("name"))
	})
}

// CertIssueWeb: POST /admin/server-config/certs/issue (name)
func (h *Handler) CertIssueWeb(w http.ResponseWriter, r *http.Request) {
	h.certAct(w, r, h.certCanManage(r), "holen", false, func(ctx context.Context, d certActDeps) (string, error) {
		return certDoIssue(ctx, d, r.FormValue("name"))
	})
}

// CertRemoveWeb: POST /admin/server-config/certs/remove (name) – nur Bereichsadministrator
func (h *Handler) CertRemoveWeb(w http.ResponseWriter, r *http.Request) {
	h.certAct(w, r, h.certMayRemove(r), "entfernen", true, func(ctx context.Context, d certActDeps) (string, error) {
		return certDoRemove(ctx, d, r.FormValue("name"))
	})
}

// CertStrictWeb: POST /admin/server-config/certs/strict (action=on|off) – dazu system.cloudflare
func (h *Handler) CertStrictWeb(w http.ResponseWriter, r *http.Request) {
	// certStrictOn/Off melden die Änderung selbst an die Administratoren.
	h.certAct(w, r, h.certCanManage(r) && h.hasPerm(r, permCloudflare), "full-strict", false, func(ctx context.Context, d certActDeps) (string, error) {
		switch r.FormValue("action") {
		case "on":
			return certDoStrict(ctx, d, true)
		case "off":
			return certDoStrict(ctx, d, false)
		}
		return "", errors.New("unbekannte Aktion")
	})
}
