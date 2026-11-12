package web

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"pdh/internal/integrations/cloudflare"
)

// „Full (strict)“ (optional): Erst wenn für alle geführten Namen ein gültiges Zertifikat der
// echten Umgebung eingerichtet ist, stellt ein Administrator per Knopf die Ziele im Tunnel
// und die Verschlüsselung der Domain um – und kann sie auf den früheren Stand zurückstellen.
// Nichts davon läuft von selbst oder im Hintergrund.

// strictBefore ist der Stand vor der Umstellung (JSON in keyCertStrictBefore).
type strictBefore struct {
	SSL     string               `json:"ssl"`
	Ingress []cloudflare.Ingress `json:"ingress"`
}

const certStrictService = "https://localhost:443"

// certStrictIngress stellt die Ziele der geführten Namen auf https://localhost:443 mit
// Prüfung um. Andere Ziele (und weitere Angaben in deren originRequest) bleiben unverändert;
// Reihenfolge und Auffangregel auch.
func certStrictIngress(list []cloudflare.Ingress, names []string) []cloudflare.Ingress {
	out := make([]cloudflare.Ingress, len(list))
	for i, in := range list {
		out[i] = in
		if in.Hostname == "" || !certNameIn(names, in.Hostname) {
			continue
		}
		o := map[string]json.RawMessage{}
		if len(in.OriginRequest) > 0 {
			_ = json.Unmarshal(in.OriginRequest, &o)
			if o == nil {
				o = map[string]json.RawMessage{}
			}
		}
		host, _ := json.Marshal(in.Hostname)
		o["originServerName"] = host
		o["httpHostHeader"] = host
		o["noTLSVerify"] = json.RawMessage("false")
		raw, _ := json.Marshal(o)
		out[i].Service = certStrictService
		out[i].OriginRequest = raw
	}
	return out
}

func certNameIn(names []string, n string) bool {
	for _, x := range names {
		if strings.EqualFold(x, n) {
			return true
		}
	}
	return false
}

// certStrictReady: mindestens ein Name, alle gültig, keiner aus der Testumgebung, keiner
// abgelaufen, alle auf dem Server eingerichtet.
func certStrictReady(rows []certRow, now time.Time) bool {
	if len(rows) == 0 {
		return false
	}
	for _, r := range rows {
		if r.State != certStateGueltig || r.Staging || r.InstalledFP == "" || !r.NotAfter.After(now) {
			return false
		}
	}
	return true
}

// certLocalCheck: TLS-Verbindung zum eigenen Server (127.0.0.1:443) mit dem Servernamen und
// Prüfung gegen die Wurzelzertifikate des Systems – so wie der Tunnel es später tut.
func certLocalCheck(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	d := tls.Dialer{Config: &tls.Config{ServerName: name, MinVersion: tls.VersionTLS12}}
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", "443"))
	if err != nil {
		return err
	}
	return c.Close()
}

// strictDeps sind die Teile der Umstellung; im Betrieb aus strictDepsLive, in Tests im Speicher.
type strictDeps struct {
	blocked    func() bool
	rows       func(ctx context.Context) ([]certRow, error)
	access     func(ctx context.Context) (*cfAccess, error)
	localCheck func(ctx context.Context, name string) error
	getSetting func(ctx context.Context, key string) string
	setSetting func(ctx context.Context, key, value string) error
	notify     func(ctx context.Context, text string)
	now        func() time.Time
}

func (h *Handler) strictDepsLive() strictDeps {
	return strictDeps{
		blocked:    func() bool { return h.Mirror().On || h.MaintLocked() },
		rows:       h.certList,
		access:     h.cfAccess,
		localCheck: certLocalCheck,
		getSetting: h.certSetting,
		setSetting: h.certPutSetting,
		notify: func(ctx context.Context, text string) {
			if h.db != nil {
				h.notifyAdmins(ctx, text)
			}
		},
		now: time.Now,
	}
}

// certStrictOn stellt Tunnel-Ziele und Verschlüsselung der Domain auf Full (strict) um.
func (h *Handler) certStrictOn(ctx context.Context) error {
	return certStrictOn(ctx, h.strictDepsLive())
}

// certStrictOff stellt den vor der Umstellung gemerkten Stand wieder her.
func (h *Handler) certStrictOff(ctx context.Context) error {
	return certStrictOff(ctx, h.strictDepsLive())
}

// done protokolliert das Ergebnis und meldet eine gelungene Änderung den Administratoren.
func (d strictDeps) done(ctx context.Context, what string, err error) error {
	l := componentLog("zertifikate")
	if err != nil {
		l.Warn().Err(err).Msg("full strict")
		return err
	}
	l.Info().Msg(what)
	if d.notify != nil {
		d.notify(ctx, "🔒 **Zertifikate / Cloudflare geändert:** "+what)
	}
	return nil
}

// cfState holt Zone und Tunnel-Einstellungen.
func (d strictDeps) cfState(ctx context.Context) (a *cfAccess, zoneID string, cfg *cloudflare.TunnelConfig, err error) {
	a, err = d.access(ctx)
	if err != nil {
		return nil, "", nil, err
	}
	if a.account == "" || a.tunnel == "" {
		return nil, "", nil, errors.New("Tunnel-Token fehlt: Zuerst in den Cloudflare-Einstellungen den Tunnel eintragen")
	}
	zone, err := a.client.ZoneByName(ctx, a.zone)
	if err != nil {
		return nil, "", nil, err
	}
	cfg, err = a.client.TunnelConfig(ctx, a.account, a.tunnel)
	if err != nil {
		return nil, "", nil, err
	}
	return a, zone.ID, cfg, nil
}

