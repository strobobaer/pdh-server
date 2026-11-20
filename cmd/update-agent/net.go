package main

// Netzwerk dieses Servers (Seite „Netzwerk“ → Karte „Dieser Server“): Zustand lesen, die
// jetzige Adresse fest einstellen und die DNS-Server ändern – mit Rückfall nach 2 Minuten.
//
// Der Agent ruft scripts/pdh-static-ip.sh im Host-Namensraum per systemd-run auf (Muster
// esppdh.go): außerhalb von Agent-cgroup und -Sandbox (ProtectSystem=full), damit /etc/netplan
// beschreibbar ist und ein Neustart des Agenten den Lauf nicht abbricht. Das Skript stellt vor
// dem Schreiben einen eigenen systemd-Wecker, der ohne Bestätigung die vorige Einstellung
// wiederherstellt – auch wenn Agent oder PDH neu starten. Den Stand hält das Skript in
// /var/lib/pdh-net/state; der Agent liest ihn nur. Nach einem Neustart des ganzen Servers gibt es
// den Wecker nicht mehr: Beim Start ruft der Agent deshalb „--resume“ auf.
//
// Werte (DNS-Server, wer) gehen nur über die Umgebung an das Skript, nie in den Skripttext.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"pdh/internal/ha"
	"pdh/internal/hostnet"
)

const (
	netDockerMsg    = "Im Docker-Testsystem nicht verfügbar"
	netRollbackSecs = 120

	netApplyTimeout   = 5 * time.Minute // Sperre des Skripts (bis 120 s) plus netplan
	netConfirmTimeout = 30 * time.Second
	netResumeTimeout  = 2 * time.Minute
	netProbeTimeout   = 3 * time.Second
	netCollectTimeout = 15 * time.Second

	netPendingMsg = "Eine Änderung wartet noch auf Bestätigung"
	netHAUnreach  = "Ausfallschutz nicht erreichbar – bitte später erneut versuchen"
)

// netWatchEvery: so oft prüft der Agent, ob eine wartende Änderung längst hätte zurückgenommen
// sein müssen (Wecker gescheitert); netWatchGrace: so lange nach Fristende wartet er auf den Wecker.
var (
	netWatchEvery = 30 * time.Second
	netWatchGrace = 30 * time.Second
)

// netPending: Inhalt der Zustandsdatei des Skripts (jeder Stand, nicht nur „pending“).
type netPending struct {
	ID       string    `json:"id"`
	State    string    `json:"state"` // pending, confirmed, restored, failed
	Action   string    `json:"action"`
	By       string    `json:"by"`
	Msg      string    `json:"msg"`
	Started  time.Time `json:"started"`
	Deadline time.Time `json:"deadline"`
	Maint    bool      `json:"maint"` // Wartung des Ausfallschutzes von dieser Änderung eingeschaltet
}

// netStatusResp: GET /v1/net/status.
type netStatusResp struct {
	OK      bool             `json:"ok"`
	Error   string           `json:"error,omitempty"`
	Net     *hostnet.Summary `json:"net,omitempty"`
	Pending *netPending      `json:"pending,omitempty"`
	Running bool             `json:"running"`
	JobErr  string           `json:"job_error,omitempty"`
}

// netApplyReq: POST /v1/net/apply. Bestätigen: POST /v1/net/confirm {"by": "…"}.
type netApplyReq struct {
	Action  string   `json:"action"` // fix (jetzige Adresse fest) oder dns (DNS-Server ändern)
	DNS     []string `json:"dns"`
	By      string   `json:"by"`
	HAMaint bool     `json:"ha_maint"`
}

// netJob: Anwenden im Hintergrund, nur im Speicher. start serialisiert Prüfung und Start, damit
// zwei gleichzeitige Anfragen nie zwei Läufe starten (mu bleibt für den Status frei).
type netJob struct {
	start   sync.Mutex
	mu      sync.Mutex
	running bool
	err     string
}

func (j *netJob) get() (bool, string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.running, j.err
}

