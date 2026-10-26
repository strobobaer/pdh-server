package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"pdh/internal/ha"
)

// Socket-API für das PDH und den Update-Agenten (HTTP über Unix-Socket).
//
// GET /v1/status liefert apiStatus, z. B.:
//
//	{"ok":true,"configured":true,"mode":"trio",
//	 "self":{"id":"pdh1","kind":"node","role":"aktiv","term":3,"version":"0.75.0","commit":"51c21bc","gateway":true,
//	         "seen":true,"last_seen_ago":0,"clock_skew":0,"paths":[]},
//	 "peers":[{"id":"pdh2","kind":"node","role":"passiv","term":3,"version":"0.75.0","gateway":true,
//	           "seen":true,"last_seen_ago":0.4,"clock_skew":-0.1,
//	           "paths":[{"addr":"10.0.0.2:7480","alive":true,"last_recv_ago":0.4,"err":""}]}],
//	 "maint":false,"settings":{"tick_s":1,"fence_s":10,"takeover_trio_s":30,"takeover_duo_s":60,
//	 "cooldown_min":15,"max_per_day":3},"gateway_targets":[],"blocked":"","observe":true,
//	 "warnings":[],"ca_holder":true}
//
// last_seen_ago / last_recv_ago in Sekunden, -1 = seit Start nie; clock_skew in
// Sekunden (Gegenstelle minus hier). Uneingerichtet: configured=false, mode
// "einzel", self.role "aktiv" (wie ohne pdh-ha).
type apiStatus struct {
	OK             bool        `json:"ok"`
	Configured     bool        `json:"configured"`
	Mode           ha.Mode     `json:"mode"`
	Self           apiNode     `json:"self"`
	Peers          []apiNode   `json:"peers"`
	Maint          bool        `json:"maint"`
	Settings       ha.Settings `json:"settings"`
	GatewayTargets []string    `json:"gateway_targets"`
	Blocked        string      `json:"blocked"` // ha.Allowed-Grund, "" = frei
	Observe        bool        `json:"observe"` // automatische Umschaltung nur gemeldet (auto_mode observe)
	SwapTested     bool        `json:"swap_tested"`
	Warnings       []string    `json:"warnings"`
	CAHolder       bool        `json:"ca_holder"`
	Error          string      `json:"error"` // Konfiguration nicht lesbar (configured=false)
	AutoMode       string      `json:"auto_mode"`
	Busy           bool        `json:"busy"` // eine Aktion läuft
}

type apiNode struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Role        ha.Role   `json:"role"`
	Term        uint64    `json:"term"`
	Version     string    `json:"version"`
	Commit      string    `json:"commit"` // Stand des Quelltexts (leer = unbekannt)
	Gateway     bool      `json:"gateway"`
	Seen        bool      `json:"seen"`
	LastSeenAgo float64   `json:"last_seen_ago"`
	ClockSkew   float64   `json:"clock_skew"`
	Paths       []apiPath `json:"paths"`

	// Datenbank-Spiegel (leer/unknown ohne Spiegel)
	PG          string         `json:"pg"`
	LSN         uint64         `json:"lsn"`
	LagBytes    int64          `json:"lag_bytes"`
	Sync        bool           `json:"sync"`
	Stale       bool           `json:"stale"`
	Timeline    uint32         `json:"timeline"`
	DataCurrent bool           `json:"data_current"`
	Diverged    []divergedInfo `json:"diverged"`
}

type apiPath struct {
	Addr        string  `json:"addr"`
	Alive       bool    `json:"alive"`
	LastRecvAgo float64 `json:"last_recv_ago"`
	Err         string  `json:"err"`
}

const maxEvents = 1000

func socketPath() string {
	if v := os.Getenv("PDH_HA_SOCKET"); v != "" {
		return v
	}
	return "/run/pdh-ha/api.sock"
}

