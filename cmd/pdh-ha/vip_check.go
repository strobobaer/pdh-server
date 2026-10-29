package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"pdh/internal/ha"
)

// Test der mitwandernden Adresse (Spezifikation 2.6): Jeder Server hält
// nacheinander eine Prüfadresse, alle anderen Mitglieder bauen eine
// TLS-Verbindung zu pdh-ha an dieser Adresse auf und prüfen, dass das
// Zertifikat zum erwarteten Server gehört. Danach wird die Prüfadresse
// abgegeben – auch nach Fehlern.

const (
	probeHoldMax = 20              // Sekunden, die ein Server die Prüfadresse höchstens hält
	probeDialMax = 3 * time.Second // Frist für die TLS-Prüfung einer Adresse
	probeRunMax  = 2 * time.Minute // Frist für den ganzen Test
)

// vipCheckResult: Ergebnis einer Richtung (Halter hält, Prüfender prüft).
// Checker "" = Fehler beim Halter selbst (z. B. nicht abgegeben).
type vipCheckResult struct {
	Holder  string `json:"holder"`
	Checker string `json:"checker"`
	OK      bool   `json:"ok"`
	Err     string `json:"err"`
}

// Anfragen und Antwort der Verbund-Aufrufe /v1/probe-*.
type probeAddrReq struct {
	Addr   string `json:"addr"`   // nur IP, z. B. 10.77.1.50
	Expect string `json:"expect"` // Kennung des Servers, der sie halten soll
}

type probeHoldReq struct {
	Addr    string `json:"addr"` // mit Präfix, z. B. 10.77.1.60/24
	Seconds int    `json:"seconds"`
}

type probeReleaseReq struct {
	Addr string `json:"addr"`
}

type probeResp struct {
	OK    bool   `json:"ok"`
	Err   string `json:"err"`
	Iface string `json:"iface,omitempty"` // probe-hold: benutzte Netzwerkkarte
}

func probeResult(err error) probeResp {
	if err != nil {
		return probeResp{Err: err.Error()}
	}
	return probeResp{OK: true}
}

var errProbeBusy = errors.New("Prüfadresse ist belegt – freie Testadresse angeben")

// errProbeIsVIP: Die eingeschaltete mitwandernde Adresse gehört dem Abgleich.
// Ein Test damit träfe auf den Arbeiter des aktiven Servers (zwei getrennte
// Steuerungen für dieselbe Adresse) und nähme sie nach dem Test womöglich weg.
var errProbeIsVIP = errors.New("Adresse ist eingeschaltet – mit freier Testadresse testen oder vorher ausschalten")

// probeIsVIP: Prüfadresse gleich der eingeschalteten mitwandernden Adresse?
func probeIsVIP(a accessConfig, p netip.Prefix) bool {
	if !a.VIPOn {
		return false
	}
	v, err := parseVIP(a.VIP)
	return err == nil && v.Addr() == p.Addr()
}

func probeBusy(err error) error {
	var dup *errDuplicate
	if errors.As(err, &dup) {
		return fmt.Errorf("%w (MAC %s)", errProbeBusy, dup.MAC)
	}
	return err
}

// probeHolding: eine gerade gehaltene Prüfadresse (höchstens eine je Server).
type probeHolding struct {
	addr netip.Prefix
	stop chan struct{} // geschlossen: sofort abgeben
	once sync.Once
	done chan struct{} // geschlossen, wenn abgegeben (oder nie angenommen)
}

func (h *probeHolding) release() { h.once.Do(func() { close(h.stop) }) }

// --- TLS-Prüfung einer Adresse ---

// clusterTLS: eigenes Knotenzertifikat und Verbund-CA aus dem Transport
// (dort wird das Zertifikat auch nach einer Erneuerung aktuell gehalten).
func (d *daemon) clusterTLS() (*tls.Certificate, *x509.CertPool, error) {
	d.mu.Lock()
	tr, ok := d.net.(*transport)
	d.mu.Unlock()
	if !ok || tr == nil {
		return nil, nil, errors.New("Verbund-Verbindung ist noch nicht bereit")
	}
	return tr.currentCert(), tr.ca, nil
}

