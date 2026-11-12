package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/rs/zerolog"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"pdh/internal/integrations/cloudflare"
)

// Server-Einstellungen → Cloudflare: DNS-Einträge, Proxy je Eintrag, Verschlüsselung
// der Zone und die Ziele im Tunnel direkt im PDH pflegen. Dafür braucht es einen
// API-Token aus dem Cloudflare-Dashboard (verschlüsselt in den Server-Einstellungen)
// und die Domain. Das PDH ändert nichts von selbst: Jede Änderung ist ein Knopf.

const (
	cfAPITokenKey  = "PDH_CLOUDFLARE_API_TOKEN"
	cfZoneKey      = "PDH_CLOUDFLARE_ZONE"
	permCloudflare = "system.cloudflare"
	cfTunnelSuffix = ".cfargotunnel.com"
)

// cfAPIBase: Adresse der Cloudflare-Schnittstelle (Tests ersetzen sie).
var cfAPIBase = cloudflare.DefaultBase

var (
	cfNameRe    = regexp.MustCompile(`^(\*\.)?[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?(\.[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?)*$`)
	cfServiceRe = regexp.MustCompile(`^(https?|tcp|ssh|rdp)://[A-Za-z0-9.\-\[\]:]{1,200}$`)
	cfIDRe      = regexp.MustCompile(`^[a-f0-9]{16,64}$`)
)

var cfRecordTypes = []string{"A", "AAAA", "CNAME", "TXT"}

type cfRecordView struct {
	cloudflare.Record
	Tunnel bool // CNAME auf einen Tunnel – läuft immer über den Proxy
}

// cfTarget: ein Name, den das PDH braucht (PDH selbst, Nextcloud), mit Soll und Ist.
type cfTarget struct {
	Label, Host, Service string
	NoTLS                bool
	TunnelOK, DNSOK      bool
	TunnelNote, DNSNote  string
	Replace              bool // ein DNS-Eintrag zeigt woanders hin – Einrichten ersetzt ihn
}

type cfPanel struct {
	Message, Error string
	Zone           string
	Records        []cfRecordView
	Types          []string
	SSL            string
	AlwaysHTTPS    bool
	SettingsErr    string
	TunnelErr      string
	Ingress        []cloudflare.Ingress
	Targets        []cfTarget
}

func (h *Handler) canCloudflare(r *http.Request) bool { return h.hasPerm(r, permCloudflare) }

// cfZoneGuess: Domain aus einem Hostnamen (pdh.firma.de → firma.de). Bei Endungen
// wie .co.uk stimmt das nicht – dann die Domain eintragen.
func cfZoneGuess(host string) string {
	p := strings.Split(strings.ToLower(strings.TrimSpace(host)), ".")
	if len(p) < 2 {
		return ""
	}
	return strings.Join(p[len(p)-2:], ".")
}

// cfAccess: Token, Domain und (wenn vorhanden) Konto und Tunnel aus dem Tunnel-Token.
type cfAccess struct {
	client          *cloudflare.Client
	zone            string
	account, tunnel string
}

func (h *Handler) cfAccess(ctx context.Context) (*cfAccess, error) {
	token, _ := h.nextStartValue(ctx, cfAPITokenKey)
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("Zuerst oben den API-Token eintragen und speichern")
	}
	zone, _ := h.nextStartValue(ctx, cfZoneKey)
	zone = strings.ToLower(strings.TrimSpace(zone))
	if zone == "" {
		host, _ := h.nextStartValue(ctx, cfHostKey)
		zone = cfZoneGuess(host)
	}
	if zone == "" {
		return nil, errors.New("Zuerst oben die Domain eintragen und speichern")
	}
	c := cloudflare.New(token)
	c.Base = cfAPIBase
	a := &cfAccess{client: c, zone: zone}
	if tt, _ := h.nextStartValue(ctx, cfTokenKey); tt != "" {
		a.account, a.tunnel, _ = cloudflare.TunnelIDs(tt)
	}
	return a, nil
}

