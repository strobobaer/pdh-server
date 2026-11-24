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

	netPendingMsg  = "Eine Änderung wartet noch auf Bestätigung"
	netHAUnreach   = "Ausfallschutz nicht erreichbar – bitte später erneut versuchen"
	netNotWaiting  = "Keine Änderung wartet auf Bestätigung"
	netStillApply  = "Die Änderung wird noch angewendet – bitte gleich erneut bestätigen"
	netRunWaitStop = 10 * time.Second // nach Ablauf der Frist höchstens so lange auf die Ausgabe warten
	netLogMaxAge   = 24 * time.Hour   // ältere Ergebnisse schreibt die Wache nicht mehr ins Protokoll

	// Sperrgründe – wörtlich wie auf der Karte (internal/web/network_server.go)
	netMsgMaint    = "Die Wartung des Ausfallschutzes ist an – bitte zuerst beenden."
	netMsgSwap     = "Ein Rollentausch läuft – bitte warten, bis er fertig ist."
	netMsgPeerGone = "Der Partner-Server ist gerade nicht erreichbar – am aktiven Server erst ändern, wenn beide Server sich sehen."
)

// netWatchEvery: so oft prüft der Agent, ob eine wartende Änderung längst hätte zurückgenommen
// sein müssen (Wecker gescheitert) und ob eine Wartung noch aus muss; netWatchGrace: so lange nach
// Fristende wartet er auf den Wecker; netMaintMaxWait: längster Abstand der Versuche „Wartung aus“.
var (
	netWatchEvery   = 30 * time.Second
	netWatchGrace   = 30 * time.Second
	netMaintMaxWait = 10 * time.Minute
)