// probeAddrLocal prüft von hier aus, ob hostport zu expect gehört.
func (d *daemon) probeAddrLocal(ctx context.Context, hostport, expect string) error {
	cert, pool, err := d.tlsFn()
	if err != nil {
		return err
	}
	d.mu.Lock()
	revoked := append([]string(nil), d.cl.Revoked...)
	d.mu.Unlock()
	return probeTLS(ctx, hostport, expect, cert, pool, revoked)
}

// probeTLS baut eine TLS-Verbindung zu hostport auf: Das Zertifikat muss von
// der Verbund-CA stammen, nicht gesperrt sein und auf expect lauten. Es wird
// nur der Handshake gemacht, danach sofort getrennt.
func probeTLS(ctx context.Context, hostport, expect string, cert *tls.Certificate, pool *x509.CertPool, revoked []string) error {
	ctx, cancel := context.WithTimeout(ctx, probeDialMax)
	defer cancel()
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	var dl net.Dialer
	raw, err := dl.DialContext(ctx, "tcp", hostport)
	if err != nil {
		return fmt.Errorf("Adresse %s nicht erreichbar: %v", host, err)
	}
	defer raw.Close()
	got := ""
	cfg := &tls.Config{
		// Die Kennung wird erst nach dem Handshake verglichen, damit die
		// Meldung sagen kann, wem die Adresse gehört. Kette (Verbund-CA) und
		// Sperrliste prüft VerifyConnection – ohne gültige Kette kein Handshake.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("kein Zertifikat")
			}
			inter := x509.NewCertPool()
			for _, c := range cs.PeerCertificates[1:] {
				inter.AddCert(c)
			}
			chains, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{
				Roots: pool, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			if err != nil {
				return fmt.Errorf("Zertifikat nicht von der Verbund-CA: %v", err)
			}
			cs.VerifiedChains = chains
			id, _, err := peerIdentity(cs, revoked)
			if err != nil {
				return err
			}
			got = id
			return nil
		},
		NextProtos: []string{alpnJoin},
		MinVersion: tls.VersionTLS13,
	}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	c := tls.Client(raw, cfg)
	if err := c.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("Adresse %s: TLS: %v", host, err)
	}
	c.Close()
	if got != expect {
		return fmt.Errorf("Adresse %s gehört zu %s statt zu %s", host, got, expect)
	}
	return nil
}

// --- Verbund-Aufrufe (Port 7480, mTLS) ---

// probeCaller: nur Server des Verbunds mit gültigem, nicht gesperrtem
// Knotenzertifikat – nicht der Zeuge, nicht dieser Server selbst.
func (d *daemon) probeCaller(r *http.Request) (string, error) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
		return "", errors.New("Nur mit Knotenzertifikat")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	id, kind, err := peerIdentity(*r.TLS, d.cl.Revoked)
	if err != nil {
		return "", err
	}
	n, ok := d.clusterNodeLocked(id)
	if !ok || kind != "node" || n.Kind != "node" || id == d.node.ID {
		return "", errors.New("Nur für Server des Verbunds")
	}
	return id, nil
}

func decodeProbe(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(v); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return false
	}
	return true
}