// netApplyScript: Aufruf des Skripts ohne Rückfrage. Die Werte stehen nur in der Umgebung und
// gehen als einzelne Argumente an das Skript.
const netApplyScript = `set -euo pipefail
args=(--auto --rollback "$PDH_NET_ROLLBACK" --by "$PDH_NET_BY" --if "$PDH_NET_IF" --ip "$PDH_NET_IP" --gw "$PDH_NET_GW")
if [[ "$PDH_NET_ACTION" == dns ]]; then args+=(--dns "$PDH_NET_DNS"); fi
if [[ "$PDH_NET_HA_MAINT" == 1 ]]; then args+=(--ha-maint); fi
exec systemd-run --wait --pipe --collect --quiet --unit="pdh-static-ip-apply-$(date +%s)-$$" bash "$PDH_NET_SCRIPT" "${args[@]}"
`

const netConfirmScript = `set -euo pipefail
exec systemd-run --wait --pipe --collect --quiet --unit="pdh-static-ip-confirm-$(date +%s)-$$" bash "$PDH_NET_SCRIPT" --confirm --by "$PDH_NET_BY"
`

const netResumeScript = `set -euo pipefail
exec systemd-run --wait --pipe --collect --quiet --unit="pdh-static-ip-resume-$(date +%s)-$$" bash "$PDH_NET_SCRIPT" --resume
`

func (a *updateAgent) netStateFile() string {
	dir := a.netStateDir
	if dir == "" {
		dir = "/var/lib/pdh-net"
	}
	return filepath.Join(dir, "state")
}

func (a *updateAgent) netScriptPath() string {
	return a.repoDir + "/scripts/pdh-static-ip.sh"
}

func (a *updateAgent) netRunFn() func(context.Context, string, map[string]string) (string, error) {
	if a.netRun != nil {
		return a.netRun
	}
	return func(ctx context.Context, script string, env map[string]string) (string, error) {
		out, err := a.hostCommandEnv(ctx, false, script, env).CombinedOutput()
		return string(out), err
	}
}

func (a *updateAgent) netProbeFn() func(context.Context, string) error {
	if a.netProbe != nil {
		return a.netProbe
	}
	return hostnet.ProbeDNS
}

// netSummary: Netzwerkzustand dieses Servers; exclude = gemeinsame Adressen des Ausfallschutzes.
func (a *updateAgent) netSummary(ctx context.Context, exclude []netip.Addr) hostnet.Summary {
	if a.netCollect != nil {
		return a.netCollect(ctx)
	}
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		out, err := a.run(ctx, name, args...)
		return []byte(out), err
	}
	return hostnet.Collect(ctx, run, os.ReadFile, exclude...)
}

// netReadState: Zustandsdatei des Skripts (nil = keine).
func (a *updateAgent) netReadState() *netPending {
	b, err := os.ReadFile(a.netStateFile())
	if err != nil {
		return nil
	}
	return parseNetState(b)
}

// parseNetState liest Zeilen „schlüssel=wert“; unbekannte Schlüssel und Zeilen ohne „=“ fallen
// weg, Zeiten sind Unix-Sekunden. Ohne Schlüssel state: nil.
func parseNetState(b []byte) *netPending {
	var p netPending
	unix := func(v string) time.Time {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return time.Time{}
		}
		return time.Unix(n, 0).UTC()
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimRight(line, "\r"), "=")
		if !ok || k == "" {
			continue
		}
		switch k {
		case "id":
			p.ID = v
		case "state":
			p.State = v
		case "action":
			p.Action = v
		case "by":
			p.By = v
		case "msg":
			p.Msg = v
		case "started":
			p.Started = unix(v)
		case "deadline":
			p.Deadline = unix(v)
		case "maint":
			p.Maint = v == "1"
		}
	}
	if p.State == "" {
		return nil
	}
	return &p
}

