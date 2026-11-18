package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"pdh/internal/integrations/cloudflare"
)

// Karte „Client-Zertifikate bei Cloudflare (mTLS)“ im Reiter Cloudflare-Tunnel (optional).
//
//	GET  /admin/server-config/cloudflare/mtls      Karte laden (nur auf Knopfdruck)
//	POST /admin/server-config/cloudflare/mtls-off  mTLS für einen Namen abschalten (host)
//	POST /admin/server-config/cloudflare/mtls-on   mTLS für einen Namen wieder einschalten (host)
//
// Alles dazu braucht system.cloudflare. Das PDH ändert nur die Namensliste von Cloudflare und
// auch dort nur den einen Namen; WAF-Regeln liest es nur und ändert sie nie.

const (
	mtlsErrAuth  = "Das Cloudflare-Token darf das nicht – Recht ‚SSL and Certificates: Edit‘ bzw. ‚Zone WAF: Read‘ ergänzen"
	mtlsErrOther = "Cloudflare hat die Anfrage nicht angenommen – Einzelheiten stehen im Server-Protokoll"
)

// mtlsMu: Lesen und Schreiben der Namensliste gehören zusammen (PUT ersetzt alles).
var mtlsMu sync.Mutex

type mtlsHost struct {
	Name string
	Role string // "PDH-Adresse", "Nextcloud-Adresse" oder ""
	On   bool
}

type mtlsRuleView struct {
	Description, Action string
	Enabled             bool
}

type mtlsPanel struct {
	Zone, Msg, Warn, Err string
	Hint                 string // Cloudflare nicht eingerichtet
	RulesErr             string
	Hosts                []mtlsHost
	Rules                []mtlsRuleView
}

// mtlsDeps: was Karte und Knöpfe brauchen; im Betrieb aus mtlsDepsLive, in Tests im Speicher.
type mtlsDeps struct {
	access  func(ctx context.Context) (*cfAccess, error)
	pdhHost func(ctx context.Context) string
	ncURL   string
	blocked func() bool
	notify  func(ctx context.Context, text string)
}

func (h *Handler) mtlsDepsLive() mtlsDeps {
	return mtlsDeps{
		access: h.cfAccess,
		pdhHost: func(ctx context.Context) string {
			v, _ := h.nextStartValue(ctx, cfHostKey)
			return v
		},
		ncURL:   os.Getenv("PDH_NEXTCLOUD_BASEURL"),
		blocked: func() bool { return h.Mirror().On || h.MaintLocked() },
		notify: func(ctx context.Context, text string) {
			if h.db != nil {
				h.notifyAdmins(ctx, text)
			}
		},
	}
}

// mtlsErrText: fester Text für den Anwender; die Einzelheiten gehören ins Protokoll.
func mtlsErrText(err error) string {
	if errors.Is(err, cloudflare.ErrAuth) {
		return mtlsErrAuth
	}
	return mtlsErrOther
}

// mtlsUserErr: eigene, schon verständliche Fehler (userError) bleiben, Cloudflare-Fehler werden fest.
type mtlsUserErr struct{ msg string }

func (e mtlsUserErr) Error() string { return e.msg }

func mtlsText(err error) string {
	var u mtlsUserErr
	if errors.As(err, &u) {
		return u.msg
	}
	return mtlsErrText(err)
}

func mtlsHas(list []string, n string) bool {
	for _, x := range list {
		if cfSameHost(x, n) {
			return true
		}
	}
	return false
}

// mtlsRuleHits: aktive Regeln mit Zertifikatsprüfung, die den Namen treffen können – der Name
// steht im Ausdruck, oder die Regel nennt gar keinen Host (gilt dann für alle).
func mtlsRuleHits(rules []cloudflare.MTLSRule, host string) []cloudflare.MTLSRule {
	var out []cloudflare.MTLSRule
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		e := strings.ToLower(r.Expression)
		if strings.Contains(e, strings.ToLower(host)) || !strings.Contains(e, "http.host") {
			out = append(out, r)
		}
	}
	return out
}

