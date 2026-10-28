package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"pdh/internal/ha"
)

// Skripte beim Rollenwechsel: alle ausführbaren Dateien in hooksDir laufen
// nacheinander im Hintergrund. Die Skripte sind nicht im PDH bearbeitbar.

const (
	hooksDir         = "/etc/pdh/ha-hooks"
	hookTimeout      = 30 * time.Second
	hookOutMax       = 2048
	hookRescan       = 30 * time.Second
	hookWaitHeld     = 10 * time.Second
	hookKillWaitTime = 500 * time.Millisecond // Frist, bis die Ausgabe-Rohre nach dem Abbruch schließen
)

// hookOwnerUID: Skripte müssen diesem Besitzer gehören (root). Tests setzen
// den Wert, weil sie im Container selbst als root laufen.
var hookOwnerUID uint32

type hookFile struct {
	Name   string
	Path   string
	Unsafe string // Grund, warum das Skript nicht läuft ("" = ok)
}

// scanHooks liest den Ordner: ausführbare reguläre Dateien in alphabetischer
// Reihenfolge, dazu eine Prüfsumme (SHA-256 hex) über „Name\0Inhalt\0“. Ein
// fehlender Ordner ergibt eine leere Liste ohne Fehler.
func scanHooks(dir string) ([]hookFile, string, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	var out []hookFile
	h := sha256.New()
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, "", err
		}
		f := hookFile{Name: e.Name(), Path: p}
		switch uid, ok := fileUID(fi); {
		case fi.Mode().Perm()&0o022 != 0:
			f.Unsafe = "für andere schreibbar"
		case ok && uid != hookOwnerUID:
			f.Unsafe = "gehört nicht root"
		}
		out = append(out, f)
		h.Write([]byte(e.Name()))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
	}
	if len(out) == 0 {
		return nil, "", nil
	}
	return out, hex.EncodeToString(h.Sum(nil)), nil
}

// hookEnv: Umgebung der Skripte (PDH_HA_…).
type hookEnv struct {
	Role                            ha.Role
	Term                            uint64
	Node, OwnAddr, PartnerAddr, VIP string
}

func (e hookEnv) vars() []string {
	return []string{
		"PDH_HA_ROLLE=" + string(e.Role),
		"PDH_HA_AMTSZEIT=" + strconv.FormatUint(e.Term, 10),
		"PDH_HA_KNOTEN=" + e.Node,
		"PDH_HA_EIGENE_ADRESSE=" + e.OwnAddr,
		"PDH_HA_PARTNER_ADRESSE=" + e.PartnerAddr,
		"PDH_HA_VIP=" + e.VIP,
	}
}

type hookResult struct {
	Name string  `json:"name"`
	OK   bool    `json:"ok"`
	Out  string  `json:"out"`
	Dur  float64 `json:"dur"`
}

// hookRun: letzter Lauf (für Status und Oberfläche).
type hookRun struct {
	At      time.Time    `json:"at"`
	Role    ha.Role      `json:"role"`
	Results []hookResult `json:"results"`
}

// tailBuf behält nur die letzten hookOutMax Bytes der Ausgabe.
type tailBuf struct{ b []byte }

func (t *tailBuf) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 2*hookOutMax {
		t.b = append([]byte(nil), t.b[len(t.b)-hookOutMax:]...)
	}
	return len(p), nil
}

func (t *tailBuf) String() string {
	b := t.b
	if len(b) > hookOutMax {
		b = b[len(b)-hookOutMax:]
	}
	return strings.TrimSpace(string(bytes.ToValidUTF8(b, nil)))
}

type hookJob struct {
	env  hookEnv
	held *accessWorker     // wenn gesetzt: bei Rolle aktiv vorher auf die Adresse warten
	done chan []hookResult // gepuffert, nie blockierend
}

type hookRunner struct {
	dir     string
	timeout time.Duration
	ev      *eventLog
	ctx     context.Context // Lebensdauer des Dienstes (nil = Background)

	mu      sync.Mutex // schützt alles unten; wird nie während eines Skripts gehalten
	queue   []hookJob
	working bool
	scanned bool
	names   []string
	sum     string
	last    *hookRun
}