// serveAPI lauscht auf dem Socket bis ctx endet. ready (darf nil sein)
// bekommt das Ergebnis des Öffnens.
func serveAPI(ctx context.Context, d *daemon, sock string, ready chan<- error) error {
	ln, warns, err := listenUnix(sock)
	if ready != nil {
		ready <- err
	}
	if err != nil {
		return err
	}
	for _, w := range warns {
		d.event("warn", w)
	}
	srv := &http.Server{Handler: apiHandler(d), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	err = srv.Serve(ln)
	os.Remove(sock)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// listenUnix: alte Socket-Datei entfernen, 0660, Gruppe pdh (falls vorhanden).
// Rechte-Probleme sind Warnungen (das PDH erreicht den Socket dann evtl. nicht).
func listenUnix(sock string) (net.Listener, []string, error) {
	if err := os.MkdirAll(filepath.Dir(sock), 0o750); err != nil {
		return nil, nil, err
	}
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, nil, err
	}
	var warns []string
	if err := os.Chmod(sock, 0o660); err != nil && runtime.GOOS != "windows" {
		warns = append(warns, "Socket-Rechte nicht gesetzt: "+err.Error())
	}
	if g, err := user.LookupGroup("pdh"); err == nil {
		if gid, err := strconv.Atoi(g.Gid); err == nil {
			if err := os.Chown(sock, -1, gid); err != nil {
				warns = append(warns, "Socket-Gruppe pdh nicht gesetzt: "+err.Error())
			}
			// eigener Laufzeitordner (systemd RuntimeDirectory): Gruppe darf hinein
			if dir := filepath.Dir(sock); filepath.Base(dir) == "pdh-ha" {
				if err := os.Chown(dir, -1, gid); err != nil {
					warns = append(warns, "Gruppe pdh für "+dir+" nicht gesetzt: "+err.Error())
				}
			}
		}
	}
	return ln, warns, nil
}

type apiErr struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

func apiFail(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, apiErr{Error: err.Error()})
}

func apiOK(w http.ResponseWriter) { writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) }

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLine)).Decode(v); err != nil {
		apiFail(w, errors.New("Anfrage ist unlesbar"))
		return false
	}
	return true
}

func apiHandler(d *daemon) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, d.statusNow())
	})
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
		limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
		if err != nil || limit <= 0 {
			limit = 100
		}
		limit = min(limit, maxEvents)
		evs := d.ev.since(since, limit)
		if evs == nil {
			evs = []loggedEvent{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "events": evs})
	})
	mux.HandleFunc("POST /v1/setup", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Addrs []string `json:"addrs"`
			ID    string   `json:"id"`
		}
		if !decodeBody(w, r, &req) {
			return
		}
		if err := d.setupCluster(setupOpts{ID: req.ID, Addrs: req.Addrs}); err != nil {
			apiFail(w, err)
			return
		}
		apiOK(w)
	})
	mux.HandleFunc("POST /v1/join-code", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Role string `json:"role"`
		}
		if !decodeBody(w, r, &req) {
			return
		}
		code, exp, err := d.issueJoinCode(req.Role)
		if err != nil {
			apiFail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "code": code, "expires": exp})
	})
	mux.HandleFunc("POST /v1/maintenance", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			On *bool `json:"on"`
		}
		if !decodeBody(w, r, &req) {
			return
		}
		if req.On == nil {
			apiFail(w, errors.New("Feld on fehlt"))
			return
		}
		if err := d.setMaintenance(*req.On); err != nil {
			apiFail(w, err)
			return
		}
		apiOK(w)
	})
	mux.HandleFunc("POST /v1/command", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Cmd string `json:"cmd"`
		}
		if !decodeBody(w, r, &req) {
			return
		}
		if err := d.command(req.Cmd); err != nil {
			apiFail(w, err)
			return
		}
		apiOK(w)
	})
	mux.HandleFunc("POST /v1/diverged/delete", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID   string `json:"id"`
			Node string `json:"node"` // leer = dieser Server
		}
		if !decodeBody(w, r, &req) {
			return
		}
		if err := d.deleteDivergedAt(req.Node, req.ID); err != nil {
			apiFail(w, err)
			return
		}
		apiOK(w)
	})
	mux.HandleFunc("POST /v1/nodes/remove", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID   string `json:"id"`
			Gone bool   `json:"gone"`
		}
		if !decodeBody(w, r, &req) {
			return
		}
		if err := d.removeNode(req.ID, req.Gone); err != nil {
			apiFail(w, err)
			return
		}
		apiOK(w)
	})
	mux.HandleFunc("PUT /v1/settings", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Settings       *ha.Settings `json:"settings"`
			GatewayTargets []string     `json:"gateway_targets"`
		}
		if !decodeBody(w, r, &req) {
			return
		}
		if req.Settings == nil {
			apiFail(w, errors.New("Feld settings fehlt"))
			return
		}
		if err := d.updateSettings(*req.Settings, req.GatewayTargets); err != nil {
			apiFail(w, err)
			return
		}
		apiOK(w)
	})
	return mux
}