// probeAddrHTTP: POST /v1/probe-addr – auf allen Mitgliedern (auch Zeuge).
func (d *daemon) probeAddrHTTP(w http.ResponseWriter, r *http.Request) {
	if _, err := d.probeCaller(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	var req probeAddrReq
	if !decodeProbe(w, r, &req) {
		return
	}
	writeJSON(w, http.StatusOK, probeResult(d.probeAddr(r.Context(), req)))
}

// probeAddr: Ziel ist der Port des erwarteten Servers an der Prüfadresse.
func (d *daemon) probeAddr(ctx context.Context, req probeAddrReq) error {
	ip, err := netip.ParseAddr(strings.TrimSpace(req.Addr))
	if err != nil || !ip.Is4() {
		return fmt.Errorf("Adresse %q ungültig", req.Addr)
	}
	d.mu.Lock()
	n, ok := d.clusterNodeLocked(req.Expect)
	d.mu.Unlock()
	if !ok || n.Kind != "node" {
		return fmt.Errorf("Unbekannter Server %q", req.Expect)
	}
	return d.probeAddrFn(ctx, net.JoinHostPort(ip.String(), portOf(n.Addrs)), req.Expect)
}

// probeHoldHTTP: POST /v1/probe-hold – nur auf Servern.
func (d *daemon) probeHoldHTTP(w http.ResponseWriter, r *http.Request) {
	if _, err := d.probeCaller(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	var req probeHoldReq
	if !decodeProbe(w, r, &req) {
		return
	}
	iface, err := d.probeHold(r.Context(), req.Addr, req.Seconds)
	resp := probeResult(err)
	resp.Iface = iface
	writeJSON(w, http.StatusOK, resp)
}

// probeReleaseHTTP: POST /v1/probe-release – nur auf Servern.
func (d *daemon) probeReleaseHTTP(w http.ResponseWriter, r *http.Request) {
	if _, err := d.probeCaller(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	var req probeReleaseReq
	if !decodeProbe(w, r, &req) {
		return
	}
	writeJSON(w, http.StatusOK, probeResult(d.probeRelease(r.Context(), req.Addr)))
}

// probeHold nimmt die Prüfadresse an (mit Duplikat-Prüfung) und gibt sie nach
// seconds (höchstens probeHoldMax) selbst wieder ab – auch wenn kein
// probe-release kommt – und spätestens beim Ende des Dienstes. Nur für
// Adressen im Netz einer eigenen Karte, nie für eine schon vorhandene Adresse.
func (d *daemon) probeHold(ctx context.Context, addr string, seconds int) (string, error) {
	p, err := parseVIP(addr)
	if err != nil {
		return "", err
	}
	if seconds <= 0 || seconds > probeHoldMax {
		seconds = probeHoldMax
	}
	d.mu.Lock()
	switch {
	case !d.configured || d.removed || d.node.Kind != "node":
		d.mu.Unlock()
		return "", errors.New("Prüfadresse nur auf einem Server des Verbunds")
	case d.probeHeld != nil:
		d.mu.Unlock()
		return "", errors.New("Ein Adresstest läuft schon")
	}
	h := &probeHolding{addr: p, stop: make(chan struct{}), done: make(chan struct{})}
	d.probeHeld = h // vorgemerkt: kein zweiter Test daneben
	override, sys, base, cl := d.node.VIPInterface, d.sys, d.runCtx, d.cl
	d.mu.Unlock()
	fail := func(err error) (string, error) {
		d.mu.Lock()
		if d.probeHeld == h {
			d.probeHeld = nil
		}
		d.mu.Unlock()
		close(h.done)
		return "", err
	}
	// auch hier prüfen, nicht nur beim Auslösenden: keine Adresse eines
	// Mitglieds, nur in einem Netz beider Server
	if probeIsVIP(cl.Access, p) {
		return fail(errProbeIsVIP)
	}
	if err := validateAccess(cl, accessConfig{VIP: p.String()}); err != nil {
		return fail(err)
	}
	addrs, err := d.ifAddrsFn()
	if err != nil {
		return fail(err)
	}
	for _, a := range addrs {
		if a.Prefix.Addr() == p.Addr() {
			return fail(errProbeBusy)
		}
	}
	iface, err := pickInterface(addrs, p, override)
	if err != nil {
		return fail(err)
	}
	// pickInterface prüft bei vip_interface das Netz nicht: die Karte muss
	// selbst eine Adresse im Netz der Prüfadresse haben (sonst entstünde eine
	// Route in ein fremdes Netz)
	inNet := false
	for _, a := range addrs {
		if a.Iface == iface && p.Masked().Contains(a.Prefix.Addr()) {
			inNet = true
			break
		}
	}
	if !inNet {
		return fail(fmt.Errorf("Prüfadresse %s liegt nicht im Netz der Netzwerkkarte %s", p, iface))
	}
	v := newVIPCtl(sys, iface, p)
	// schon gesetzt (z. B. als mitwandernde Adresse): nicht anfassen, sonst
	// nähme das Abgeben sie weg
	if has, err := v.Has(ctx); err != nil {
		return fail(err)
	} else if has {
		return fail(errProbeBusy)
	}
	if err := v.Take(ctx); err != nil {
		// Take kann nach dem Setzen scheitern (Ankündigung) – sicherheitshalber
		// abgeben; nicht vorhanden gilt als Erfolg
		rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = v.Release(rctx)
		cancel()
		return fail(probeBusy(err))
	}
	if base == nil {
		base = context.Background()
	}
	after := d.holdAfter(time.Duration(seconds) * time.Second)
	go func() {
		defer close(h.done)
		select {
		case <-after:
		case <-h.stop:
		case <-base.Done():
		}
		rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := v.Release(rctx); err != nil {
			d.event("alarm", fmt.Sprintf("Prüfadresse %s nicht abgegeben: %v", p.Addr(), err))
		}
		d.mu.Lock()
		if d.probeHeld == h {
			d.probeHeld = nil
		}
		d.mu.Unlock()
	}()
	return iface, nil
}

// probeRelease gibt die Prüfadresse sofort ab und wartet darauf.
// Keine oder eine andere Prüfadresse gehalten → nichts zu tun.
func (d *daemon) probeRelease(ctx context.Context, addr string) error {
	p, err := parseVIP(addr)
	if err != nil {
		return err
	}
	d.mu.Lock()
	h := d.probeHeld
	d.mu.Unlock()
	if h == nil || h.addr.Addr() != p.Addr() {
		return nil
	}
	h.release()
	select {
	case <-h.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// stopProbeHold gibt eine gehaltene Prüfadresse beim Ende des Dienstes ab.
func (d *daemon) stopProbeHold() {
	d.mu.Lock()
	h := d.probeHeld
	d.mu.Unlock()
	if h == nil {
		return
	}
	h.release()
	select {
	case <-h.done:
	case <-time.After(15 * time.Second):
	}
}

// peerPost ruft einen Verbund-Aufruf beim Mitglied peer auf (mTLS mit dem
// eigenen Knotenzertifikat, Gegenstelle muss auf peer.ID lauten). Die
// Adressen werden der Reihe nach versucht, bis eine antwortet.
func (d *daemon) peerPost(ctx context.Context, peer clusterNode, path string, in, out any) error {
	cert, pool, err := d.tlsFn()
	if err != nil {
		return err
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{
		Certificates: []tls.Certificate{*cert}, RootCAs: pool, ServerName: peer.ID,
		NextProtos: []string{alpnJoin}, MinVersion: tls.VersionTLS13,
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Timeout: 30 * time.Second, Transport: tr}
	var lastErr error = fmt.Errorf("%s hat keine Adresse", peer.ID)
	for _, addr := range peer.Addrs {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+addr+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%s nicht erreichbar: %v", peer.ID, err)
			if ctx.Err() != nil {
				return lastErr
			}
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s: %s", peer.ID, strings.TrimSpace(string(b)))
		}
		return json.Unmarshal(b, out)
	}
	return lastErr
}

// --- Ablauf am auslösenden Server ---

// vipTestAddr: angegebene Prüfadresse, sonst die mitwandernde Adresse.
func (d *daemon) vipTestAddr(s string) (netip.Prefix, error) {
	if s = strings.TrimSpace(s); s == "" {
		d.mu.Lock()
		s = d.cl.Access.VIP
		d.mu.Unlock()
	}
	if s == "" {
		return netip.Prefix{}, errors.New("Keine Adresse eingetragen – Prüfadresse angeben (--adresse <ip/präfix>)")
	}
	return parseVIP(s)
}

// runVIPCheck testet die Prüfadresse addr in alle Richtungen (nur am
// CA-Halter oder am aktiven Server). Gelingt alles, wird der Test in der
// Verbund-Konfiguration gespeichert (Netz, Karten, Zeitpunkt).
func (d *daemon) runVIPCheck(ctx context.Context, addr netip.Prefix) ([]vipCheckResult, error) {
	d.lock()
	err := d.clusterEditable()
	if err == nil && !d.node.CAHolder && d.p.Role != ha.Active {
		err = errors.New("Den Test bitte am aktiven Server starten")
	}
	cl, self, override, sys := d.cl, d.node.ID, d.node.VIPInterface, d.sys
	d.unlock()
	if err != nil {
		return nil, err
	}
	var servers []clusterNode
	for _, n := range cl.Nodes {
		if n.Kind == "node" {
			servers = append(servers, n)
		}
	}
	if len(servers) < 2 {
		return nil, errors.New("Der Test braucht zwei Server im Verbund")
	}
	if probeIsVIP(cl.Access, addr) {
		return nil, errProbeIsVIP
	}
	if err := validateAccess(cl, accessConfig{VIP: addr.String()}); err != nil {
		return nil, err
	}
	if !d.vipChecking.CompareAndSwap(false, true) {
		return nil, errors.New("Ein Adresstest läuft schon")
	}
	defer d.vipChecking.Store(false)
	ctx, cancel := context.WithTimeout(ctx, probeRunMax)
	defer cancel()

	// Prüfadresse frei? Eigene Karten und ARP (arping hört sich selbst nicht).
	addrs, err := d.ifAddrsFn()
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		if a.Prefix.Addr() == addr.Addr() {
			return nil, errProbeBusy
		}
	}
	iface, err := pickInterface(addrs, addr, override)
	if err != nil {
		return nil, err
	}
	if err := newVIPCtl(sys, iface, addr).probeFree(ctx, addr.Addr()); err != nil {
		return nil, probeBusy(err)
	}

	d.event("info", fmt.Sprintf("Adresstest mit %s gestartet", addr))
	ifaces := map[string]string{}
	var results []vipCheckResult
	ok := true
	for _, holder := range servers {
		res, hIface := d.vipCheckHolder(ctx, cl, self, holder, addr)
		for _, r := range res {
			ok = ok && r.OK
		}
		results = append(results, res...)
		ifaces[holder.ID] = hIface
	}
	if !ok {
		d.event("warn", fmt.Sprintf("Adresstest mit %s gescheitert", addr))
		return results, nil
	}
	d.lock()
	if err := d.clusterEditable(); err != nil {
		d.unlock()
		return results, err
	}
	c := d.cl
	c.Access.Tested = &vipTested{Net: addr.Masked().String(), Ifaces: ifaces, At: d.wall()}
	c.Version++
	pl, err := d.publishClusterLocked(c)
	d.unlock()
	if err != nil {
		return results, err
	}
	pl.event("info", fmt.Sprintf("Adresstest mit %s in alle Richtungen erfolgreich – gilt für das Netz %s", addr, addr.Masked()))
	d.execute(context.Background(), pl)
	return results, nil
}

// vipCheckHolder: holder hält die Prüfadresse, alle anderen Mitglieder
// prüfen; danach abgeben, auch nach Fehlern. Liefert die Karte des Halters.
func (d *daemon) vipCheckHolder(ctx context.Context, cl clusterConfig, self string, holder clusterNode, addr netip.Prefix) ([]vipCheckResult, string) {
	hctx, cancel := context.WithTimeout(ctx, probeHoldMax*time.Second)
	defer cancel()
	var iface string
	var takeErr error
	if holder.ID == self {
		iface, takeErr = d.probeHold(hctx, addr.String(), probeHoldMax)
	} else {
		var r probeResp
		takeErr = d.peerPostFn(hctx, holder, "/v1/probe-hold", probeHoldReq{Addr: addr.String(), Seconds: probeHoldMax}, &r)
		if takeErr == nil && !r.OK {
			takeErr = errors.New(r.Err)
		}
		iface = r.Iface
	}
	var out []vipCheckResult
	for _, c := range cl.Nodes {
		if c.ID == holder.ID {
			continue
		}
		var err error
		switch {
		case takeErr != nil:
			err = fmt.Errorf("Prüfadresse auf %s nicht angenommen: %v", holder.ID, takeErr)
		case c.ID == self:
			err = d.probeAddrFn(hctx, net.JoinHostPort(addr.Addr().String(), portOf(holder.Addrs)), holder.ID)
		default:
			var r probeResp
			err = d.peerPostFn(hctx, c, "/v1/probe-addr", probeAddrReq{Addr: addr.Addr().String(), Expect: holder.ID}, &r)
			if err == nil && !r.OK {
				err = errors.New(r.Err)
			}
		}
		res := vipCheckResult{Holder: holder.ID, Checker: c.ID, OK: err == nil}
		if err != nil {
			res.Err = err.Error()
		}
		out = append(out, res)
	}
	// abgeben – eigener Kontext, damit auch ein abgelaufener Test aufräumt
	rctx, rcancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer rcancel()
	var relErr error
	if holder.ID == self {
		relErr = d.probeRelease(rctx, addr.String())
	} else {
		var r probeResp
		relErr = d.peerPostFn(rctx, holder, "/v1/probe-release", probeReleaseReq{Addr: addr.String()}, &r)
		if relErr == nil && !r.OK {
			relErr = errors.New(r.Err)
		}
	}
	if relErr != nil {
		text := fmt.Sprintf("Prüfadresse %s auf %s nicht abgegeben: %v", addr.Addr(), holder.ID, relErr)
		d.event("alarm", text)
		out = append(out, vipCheckResult{Holder: holder.ID, Err: text})
	}
	return out, iface
}

// --- Einstellung ---

// accessUpdate: PUT /v1/access; nil = unverändert.
type accessUpdate struct {
	VIP     *string `json:"vip"`
	VIPOn   *bool   `json:"vip_on"`
	HooksOn *bool   `json:"hooks_on"`
}

func onOff(b bool) string {
	if b {
		return "an"
	}
	return "aus"
}

// updateAccess ändert den Zugangsweg für den ganzen Verbund (Rechte wie
// updateSettings). Die mitwandernde Adresse lässt sich nur einschalten (oder
// eingeschaltet ändern), wenn ein Test dieses Netz und die eigene Karte abdeckt.
func (d *daemon) updateAccess(u accessUpdate) error {
	d.lock()
	if err := d.clusterEditable(); err != nil {
		d.unlock()
		return err
	}
	if !d.node.CAHolder && d.p.Role != ha.Active {
		d.unlock()
		return errors.New("Einstellungen bitte am aktiven Server ändern")
	}
	old := d.cl.Access
	a := old
	if u.VIP != nil {
		a.VIP = strings.TrimSpace(*u.VIP)
	}
	if u.VIPOn != nil {
		a.VIPOn = *u.VIPOn
	}
	if u.HooksOn != nil {
		a.HooksOn = *u.HooksOn
	}
	if err := validateAccess(d.cl, a); err != nil {
		d.unlock()
		return err
	}
	if a.VIP != "" {
		p, _ := parseVIP(a.VIP) // von validateAccess geprüft
		a.VIP = p.String()
		if a.VIPOn && (!old.VIPOn || old.VIP != a.VIP) {
			addrs, err := d.ifAddrsFn()
			iface := ""
			if err == nil {
				iface, err = pickInterface(addrs, p, d.node.VIPInterface)
			}
			if err != nil {
				d.unlock()
				return fmt.Errorf("Mitwandernde Adresse: %v", err)
			}
			if !a.Tested.covers(p, map[string]string{d.node.ID: iface}) {
				d.unlock()
				return errors.New("Bitte zuerst testen – der Test muss für dieses Netz und diese Netzwerkkarte in alle Richtungen gelungen sein")
			}
		}
	}
	if a.VIP == old.VIP && a.VIPOn == old.VIPOn && a.HooksOn == old.HooksOn {
		d.unlock()
		return nil
	}
	cl := d.cl
	cl.Access = a
	cl.Version++
	pl, err := d.publishClusterLocked(cl)
	d.unlock()
	if err != nil {
		return err
	}
	vip := a.VIP
	if vip == "" {
		vip = "keine"
	}
	pl.event("info", fmt.Sprintf("Zugangsweg geändert: Adresse %s (%s), Skripte %s", vip, onOff(a.VIPOn), onOff(a.HooksOn)))
	d.execute(context.Background(), pl)
	return nil
}