// cfFullName: Eingabe („nc“, „@“, „nc.firma.de“) als vollständiger Name in der Zone.
func cfFullName(name, zone string) (string, error) {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	switch {
	case name == "" || name == "@":
		name = zone
	case name != zone && !strings.HasSuffix(name, "."+zone):
		name += "." + zone
	}
	if len(name) > 253 || !cfNameRe.MatchString(name) {
		return "", errors.New("Name: nur Buchstaben, Ziffern, Bindestrich, Unterstrich und Punkte, z. B. nc oder nc." + zone)
	}
	return name, nil
}

// cfRecordFrom prüft die Eingabe für einen DNS-Eintrag.
func cfRecordFrom(typ, name, content string, proxied bool, zone string) (cloudflare.Record, error) {
	typ = strings.ToUpper(strings.TrimSpace(typ))
	content = strings.TrimSpace(content)
	full, err := cfFullName(name, zone)
	if err != nil {
		return cloudflare.Record{}, err
	}
	rec := cloudflare.Record{Type: typ, Name: full, Content: content, Proxied: proxied, TTL: 1}
	switch typ {
	case "A":
		if ip := net.ParseIP(content); ip == nil || ip.To4() == nil {
			return rec, errors.New("Ziel: bitte eine IPv4-Adresse eingeben, z. B. 203.0.113.10")
		}
	case "AAAA":
		if ip := net.ParseIP(content); ip == nil || ip.To4() != nil {
			return rec, errors.New("Ziel: bitte eine IPv6-Adresse eingeben")
		}
	case "CNAME":
		rec.Content = strings.TrimSuffix(strings.ToLower(content), ".")
		if !cfNameRe.MatchString(rec.Content) || !strings.Contains(rec.Content, ".") {
			return rec, errors.New("Ziel: bitte einen Namen eingeben, z. B. server.firma.de")
		}
		if strings.HasSuffix(rec.Content, cfTunnelSuffix) {
			rec.Proxied = true // Tunnel-Namen sind nur über den Proxy erreichbar
		}
	case "TXT":
		if content == "" || len(content) > 2000 || strings.ContainsAny(content, "\r\n") {
			return rec, errors.New("Text: 1 bis 2000 Zeichen, eine Zeile")
		}
		rec.Proxied = false
	default:
		return rec, errors.New("Art: A, AAAA, CNAME oder TXT")
	}
	return rec, nil
}

func cfSameHost(a, b string) bool {
	return strings.EqualFold(strings.TrimSuffix(a, "."), strings.TrimSuffix(b, "."))
}

// cfCheckTarget vergleicht Soll (Name → Dienst über den Tunnel) mit dem Stand bei Cloudflare.
func cfCheckTarget(t cfTarget, tunnel string, ingress []cloudflare.Ingress, records []cloudflare.Record) cfTarget {
	t.TunnelNote = "fehlt"
	for _, in := range ingress {
		if !cfSameHost(in.Hostname, t.Host) {
			continue
		}
		if in.Service == t.Service {
			t.TunnelOK, t.TunnelNote = true, "eingerichtet"
		} else {
			t.TunnelNote = "zeigt auf " + in.Service
		}
		break
	}
	want := tunnel + cfTunnelSuffix
	t.DNSNote = "fehlt"
	for _, r := range records {
		if !cfSameHost(r.Name, t.Host) || (r.Type != "A" && r.Type != "AAAA" && r.Type != "CNAME") {
			continue
		}
		if r.Type == "CNAME" && cfSameHost(r.Content, want) && r.Proxied {
			t.DNSOK, t.DNSNote = true, "eingerichtet"
		} else {
			t.DNSNote, t.Replace = r.Type+" zeigt auf "+r.Content, true
		}
		break
	}
	return t
}

// cfWantedTargets: Namen, die das PDH über den Tunnel braucht.
func (h *Handler) cfWantedTargets(ctx context.Context, zone string, ingress []cloudflare.Ingress) []cfTarget {
	var out []cfTarget
	inZone := func(host string) bool { return host == zone || strings.HasSuffix(host, "."+zone) }
	if host, _ := h.nextStartValue(ctx, cfHostKey); host != "" && inZone(host) {
		origin, _ := h.nextStartValue(ctx, cfOriginKey)
		if origin == "" {
			origin = cfOriginDef
		}
		out = append(out, cfTarget{Label: "PDH", Host: host, Service: strings.TrimRight(origin, "/"), NoTLS: strings.HasPrefix(origin, "https://")})
	}
	if u, err := url.Parse(strings.TrimSpace(os.Getenv("PDH_NEXTCLOUD_BASEURL"))); err == nil && u.Hostname() != "" && inZone(strings.ToLower(u.Hostname())) {
		t := cfTarget{Label: "Nextcloud", Host: strings.ToLower(u.Hostname()), Service: "http://localhost:80"}
		for _, in := range ingress { // ein vorhandenes Ziel gilt als gewollt
			if cfSameHost(in.Hostname, t.Host) {
				t.Service, t.NoTLS = in.Service, in.NoTLSVerify()
			}
		}
		out = append(out, t)
	}
	return cfStrictWanted(out, h.cfStrictNames(ctx))
}