// netPending: Inhalt der Zustandsdatei des Skripts (jeder Stand, nicht nur „pending“). Die Karte
// bestätigt mit ID; MaintOffPending: die Wartung des Ausfallschutzes aus dieser Änderung ist noch
// an und wird nachgeholt (MaintMsg nennt den Grund).
type netPending struct {
	ID              string    `json:"id"`
	State           string    `json:"state"` // pending, confirmed, restored, failed
	Action          string    `json:"action"`
	By              string    `json:"by"`
	Msg             string    `json:"msg"`
	Started         time.Time `json:"started,omitzero"`
	Deadline        time.Time `json:"deadline,omitzero"`
	Applied         bool      `json:"applied"` // netplan apply gelungen – erst dann lässt sich bestätigen
	Maint           bool      `json:"maint"`   // Wartung des Ausfallschutzes von dieser Änderung eingeschaltet
	MaintOffPending bool      `json:"maint_off_pending"`
	MaintMsg        string    `json:"maint_msg,omitempty"`
	ConfirmedBy     string    `json:"confirmed_by,omitempty"`
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

// netApplyReq: POST /v1/net/apply. Bestätigen: POST /v1/net/confirm {"id": "…", "by": "…"}.
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

	maintNext time.Time     // nächster Versuch „Wartung aus“ (Wache)
	maintWait time.Duration // Abstand nach dem letzten Fehlschlag
	logged    string        // Kennung der Änderung, deren Ergebnis schon im Protokoll steht
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
exec systemd-run --wait --pipe --collect --quiet --unit="pdh-static-ip-confirm-$(date +%s)-$$" bash "$PDH_NET_SCRIPT" --confirm --id "$PDH_NET_CONFIRM_ID" --by "$PDH_NET_BY"
`

const netResumeScript = `set -euo pipefail
exec systemd-run --wait --pipe --collect --quiet --unit="pdh-static-ip-resume-$(date +%s)-$$" bash "$PDH_NET_SCRIPT" --resume
`

const netMaintOffScript = `set -euo pipefail
exec systemd-run --wait --pipe --collect --quiet --unit="pdh-static-ip-maint-off-$(date +%s)-$$" bash "$PDH_NET_SCRIPT" --maint-off
`

// netIDRe: Kennung einer Änderung (vom Skript: Datum und Prozessnummer).
var netIDRe = regexp.MustCompile(`^[0-9A-Za-z-]{1,64}$`)

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
		cmd := a.hostCommandEnv(ctx, false, script, env)
		// Der systemd-Dienst hält die Ausgabe (--pipe) offen, auch wenn der Aufrufer nach Ablauf
		// der Frist beendet ist: nicht unbegrenzt auf das Ende der Ausgabe warten.
		cmd.WaitDelay = netRunWaitStop
		out, err := cmd.CombinedOutput()
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
// weg, Zeiten sind Unix-Sekunden. Ohne Schlüssel state: nil. MaintOffPending folgt derselben
// Regel wie maint_open im Skript: Die Änderung wartet nicht mehr, und ihre Wartung ist noch
// nicht geklärt (maint_off=pending, oder eingeschaltet/vorgemerkt ohne Ergebnis).
func parseNetState(b []byte) *netPending {
	var p netPending
	maint, maintOff := "", ""
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
		case "applied":
			p.Applied = v == "1"
		case "maint":
			maint = v
		case "maint_off":
			maintOff = v
		case "maint_msg":
			p.MaintMsg = v
		case "confirmed_by":
			p.ConfirmedBy = v
		}
	}
	if p.State == "" {
		return nil
	}
	p.Maint = maint == "1"
	p.MaintOffPending = p.State != "pending" &&
		(maintOff == "pending" || maintOff == "" && (maint == "1" || maint == "on-requested"))
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
	Fenced     bool // dieser Server hat sich abgeschaltet
	Update     bool // Update im Verbund offen
	Cluster    bool // Verbund mit Partner (Modus nicht „einzel“)
	PeerSeen   bool // es gibt einen Partner-Server, und jeder wird gerade gesehen (Zeuge zählt nicht)
	Exclude    []netip.Addr
}

// refusal: Sperrgrund laut pdh-ha ("" = frei), Wortlaut wie auf der Karte. Am aktiven Server im
// Verbund nur, wenn er den Partner sieht: Die Wartung gilt nur mit einem Partner, der zustimmt,
// und bei längerem Gateway-Ausfall während der Änderung könnte er sich sonst abschalten.
func (h netHA) refusal() string {
	switch {
	case h.Maint:
		return netMsgMaint
	case h.Busy || h.Rejoin || h.Fenced:
		return netMsgSwap
	case h.Update:
		return "Ein PDH-Update läuft – bitte danach erneut versuchen"
	case h.Active && h.Cluster && !h.PeerSeen:
		return netMsgPeerGone
	}
	return ""
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
		Mode       string `json:"mode"`
		Maint      bool   `json:"maint"`
		Busy       bool   `json:"busy"`
		Self       struct {
			Role     string `json:"role"`
			VIP6Held string `json:"vip6_held"`
		} `json:"self"`
		Peers []struct {
			Kind string `json:"kind"`
			Seen bool   `json:"seen"`
		} `json:"peers"`
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
		Rejoin: st.Self.Role == string(ha.Rejoin), Fenced: st.Self.Role == string(ha.Fenced), Update: st.Update.Active,
		Cluster: st.Mode != string(ha.Single), PeerSeen: true}
	nodes := 0
	for _, p := range st.Peers {
		if p.Kind == "node" {
			nodes++
			info.PeerSeen = info.PeerSeen && p.Seen
		}
	}
	info.PeerSeen = info.PeerSeen && nodes > 0
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
	// gemeinsame Sperre für die Laufzeit des Skripts; bei jeder Ablehnung wieder frei
	release, busy := a.lockAcquire(lockNet)
	if busy != "" {
		netRefuse(w, lockBusyMsg(busy))
		return
	}
	held := true
	defer func() {
		if held {
			release()
		}
	}()

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
	} else if p != nil && p.MaintOffPending {
		netRefuse(w, "Die Wartung des Ausfallschutzes aus der vorigen Änderung wird noch ausgeschaltet – bitte danach erneut versuchen")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), netCollectTimeout)
	defer cancel()
	info, err := a.netHAInfo(ctx)
	if err != nil {
		netRefuse(w, netHAUnreach)
		return
	}
	if why := info.refusal(); why != "" {
		netRefuse(w, why)
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
	held = false // das Skript gibt die Sperre am Ende frei
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
		release()
		j.mu.Lock()
		j.running, j.err = false, msg
		j.mu.Unlock()
	}()
	writeMountJSON(w, http.StatusOK, map[string]any{"ok": true, "running": true})
}

// netConfirmHandler: POST /v1/net/confirm {"id":"…","by":"…"} → Einstellung behalten (synchron).
// Bestätigt wird nur die Änderung mit dieser Kennung, solange sie wartet und angewendet ist; das
// Skript prüft dasselbe noch einmal unter der Sperre.
func (a *updateAgent) netConfirmHandler(w http.ResponseWriter, r *http.Request) {
	if a.mode != "systemd" {
		netRefuse(w, netDockerMsg)
		return
	}
	var req struct {
		ID string `json:"id"`
		By string `json:"by"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			netRefuse(w, "ungültiger Rumpf")
			return
		}
	}
	if !netIDRe.MatchString(req.ID) {
		netRefuse(w, "Kennung der Änderung fehlt")
		return
	}
	if running, _ := a.netJob.get(); running {
		netRefuse(w, netStillApply)
		return
	}
	switch p := a.netReadState(); {
	case p == nil || p.State != "pending":
		netRefuse(w, netNotWaiting)
		return
	case p.ID != req.ID:
		netRefuse(w, "Diese Änderung wartet nicht mehr auf Bestätigung")
		return
	case !p.Applied:
		netRefuse(w, netStillApply)
		return
	}
	by := netBy(req.By)
	ctx, cancel := context.WithTimeout(r.Context(), netConfirmTimeout)
	defer cancel()
	out, err := a.netRunFn()(ctx, netConfirmScript, map[string]string{
		"PDH_NET_SCRIPT": a.netScriptPath(), "PDH_NET_BY": by, "PDH_NET_CONFIRM_ID": req.ID,
	})
	if err != nil {
		msg := netLastLine(out, err)
		log.Printf("netzwerk: Bestätigung von %q abgelehnt: %s", by, msg)
		netRefuse(w, msg)
		return
	}
	log.Printf("netzwerk: Einstellung %s von %q bestätigt", req.ID, by)
	a.netJob.mu.Lock()
	a.netJob.logged = req.ID // steht schon im Protokoll – die Wache schreibt es nicht noch einmal
	a.netJob.mu.Unlock()
	writeMountJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// netScriptRun: Skript mit festem Ablauf (--resume, --maint-off) aufrufen und ins Protokoll