// --- Befehle (API und Tests) ---

var errNotConfigured = errors.New("Ausfallschutz ist noch nicht eingerichtet")

// setupCluster richtet diesen Server als ersten Knoten ein (wie pdh-ha setup).
func (d *daemon) setupCluster(o setupOpts) error {
	d.lock()
	if d.configured {
		d.unlock()
		return errConfigured
	}
	err := setupFiles(d.st, o)
	if err == nil {
		err = d.loadLocked()
	}
	if err == nil {
		d.startLocked()
	}
	id := d.node.ID
	d.unlock()
	if err != nil {
		return err
	}
	d.event("info", fmt.Sprintf("Ausfallschutz eingerichtet: %s, Amtszeit 1, Wartung an", id))
	go d.applyFirewall()
	return nil
}

func countKinds(cl clusterConfig) (nodes, witnesses int) {
	for _, n := range cl.Nodes {
		switch n.Kind {
		case "node":
			nodes++
		case "witness":
			witnesses++
		}
	}
	return
}

// issueJoinCode erzeugt einen Beitrittscode (nur auf dem CA-Knoten).
func (d *daemon) issueJoinCode(role string) (string, time.Time, error) {
	d.lock()
	defer func() { go d.applyFirewall() }()
	defer d.unlock()
	if !d.configured {
		return "", time.Time{}, errNotConfigured
	}
	if !validKind(role) {
		return "", time.Time{}, fmt.Errorf("Ungültige Rolle %q", role)
	}
	if !d.node.CAHolder {
		return "", time.Time{}, errors.New("Beitrittscodes gibt es nur auf dem Server, der den Ausfallschutz eingerichtet hat")
	}
	nodes, witnesses := countKinds(d.cl)
	switch {
	case role == "witness" && nodes < 2:
		return "", time.Time{}, errors.New("Zuerst den zweiten Server hinzufügen")
	case role == "witness" && witnesses > 0:
		return "", time.Time{}, errors.New("Der Cluster hat bereits einen Zeugen")
	case role == "node" && nodes >= 2:
		return "", time.Time{}, errors.New("Der Cluster hat bereits zwei Server")
	}
	caPEM, err := os.ReadFile(d.st.etcPath("ca.crt"))
	if err != nil {
		return "", time.Time{}, err
	}
	now := d.wall()
	secret, err := d.codes.issue(role, now)
	if err != nil {
		return "", time.Time{}, err
	}
	code := joinCode{Addrs: d.ownAddrsLocked(), FP: fingerprint(caPEM), Secret: secret, Role: role, Commit: d.commit, Cluster: d.cl.ClusterID}
	d.event("info", fmt.Sprintf("Beitrittscode für %s erzeugt (30 min gültig)", kindText(role)))
	return code.String(), now.Add(joinCodeTTL), nil
}