// cfStrictNames: die geführten Namen, solange Full (strict) eingeschaltet ist; sonst nichts.
func (h *Handler) cfStrictNames(ctx context.Context) []string {
	if h.db == nil || h.certSetting(ctx, keyCertStrictBefore) == "" {
		return nil
	}
	rows, err := h.certList(ctx)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}
	return names
}

// cfKeepStrict: Wird bei eingeschaltetem Full (strict) das Ziel eines geführten Namens auf
// https://localhost:443 gespeichert (Knopf „Einrichten“), gehören die Angaben für die
// Zertifikatsprüfung dazu – sonst prüfte der Tunnel gegen „localhost“ und der Name wäre von
// außen weg. Andere Ziele und andere Namen bleiben, wie sie gespeichert wurden.
func cfKeepStrict(list []cloudflare.Ingress, host string, strictNames []string) []cloudflare.Ingress {
	if !certNameIn(strictNames, host) {
		return list
	}
	for _, in := range list {
		if cfSameHost(in.Hostname, host) && in.Service == certStrictService {
			return certStrictIngress(list, []string{in.Hostname})
		}
	}
	return list
}

// cfStrictWanted: Solange Full (strict) eingeschaltet ist, ist für die geführten Namen
// https://localhost:443 mit Zertifikatsprüfung das gewollte Ziel – kein Hinweis und kein
// „Einrichten“, das das alte Ziel zurückschreiben würde.
func cfStrictWanted(targets []cfTarget, names []string) []cfTarget {
	for i := range targets {
		if certNameIn(names, targets[i].Host) {
			targets[i].Service, targets[i].NoTLS = certStrictService, false
		}
	}
	return targets
}

// cfBuildPanel lädt den Stand von Cloudflare. Teile, die der Token nicht darf,
// melden sich einzeln – der Rest bleibt benutzbar.
func (h *Handler) cfBuildPanel(ctx context.Context, msg string, perr error) cfPanel {
	p := h.cfLoadPanel(ctx, msg, perr)
	p.logNotes(componentLog("cloudflare"), perr != nil)
	return p
}

// cfNoTunnelToken: Hinweis ohne Fehler – steht nicht im Protokoll.
const cfNoTunnelToken = "Kein Tunnel-Token eingetragen – die Ziele im Tunnel lassen sich erst damit pflegen."

// logNotes schreibt die Meldungen der Karte ins Protokoll: Sonst sieht sie nur, wer gerade
// davor sitzt. ownErr: die rote Meldung stammt aus einer Änderung und ist schon protokolliert.
func (p cfPanel) logNotes(l *zerolog.Logger, ownErr bool) {
	note := func(teil, text string) {
		if text != "" {
			l.Warn().Str("teil", teil).Str("domain", p.Zone).Str("meldung", text).Msg("cloudflare-karte zeigt eine meldung")
		}
	}
	if !ownErr {
		note("zugang", p.Error)
	}
	note("verschluesselung", p.SettingsErr)
	if p.TunnelErr != cfNoTunnelToken {
		note("tunnel", p.TunnelErr)
	}
}