// schreiben; true = gelungen.
func (a *updateAgent) netScriptRun(ctx context.Context, what, script string) bool {
	rctx, cancel := context.WithTimeout(ctx, netResumeTimeout)
	defer cancel()
	out, err := a.netRunFn()(rctx, script, map[string]string{"PDH_NET_SCRIPT": a.netScriptPath()})
	if err != nil {
		log.Printf("netzwerk: %s gescheitert: %s", what, netLastLine(out, err))
		return false
	}
	log.Printf("netzwerk: %s: %s", what, netLastLine(out, nil))
	return true
}

// netResume: beim Start des Agenten (systemd). Wartet eine Änderung oder ist ihre Wartung noch
// an, ruft er das Skript mit --resume: Frist vorbei → Rückfall, sonst Wecker neu stellen (nach
// einem Neustart des Servers fehlt er); offene Wartung ausschalten. Danach Wache alle
// netWatchEvery (netWatch). Kehrt zurück, wenn ctx endet.
func (a *updateAgent) netResume(ctx context.Context) {
	if a.mode != "systemd" {
		return
	}
	if p := a.netReadState(); p != nil && (p.State == "pending" || p.MaintOffPending) {
		a.netScriptRun(ctx, "Wiederaufnahme", netResumeScript)
	}
	t := time.NewTicker(netWatchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		a.netWatch(ctx, time.Now())
	}
}