// setMaintenance setzt die clusterweite Wartung (höherer Stand gewinnt).
func (d *daemon) setMaintenance(on bool) error {
	d.lock()
	defer d.unlock()
	switch {
	case !d.configured:
		return errNotConfigured
	case d.removed:
		return errors.New("Aus dem Cluster entfernt")
	case d.node.Kind == "witness":
		return errors.New("Auf dem Zeugen nicht möglich")
	}
	p := d.p
	p.Maint = on
	p.MaintSeq++
	if err := d.saveFn(p); err != nil {
		return err
	}
	d.p = p
	if on {
		d.event("info", "Wartung an – Automatik aus")
	} else {
		d.event("info", "Wartung aus – Automatik an (Beobachtung)")
	}
	return nil
}

// command merkt einen Handbefehl für den nächsten Takt vor.
func (d *daemon) command(cmd string) error {
	d.lock()
	defer d.unlock()
	var c ha.CommandKind
	text := ""
	switch cmd {
	case "swap":
		c, text = ha.CmdSwap, "Rollen tauschen"
	case "force_takeover":
		c, text = ha.CmdForceTakeover, "Übernahme erzwingen"
	default:
		return fmt.Errorf("Unbekannter Befehl %q", cmd)
	}
	switch {
	case !d.configured:
		return errNotConfigured
	case d.removed:
		return errors.New("Aus dem Cluster entfernt")
	case d.node.Kind == "witness":
		return errors.New("Auf dem Zeugen nicht möglich")
	case d.modeLocked() == ha.Single:
		return errors.New("Nur mit zwei Servern möglich")
	case c == ha.CmdForceTakeover && d.p.Role == ha.Active:
		return errors.New("Nur auf einem nicht aktiven Server möglich")
	case c == ha.CmdSwap && d.p.Role != ha.Active:
		return errors.New("Nur auf dem aktiven Server möglich")
	}
	d.cmd = c
	d.event("info", "Befehl: "+text)
	return nil
}

// updateSettings ändert Zeiten und Gateway-Ziele für den ganzen Cluster.
func (d *daemon) updateSettings(s ha.Settings, targets []string) error {
	if d.fixed == nil {
		if err := s.Validate(); err != nil {
			return err
		}
	}
	if len(targets) > maxJoinAddrs {
		return fmt.Errorf("Höchstens %d Gateway-Ziele", maxJoinAddrs)
	}
	clean := []string{}
	for _, t := range targets {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if !validTarget(t) {
			return fmt.Errorf("Ungültiges Gateway-Ziel %q", t)
		}
		clean = append(clean, t)
	}
	d.lock()
	if err := d.clusterEditable(); err != nil {
		d.unlock()
		return err
	}
	if !d.node.CAHolder {
		d.unlock()
		return errors.New("Einstellungen bitte am ersten Server ändern")
	}
	if s.Real() && !d.cl.SwapTested && d.fixed == nil {
		d.unlock()
		return errors.New("Erst einmal Rollen tauschen – hin und zurück")
	}
	cl := d.cl
	if targets == nil {
		clean = d.cl.GatewayTargets // nicht angegeben: Ziele bleiben
	}
	cl.Settings, cl.GatewayTargets = s, clean
	cl.Version++
	pl, err := d.publishClusterLocked(cl)
	d.unlock()
	if err != nil {
		return err
	}
	pl.event("info", "Zeiten und Gateway-Ziele geändert")
	d.execute(context.Background(), pl)
	return nil
}

func (d *daemon) clusterEditable() error {
	switch {
	case !d.configured:
		return errNotConfigured
	case d.removed:
		return errors.New("Aus dem Cluster entfernt")
	case d.node.Kind == "witness":
		return errors.New("Auf dem Zeugen nicht möglich")
	}
	return nil
}

// publishClusterLocked speichert eine neue Knotenliste und verteilt sie.
func (d *daemon) publishClusterLocked(cl clusterConfig) (plan, error) {
	pl := plan{net: d.net, ufw: true}
	if err := d.st.saveCluster(cl); err != nil {
		return pl, err
	}
	d.cl = cl
	d.net.setCluster(cl)
	pl.send("", message{T: "cluster", ID: d.node.ID, Seq: d.nextSeq(), Cluster: &cl})
	return pl, nil
}