func (h *Handler) cfLoadPanel(ctx context.Context, msg string, perr error) cfPanel {
	p := cfPanel{Message: msg, Types: cfRecordTypes}
	if perr != nil {
		p.Error = perr.Error()
	}
	a, err := h.cfAccess(ctx)
	if err == nil {
		err = a.client.Verify(ctx)
	}
	var zone *cloudflare.Zone
	if err == nil {
		zone, err = a.client.ZoneByName(ctx, a.zone)
	}
	if err != nil {
		if p.Error == "" {
			p.Error = err.Error()
		}
		return p
	}
	p.Zone = zone.Name
	records, err := a.client.Records(ctx, zone.ID)
	if err != nil && p.Error == "" {
		p.Error = "DNS-Einträge: " + err.Error()
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Name != records[j].Name {
			return records[i].Name < records[j].Name
		}
		return records[i].Type < records[j].Type
	})
	for _, r := range records {
		p.Records = append(p.Records, cfRecordView{Record: r, Tunnel: r.Type == "CNAME" && strings.HasSuffix(r.Content, cfTunnelSuffix)})
	}
	if ssl, err := a.client.Setting(ctx, zone.ID, "ssl"); err != nil {
		p.SettingsErr = err.Error()
	} else {
		p.SSL = ssl
		v, _ := a.client.Setting(ctx, zone.ID, "always_use_https")
		p.AlwaysHTTPS = v == "on"
	}
	switch {
	case a.tunnel == "":
		p.TunnelErr = cfNoTunnelToken
	default:
		cfg, err := a.client.TunnelConfig(ctx, a.account, a.tunnel)
		if err != nil {
			p.TunnelErr = err.Error()
			break
		}
		for _, in := range cfg.Ingress {
			if in.Hostname != "" {
				p.Ingress = append(p.Ingress, in)
			}
		}
		for _, t := range h.cfWantedTargets(ctx, zone.Name, cfg.Ingress) {
			p.Targets = append(p.Targets, cfCheckTarget(t, a.tunnel, cfg.Ingress, records))
		}
	}
	return p
}

func (h *Handler) cfRender(w http.ResponseWriter, p cfPanel) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	t, err := h.tmpl.Clone()
	if err == nil {
		err = t.ExecuteTemplate(w, "cf-panel", p)
	}
	if err != nil {
		fmt.Fprintf(w, `<div style="color:var(--red);font-size:12px">Cloudflare: %s</div>`, esc(err.Error()))
	}
}

// cfDone protokolliert eine Änderung, meldet sie den Administratoren und zeigt den neuen Stand.
func (h *Handler) cfDone(w http.ResponseWriter, r *http.Request, what string, err error) {
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	if err != nil {
		componentLog("cloudflare").Warn().Err(err).Str("von", getUser(r).ID).Msg(what)
		h.cfRender(w, h.cfBuildPanel(ctx, "", err))
		return
	}
	componentLog("cloudflare").Info().Str("von", getUser(r).ID).Msg(what)
	if h.db != nil {
		h.notifyAdmins(ctx, "☁️ **Cloudflare geändert** von "+h.chatUserName(ctx, getUser(r).ID)+": "+what)
	}
	h.cfRender(w, h.cfBuildPanel(ctx, what, nil))
}

// CloudflarePanelWeb: GET /admin/server-config/cloudflare/panel (htmx)
func (h *Handler) CloudflarePanelWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canCloudflare(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	h.cfRender(w, h.cfBuildPanel(ctx, "", nil))
}