// mtlsHostRows: je Name der Cloudflare-Liste eine Zeile; dazu die Namen des PDH (PDH-Adresse,
// Nextcloud-Adresse), die nicht in der Liste stehen, als „aus“ – damit sich Abschalten
// rückgängig machen lässt.
func mtlsHostRows(list []string, pdhHost, ncURL, zone string) []mtlsHost {
	var rows []mtlsHost
	role := func(n string) string {
		switch certRole(n, pdhHost, ncURL) {
		case "pdh":
			return "PDH-Adresse"
		case "nextcloud":
			return "Nextcloud-Adresse"
		}
		return ""
	}
	for _, n := range list {
		rows = append(rows, mtlsHost{Name: n, Role: role(n), On: true})
	}
	for _, n := range certSuggested(pdhHost, ncURL) {
		if zone != "" && n != zone && !strings.HasSuffix(n, "."+zone) {
			continue
		}
		if !mtlsHas(list, n) {
			rows = append(rows, mtlsHost{Name: n, Role: role(n)})
		}
	}
	return rows
}

// mtlsLoad liest Namensliste und Regeln. Nur lesen.
func mtlsLoad(ctx context.Context, d mtlsDeps, msg, warn string, perr error) mtlsPanel {
	p := mtlsPanel{Msg: msg, Warn: warn}
	if perr != nil {
		p.Err = mtlsText(perr)
	}
	a, err := d.access(ctx)
	if err != nil {
		p.Hint = err.Error()
		return p
	}
	p.Zone = a.zone
	l := componentLog("cloudflare")
	zone, err := a.client.ZoneByName(ctx, a.zone)
	if err != nil {
		l.Warn().Err(err).Msg("mtls: domain nicht gelesen")
		if p.Err == "" {
			p.Err = mtlsErrText(err)
		}
		return p
	}
	list, err := a.client.MTLSHostnames(ctx, zone.ID)
	if err != nil {
		l.Warn().Err(err).Msg("mtls: namensliste nicht gelesen")
		if p.Err == "" {
			p.Err = mtlsErrText(err)
		}
	} else {
		p.Hosts = mtlsHostRows(list, d.pdhHost(ctx), d.ncURL, a.zone)
	}
	rules, err := a.client.MTLSRules(ctx, zone.ID)
	if err != nil {
		l.Warn().Err(err).Msg("mtls: waf-regeln nicht gelesen")
		p.RulesErr = mtlsErrText(err)
	}
	for _, r := range rules {
		p.Rules = append(p.Rules, mtlsRuleView{Description: r.Description, Action: r.Action, Enabled: r.Enabled})
	}
	return p
}

