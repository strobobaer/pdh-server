package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// VPN: Verbindungsstatus je Gerät, Gateway und Tunnel (aus dem Status des
// Dienstes pdh-wireguard, vpn_conf.go) und Aktivieren/Deaktivieren.
// WireGuard kennt keine „Verbindung“ – als verbunden gilt ein Handshake in den
// letzten 3 Minuten (bei bestehender Verbindung alle 2 Minuten erneuert).

// vpnConn: Zustand und Text für die Anzeige.
//
//	on (verbunden) · lost (getrennt) · never (noch nie verbunden) · wait (Tunnel
//	wartet auf Antwort) · off (deaktiviert) · revoked (gesperrt) · unknown (Dienst aus)
func vpnConn(p vpnPeer, serviceOK bool) (state, label string) {
	switch {
	case p.Revoked:
		return "revoked", "gesperrt"
	case p.Disabled:
		return "off", "deaktiviert"
	case !serviceOK:
		return "unknown", "Status unbekannt – VPN-Dienst läuft nicht"
	case p.Status.Online:
		return "on", "verbunden"
	case p.Status.Handshake != "":
		return "lost", "getrennt – letzter Kontakt " + p.Status.Handshake
	case p.Kind == "outbound":
		return "wait", "keine Antwort vom Server"
	}
	return "never", "noch nie verbunden"
}

// Detail: Endpunkt und Datenmenge für die Anzeige.
func (p vpnPeer) Detail() string { return vpnConnDetail(p) }

// vpnConnDetail: Endpunkt und Datenmenge (nur bei Kontakt).
func vpnConnDetail(p vpnPeer) string {
	if p.Status.Handshake == "" || p.Revoked {
		return ""
	}
	d := "↓ " + p.Status.Received + " ↑ " + p.Status.Sent
	if p.Status.RemoteEndpoint != "" && p.Kind != "outbound" {
		d = "von " + p.Status.RemoteEndpoint + " · " + d
	}
	return d
}

// vpnSummary: Zähler für die Übersicht (nur aktive Einträge).
type vpnSummary struct {
	Tunnels, TunnelsOn, Gateways, GatewaysOn, Devices, DevicesOn int
}

func vpnSummarize(peers []vpnPeer) vpnSummary {
	var s vpnSummary
	for _, p := range peers {
		if !p.Active() {
			continue
		}
		on := 0
		if p.ConnState == "on" {
			on = 1
		}
		switch p.Kind {
		case "outbound":
			s.Tunnels, s.TunnelsOn = s.Tunnels+1, s.TunnelsOn+on
		case "gateway":
			s.Gateways, s.GatewaysOn = s.Gateways+1, s.GatewaysOn+on
		case "user":
			s.Devices, s.DevicesOn = s.Devices+1, s.DevicesOn+on
		}
	}
	return s
}

type vpnStatusJSON struct {
	Service vpnService                `json:"service"`
	Summary vpnSummary                `json:"summary"`
	Peers   map[string]vpnPeerStatusJ `json:"peers"`
}

type vpnPeerStatusJ struct {
	State  string `json:"state"`
	Label  string `json:"label"`
	Detail string `json:"detail"`
}

// VPNStatusWeb: GET /vpn/status (eigene Geräte) bzw. /admin/server-config/vpn/status
// (alles) – für die Live-Anzeige, alle 10 Sekunden.
func (h *Handler) VPNStatusWeb(w http.ResponseWriter, r *http.Request) {
	admin := strings.HasPrefix(r.URL.Path, vpnAdminURL)
	if (admin && !h.canServerConfig(r)) || !h.canVPNUse(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	s, _ := h.vpnLoadSettings(ctx)
	peers, err := h.vpnLoadPeers(ctx, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	svc, status := vpnReadService(vpnServiceWanted(s, peers), time.Now())
	uid := getUser(r).ID
	out := vpnStatusJSON{Service: svc, Peers: map[string]vpnPeerStatusJ{}}
	var shown []vpnPeer
	for _, p := range peers {
		if !admin && (p.Kind != "user" || p.UserID != uid) {
			continue
		}
		p = vpnWithStatus(p, status, svc.State == "ok")
		shown = append(shown, p)
		out.Peers[p.ID] = vpnPeerStatusJ{State: p.ConnState, Label: p.ConnLabel, Detail: vpnConnDetail(p)}
	}
	out.Summary = vpnSummarize(shown)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

// vpnWithStatus: Status aus dem Dienst und Anzeige-Zustand eintragen.
func vpnWithStatus(p vpnPeer, status map[string]vpnPeerStatus, serviceOK bool) vpnPeer {
	key := p.PublicKey
	if p.Kind == "outbound" {
		key = p.RemotePublicKey
	}
	p.Status = status[key]
	p.ConnState, p.ConnLabel = vpnConn(p, serviceOK)
	return p
}

// VPNToggleWeb: POST …/peers/{id}/toggle – aktivieren bzw. deaktivieren (sofort wirksam).
// Benutzer: eigene Geräte; Verwaltung: alles.
func (h *Handler) VPNToggleWeb(w http.ResponseWriter, r *http.Request) {
	admin := strings.HasPrefix(r.URL.Path, vpnAdminURL)
	p, err := h.vpnTarget(r, admin)
	if err == nil && p.Revoked {
		err = errors.New("Gesperrte Zugänge lassen sich nicht wieder aktivieren")
	}
	if err == nil {
		_, err = h.db.Exec(r.Context(), `UPDATE vpn_peers SET disabled_at = CASE WHEN disabled_at IS NULL THEN NOW() END,
			disabled_by = CASE WHEN disabled_at IS NULL THEN $2::uuid END WHERE id = $1::uuid AND revoked_at IS NULL`, p.ID, nullID(getUser(r).ID))
	}
	what, msg := "deaktiviert", "„"+p.Name+"“ deaktiviert – die Verbindung ist getrennt"
	if p.Disabled {
		what, msg = "aktiviert", "„"+p.Name+"“ aktiviert – die Verbindung steht in wenigen Sekunden wieder"
	}
	if err == nil {
		h.vpnLog(r.Context(), getUser(r).ID, p.ID, p.Name, "toggle", what)
		err = h.vpnApply(r.Context())
	}
	if err != nil {
		vpnRedirect(w, r, admin, "", "", err)
		return
	}
	vpnRedirect(w, r, admin, msg, "", nil)
}
