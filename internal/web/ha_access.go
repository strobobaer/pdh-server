package web

// Karte „Zugangsweg“ im Reiter Ausfallschutz (Spec Zugangsweg §5): Tunnel,
// mitwandernde Adresse und Umschalt-Skripte. Das PDH zeigt nur an und reicht
// die Eingaben an pdh-ha weiter (PUT /v1/access, POST /v1/vip/test,
// POST /v1/hooks/run).

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Fristen der langsamen Aufrufe: der Adresstest dauert bis ~45 s (jeder
// Server hält die Adresse höchstens 20 s), ein Skript bis 30 s.
const (
	haVIPTestTimeout  = 150 * time.Second // pdh-ha bricht nach 2 min ab (probeRunMax)
	haHooksRunTimeout = 150 * time.Second
)

// haVIPTested: letzter erfolgreicher Adresstest.
type haVIPTested struct {
	Net    string            `json:"net"`
	Ifaces map[string]string `json:"ifaces"` // Server -> Netzwerkkarte
	At     time.Time         `json:"at"`
}

type haHookResult struct {
	Name string  `json:"name"`
	OK   bool    `json:"ok"`
	Out  string  `json:"out"`
	Dur  float64 `json:"dur"`
}

type haHooksRun struct {
	At      time.Time      `json:"at"`
	Role    string         `json:"role"`
	Results []haHookResult `json:"results"`
}

// haAccessRow: eine Zeile der Karte. Level: ok, warn, err oder off.
type haAccessRow struct {
	Title, Icon, State, Detail, Level string
}

func accessLevelMax(a, b string) string {
	rank := map[string]int{"off": 0, "ok": 1, "warn": 2, "err": 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// vipAddr: Adresse ohne Präfixlänge.
func vipAddr(vip string) string {
	a, _, _ := strings.Cut(vip, "/")
	return a
}

// originIsLocal: Ziel des Tunnels ist dieser Rechner (localhost bzw. 127.0.0.1).
func originIsLocal(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	h := u.Hostname()
	return h == "localhost" || h == "127.0.0.1"
}

// AccessRows: Zeilen Tunnel, Adresse, Skripte.
func (v haView) AccessRows() []haAccessRow {
	return []haAccessRow{v.tunnelRow(), v.vipRow(), v.hooksRow()}
}

func (v haView) tunnelRow() haAccessRow {
	a, self := v.Status.Access, v.Status.Self
	r := haAccessRow{Title: "Tunnel", Icon: "ti-cloud", Level: "off", State: "nicht eingerichtet"}
	configured := a.Tunnel.Installed || a.Tunnel.Sum != ""
	switch {
	case a.Tunnel.Running && self.Role == "aktiv":
		r.State, r.Level = "läuft auf "+self.ID, "ok"
	case a.Tunnel.Running:
		r.State, r.Level = "läuft auf "+self.ID+", ist aber nicht aktiv", "err"
	case a.Tunnel.Installed && a.Tunnel.Sum == "":
		// cloudflared da, aber pdh-ha hat kein Token: nach einer Übernahme
		// gäbe es hier keinen Tunnel
		r.State, r.Level = "Token fehlt – Tunnel-Token in den Server-Einstellungen eintragen bzw. PDH hier aktualisieren", "warn"
		if self.Role == "aktiv" {
			r.Level = "err"
		}
	case a.Tunnel.Installed && self.Role == "aktiv":
		r.State, r.Level = "eingerichtet, läuft aber nicht auf dem aktiven Server", "err"
	case a.Tunnel.Installed:
		r.State, r.Level = "bereit – läuft nur auf dem aktiven Server", "ok"
	}
	var detail []string
	if a.Tunnel.Running && a.Tunnel.Sum == "" {
		detail = append(detail, "kein Token bei pdh-ha abgelegt – nach einer Übernahme fehlt der Tunnel")
		r.Level = accessLevelMax(r.Level, "warn")
	}
	// gesehene Partner: fehlt das Token auf einer Seite, gilt das als verschieden
	for _, p := range v.Status.Peers {
		if p.Kind != "node" || !p.Seen || p.TunnelSum == a.Tunnel.Sum {
			continue
		}
		switch {
		case a.Tunnel.Sum == "":
			detail = append(detail, "Token fehlt hier, auf "+p.ID+" vorhanden")
		case p.TunnelSum == "":
			detail = append(detail, "Token fehlt auf "+p.ID)
		default:
			detail = append(detail, "Token auf den Servern verschieden")
		}
		r.Level = accessLevelMax(r.Level, "warn")
		break
	}
	if configured && v.Origin != "" && !originIsLocal(v.Origin) {
		detail = append(detail, "Ziel im Cloudflare-Dashboard muss localhost:8090 sein")
		r.Level = accessLevelMax(r.Level, "warn")
	}
	r.Detail = strings.Join(detail, " · ")
	return r
}

func (v haView) vipRow() haAccessRow {
	a := v.Status.Access
	r := haAccessRow{Title: "Mitwandernde Adresse", Icon: "ti-network", Level: "off", State: "nicht eingerichtet"}
	if a.VIP == "" {
		return r
	}
	var detail []string
	switch {
	case !a.VIPOn:
		r.State = a.VIP + " (ausgeschaltet)"
	case a.Holder == "":
		r.State, r.Level = a.VIP+" liegt auf keinem Server", "warn"
	default:
		r.State, r.Level = a.VIP+" liegt auf "+a.Holder, "ok"
	}
	if a.Tested != nil {
		d := "Letzter Test " + a.Tested.At.Local().Format("02.01. 15:04") + " bestanden"
		var cards []string
		for _, n := range v.Nodes() {
			if c := a.Tested.Ifaces[n.ID]; c != "" {
				cards = append(cards, n.ID+": "+c)
			}
		}
		if len(cards) > 0 {
			d += " (" + strings.Join(cards, ", ") + ")"
		}
		detail = append(detail, d)
	} else if a.VIPOn {
		detail = append(detail, "noch nicht getestet")
	}
	if a.IfaceErr != "" {
		detail = append(detail, "Netzwerkkarte: "+a.IfaceErr)
		r.Level = accessLevelMax(r.Level, "warn")
	}
	r.Detail = strings.Join(detail, " · ")
	return r
}

func (v haView) hooksRow() haAccessRow {
	a := v.Status.Access
	h := a.Hooks
	r := haAccessRow{Title: "Skripte", Icon: "ti-terminal-2", Level: "off", State: "keine Skripte"}
	if len(h.Names) == 0 {
		return r
	}
	names := strings.Join(h.Names, ", ")
	switch {
	case !h.Same:
		r.State, r.Level = names+" – auf den Servern verschieden", "warn"
	case a.HooksOn:
		r.State, r.Level = names+" – gleich auf beiden Servern", "ok"
	default:
		r.State = names + " (ausgeschaltet)"
	}
	if l := h.Last; l != nil {
		ok := 0
		for _, x := range l.Results {
			if x.OK {
				ok++
			}
		}
		r.Detail = fmt.Sprintf("Letzter Lauf %s als %s: %d von %d ohne Fehler", l.At.Local().Format("02.01. 15:04"), l.Role, ok, len(l.Results))
		if ok < len(l.Results) && r.Level != "off" {
			r.Level = accessLevelMax(r.Level, "warn")
		}
	}
	return r
}

// FailedHooks: Skripte des letzten Laufs, die fehlgeschlagen sind.
func (v haView) FailedHooks() []haHookResult {
	var out []haHookResult
	if l := v.Status.Access.Hooks.Last; l != nil {
		for _, x := range l.Results {
			if !x.OK {
				out = append(out, x)
			}
		}
	}
	return out
}

// ── Aktionen ────────────────────────────────────────────────

var errAccessEmpty = errors.New("Nichts zu speichern")

// haDoAccess: PUT /v1/access. Nur Felder, die im Formular vorkommen, werden
// gesendet; Schalter kommen als Paar „0“ (versteckt) und „1“ (Haken).
func (h *Handler) haDoAccess(ctx context.Context, f url.Values) (string, error) {
	body := map[string]any{}
	var msgs []string
	if _, ok := f["vip"]; ok {
		body["vip"] = strings.TrimSpace(f.Get("vip"))
		msgs = append(msgs, "Adresse gespeichert")
	}
	flag := func(key, label string) {
		vals, ok := f[key]
		if !ok {
			return
		}
		on := false
		for _, s := range vals {
			if s == "1" {
				on = true
			}
		}
		body[key] = on
		if on {
			msgs = append(msgs, label+" eingeschaltet")
		} else {
			msgs = append(msgs, label+" ausgeschaltet")
		}
	}
	flag("vip_on", "Mitwandernde Adresse")
	flag("hooks_on", "Skripte")
	if len(body) == 0 {
		return "", errAccessEmpty
	}
	if err := h.haDo(ctx, http.MethodPut, "/v1/access", body, nil); err != nil {
		return "", err
	}
	return strings.Join(msgs, ", "), nil
}

// haDoHooksRun: POST /v1/hooks/run – alle Skripte jetzt ausführen.
func (h *Handler) haDoHooksRun(ctx context.Context, _ url.Values) (string, error) {
	var res struct {
		Results []haHookResult `json:"results"`
	}
	if err := h.haDoT(ctx, haHooksRunTimeout, http.MethodPost, "/v1/hooks/run", map[string]any{}, &res); err != nil {
		return "", err
	}
	var bad []string
	for _, x := range res.Results {
		if !x.OK {
			bad = append(bad, x.Name)
		}
	}
	if len(bad) > 0 {
		return "", fmt.Errorf("%d von %d Skripten fehlgeschlagen: %s", len(bad), len(res.Results), strings.Join(bad, ", "))
	}
	return fmt.Sprintf("%d Skripte ausgeführt", len(res.Results)), nil
}

// HAAccessWeb: POST /admin/server-config/ha/access (vip, vip_on, hooks_on)
func (h *Handler) HAAccessWeb(w http.ResponseWriter, r *http.Request) {
	h.haRoute(h.haDoAccess, "access")(w, r)
}

// HAHooksRunWeb: POST /admin/server-config/ha/hooks-run
func (h *Handler) HAHooksRunWeb(w http.ResponseWriter, r *http.Request) {
	h.haRoute(h.haDoHooksRun, "hooks-run")(w, r)
}

// HAVIPTestWeb: POST /admin/server-config/ha/vip-test (addr optional) – liefert
// ein HTML-Fragment (htmx) mit dem Ergebnis je Richtung.
func (h *Handler) HAVIPTestWeb(w http.ResponseWriter, r *http.Request) {
	if !h.vpnAdminGuard(w, r) {
		return
	}
	_ = r.ParseForm()
	frag := h.haVIPTestFragment(r.Context(), r.Form.Get("addr"))
	componentLog("ha").Info().Str("action", "vip-test").Str("user", getUser(r).ID).Msg("ausfallschutz-betriebsart")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(frag))
}

// haVIPTestFragment: Adresstest ausführen und das Ergebnis als HTML (maskiert).
func (h *Handler) haVIPTestFragment(ctx context.Context, addr string) string {
	var res struct {
		Results []struct {
			Holder  string `json:"holder"`
			Checker string `json:"checker"`
			OK      bool   `json:"ok"`
			Err     string `json:"err"`
		} `json:"results"`
	}
	err := h.haDoT(ctx, haVIPTestTimeout, http.MethodPost, "/v1/vip/test", map[string]any{"addr": strings.TrimSpace(addr)}, &res)
	var b strings.Builder
	failed := err != nil
	if err != nil {
		fmt.Fprintf(&b, `<div class="vp-meta" style="color:var(--red)"><i class="ti ti-alert-triangle"></i> %s</div>`, html.EscapeString(err.Error()))
	}
	for _, x := range res.Results {
		if x.OK {
			fmt.Fprintf(&b, `<div class="vp-meta" style="color:var(--accent)"><i class="ti ti-check"></i> Adresse auf <b>%s</b>: von <b>%s</b> erreichbar</div>`,
				html.EscapeString(x.Holder), html.EscapeString(x.Checker))
			continue
		}
		failed = true
		why := x.Err
		if why == "" {
			why = "keine Antwort"
		}
		fmt.Fprintf(&b, `<div class="vp-meta" style="color:var(--red)"><i class="ti ti-x"></i> Adresse auf <b>%s</b>: von <b>%s</b> nicht erreichbar (%s)</div>`,
			html.EscapeString(x.Holder), html.EscapeString(x.Checker), html.EscapeString(why))
	}
	if err == nil && len(res.Results) == 0 {
		b.WriteString(`<div class="vp-meta">Keine Ergebnisse – ist ein zweiter Server verbunden?</div>`)
	}
	if failed {
		b.WriteString(`<p class="vp-hint">Mögliche Ursachen:</p><ul class="vp-hint">` +
			`<li>Proxmox: Der IP-Filter der virtuellen Netzwerkkarte erlaubt nur die eigene Adresse – Filter ausschalten oder die Adresse freigeben.</li>` +
			`<li>Switch: ARP-Prüfung (Dynamic ARP Inspection) oder Port-Security blockiert die zweite Adresse auf dem Anschluss.</li>` +
			`<li>Cloud-Server: Dort sind zusätzliche Adressen oft nicht frei verschiebbar – stattdessen die Skripte nutzen (z. B. Floating-IP des Anbieters umhängen).</li></ul>`)
	}
	return b.String()
}