func certStrictOn(ctx context.Context, d strictDeps) error {
	if d.blocked != nil && d.blocked() {
		return errors.New("Am Spiegelserver und während der Wartung wird nichts umgestellt")
	}
	rows, err := d.rows(ctx)
	if err != nil {
		return err
	}
	if !certStrictReady(rows, d.now()) {
		return errors.New("Erst wenn alle Namen ein gültiges Zertifikat der echten Umgebung haben und eingerichtet sind, kann auf Full (strict) umgestellt werden")
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}
	for _, n := range names {
		if err := d.localCheck(ctx, n); err != nil {
			return fmt.Errorf("Prüfung auf dem Server für %s gescheitert, bei Cloudflare wurde nichts geändert: %v", n, err)
		}
	}
	a, zoneID, cfg, err := d.cfState(ctx)
	if err != nil {
		return err
	}
	ssl, err := a.client.Setting(ctx, zoneID, "ssl")
	if err != nil {
		return err
	}
	cur := strictBefore{SSL: ssl, Ingress: cfg.Ingress}
	// Ist schon ein früherer Stand gemerkt (Full strict ist an), bleibt er der Rückweg.
	created := false
	if d.getSetting(ctx, keyCertStrictBefore) == "" {
		raw, err := json.Marshal(cur)
		if err != nil {
			return err
		}
		if err := d.setSetting(ctx, keyCertStrictBefore, string(raw)); err != nil {
			return fmt.Errorf("Der bisherige Stand konnte nicht gemerkt werden, bei Cloudflare wurde nichts geändert: %v", err)
		}
		created = true
	}
	bg := context.WithoutCancel(ctx)
	forget := func() {
		if created {
			_ = d.setSetting(bg, keyCertStrictBefore, "")
		}
	}
	cfg.Ingress = certStrictIngress(cfg.Ingress, names)
	if err := a.client.PutTunnelConfig(ctx, a.account, a.tunnel, cfg); err != nil {
		// Unklar, ob der Tausch angekommen ist; sicherheitshalber den alten Stand zurückschreiben.
		cfg.Ingress = cur.Ingress
		if rerr := a.client.PutTunnelConfig(bg, a.account, a.tunnel, cfg); rerr != nil {
			return d.done(ctx, "", fmt.Errorf("Tunnel-Ziele umstellen gescheitert (%v); Zurücksetzen ebenfalls gescheitert (%v) – bitte bei Cloudflare prüfen", err, rerr))
		}
		forget()
		return d.done(ctx, "", fmt.Errorf("Tunnel-Ziele umstellen gescheitert (%v); der vorherige Stand ist wiederhergestellt", err))
	}
	if err := a.client.SetSetting(ctx, zoneID, "ssl", "strict"); err != nil {
		cfg.Ingress = cur.Ingress
		if rerr := a.client.PutTunnelConfig(bg, a.account, a.tunnel, cfg); rerr != nil {
			return d.done(ctx, "", fmt.Errorf("Verschlüsselung auf Full (strict) setzen gescheitert (%v); Tunnel-Ziele zurücksetzen ebenfalls gescheitert (%v) – bitte bei Cloudflare prüfen", err, rerr))
		}
		forget()
		return d.done(ctx, "", fmt.Errorf("Verschlüsselung auf Full (strict) setzen gescheitert (%v); die Tunnel-Ziele sind wieder auf dem vorherigen Stand", err))
	}
	return d.done(ctx, "Tunnel und Domain auf Full (strict) umgestellt ("+strings.Join(names, ", ")+")", nil)
}

func certStrictOff(ctx context.Context, d strictDeps) error {
	if d.blocked != nil && d.blocked() {
		return errors.New("Am Spiegelserver und während der Wartung wird nichts zurückgestellt")
	}
	raw := d.getSetting(ctx, keyCertStrictBefore)
	var before strictBefore
	if raw == "" || json.Unmarshal([]byte(raw), &before) != nil || before.SSL == "" {
		return errors.New("Es ist kein früherer Stand gemerkt – es gibt nichts zurückzustellen")
	}
	a, zoneID, cfg, err := d.cfState(ctx)
	if err != nil {
		return err
	}
	cfg.Ingress = before.Ingress
	if err := a.client.PutTunnelConfig(ctx, a.account, a.tunnel, cfg); err != nil {
		return d.done(ctx, "", fmt.Errorf("Tunnel-Ziele zurückstellen gescheitert (%v); der gemerkte Stand bleibt erhalten", err))
	}
	if err := a.client.SetSetting(ctx, zoneID, "ssl", before.SSL); err != nil {
		return d.done(ctx, "", fmt.Errorf("Verschlüsselung zurückstellen gescheitert (%v); der gemerkte Stand bleibt erhalten", err))
	}
	if err := d.setSetting(ctx, keyCertStrictBefore, ""); err != nil {
		return d.done(ctx, "", fmt.Errorf("Zurückgestellt, aber der gemerkte Stand ließ sich nicht löschen (%v)", err))
	}
	return d.done(ctx, "Tunnel und Domain auf den früheren Stand zurückgestellt (Verschlüsselung: "+before.SSL+")", nil)
}
