package web

import (
	"archive/zip"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
)

// VPN (Server-Einstellungen → VPN, nur Administratoren):
//   - Tunnel zum Maschinennetz: Client-Konfiguration des entfernten Servers
//     prüfen (Vorschau) und in einen bestehenden Tunnel neu einspielen
//   - Konfigurationen für mehrere Benutzer auf einmal erstellen und als ZIP laden

const vpnMaxBulk = 200

// vpnDeviceCount: aktive Geräte einer Person.
func vpnDeviceCount(peers []vpnPeer, userID string) int {
	n := 0
	for _, x := range peers {
		if x.Kind == "user" && !x.Revoked && x.UserID == userID {
			n++
		}
	}
	return n
}

// vpnNewDevice: neuer Zugang (Gerät) für eine Person – Adresse, Schlüssel, PSK.
func vpnNewDevice(s vpnSettings, peers []vpnPeer, userID, name string) (vpnPeer, error) {
	p := vpnPeer{Kind: "user", Name: name, UserID: userID}
	if vpnDeviceCount(peers, userID) >= vpnMaxDevices {
		return p, fmt.Errorf("höchstens %d aktive Geräte je Person", vpnMaxDevices)
	}
	used := map[string]bool{}
	for _, x := range peers {
		if !x.Revoked && x.Kind != "outbound" {
			used[x.Address] = true
		}
	}
	var err error
	if p.Address, err = vpnNextAddress(s.Subnet, used); err != nil {
		return p, err
	}
	if p.privateKey, p.PublicKey, err = vpnKeyPair(); err != nil {
		return p, err
	}
	p.psk, err = vpnPSK()
	return p, err
}

// VPNBulkWeb: POST /admin/server-config/vpn/bulk (user_id mehrfach, name) –
// je gewählter Person ein Zugang; danach ZIP mit allen Konfigurationen.
func (h *Handler) VPNBulkWeb(w http.ResponseWriter, r *http.Request) {
	if !h.vpnAdminGuard(w, r) {
		return
	}
	ctx := r.Context()
	r.ParseForm()
	uid := getUser(r).ID
	name := oneLine(r.FormValue("name"))
	if name == "" {
		name = "VPN"
	}
	ids := r.Form["user_id"]
	switch {
	case !vpnNameRe.MatchString(name):
		vpnRedirect(w, r, true, "", "", errors.New("Gerätename: höchstens 80 Zeichen"))
		return
	case len(ids) == 0:
		vpnRedirect(w, r, true, "", "", errors.New("Bitte mindestens eine Person auswählen"))
		return
	case len(ids) > vpnMaxBulk:
		vpnRedirect(w, r, true, "", "", fmt.Errorf("Höchstens %d Personen auf einmal", vpnMaxBulk))
		return
	}
	names := map[string]string{}
	for _, u := range h.userOptions(ctx) {
		names[u.ID] = u.Name
	}
	s, err := h.vpnLoadSettings(ctx)
	if err != nil {
		vpnRedirect(w, r, true, "", "", err)
		return
	}
	vpnMu.Lock()
	peers, err := h.vpnLoadPeers(ctx, false)
	var created, skipped []string
	for _, id := range ids {
		if err != nil {
			break
		}
		who, ok := names[id]
		if !ok {
			continue // unbekannte oder inaktive Person
		}
		p, perr := vpnNewDevice(s, peers, id, name)
		if perr == nil {
			perr = h.vpnInsert(ctx, &p, uid)
		}
		if perr != nil {
			skipped = append(skipped, who+" ("+perr.Error()+")")
			continue
		}
		peers = append(peers, p)
		created = append(created, p.ID)
		h.vpnLog(ctx, uid, p.ID, p.Name, "create", "Gerät für "+who+" angelegt (Sammelanlage)")
	}
	vpnMu.Unlock()
	if err == nil && len(created) > 0 {
		err = h.vpnApply(ctx)
	}
	if err != nil {
		vpnRedirect(w, r, true, "", "", err)
		return
	}
	msg := fmt.Sprintf("%d Zugänge „%s“ angelegt", len(created), name)
	if len(skipped) > 0 {
		msg += " – nicht angelegt: " + strings.Join(skipped, ", ")
	}
	target := vpnAdminURL + "?tab=vpn&msg=" + urlQueryEscapeCode(msg)
	if len(created) > 0 {
		target += "&zip=" + strings.Join(created, ",")
	}
	http.Redirect(w, r, target+"#devices", http.StatusSeeOther)
}