// netLogFinal: Ergebnis einer Änderung (bestätigt, zurückgestellt, gescheitert) einmal je Kennung
// ins Protokoll des Agenten (journalctl -u pdh-update-agent) – auch wenn niemand die Karte öffnet,
// z. B. ein Rückfall in der Nacht. Das Protokoll des PDH bekommt den Eintrag erst, wenn jemand die
// Karte öffnet (internal/web netLogFinal). Ergebnisse älter als netLogMaxAge nicht mehr; nach
// einem Neustart des Agenten erscheint das letzte Ergebnis höchstens einmal erneut.
func (a *updateAgent) netLogFinal(p *netPending, now time.Time) {
	if p == nil || p.ID == "" || (p.State != "confirmed" && p.State != "restored" && p.State != "failed") {
		return
	}
	j := &a.netJob
	j.mu.Lock()
	seen := j.logged == p.ID
	j.logged = p.ID
	j.mu.Unlock()
	if seen || !p.Started.IsZero() && now.Sub(p.Started) > netLogMaxAge {
		return
	}
	what := "Jetzige Adresse fest einstellen"
	if p.Action == "dns" {
		what = "DNS-Server ändern"
	}
	switch p.State {
	case "confirmed":
		log.Printf("netzwerk: Änderung %s (%s, von %q) bestätigt von %q", p.ID, what, p.By, p.ConfirmedBy)
	case "restored":
		msg := p.Msg
		if msg == "" {
			msg = "Nicht bestätigt – vorige Einstellung wiederhergestellt"
		}
		log.Printf("netzwerk: Änderung %s (%s, von %q) zurückgenommen: %s", p.ID, what, p.By, msg)
	default:
		log.Printf("netzwerk: Änderung %s (%s, von %q) gescheitert: %s", p.ID, what, p.By, p.Msg)
	}
}

// netWatch: ein Durchgang der Wache.
//   - Eine Änderung wartet und ist seit netWatchGrace über der Frist (Wecker gescheitert) →
//     Rückfall über --resume. Läuft das Anwenden laut Agent noch, wartet die Wache zusätzlich die
//     Höchstdauer des Anwendens ab – nie unbegrenzt.
//   - Die Wartung einer früheren Änderung ist noch an → --maint-off; nicht während eines Updates
//     im Verbund (pdh-ha lehnt dann ab), nach Fehlschlägen mit wachsendem Abstand.
func (a *updateAgent) netWatch(ctx context.Context, now time.Time) {
	p := a.netReadState()
	if p == nil {
		return
	}
	a.netLogFinal(p, now)
	running, _ := a.netJob.get()
	if p.State == "pending" {
		// ohne gültige Frist nimmt das Skript die Änderung ebenfalls zurück
		over := p.Deadline.IsZero() || now.After(p.Deadline.Add(netWatchGrace))
		if over && (!running || now.After(p.Deadline.Add(netWatchGrace+netApplyTimeout))) {
			log.Printf("netzwerk: Frist der Änderung %s seit %s vorbei – Rückfall wird nachgeholt", p.ID, now.Sub(p.Deadline).Round(time.Second))
			a.netScriptRun(ctx, "Rückfall", netResumeScript)
		}
		return
	}
	if !p.MaintOffPending || running {
		return
	}
	j := &a.netJob
	j.mu.Lock()
	wait := now.Before(j.maintNext)
	j.mu.Unlock()
	if wait {
		return
	}
	if info, err := a.netHAInfo(ctx); err == nil && info.Update {
		return // pdh-ha schaltet die Wartung während eines Updates im Verbund nicht aus
	}
	ok := a.netScriptRun(ctx, "Wartung des Ausfallschutzes aus", netMaintOffScript)
	j.mu.Lock()
	if ok {
		j.maintWait, j.maintNext = 0, time.Time{}
	} else {
		j.maintWait = min(max(2*j.maintWait, netWatchEvery), netMaintMaxWait)
		j.maintNext = now.Add(j.maintWait)
	}
	j.mu.Unlock()
}