// CloudflareDNSWeb: POST /admin/server-config/cloudflare/dns (action=save|proxy|delete)
func (h *Handler) CloudflareDNSWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canCloudflare(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	a, err := h.cfAccess(ctx)
	var zone *cloudflare.Zone
	if err == nil {
		zone, err = a.client.ZoneByName(ctx, a.zone)
	}
	if err != nil {
		h.cfDone(w, r, "DNS-Eintrag", err)
		return
	}
	id := strings.TrimSpace(r.FormValue("id"))
	if id != "" && !cfIDRe.MatchString(id) {
		h.cfDone(w, r, "DNS-Eintrag", errors.New("ungültige Kennung"))
		return
	}
	find := func() (*cloudflare.Record, error) {
		recs, err := a.client.Records(ctx, zone.ID)
		if err != nil {
			return nil, err
		}
		for i := range recs {
			if recs[i].ID == id {
				return &recs[i], nil
			}
		}
		return nil, errors.New("den DNS-Eintrag gibt es nicht mehr")
	}
	switch r.FormValue("action") {
	case "delete":
		rec, err := find()
		if err == nil {
			err = a.client.DeleteRecord(ctx, zone.ID, id)
		}
		name := ""
		if rec != nil {
			name = rec.Type + " " + rec.Name
		}
		h.cfDone(w, r, "DNS-Eintrag "+name+" gelöscht", err)
	case "proxy":
		rec, err := find()
		if err != nil {
			h.cfDone(w, r, "Proxy umschalten", err)
			return
		}
		if rec.Type == "CNAME" && strings.HasSuffix(rec.Content, cfTunnelSuffix) {
			h.cfDone(w, r, "Proxy umschalten", errors.New("Ein Name, der auf den Tunnel zeigt, läuft immer über den Proxy"))
			return
		}
		if rec.Type == "TXT" {
			h.cfDone(w, r, "Proxy umschalten", errors.New("Ein Text-Eintrag hat keinen Proxy"))
			return
		}
		rec.Proxied = !rec.Proxied
		state := "aus (direkt)"
		if rec.Proxied {
			state = "an (über Cloudflare)"
		}
		h.cfDone(w, r, "Proxy für "+rec.Name+" "+state, a.client.UpdateRecord(ctx, zone.ID, id, *rec))
	case "save":
		rec, err := cfRecordFrom(r.FormValue("type"), r.FormValue("name"), r.FormValue("content"), r.FormValue("proxied") == "on", zone.Name)
		if err != nil {
			h.cfDone(w, r, "DNS-Eintrag", err)
			return
		}
		if id != "" {
			h.cfDone(w, r, "DNS-Eintrag "+rec.Type+" "+rec.Name+" geändert", a.client.UpdateRecord(ctx, zone.ID, id, rec))
			return
		}
		rec.Comment = "PDH"
		_, err = a.client.CreateRecord(ctx, zone.ID, rec)
		h.cfDone(w, r, "DNS-Eintrag "+rec.Type+" "+rec.Name+" angelegt", err)
	default:
		h.cfDone(w, r, "DNS-Eintrag", errors.New("unbekannte Aktion"))
	}
}

// CloudflareZoneWeb: POST /admin/server-config/cloudflare/zone – Verschlüsselung der Zone.
func (h *Handler) CloudflareZoneWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canCloudflare(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	ssl := r.FormValue("ssl")
	if ssl != "off" && ssl != "flexible" && ssl != "full" && ssl != "strict" {
		h.cfDone(w, r, "Verschlüsselung", errors.New("ungültige Auswahl"))
		return
	}
	always := "off"
	if r.FormValue("always_https") == "on" {
		always = "on"
	}
	a, err := h.cfAccess(ctx)
	var zone *cloudflare.Zone
	if err == nil {
		zone, err = a.client.ZoneByName(ctx, a.zone)
	}
	if err == nil {
		err = a.client.SetSetting(ctx, zone.ID, "ssl", ssl)
	}
	if err == nil {
		err = a.client.SetSetting(ctx, zone.ID, "always_use_https", always)
	}
	h.cfDone(w, r, "Verschlüsselung der Domain: "+ssl+", immer HTTPS "+always, err)
}

// cfSetIngress setzt bzw. ersetzt ein Ziel (old = bisheriger Name beim Umbenennen).
func cfSetIngress(list []cloudflare.Ingress, old string, in cloudflare.Ingress) []cloudflare.Ingress {
	var out []cloudflare.Ingress
	done := false
	for _, x := range list {
		switch {
		case x.Hostname == "": // Auffangregel bleibt am Ende
			if !done {
				out, done = append(out, in), true
			}
			out = append(out, x)
		case cfSameHost(x.Hostname, in.Hostname) || (old != "" && cfSameHost(x.Hostname, old)):
			if !done {
				out, done = append(out, in), true
			}
		default:
			out = append(out, x)
		}
	}
	if !done {
		out = append(out, in)
	}
	return out
}