// vpnZipIDsOK: ?zip= nach der Sammelanlage (kommagetrennte IDs).
func vpnZipIDsOK(s string) bool {
	ids := strings.Split(s, ",")
	if s == "" || len(ids) > vpnMaxBulk {
		return false
	}
	for _, id := range ids {
		if !bareUUIDRe.MatchString(id) {
			return false
		}
	}
	return true
}

var vpnZipDirRe = regexp.MustCompile(`[^\p{L}\p{N} ._-]+`)

// VPNZipWeb: GET /admin/server-config/vpn/zip?ids=a,b – Konfigurationen als ZIP
// (je Person ein Ordner, Dateiname = Tunnelname in der WireGuard-App).
func (h *Handler) VPNZipWeb(w http.ResponseWriter, r *http.Request) {
	if !h.vpnAdminGuard(w, r) {
		return
	}
	ctx := r.Context()
	want := map[string]bool{}
	for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if id = strings.TrimSpace(id); id != "" && len(want) < vpnMaxBulk {
			want[id] = true
		}
	}
	s, err := h.vpnLoadSettings(ctx)
	var peers []vpnPeer
	if err == nil {
		peers, err = h.vpnLoadPeers(ctx, true)
	}
	if err == nil && s.Endpoint == "" {
		err = errors.New("Zuerst den öffentlichen Namen des Servers eintragen")
	}
	if err != nil {
		vpnRedirect(w, r, true, "", "", err)
		return
	}
	var list []vpnPeer
	for _, p := range peers {
		if want[p.ID] && !p.Revoked && p.Kind != "outbound" {
			list = append(list, p)
		}
	}
	if len(list) == 0 {
		vpnRedirect(w, r, true, "", "", errors.New("Keine aktiven Zugänge für die ZIP-Datei"))
		return
	}
	uid := getUser(r).ID
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="pdh-vpn-konfigurationen.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	zw := zip.NewWriter(w)
	seen := map[string]int{}
	for _, p := range list {
		conf, err := vpnClientConf(s, p, peers)
		if err != nil {
			continue
		}
		dir := "Gateways"
		if p.Kind == "user" {
			dir = strings.TrimSpace(vpnZipDirRe.ReplaceAllString(p.UserName, "_"))
			if dir == "" {
				dir = "Ohne Namen"
			}
		}
		path := dir + "/" + vpnFileName(p.Name) + ".conf"
		if seen[path]++; seen[path] > 1 {
			path = fmt.Sprintf("%s/%s-%d.conf", dir, vpnFileName(p.Name), seen[path])
		}
		f, err := zw.Create(path)
		if err != nil {
			break
		}
		_, _ = f.Write([]byte(conf))
		h.vpnLog(ctx, uid, p.ID, p.Name, "download", "Konfiguration als ZIP heruntergeladen")
	}
	_ = zw.Close()
}

// vpnImportSummary: Vorschau einer eingefügten Client-Konfiguration.
func vpnImportSummary(text, routes, subnet string) (string, error) {
	in, err := vpnParseImport(text)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(routes) == "" {
		routes = in.Routes
	}
	nets, err := vpnParseRoutes(routes, subnet)
	if err != nil {
		return "", err
	}
	if nets == "" {
		if in.FullTunnel {
			return "", errors.New("Die Konfiguration leitet alles durch den Tunnel (0.0.0.0/0) – bitte unter „Netze dahinter“ die Netze im Maschinennetz angeben")
		}
		return "", errors.New("Keine Netze dahinter – bitte unter „Netze dahinter“ angeben")
	}
	if _, err := vpnEndpoint(in.Endpoint); err != nil {
		return "", err
	}
	psk := "ohne Pre-Shared-Key"
	if in.PSK != "" {
		psk = "mit Pre-Shared-Key"
	}
	return fmt.Sprintf("Endpunkt %s · eigene Tunneladresse %s · Netze %s · %s", in.Endpoint, in.Address, nets, psk), nil
}