// netBy: Name für Zustand und Protokoll – ohne Steuerzeichen und „=“, höchstens 80 Zeichen.
func netBy(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '=' {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 80 {
		s = strings.TrimSpace(string(r[:80]))
	}
	return s
}

var netANSI = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

// netLastLine: letzte nicht leere Zeile der Ausgabe (die Meldung des Skripts), sonst der Fehler.
func netLastLine(out string, err error) string {
	lines := strings.Split(netANSI.ReplaceAllString(out, ""), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

// netHA: was der Agent vor einer Aktion von pdh-ha wissen muss.
type netHA struct {
	Configured bool
	Active     bool // dieser Server ist der aktive (Wartung während der Aktion)
	Maint      bool
	Busy       bool // pdh-ha führt eine Aktion aus (Rollentausch, Übernahme …)
	Rejoin     bool
	Update     bool // Update im Verbund offen
	Exclude    []netip.Addr
}

// netHAInfo: Stand laut pdh-ha. Ohne node.json (kein Verbund) leer und ohne Fehler; eingerichtet,
// aber nicht lesbar: Fehler – ohne die gemeinsame Adresse darf nichts geschrieben werden.
func (a *updateAgent) netHAInfo(ctx context.Context) (netHA, error) {
	if _, err := os.Stat(filepath.Join(haEtcDir(), "node.json")); errors.Is(err, os.ErrNotExist) {
		return netHA{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var st struct {
		Configured bool   `json:"configured"`
		Error      string `json:"error"`
		Maint      bool   `json:"maint"`
		Busy       bool   `json:"busy"`
		Self       struct {
			Role     string `json:"role"`
			VIP6Held string `json:"vip6_held"`
		} `json:"self"`
		Access struct {
			VIP  string `json:"vip"`
			VIP6 string `json:"vip6"`
		} `json:"access"`
		Update struct {
			Active bool `json:"active"`
		} `json:"update"`
	}
	code, err := haDo(ctx, http.MethodGet, "/v1/status", nil, &st)
	switch {
	case err != nil:
		return netHA{}, err
	case code != http.StatusOK:
		return netHA{}, fmt.Errorf("pdh-ha: HTTP %d", code)
	case st.Error != "":
		return netHA{}, errors.New(st.Error)
	case !st.Configured:
		return netHA{}, nil
	}
	info := netHA{Configured: true, Active: st.Self.Role == string(ha.Active), Maint: st.Maint, Busy: st.Busy,
		Rejoin: st.Self.Role == string(ha.Rejoin), Update: st.Update.Active}
	add := func(a netip.Addr) {
		for _, x := range info.Exclude {
			if x == a {
				return
			}
		}
		info.Exclude = append(info.Exclude, a)
	}
	if p, err := netip.ParsePrefix(st.Access.VIP); err == nil {
		add(p.Addr())
	} else if ad, err := netip.ParseAddr(st.Access.VIP); err == nil {
		add(ad)
	}
	for _, s := range []string{st.Self.VIP6Held, st.Access.VIP6} {
		if ad, err := netip.ParseAddr(s); err == nil {
			add(ad)
		}
	}
	return info, nil
}

func netRefuse(w http.ResponseWriter, msg string) {
	writeMountJSON(w, http.StatusOK, map[string]any{"ok": false, "error": msg})
}

// netStatusHandler: GET /v1/net/status → Netzwerkzustand, Stand der Änderung, laufender Auftrag.
func (a *updateAgent) netStatusHandler(w http.ResponseWriter, r *http.Request) {
	if a.mode != "systemd" {
		netRefuse(w, netDockerMsg)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), netCollectTimeout)
	defer cancel()
	info, _ := a.netHAInfo(ctx) // nur Anzeige: ohne pdh-ha ohne Ausschluss
	sum := a.netSummary(ctx, info.Exclude)
	running, jobErr := a.netJob.get()
	writeMountJSON(w, http.StatusOK, netStatusResp{OK: true, Net: &sum, Pending: a.netReadState(), Running: running, JobErr: jobErr})
}

// netValidDNS: ein bis drei verschiedene IPv4-Adressen (hostnet.ValidDNS).
func netValidDNS(list []string) ([]string, error) {
	if len(list) < 1 || len(list) > 3 {
		return nil, errors.New("Bitte ein bis drei DNS-Server angeben")
	}
	var out []string
	for _, s := range list {
		s = strings.TrimSpace(s)
		if !hostnet.ValidDNS(s) {
			return nil, fmt.Errorf("Ungültiger DNS-Server: %q", s)
		}
		s = netip.MustParseAddr(s).String()
		for _, x := range out {
			if x == s {
				return nil, fmt.Errorf("DNS-Server %s doppelt angegeben", s)
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// netApplyHandler: POST /v1/net/apply → prüft und startet das Skript im Hintergrund, sofort
// {"ok":true,"running":true}. Abgelehnt wird alles, was nicht eindeutig sicher ist.
func (a *updateAgent) netApplyHandler(w http.ResponseWriter, r *http.Request) {
	if a.mode != "systemd" {
		netRefuse(w, netDockerMsg)
		return
	}
	var req netApplyReq
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			netRefuse(w, "ungültiger Rumpf")
			return
		}
	}
	var dns []string
	switch req.Action {
	case "fix":
		if len(req.DNS) > 0 {
			netRefuse(w, "DNS-Server nur bei „DNS-Server ändern“")
			return
		}
	case "dns":
		var err error
		if dns, err = netValidDNS(req.DNS); err != nil {
			netRefuse(w, err.Error())
			return
		}
	default:
		netRefuse(w, "Unbekannte Aktion")
		return
	}
	a.mu.RLock()
	updating := a.status.Running
	a.mu.RUnlock()
	if updating || a.singleJobActive() {
		netRefuse(w, "Ein PDH-Update läuft – bitte danach erneut versuchen")
		return
	}

	j := &a.netJob
	j.start.Lock()
	defer j.start.Unlock()
	if running, _ := j.get(); running {
		netRefuse(w, netPendingMsg)
		return
	}
	if p := a.netReadState(); p != nil && p.State == "pending" {
		netRefuse(w, netPendingMsg)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), netCollectTimeout)
	defer cancel()
	info, err := a.netHAInfo(ctx)
	switch {
	case err != nil:
		netRefuse(w, netHAUnreach)
		return
	case info.Maint:
		netRefuse(w, "Die Wartung des Ausfallschutzes ist an – bitte danach erneut versuchen")
		return
	case info.Busy || info.Rejoin:
		netRefuse(w, "Ein Rollentausch läuft – bitte danach erneut versuchen")
		return
	case info.Update:
		netRefuse(w, "Ein PDH-Update läuft – bitte danach erneut versuchen")
		return
	}
	sum := a.netSummary(ctx, info.Exclude)
	if !sum.Netplan {
		netRefuse(w, "Fest einstellen nur auf Ubuntu-Servern mit netplan")
		return
	}
	if sum.Iface == "" || sum.Addr == "" || sum.Gateway == "" {
		netRefuse(w, "Adresse oder Gateway dieses Servers nicht erkannt – es wurde nichts geändert")
		return
	}
	probe := a.netProbeFn()
	for _, s := range dns {
		pctx, pcancel := context.WithTimeout(context.Background(), netProbeTimeout)
		err := probe(pctx, s)
		pcancel()
		if err != nil {
			netRefuse(w, err.Error()+" – es wurde nichts geändert")
			return
		}
	}

	by := netBy(req.By)
	maint := req.HAMaint || info.Active // am aktiven Server immer, auch wenn das PDH es nicht verlangt
	env := map[string]string{
		"PDH_NET_ACTION": req.Action, "PDH_NET_DNS": strings.Join(dns, ","), "PDH_NET_BY": by,
		"PDH_NET_HA_MAINT": "0", "PDH_NET_ROLLBACK": strconv.Itoa(netRollbackSecs), "PDH_NET_SCRIPT": a.netScriptPath(),
		"PDH_NET_IF": sum.Iface, "PDH_NET_IP": sum.Addr, "PDH_NET_GW": sum.Gateway,
	}
	if maint {
		env["PDH_NET_HA_MAINT"] = "1"
	}
	j.mu.Lock()
	j.running, j.err = true, ""
	j.mu.Unlock()
	log.Printf("netzwerk: %s von %q gestartet (%s über %s, DNS %v, Wartung %v)", req.Action, by, sum.Addr, sum.Iface, dns, maint)
	run := a.netRunFn()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), netApplyTimeout)
		defer cancel()
		out, err := run(ctx, netApplyScript, env)
		msg := ""
		if err != nil {
			msg = netLastLine(out, err)
			log.Printf("netzwerk: %s gescheitert: %s", req.Action, msg)
		} else {
			log.Printf("netzwerk: %s angewendet, wartet auf Bestätigung", req.Action)
		}
		j.mu.Lock()
		j.running, j.err = false, msg
		j.mu.Unlock()
	}()
	writeMountJSON(w, http.StatusOK, map[string]any{"ok": true, "running": true})
}

// netConfirmHandler: POST /v1/net/confirm {"by":"…"} → Einstellung behalten (synchron).
func (a *updateAgent) netConfirmHandler(w http.ResponseWriter, r *http.Request) {
	if a.mode != "systemd" {
		netRefuse(w, netDockerMsg)
		return
	}
	var req struct {
		By string `json:"by"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			netRefuse(w, "ungültiger Rumpf")
			return
		}
	}
	if running, _ := a.netJob.get(); running {
		netRefuse(w, "Die Änderung wird noch angewendet – bitte gleich erneut bestätigen")
		return
	}
	by := netBy(req.By)
	ctx, cancel := context.WithTimeout(r.Context(), netConfirmTimeout)
	defer cancel()
	out, err := a.netRunFn()(ctx, netConfirmScript, map[string]string{"PDH_NET_SCRIPT": a.netScriptPath(), "PDH_NET_BY": by})
	if err != nil {
		msg := netLastLine(out, err)
		log.Printf("netzwerk: Bestätigung von %q abgelehnt: %s", by, msg)
		netRefuse(w, msg)
		return
	}
	log.Printf("netzwerk: Einstellung von %q bestätigt", by)
	writeMountJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// netResume: beim Start des Agenten (systemd). Wartet eine Änderung, ruft er das Skript mit
// --resume: Frist vorbei → Rückfall, sonst Wecker neu stellen (nach einem Neustart des Servers
// fehlt er). Danach prüft er alle netWatchEvery, ob eine Änderung längst hätte zurückgenommen
// sein müssen (Wecker gescheitert), und holt das nach. Kehrt zurück, wenn ctx endet.
func (a *updateAgent) netResume(ctx context.Context) {
	if a.mode != "systemd" {
		return
	}
	resume := func() {
		rctx, cancel := context.WithTimeout(ctx, netResumeTimeout)
		defer cancel()
		out, err := a.netRunFn()(rctx, netResumeScript, map[string]string{"PDH_NET_SCRIPT": a.netScriptPath()})
		if err != nil {
			log.Printf("netzwerk: Wiederaufnahme gescheitert: %s", netLastLine(out, err))
			return
		}
		log.Printf("netzwerk: Wiederaufnahme: %s", netLastLine(out, nil))
	}
	if p := a.netReadState(); p != nil && p.State == "pending" {
		resume()
	}
	t := time.NewTicker(netWatchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if running, _ := a.netJob.get(); running {
			continue
		}
		// ohne gültige Frist nimmt das Skript die Änderung ebenfalls zurück
		if p := a.netReadState(); p != nil && p.State == "pending" && (p.Deadline.IsZero() || time.Now().After(p.Deadline.Add(netWatchGrace))) {
			log.Printf("netzwerk: Frist der Änderung %s seit %s vorbei – Rückfall wird nachgeholt", p.ID, time.Since(p.Deadline).Round(time.Second))
			resume()
		}
	}
}