// removeNode nimmt einen Server oder den Zeugen aus dem Cluster (CA-Knoten).
func (d *daemon) removeNode(id string, gone bool) error {
	d.lock()
	if err := d.clusterEditable(); err != nil {
		d.unlock()
		return err
	}
	n, ok := d.clusterNodeLocked(id)
	var err error
	switch {
	case !d.node.CAHolder:
		err = errors.New("Entfernen nur auf dem Server, der den Ausfallschutz eingerichtet hat")
	case id == d.node.ID:
		err = errors.New("Dieser Server kann sich nicht selbst entfernen")
	case !ok:
		err = fmt.Errorf("Unbekannter Server %q", id)
	}
	if err == nil {
		_, witnesses := countKinds(d.cl)
		v := d.views[id]
		switch {
		case n.Kind == "node" && witnesses > 0:
			err = errors.New("Zuerst den Zeugen entfernen")
		case v.Last.T == "hb" && ha.Role(v.Last.Role) == ha.Active && v.Last.Term >= d.p.Term:
			err = errors.New("Aktiven Server erst per Rollen tauschen abgeben")
		case v.Last.T == "hb" && (ha.Role(v.Last.Role) == ha.Fenced || ha.Role(v.Last.Role) == ha.Rejoin) && v.Last.Term >= d.p.Term:
			err = errors.New("Server ist möglicherweise noch aktiv – zuerst Übernahme erzwingen")
		case !v.seen() && !gone:
			err = errors.New("Server nicht erreichbar – zum Entfernen „dauerhaft weg“ bestätigen")
		}
	}
	if err != nil {
		d.unlock()
		return err
	}
	cl := d.cl
	cl.Nodes = nil
	for _, o := range d.cl.Nodes {
		if o.ID != id {
			cl.Nodes = append(cl.Nodes, o)
		}
	}
	cl.Revoked = append([]string(nil), d.cl.Revoked...)
	if n.Serial != "" {
		cl.Revoked = append(cl.Revoked, n.Serial)
	}
	cl.Version++
	// Wechsel zu Einzel: eigene Amtszeit erhöhen, damit der Entfernte sie nie überbietet
	if cl.Mode() == ha.Single && d.cl.Mode() != ha.Single {
		p := d.p
		p.Term++
		if err := d.saveFn(p); err != nil {
			d.unlock()
			return err
		}
		d.p = p
	}
	if err := d.st.saveCluster(cl); err != nil {
		d.unlock()
		return err
	}
	d.cl = cl
	self, nt, tick := d.node.ID, d.net, d.settingsLocked().Tick
	d.unlock()
	// erst Bescheid geben, dann die Verbindungen trennen
	nt.sendTo(id, message{T: "removed", ID: self, Seq: d.nextSeq()})
	nt.broadcast(message{T: "cluster", ID: self, Seq: d.nextSeq(), Cluster: &cl})
	time.AfterFunc(2*tick, func() { nt.setCluster(cl) })
	d.event("info", fmt.Sprintf("%s %s entfernt", kindText(n.Kind), id))
	go d.applyFirewall()
	return nil
}

// --- Client ---

func apiClient(sock string) *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dl net.Dialer
			return dl.DialContext(ctx, "unix", sock)
		}},
	}
}

func apiRaw(sock, method, path, body string) (*http.Response, error) {
	req, err := http.NewRequest(method, "http://pdh-ha"+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return apiClient(sock).Do(req)
}

// apiError ist eine Fehlerantwort des Dienstes (im Unterschied zu „nicht erreichbar“).
type apiError struct{ msg string }

func (e apiError) Error() string { return e.msg }

func apiDo(sock, method, path string, in, out any) error {
	body := ""
	if in != nil {
		if s, ok := in.(string); ok {
			body = s
		} else {
			b, err := json.Marshal(in)
			if err != nil {
				return err
			}
			body = string(b)
		}
	}
	resp, err := apiRaw(sock, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		var e apiErr
		if json.Unmarshal(b, &e) != nil || e.Error == "" {
			e.Error = "pdh-ha antwortet mit " + resp.Status
		}
		return apiError{e.Error}
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}
