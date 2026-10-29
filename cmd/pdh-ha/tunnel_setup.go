package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Cloudflare-Tunnel auf beiden Servern: Das PDH reicht das Token (verschlüsselt
// in der Datenbank) über den Socket an pdh-ha weiter. pdh-ha legt es in eine
// Datei, die nur root lesen darf, und richtet cloudflared.service so ein, dass
// es von dort liest. Autostart bleibt aus – gestartet wird nur am aktiven
// Server (pgActor).

const (
	tunnelEnvFile   = "/etc/pdh/ha/cloudflared.env"
	tunnelDropin    = "/etc/systemd/system/cloudflared.service.d/pdh-ha.conf"
	tunnelBaseUnit  = "/etc/systemd/system/cloudflared.service"
	tunnelMarker    = "# angelegt von pdh-ha"
	tunnelTokenKey  = "TUNNEL_TOKEN="
	tunnelFallbackB = "/usr/bin/cloudflared"
)

// tunnelPackagedUnits: Orte, an denen ein Paket (cloudflared service install
// legt die Datei dagegen unter /etc ab) die Dienstdatei mitbringen kann.
var tunnelPackagedUnits = []string{
	"/lib/systemd/system/cloudflared.service",
	"/usr/lib/systemd/system/cloudflared.service",
}

type tunnelSetup struct {
	r         runner
	envFile   string
	dropin    string
	baseUnit  string   // Grunddatei unter /etc, falls es sonst keine gibt
	packaged  []string // weitere Orte einer mitgelieferten Dienstdatei
	noSystemd bool     // Labor: keine systemctl-Aufrufe, keine Grunddatei

	mu sync.Mutex
}

func newTunnelSetup(r runner) *tunnelSetup {
	return &tunnelSetup{
		r: r, envFile: tunnelEnvFile, dropin: tunnelDropin, baseUnit: tunnelBaseUnit, packaged: tunnelPackagedUnits,
		noSystemd: os.Getenv("PDH_HA_TUNNEL_CMD") != "" || os.Getenv("PDH_HA_SERVICE_CMD") != "",
	}
}

// sumOf: SHA-256 (hex) des Tokens selbst; leer bleibt leer.
func sumOf(token string) string {
	if token == "" {
		return ""
	}
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// stored: das abgelegte Token ("" ohne Datei).
func (t *tunnelSetup) stored() string {
	raw, err := os.ReadFile(t.envFile)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), tunnelTokenKey); ok {
			return v
		}
	}
	return ""
}

// pendingFile: Merker „Einrichtung noch nicht vollständig übernommen“ (Dateien
// geschrieben, aber Neuladen oder Neustart von systemd gescheitert).
func (t *tunnelSetup) pendingFile() string { return t.envFile + ".pending" }

// markPending: Neuladen/Neustart steht noch aus; Sum() meldet dann "", damit
// das PDH das Token erneut schickt und Apply die Schritte wiederholt.
func (t *tunnelSetup) markPending() {
	_ = os.MkdirAll(filepath.Dir(t.pendingFile()), 0o755)
	_ = os.WriteFile(t.pendingFile(), nil, 0o600)
}

// Sum: Prüfsumme des abgelegten Tokens (nicht der Datei), "" ohne Datei oder
// solange die Übernahme nicht abgeschlossen ist.
func (t *tunnelSetup) Sum() string {
	if exists(t.pendingFile()) {
		return ""
	}
	return sumOf(t.stored())
}

// tunnelBinary: Programm cloudflared (das Installationsskript kann es nach
// /usr/local/bin legen).
func tunnelBinary() string {
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p
	}
	return tunnelFallbackB
}

// Installed: Programm cloudflared gefunden.
func (t *tunnelSetup) Installed() bool {
	if _, err := exec.LookPath("cloudflared"); err == nil {
		return true
	}
	_, err := os.Stat(tunnelFallbackB)
	return err == nil
}

func (t *tunnelSetup) dropinText() string {
	return "[Service]\n" +
		"EnvironmentFile=" + t.envFile + "\n" +
		"ExecStart=\n" +
		"ExecStart=" + tunnelBinary() + " --no-autoupdate tunnel run\n"
}

// baseUnitText: Mindest-Dienstdatei für einen Server, auf dem „cloudflared
// service install“ nie lief.
func baseUnitText() string {
	return tunnelMarker + "\n" +
		"[Unit]\n" +
		"Description=Cloudflare Tunnel\n" +
		"After=network-online.target\n" +
		"Wants=network-online.target\n\n" +
		"[Service]\n" +
		"Type=notify\n" +
		"Restart=on-failure\n" +
		"RestartSec=5s\n" +
		"TimeoutStartSec=0\n\n" +
		"[Install]\n" +
		"WantedBy=multi-user.target\n"
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// hasUnit: gibt es schon eine Dienstdatei für cloudflared (eigene unter /etc
// oder mitgelieferte)? Dann wird nie eine neue angelegt.
func (t *tunnelSetup) hasUnit() bool {
	if exists(t.baseUnit) {
		return true
	}
	for _, p := range t.packaged {
		if exists(p) {
			return true
		}
	}
	return false
}

// writeIfChanged schreibt nur bei abweichendem Inhalt; meldet, ob geschrieben wurde.
func writeIfChanged(path string, data []byte, perm os.FileMode) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && string(old) == string(data) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, writeFileAtomic(path, data, perm)
}