func newHookRunner(ev *eventLog) *hookRunner {
	return &hookRunner{dir: hooksDir, timeout: hookTimeout, ev: ev}
}

func (h *hookRunner) baseCtx() context.Context {
	if h.ctx != nil {
		return h.ctx
	}
	return context.Background()
}

func (h *hookRunner) setScan(files []hookFile, sum string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.scanned, h.sum, h.names = true, sum, []string{}
	for _, f := range files {
		h.names = append(h.names, f.Name)
	}
}

// rescan liest den Ordner neu und merkt Namen und Prüfsumme; bei einem
// Lesefehler bleibt der letzte Stand.
func (h *hookRunner) rescan() {
	if files, sum, err := scanHooks(h.dir); err == nil {
		h.setScan(files, sum)
	} else {
		h.mu.Lock()
		h.scanned = true
		h.mu.Unlock()
	}
}

// info: zuletzt gelesener Stand. Das erste Mal wird synchron gelesen, danach
// liest loop alle 30 s neu – info selbst liest nie von der Platte.
func (h *hookRunner) info() (names []string, sum string, last *hookRun) {
	h.mu.Lock()
	scanned := h.scanned
	h.mu.Unlock()
	if !scanned {
		h.rescan()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string{}, h.names...), h.sum, h.last
}

// loop liest den Ordner alle 30 s neu, bis ctx endet.
func (h *hookRunner) loop(ctx context.Context) {
	t := time.NewTicker(hookRescan)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.rescan()
		}
	}
}

// enqueue reiht einen Lauf ein (blockiert nie); die Reihenfolge bleibt erhalten.
func (h *hookRunner) enqueue(e hookEnv) { h.submit(e, nil) }

// submit wie enqueue; held: bei Rolle aktiv vorher bis zu 10 s auf die Adresse
// warten. Der Kanal liefert das Ergebnis.
func (h *hookRunner) submit(e hookEnv, held *accessWorker) <-chan []hookResult {
	done := make(chan []hookResult, 1)
	h.mu.Lock()
	h.queue = append(h.queue, hookJob{env: e, held: held, done: done})
	start := !h.working
	h.working = true
	h.mu.Unlock()
	if start {
		go h.work()
	}
	return done
}

func (h *hookRunner) work() {
	for {
		h.mu.Lock()
		if len(h.queue) == 0 {
			h.working = false
			h.mu.Unlock()
			return
		}
		j := h.queue[0]
		h.queue = h.queue[1:]
		h.mu.Unlock()
		ctx := h.baseCtx()
		if j.held != nil && j.env.Role == ha.Active {
			j.held.waitHeld(ctx, hookWaitHeld) // Ergebnis egal: nach Ablauf trotzdem laufen
		}
		res := h.run(ctx, j.env)
		h.mu.Lock()
		h.last = &hookRun{At: time.Now().UTC(), Role: j.env.Role, Results: res}
		h.mu.Unlock()
		j.done <- res
	}
}

// lastLine: letzte nichtleere Zeile der Ausgabe.
func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}

// run führt alle Skripte nacheinander aus. Jedes bekommt höchstens timeout;
// ein Fehler stoppt die übrigen nicht. Fehler und Zeitüberschreitung melden
// einen Alarm mit der letzten Ausgabezeile.
func (h *hookRunner) run(ctx context.Context, e hookEnv) []hookResult {
	files, sum, err := scanHooks(h.dir)
	if err != nil {
		h.ev.add(ha.Event{Kind: "alarm", Text: "Skripte beim Rollenwechsel: Ordner nicht lesbar: " + err.Error()})
		return []hookResult{{Name: h.dir, Out: err.Error()}}
	}
	h.setScan(files, sum)
	res := []hookResult{}
	for _, f := range files {
		if f.Unsafe != "" {
			res = append(res, hookResult{Name: f.Name, Out: "unsicher – übersprungen (" + f.Unsafe + ")"})
			h.ev.add(ha.Event{Kind: "warn", Text: fmt.Sprintf("Skript %s unsicher – übersprungen (%s)", f.Name, f.Unsafe)})
			continue
		}
		res = append(res, h.runOne(ctx, f, e))
	}
	return res
}