// VPNImportCheckWeb: POST /admin/server-config/vpn/import-check (import, routes) – Vorschau (htmx).
func (h *Handler) VPNImportCheckWeb(w http.ResponseWriter, r *http.Request) {
	if !h.vpnAdminGuard(w, r) {
		return
	}
	r.ParseForm()
	s, _ := h.vpnLoadSettings(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	sum, err := vpnImportSummary(r.FormValue("import"), r.FormValue("routes"), s.Subnet)
	if err != nil {
		fmt.Fprintf(w, `<span style="color:var(--red)"><i class="ti ti-alert-circle"></i> %s</span>`, esc(err.Error()))
		return
	}
	fmt.Fprintf(w, `<span style="color:var(--green)"><i class="ti ti-circle-check"></i> Konfiguration gültig: %s</span>`, esc(sum))
}

// VPNReimportWeb: POST /admin/server-config/vpn/peers/{id}/import – neue
// Client-Konfiguration in einen bestehenden ausgehenden Tunnel einspielen
// (z. B. neue Schlüssel am Router); Name und Verlauf bleiben.
func (h *Handler) VPNReimportWeb(w http.ResponseWriter, r *http.Request) {
	if !h.vpnAdminGuard(w, r) {
		return
	}
	ctx := r.Context()
	r.ParseForm()
	id := chi.URLParam(r, "id")
	fail := func(err error) { vpnRedirect(w, r, true, "", "", err) }
	s, err := h.vpnLoadSettings(ctx)
	if err != nil {
		fail(err)
		return
	}
	vpnMu.Lock()
	peers, err := h.vpnLoadPeers(ctx, false)
	var old *vpnPeer
	for i := range peers {
		if peers[i].ID == id && peers[i].Kind == "outbound" && !peers[i].Revoked {
			old = &peers[i]
		}
	}
	if err == nil && old == nil {
		err = errors.New("Ausgehender Tunnel nicht gefunden oder gesperrt")
	}
	var p vpnPeer
	if err == nil {
		p = vpnPeer{ID: id, Kind: "outbound", Name: old.Name}
		if strings.TrimSpace(r.FormValue("import")) == "" {
			err = errors.New("Bitte die neue Konfiguration einfügen oder als Datei wählen")
		} else {
			err = h.vpnOutboundFromForm(r, s, &p, nil)
		}
	}
	if err == nil {
		err = vpnRouteConflict(p.Routes, peers, id)
	}
	if err == nil {
		err = h.vpnUpdateOutbound(r, &p)
	}
	vpnMu.Unlock()
	if err != nil {
		fail(err)
		return
	}
	h.vpnLog(ctx, getUser(r).ID, p.ID, p.Name, "import", "Konfiguration neu eingespielt ("+p.Endpoint+", Netze "+p.Routes+")")
	if err := h.vpnApply(ctx); err != nil {
		fail(err)
		return
	}
	vpnRedirect(w, r, true, "Konfiguration für „"+p.Name+"“ eingespielt – der Tunnel verbindet sich neu", "", nil)
}

func (h *Handler) vpnUpdateOutbound(r *http.Request, p *vpnPeer) error {
	privEnc, err := encryptSecret(h.vpnKey(), p.privateKey)
	if err != nil {
		return err
	}
	pskEnc := ""
	if p.psk != "" {
		if pskEnc, err = encryptSecret(h.vpnKey(), p.psk); err != nil {
			return err
		}
	}
	_, err = h.db.Exec(r.Context(), `UPDATE vpn_peers SET address = $2, public_key = $3, private_key_enc = $4, psk_enc = $5,
		routes = $6, endpoint = $7, remote_public_key = $8 WHERE id = $1::uuid AND kind = 'outbound' AND revoked_at IS NULL`,
		p.ID, p.Address, p.PublicKey, privEnc, pskEnc, p.Routes, p.Endpoint, p.RemotePublicKey)
	if err != nil {
		return errors.New(friendlyDBError(err))
	}
	return nil
}