func removeFile(path string) (bool, error) {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Apply legt das Token ab (leer = Einrichtung entfernen). changed: an den
// Dateien hat sich etwas geändert. mirrored: Server mit Datenbank-Spiegel –
// dort startet pdh-ha cloudflared nur am aktiven Server, der Autostart wird
// abgeschaltet. Ohne Spiegel (Einzelserver, erster Server vor dem Beitritt
// des Partners) startet pdh-ha den Tunnel nie: dann bleibt der Autostart, wie
// er ist, sonst käme Cloudflare nach einem Neustart des Rechners nicht hoch.
func (t *tunnelSetup) Apply(ctx context.Context, token string, mirrored bool) (changed bool, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if strings.ContainsAny(token, "\r\n \t\"'\\$") {
		return false, errors.New("Tunnel-Token enthält unzulässige Zeichen")
	}
	defer func() {
		if err == nil && changed && token != "" {
			t.markPending() // bis Neuladen (und Neustart) gelungen sind
		}
		if err == nil && changed && !t.noSystemd {
			if _, e := t.r.Run(ctx, []string{"systemctl", "daemon-reload"}, nil, nil); e != nil {
				err = e
				return
			}
			if token != "" && mirrored {
				// Autostart bleibt aus: gestartet wird nur am aktiven Server
				if _, e := t.r.Run(ctx, []string{"systemctl", "disable", tunnelUnit}, nil, nil); e != nil {
					err = e
					return
				}
			}
		}
		if err == nil && token != "" {
			os.Remove(t.pendingFile())
		}
	}()
	if token == "" {
		for _, p := range []string{t.envFile, t.dropin} {
			c, err := removeFile(p)
			if err != nil {
				return changed, err
			}
			changed = changed || c
		}
		// nur eine selbst angelegte Grunddatei wieder entfernen
		if raw, e := os.ReadFile(t.baseUnit); e == nil && strings.HasPrefix(string(raw), tunnelMarker) {
			c, err := removeFile(t.baseUnit)
			if err != nil {
				return changed, err
			}
			changed = changed || c
		}
		if c, _ := removeFile(t.pendingFile()); c {
			changed = true
		}
		os.Remove(filepath.Dir(t.dropin)) // leeren Ordner aufräumen
		return changed, nil
	}
	if !t.noSystemd && !t.hasUnit() {
		c, err := writeIfChanged(t.baseUnit, []byte(baseUnitText()), 0o644)
		if err != nil {
			return false, err
		}
		changed = changed || c
	}
	c, err := writeIfChanged(t.dropin, []byte(t.dropinText()), 0o644)
	if err != nil {
		return changed, err
	}
	changed = changed || c
	c, err = writeIfChanged(t.envFile, []byte(tunnelTokenKey+token+"\n"), 0o600)
	if err != nil {
		return changed, err
	}
	// früher abgebrochene Übernahme: Neuladen und Neustart nachholen
	return changed || c || exists(t.pendingFile()), nil
}

// putTunnel: Token vom PDH übernehmen. Ein laufender Tunnel wird bei geänderter
// Einrichtung neu gestartet, bei leerem Token gestoppt; gestartet wird hier nie
// (das entscheidet der Abgleich nach Rolle). Liefert die neue Prüfsumme.
func (d *daemon) putTunnel(ctx context.Context, token string) (string, error) {
	// Entfernen: zuerst stoppen, solange die Dienstdatei noch da ist – ohne sie
	// meldet tunnelService „nicht installiert“ und könnte nie mehr stoppen.
	if token == "" && (d.tunnel.stored() != "" || exists(d.tunnel.pendingFile())) && d.tunCtl.Active(ctx) {
		if err := d.tunCtl.Stop(ctx); err != nil {
			d.event("warn", "Tunnel nicht gestoppt: "+err.Error())
			return "", err
		}
	}
	d.mu.Lock()
	mirrored := d.pgActorLocked() != nil // nur dann startet pdh-ha den Tunnel selbst
	d.mu.Unlock()
	changed, err := d.tunnel.Apply(ctx, token, mirrored)
	if err != nil {
		d.event("warn", "Tunnel-Token nicht übernommen: "+err.Error())
		return "", err
	}
	if token != "" && changed && d.tunCtl.Active(ctx) {
		if err := d.tunCtl.Restart(ctx); err != nil {
			d.tunnel.markPending() // beim nächsten Abgleich erneut versuchen
			d.event("warn", "Tunnel nach Token-Änderung nicht neu gestartet: "+err.Error())
			return "", err
		}
	}
	if changed {
		if token == "" {
			d.event("info", "Tunnel-Token entfernt")
		} else {
			d.event("info", "Tunnel-Token übernommen")
		}
	}
	return d.tunnel.Sum(), nil
}

// tunnelLive ergänzt den Status um Angaben, die einen Aufruf brauchen
// (außerhalb der Sperre). Die Art des Knotens kommt aus dem Status (unter
// der Sperre kopiert), nicht aus d.node – das ändert sich nur unter d.mu.
func (d *daemon) tunnelLive(ctx context.Context, st *apiStatus) {
	if d.tunnel == nil || !st.Configured || st.Self.Kind != "node" {
		return
	}
	st.Access.Tunnel.Installed = d.tunnel.Installed()
	st.Access.Tunnel.Running = d.tunCtl.Active(ctx)
}