// CloudflareTunnelWeb: POST /admin/server-config/cloudflare/tunnel (action=save|delete)
// – Ziel im Tunnel und der passende DNS-Name (CNAME auf den Tunnel, über den Proxy).
func (h *Handler) CloudflareTunnelWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canCloudflare(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	a, err := h.cfAccess(ctx)
	if err == nil && a.tunnel == "" {
		err = errors.New("Zuerst oben den Tunnel-Token eintragen und speichern")
	}
	var zone *cloudflare.Zone
	if err == nil {
		zone, err = a.client.ZoneByName(ctx, a.zone)
	}
	if err != nil {
		h.cfDone(w, r, "Tunnel-Ziel", err)
		return
	}
	host, err := cfFullName(r.FormValue("hostname"), zone.Name)
	if err != nil || !cfHostRe.MatchString(host) {
		h.cfDone(w, r, "Tunnel-Ziel", errors.New("Hostname: bitte einen Namen in der Domain eingeben, z. B. nc."+zone.Name))
		return
	}
	old := strings.ToLower(strings.TrimSpace(r.FormValue("old_hostname")))
	cfg, err := a.client.TunnelConfig(ctx, a.account, a.tunnel)
	if err != nil {
		h.cfDone(w, r, "Tunnel-Ziel", err)
		return
	}
	records, err := a.client.Records(ctx, zone.ID)
	if err != nil {
		h.cfDone(w, r, "Tunnel-Ziel", err)
		return
	}
	target := a.tunnel + cfTunnelSuffix
	var dns *cloudflare.Record
	for i := range records {
		if cfSameHost(records[i].Name, host) && (records[i].Type == "A" || records[i].Type == "AAAA" || records[i].Type == "CNAME") {
			dns = &records[i]
			break
		}
	}
	if r.FormValue("action") == "delete" {
		var keep []cloudflare.Ingress
		for _, x := range cfg.Ingress {
			if !cfSameHost(x.Hostname, host) {
				keep = append(keep, x)
			}
		}
		cfg.Ingress = keep
		err = a.client.PutTunnelConfig(ctx, a.account, a.tunnel, cfg)
		if err == nil && dns != nil && dns.Type == "CNAME" && cfSameHost(dns.Content, target) {
			err = a.client.DeleteRecord(ctx, zone.ID, dns.ID)
		}
		h.cfDone(w, r, "Tunnel-Ziel "+host+" entfernt", err)
		return
	}
	service := strings.TrimRight(strings.TrimSpace(r.FormValue("service")), "/")
	if !cfServiceRe.MatchString(service) {
		h.cfDone(w, r, "Tunnel-Ziel", errors.New("Ziel: bitte eine Adresse am Server eingeben, z. B. http://localhost:80 oder https://localhost:8090"))
		return
	}
	in := cloudflare.Ingress{Hostname: host, Service: service}
	if r.FormValue("no_tls_verify") == "on" && strings.HasPrefix(service, "https://") {
		in.OriginRequest = json.RawMessage(`{"noTLSVerify":true}`)
	}
	// Ein DNS-Eintrag, der woanders hin zeigt, wird nur mit ausdrücklichem Haken ersetzt.
	if dns != nil && !(dns.Type == "CNAME" && cfSameHost(dns.Content, target)) && r.FormValue("replace_dns") != "on" {
		h.cfDone(w, r, "Tunnel-Ziel", fmt.Errorf("Für %s gibt es schon einen DNS-Eintrag (%s → %s). Mit dem Haken „vorhandenen DNS-Eintrag ersetzen“ noch einmal speichern", host, dns.Type, dns.Content))
		return
	}
	cfg.Ingress = cfKeepStrict(cfSetIngress(cfg.Ingress, old, in), host, h.cfStrictNames(ctx))
	if err = a.client.PutTunnelConfig(ctx, a.account, a.tunnel, cfg); err != nil {
		h.cfDone(w, r, "Tunnel-Ziel", err)
		return
	}
	want := cloudflare.Record{Type: "CNAME", Name: host, Content: target, Proxied: true, TTL: 1, Comment: "PDH: Tunnel"}
	switch {
	case dns == nil:
		_, err = a.client.CreateRecord(ctx, zone.ID, want)
	case dns.Type != "CNAME" || !cfSameHost(dns.Content, target) || !dns.Proxied:
		err = a.client.UpdateRecord(ctx, zone.ID, dns.ID, want)
	}
	if err != nil {
		err = fmt.Errorf("Ziel im Tunnel gespeichert, aber der DNS-Eintrag nicht: %w", err)
	}
	h.cfDone(w, r, "Tunnel-Ziel "+host+" → "+service+" eingerichtet (mit DNS-Eintrag)", err)
}