// mtlsSwitch schaltet mTLS für genau einen Namen ab (on=false) oder ein (on=true). Die Liste
// wird erst frisch gelesen und mit dem einen Unterschied zurückgeschrieben (PUT ersetzt alles).
// Rückgabe: Meldung und gegebenenfalls Hinweis auf aktive WAF-Regeln.
func mtlsSwitch(ctx context.Context, d mtlsDeps, host string, on bool) (msg, warn string, err error) {
	if d.blocked != nil && d.blocked() {
		return "", "", mtlsUserErr{"Am Spiegelserver und während der Wartung wird bei Cloudflare nichts geändert"}
	}
	a, err := d.access(ctx)
	if err != nil {
		return "", "", mtlsUserErr{err.Error()}
	}
	mtlsMu.Lock()
	defer mtlsMu.Unlock()
	name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	zone, err := a.client.ZoneByName(ctx, a.zone)
	if err != nil {
		return "", "", err
	}
	list, err := a.client.MTLSHostnames(ctx, zone.ID)
	if err != nil {
		return "", "", err
	}
	// Nur Namen der angezeigten Liste oder des PDH (PDH- und Nextcloud-Adresse); nichts Fremdes.
	if name == "" || !(mtlsHas(list, name) || mtlsHas(certSuggested(d.pdhHost(ctx), d.ncURL), name)) {
		return "", "", mtlsUserErr{"Name: bitte einen Namen aus der Liste wählen"}
	}
	var next []string
	switch {
	case on && mtlsHas(list, name):
		return name + ": mTLS ist schon eingeschaltet.", "", nil
	case on:
		next = append(append([]string{}, list...), name)
	default:
		if !mtlsHas(list, name) {
			return name + ": mTLS ist schon abgeschaltet.", "", nil
		}
		next = make([]string, 0, len(list))
		for _, x := range list {
			if !cfSameHost(x, name) {
				next = append(next, x)
			}
		}
	}
	if err := a.client.SetMTLSHostnames(ctx, zone.ID, next); err != nil {
		return "", "", err
	}
	if on {
		return "mTLS für " + name + " eingeschaltet – Cloudflare verlangt dort wieder ein Client-Zertifikat.", "", nil
	}
	msg = "mTLS für " + name + " abgeschaltet – Cloudflare verlangt dort kein Client-Zertifikat mehr."
	// Regeln nur lesen: Eine Sperrregel bleibt auch ohne mTLS wirksam.
	if rules, rerr := a.client.MTLSRules(ctx, zone.ID); rerr != nil {
		componentLog("cloudflare").Warn().Err(rerr).Msg("mtls: waf-regeln nach dem abschalten nicht gelesen")
		warn = "Die WAF-Regeln ließen sich nicht lesen. Bitte im Cloudflare-Dashboard prüfen, ob eine Regel mit Zertifikatsprüfung " + name + " weiter sperrt."
	} else if hits := mtlsRuleHits(rules, name); len(hits) > 0 {
		var names []string
		for _, r := range hits {
			n := r.Description
			if n == "" {
				n = "ohne Namen"
			}
			names = append(names, "„"+n+"“")
		}
		warn = "Bei Cloudflare gibt es aktive Regeln mit Zertifikatsprüfung, die " + name + " treffen können: " + strings.Join(names, ", ") +
			". Bitte im Cloudflare-Dashboard prüfen (Sicherheit → WAF → Benutzerdefinierte Regeln). Das PDH ändert keine Regeln."
	}
	return msg, warn, nil
}

// ── HTTP ─────────────────────────────────────────────────────

func (h *Handler) mtlsRender(w http.ResponseWriter, p mtlsPanel) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	t, err := h.tmpl.Clone()
	if err == nil {
		err = t.ExecuteTemplate(w, "mtls-panel", p)
	}
	if err != nil {
		fmt.Fprintf(w, `<div id="mtls-panel" class="sc-sub" style="color:var(--red)">Client-Zertifikate: %s</div>`, esc(err.Error()))
	}
}

// CloudflareMTLSWeb: GET /admin/server-config/cloudflare/mtls
func (h *Handler) CloudflareMTLSWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canCloudflare(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	h.mtlsRender(w, mtlsLoad(ctx, h.mtlsDepsLive(), "", "", nil))
}

func (h *Handler) mtlsAct(w http.ResponseWriter, r *http.Request, on bool) {
	if !h.canCloudflare(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	d := h.mtlsDepsLive()
	host := r.FormValue("host")
	msg, warn, err := mtlsSwitch(ctx, d, host, on)
	uid := ""
	if u := getUser(r); u != nil {
		uid = u.ID
	}
	lg := componentLog("cloudflare")
	if err != nil {
		lg.Warn().Err(err).Str("von", uid).Str("host", certSafeText(host)).Bool("an", on).Msg("mtls geändert: gescheitert")
	} else {
		lg.Info().Str("von", uid).Str("host", certSafeText(host)).Bool("an", on).Msg(msg)
		if !strings.Contains(msg, "schon") && d.notify != nil {
			d.notify(ctx, "☁️ **Cloudflare geändert** von "+h.chatUserName(ctx, uid)+": "+msg)
		}
	}
	h.mtlsRender(w, mtlsLoad(ctx, d, msg, warn, err))
}

// CloudflareMTLSOffWeb: POST /admin/server-config/cloudflare/mtls-off (host)
func (h *Handler) CloudflareMTLSOffWeb(w http.ResponseWriter, r *http.Request) {
	h.mtlsAct(w, r, false)
}

// CloudflareMTLSOnWeb: POST /admin/server-config/cloudflare/mtls-on (host)
func (h *Handler) CloudflareMTLSOnWeb(w http.ResponseWriter, r *http.Request) { h.mtlsAct(w, r, true) }