func (h *hookRunner) runOne(ctx context.Context, f hookFile, e hookEnv) hookResult {
	cctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	var out tailBuf
	cmd := exec.CommandContext(cctx, f.Path)
	cmd.Dir = h.dir
	cmd.Env = append([]string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}, e.vars()...)
	cmd.Stdout, cmd.Stderr = &out, &out // Stdin bleibt leer (/dev/null)
	cmd.WaitDelay = hookKillWaitTime
	start := time.Now()
	err := cmd.Run()
	r := hookResult{Name: f.Name, OK: err == nil, Out: out.String(), Dur: time.Since(start).Seconds()}
	why := "fehlgeschlagen"
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		r.OK, why = false, "Zeitüberschreitung"
		msg := fmt.Sprintf("Zeitüberschreitung nach %.0f s", h.timeout.Seconds())
		if h.timeout < time.Second {
			msg = fmt.Sprintf("Zeitüberschreitung nach %s", h.timeout)
		}
		if r.Out != "" {
			msg += "\n" + r.Out
		}
		r.Out = msg
	} else if err != nil && r.Out == "" {
		r.Out = err.Error()
	}
	if !r.OK {
		h.ev.add(ha.Event{Kind: "alarm", Text: fmt.Sprintf("Skript %s (Rolle %s) %s: %s", f.Name, e.Role, why, lastLine(r.Out))})
	}
	return r
}

// hookEnvLocked baut die Umgebung für die Rolle role (unter mu). VIP: die
// eingetragene Adresse ohne Präfix, unabhängig vom Schalter vip_on – das
// Skript soll sie auch kennen, wenn sie noch nicht schaltet.
func (d *daemon) hookEnvLocked(role ha.Role) hookEnv {
	e := hookEnv{Role: role, Term: d.p.Term, Node: d.node.ID}
	if a := d.ownAddrsLocked(); len(a) > 0 {
		e.OwnAddr = hostOfAddr(a[0])
	}
	for _, n := range d.cl.Nodes {
		if n.Kind == "node" && n.ID != d.node.ID && len(n.Addrs) > 0 {
			e.PartnerAddr = hostOfAddr(n.Addrs[0])
			break
		}
	}
	e.VIP, _, _ = strings.Cut(d.cl.Access.VIP, "/")
	return e
}

// hooksChangedLocked startet nach einem Rollenwechsel die Skripte (nur Knoten,
// nur bei hooks_on; nie im ersten Takt nach dem Start). Der Aufruf blockiert
// nie: die Skripte laufen im Hintergrund.
func (d *daemon) hooksChangedLocked(before, after ha.Role, booting bool) {
	if booting || before == after || !d.cl.Access.HooksOn || d.node.Kind != "node" {
		return
	}
	var w *accessWorker
	if d.cl.Access.VIPOn {
		w = d.access
	}
	d.hooks.submit(d.hookEnvLocked(after), w)
}

// apiHooksLocked: Skripte für den Status. Same: kein gesehener Partner hat
// eine andere Prüfsumme.
func (d *daemon) apiHooksLocked() apiHooks {
	names, sum, last := d.hooks.info()
	h := apiHooks{Names: names, Sum: sum, Same: true, Last: last}
	for _, n := range d.cl.Nodes {
		if n.ID == d.node.ID || n.Kind != "node" {
			continue
		}
		if v := d.views[n.ID]; v.seen() && v.Last.Hooks != sum {
			h.Same = false
		}
	}
	return h
}

// runHooksNow führt die Skripte mit der aktuellen Rolle aus (pdh-ha hook run).
func (d *daemon) runHooksNow(ctx context.Context) ([]hookResult, error) {
	d.mu.Lock()
	if !d.configured || d.node.Kind != "node" {
		d.mu.Unlock()
		return nil, errors.New("Skripte laufen nur auf Servern mit Datenbank")
	}
	e := d.hookEnvLocked(d.p.Role)
	d.mu.Unlock()
	select {
	case res := <-d.hooks.submit(e, nil):
		return res, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
