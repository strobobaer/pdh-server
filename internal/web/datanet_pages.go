package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"

	"pdh"
	"pdh/internal/datanet"
)

// Datennetz – Seite /datanet: Datennetz des PDH ein- und ausschalten, die
// Freigabe im Tailnet, Daten-Clients anlegen, sperren, entfernen und die
// Programmdateien der Clients laden. Token und Einmal-Schlüssel erscheinen nur
// in der Antwort auf „Anlegen“ – nie in einer Adresse, nie im Protokoll.

var dataNetUUIDRe = regexp.MustCompile(`^` + uuidRe + `$`)

// dataNetServerRe: erlaubte Servernamen (siehe dataNetServerName).
var dataNetServerRe = regexp.MustCompile(`^[a-z0-9-]{1,63}$`)

// ── Rechte ───────────────────────────────────────────────────────────────

// dataNetCan: Recht für die Seite (id "" = global) oder einen Client.
func (h *Handler) dataNetCan(r *http.Request, perm, id string) bool {
	if h.hasPermHook != nil {
		return h.hasPermHook(r, perm)
	}
	if id == "" {
		return h.hasPerm(r, perm)
	}
	return h.hasPermRec(r, perm, dataNetRef, id)
}

func dataNetForbidden(w http.ResponseWriter) {
	http.Error(w, "Keine Berechtigung", http.StatusForbidden)
}

// ── Freigabe-Zeile und Installationsbefehl ───────────────────────────────

// dataNetCleanPorts: gültige Ports, aufsteigend und ohne Doppelte.
func dataNetCleanPorts(ports []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, p := range ports {
		if p >= 1 && p <= 65535 && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Ints(out)
	return out
}

func dataNetPortList(ports []int) string {
	s := make([]string, len(ports))
	for i, p := range ports {
		s[i] = strconv.Itoa(p)
	}
	return strings.Join(s, ",")
}

// dataNetACL ist der Ausschnitt für die Tailnet-Richtlinie (Tailscale-Admin →
// Access controls). Ohne bekannte Ports steht der Platzhalter <PORT>.
func dataNetACL(ports []int) string {
	list := dataNetPortList(dataNetCleanPorts(ports))
	if list == "" {
		list = "<PORT>"
	}
	return `"tagOwners": { "tag:pdh": ["autogroup:admin"], "tag:pdh-data": ["autogroup:admin"] },
"acls": [
  { "action": "accept", "src": ["tag:pdh"], "dst": ["tag:pdh-data:` + list + `"] },
  { "action": "accept", "src": ["tag:pdh-data"], "dst": ["tag:pdh:8443"] }
]`
}

const (
	dataNetNoNode     = "Erst das Datennetz des PDH einschalten"
	dataNetAuthHolder = "<EINMAL-SCHLÜSSEL>"
	dataNetWinExe     = "pdh-data-client-windows-amd64.exe"
	dataNetLinuxExe   = "pdh-data-client-linux-amd64"
)

// dataNetInstallArgs: die Schalter ab --id (ohne Token).
func dataNetInstallArgs(id string, pdhAddrs []string, ports []int, tokenArgs string) string {
	ports = dataNetCleanPorts(ports)
	if len(ports) == 0 {
		ports = []int{1433}
	}
	var b strings.Builder
	b.WriteString("install --id " + id)
	if tokenArgs != "" {
		b.WriteString(" " + tokenArgs)
	}
	b.WriteString(" --pdh " + strings.Join(pdhAddrs, ","))
	for _, p := range ports {
		b.WriteString(" --target " + strconv.Itoa(p))
	}
	b.WriteString(" --authkey " + dataNetAuthHolder)
	return b.String()
}

// dataNetInstallCmd ist der Installationsbefehl mit --token (Windows:
// Eingabeaufforderung als Administrator; sonst Linux mit sudo). Das Token
// bleibt dabei in der Shell-Historie; besser ist dataNetInstallCmdSafe.
func dataNetInstallCmd(goos, id, token string, pdhAddrs []string, ports []int) string {
	if len(pdhAddrs) == 0 {
		return dataNetNoNode
	}
	args := dataNetInstallArgs(id, pdhAddrs, ports, "--token "+token)
	if goos == "windows" {
		return dataNetWinExe + " " + args
	}
	return "sudo ./" + dataNetLinuxExe + " " + args
}

// dataNetInstallCmdSafe liefert zwei Schritte, bei denen das Token nicht in
// der Shell-Historie steht: erst die Eingabe des Tokens (Umgebungsvariable
// bzw. Standardeingabe), dann den Installationsbefehl ohne Token.
func dataNetInstallCmdSafe(goos, id string, pdhAddrs []string, ports []int) (prep, cmd string) {
	if len(pdhAddrs) == 0 {
		return "", dataNetNoNode
	}
	if goos == "windows" {
		return "set /p PDH_DATANET_TOKEN=Token: ",
			dataNetWinExe + " " + dataNetInstallArgs(id, pdhAddrs, ports, "")
	}
	return `read -rs -p "Token: " T; echo`,
		`printf %s "$T" | sudo ./` + dataNetLinuxExe + " " + dataNetInstallArgs(id, pdhAddrs, ports, "--token-file -") + "; unset T"
}

// ── Programmdateien der Clients ──────────────────────────────────────────

// dataNetClientDir: Ordner mit den Client-Programmen – PDH_DATA_CLIENT_DIR,
// sonst der Ordner der laufenden Programmdatei (bei systemd: bin/).
func dataNetClientDir() string {
	if d := os.Getenv("PDH_DATA_CLIENT_DIR"); d != "" {
		return d
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	return "bin"
}

// dataNetFiles sind die einzigen ausgelieferten Dateien (feste Namen, nie aus der Anfrage).
var dataNetFiles = []struct{ File, Label string }{
	{"pdh-data-client-windows-amd64.exe", "Windows (64 Bit)"},
	{dataNetLinuxExe, "Linux (64 Bit)"},
}

type dataNetDownload struct {
	File, Label, SHA256 string
	Size                int64
	Present             bool
}

type dataNetShaEntry struct {
	mod  time.Time
	size int64
	sum  string
}

var (
	dataNetShaMu    sync.Mutex
	dataNetShaCache = map[string]dataNetShaEntry{}
)

// dataNetSHA256 liefert die Prüfsumme einer Datei; unverändert (Größe, Zeit) wird nicht neu gelesen.
func dataNetSHA256(path string, fi os.FileInfo) string {
	dataNetShaMu.Lock()
	e, ok := dataNetShaCache[path]
	dataNetShaMu.Unlock()
	if ok && e.size == fi.Size() && e.mod.Equal(fi.ModTime()) {
		return e.sum
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	hs := sha256.New()
	if _, err := io.Copy(hs, f); err != nil {
		return ""
	}
	sum := hex.EncodeToString(hs.Sum(nil))
	dataNetShaMu.Lock()
	dataNetShaCache[path] = dataNetShaEntry{fi.ModTime(), fi.Size(), sum}
	dataNetShaMu.Unlock()
	return sum
}

func dataNetDownloads() []dataNetDownload {
	dir := dataNetClientDir()
	out := make([]dataNetDownload, 0, len(dataNetFiles))
	for _, f := range dataNetFiles {
		d := dataNetDownload{File: f.File, Label: f.Label}
		p := filepath.Join(dir, f.File)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			d.Present, d.Size, d.SHA256 = true, fi.Size(), dataNetSHA256(p, fi)
		}
		out = append(out, d)
	}
	return out
}

// DataNetDownload: GET /datanet/download/{file} – nur die Programme aus dataNetFiles.
func (h *Handler) DataNetDownload(w http.ResponseWriter, r *http.Request) {
	if !h.dataNetCan(r, permDataNetEdit, "") {
		dataNetForbidden(w)
		return
	}
	name := chi.URLParam(r, "file")
	for _, f := range dataNetFiles {
		if name != f.File {
			continue
		}
		file, err := os.Open(filepath.Join(dataNetClientDir(), f.File))
		if err != nil {
			break
		}
		defer file.Close()
		fi, err := file.Stat()
		if err != nil || !fi.Mode().IsRegular() {
			break
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+f.File+`"`)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, f.File, fi.ModTime(), file)
		return
	}
	http.NotFound(w, r)
}

// ── Knoten der Server ────────────────────────────────────────────────────

type dataNetNode struct {
	Server string
	Addrs  []string
	Seen   time.Time
}

// dataNetNodes liest die gemeldeten Knoten (datanet.cloud.node.*), nach Servername sortiert.
func (h *Handler) dataNetNodes(ctx context.Context) []dataNetNode {
	if h.db == nil {
		return nil
	}
	rows, err := h.db.Query(ctx, `SELECT key, value FROM app_settings WHERE key LIKE $1 ORDER BY key`, dataNetKeyNode+"%")
	if err != nil {
		log.Warn().Err(err).Msg("Datennetz: Knoten nicht lesbar")
		return nil
	}
	defer rows.Close()
	var out []dataNetNode
	for rows.Next() {
		var key, val string
		if rows.Scan(&key, &val) != nil {
			continue
		}
		var j struct {
			Addrs []string `json:"addrs"`
			Seen  string   `json:"seen"`
		}
		if json.Unmarshal([]byte(val), &j) != nil {
			continue
		}
		n := dataNetNode{Server: strings.TrimPrefix(key, dataNetKeyNode), Addrs: j.Addrs}
		n.Seen, _ = time.Parse(time.RFC3339, j.Seen)
		out = append(out, n)
	}
	return out
}

// dataNetPickAddr: die Adresse für den Client – bevorzugt IPv4.
func dataNetPickAddr(addrs []string) string {
	first := ""
	for _, a := range addrs {
		ip, err := netip.ParseAddr(a)
		if err != nil {
			continue
		}
		if ip.Is4() {
			return ip.String()
		}
		if first == "" {
			first = ip.String()
		}
	}
	return first
}

// dataNetPDHAddrs: die Ziele der PDH-Server im Datennetz, ohne Doppelte. Je
// Server der Tailnet-Name (MagicDNS, pdh-<Server>) und die bekannte Adresse;
// der eigene Server (self) steht vorn. So findet ein Client nach einem
// Rollentausch den anderen Server, auch wenn dessen Adresse noch unbekannt ist.
func dataNetPDHAddrs(self string, servers []string, nodes []dataNetNode) []string {
	names := map[string]bool{}
	var order []string
	add := func(s string) {
		if s != "" && !names[s] {
			names[s] = true
			order = append(order, s)
		}
	}
	add(self)
	for _, s := range servers {
		add(s)
	}
	for _, n := range nodes {
		add(n.Server)
	}
	rest := order[1:]
	sort.Strings(rest)
	var out []string
	seen := map[string]bool{}
	put := func(a string) {
		if a != "" && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	for _, s := range order {
		if s == "" {
			continue
		}
		put("pdh-" + s)
		for _, n := range nodes {
			if n.Server == s {
				put(dataNetPickAddr(n.Addrs))
			}
		}
	}
	return out
}

// dataNetPDHList: alle PDH-Ziele für die Meldeantwort (Host:Port).
func (h *Handler) dataNetPDHList(ctx context.Context) []string {
	var servers []string
	for s := range h.dataNetAuthKeyServers(ctx) {
		servers = append(servers, s)
	}
	sort.Strings(servers)
	list := dataNetPDHAddrs(h.dataNetServerName(), servers, h.dataNetNodes(ctx))
	for i, a := range list {
		list[i] = net.JoinHostPort(a, strconv.Itoa(datanet.ReportPort))
	}
	return list
}

// dataNetAuthKeyServers: Server, für die ein Einmal-Schlüssel hinterlegt ist.
func (h *Handler) dataNetAuthKeyServers(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	if h.db == nil {
		return out
	}
	rows, err := h.db.Query(ctx, `SELECT key FROM app_settings WHERE key LIKE $1 AND value <> ''`, dataNetKeyAuth+"%")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			out[strings.TrimPrefix(k, dataNetKeyAuth)] = true
		}
	}
	return out
}

// ── Seite ────────────────────────────────────────────────────────────────

type dataNetServerRow struct {
	Name, Addrs, Seen string
	Self, HasKey      bool
}

type dataNetTargetRow struct {
	Port int
	OK   bool
	Err  string
}

type dataNetRow struct {
	ID, Name, Host, Version, OS, LastSeen string
	State, StateLabel, StateColor         string
	Targets                               []dataNetTargetRow
	VersionDiff, Revoked                  bool
}

// dataNetCreated: Ergebnis von „Anlegen“ – wird nur in dieser Antwort gezeigt.
type dataNetCreated struct {
	ID, Name, Token                                string
	Windows, Linux                                 string // mit --token
	PrepWindows, SafeWindows, PrepLinux, SafeLinux string // ohne Token in der Historie
	NoNode                                         bool
}

type DataNetPageData struct {
	BaseData
	CanEdit, CanAdmin       bool
	Notice, Err             string
	Enabled                 bool
	NetState, NetStateLabel string
	NetErr                  string
	Servers                 []dataNetServerRow
	ACL                     string
	Clients                 []dataNetRow
	Created                 *dataNetCreated
	Downloads               []dataNetDownload
	Version                 string
}

var dataNetStateLabels = map[string][2]string{
	"ok":      {"Läuft", "var(--green)"},
	"offline": {"Offline", "var(--red)"},
	"db_down": {"Datenbank nicht erreichbar", "var(--amber)"},
	"revoked": {"Gesperrt", "var(--muted)"},
	"new":     {"Noch keine Meldung", "var(--muted)"},
}

// dataNetRows bereitet die Clients für die Anzeige auf. pdhVersion: Version dieses PDH.
func dataNetRows(clients []dataNetClient, now time.Time, pdhVersion string) []dataNetRow {
	out := make([]dataNetRow, 0, len(clients))
	for _, c := range clients {
		st := dataNetState(c, now, datanet.DefaultInterval)
		lbl := dataNetStateLabels[st]
		r := dataNetRow{ID: c.ID, Name: c.Name, Host: c.Hostname, Version: c.Version, OS: c.OS,
			State: st, StateLabel: lbl[0], StateColor: lbl[1], Revoked: c.Revoked,
			VersionDiff: c.Version != "" && c.Version != pdhVersion}
		if !c.LastSeen.IsZero() {
			r.LastSeen = c.LastSeen.Local().Format("02.01.2006 15:04:05")
		}
		for _, t := range c.Targets {
			r.Targets = append(r.Targets, dataNetTargetRow{Port: t.Port, OK: t.OK, Err: t.Error})
		}
		out = append(out, r)
	}
	return out
}

var dataNetNetLabels = map[datanet.State]string{
	datanet.StateOff:      "Ausgeschaltet",
	datanet.StateNeedsKey: "Wartet auf einen Einmal-Schlüssel",
	datanet.StateStarting: "Verbindet …",
	datanet.StateRunning:  "Verbunden",
	datanet.StateError:    "Fehler",
}

// dataNetUsedPorts: alle Ports, die Clients freigeben (für Freigabe-Zeile und Befehl).
func dataNetUsedPorts(clients []dataNetClient) []int {
	var ports []int
	for _, c := range clients {
		if c.Revoked {
			continue
		}
		for _, t := range c.Targets {
			ports = append(ports, t.Port)
		}
	}
	return dataNetCleanPorts(ports)
}

var dataNetNotices = map[string]string{
	"revoked": "Client gesperrt und getrennt. Entferne das Gerät auch im Tailscale-Admin (Machines), damit es nicht im Tailnet bleibt.",
	"deleted": "Client entfernt. Entferne das Gerät auch im Tailscale-Admin (Machines).",
	"saved":   "Gespeichert. Auf diesem Server gilt es sofort, auf dem anderen Server nach dessen Neustart.",
}

var dataNetErrors = map[string]string{
	"notfound": "Den Client gibt es nicht (mehr) oder er ist schon gesperrt.",
	"failed":   "Das ging nicht. Details stehen im Server-Protokoll.",
	"key":      "Der Einmal-Schlüssel muss mit tskey- beginnen.",
	"server":   "Unbekannter Servername.",
}

func (h *Handler) dataNetPageData(r *http.Request, created *dataNetCreated, notice, errMsg string) DataNetPageData {
	ctx := r.Context()
	d := DataNetPageData{
		BaseData: h.baseData(r, "datanet", "Datennetz", "Datennetz"),
		CanEdit:  h.dataNetCan(r, permDataNetEdit, ""),
		CanAdmin: h.dataNetCan(r, permDataNetAdmin, ""),
		Notice:   notice, Err: errMsg, Created: created, Version: pdh.Version(),
		Downloads: dataNetDownloads(),
	}
	d.Enabled = h.dataNetSetting(ctx, dataNetKeyEnabled) == "true"
	st := h.dataNetStatus()
	d.NetState, d.NetStateLabel, d.NetErr = string(st.State), dataNetNetLabels[st.State], st.Err

	nodes := h.dataNetNodes(ctx)
	self := h.dataNetServerName()
	if len(st.Addrs) > 0 { // die eigenen Adressen sind live bekannter als der gespeicherte Stand
		live := make([]string, 0, len(st.Addrs))
		for _, a := range st.Addrs {
			live = append(live, a.String())
		}
		found := false
		for i := range nodes {
			if nodes[i].Server == self {
				nodes[i].Addrs, found = live, true
			}
		}
		if !found {
			nodes = append(nodes, dataNetNode{Server: self, Addrs: live})
			sort.Slice(nodes, func(i, j int) bool { return nodes[i].Server < nodes[j].Server })
		}
	}
	keys := h.dataNetAuthKeyServers(ctx)
	names := map[string]bool{self: true}
	for _, n := range nodes {
		names[n.Server] = true
	}
	for s := range keys {
		names[s] = true
	}
	var sorted []string
	for s := range names {
		sorted = append(sorted, s)
	}
	sort.Strings(sorted)
	for _, s := range sorted {
		row := dataNetServerRow{Name: s, Self: s == self, HasKey: keys[s]}
		for _, n := range nodes {
			if n.Server == s {
				row.Addrs = strings.Join(n.Addrs, ", ")
				if !n.Seen.IsZero() {
					row.Seen = n.Seen.Local().Format("02.01.2006 15:04")
				}
			}
		}
		d.Servers = append(d.Servers, row)
	}

	var clients []dataNetClient
	if h.db != nil {
		var err error
		if clients, err = h.dataNetList(ctx); err != nil {
			log.Warn().Err(err).Msg("Datennetz: Clients nicht lesbar")
			d.Err = firstNonEmpty(d.Err, dataNetErrors["failed"])
		}
	}
	ports := dataNetUsedPorts(clients)
	d.ACL = dataNetACL(ports)
	d.Clients = dataNetRows(clients, time.Now(), d.Version)

	if created != nil {
		addrs := dataNetPDHAddrs(self, sorted, nodes)
		created.NoNode = len(addrs) == 0
		created.Windows = dataNetInstallCmd("windows", created.ID, created.Token, addrs, ports)
		created.Linux = dataNetInstallCmd("linux", created.ID, created.Token, addrs, ports)
		created.PrepWindows, created.SafeWindows = dataNetInstallCmdSafe("windows", created.ID, addrs, ports)
		created.PrepLinux, created.SafeLinux = dataNetInstallCmdSafe("linux", created.ID, addrs, ports)
	}
	return d
}

// DataNetPage: GET /datanet.
func (h *Handler) DataNetPage(w http.ResponseWriter, r *http.Request) {
	if !h.dataNetCan(r, permDataNetView, "") {
		dataNetForbidden(w)
		return
	}
	q := r.URL.Query()
	h.render(w, "datanet", h.dataNetPageData(r, nil, dataNetNotices[q.Get("n")], dataNetErrors[q.Get("e")]))
}

func dataNetBack(notice, errKey string) string {
	q := url.Values{}
	if notice != "" {
		q.Set("n", notice)
	}
	if errKey != "" {
		q.Set("e", errKey)
	}
	if len(q) == 0 {
		return "/datanet"
	}
	return "/datanet?" + q.Encode()
}

// DataNetCreate: POST /datanet/clients. Antwortet direkt mit der Seite, die das
// Token einmal zeigt (Zwischenspeicher aus: der Browser darf es nicht ablegen).
func (h *Handler) DataNetCreate(w http.ResponseWriter, r *http.Request) {
	if !h.dataNetCan(r, permDataNetEdit, "") {
		dataNetForbidden(w)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 80 {
		h.render(w, "datanet", h.dataNetPageData(r, nil, "", "Bitte einen Namen bis 80 Zeichen eingeben."))
		return
	}
	c, token, err := h.dataNetCreate(r.Context(), name, getUser(r).ID)
	switch {
	case errors.Is(err, errDataNetName):
		h.render(w, "datanet", h.dataNetPageData(r, nil, "", "Der Name ist schon vergeben."))
		return
	case err != nil:
		log.Warn().Err(err).Msg("Datennetz: Client nicht angelegt")
		h.render(w, "datanet", h.dataNetPageData(r, nil, "", dataNetErrors["failed"]))
		return
	}
	h.render(w, "datanet", h.dataNetPageData(r, &dataNetCreated{ID: c.ID, Name: c.Name, Token: token}, "", ""))
}

// DataNetRevoke: POST /datanet/clients/{id}/revoke.
func (h *Handler) DataNetRevoke(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.dataNetCan(r, permDataNetEdit, id) {
		dataNetForbidden(w)
		return
	}
	if !dataNetUUIDRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	err := h.dataNetRevokeAndCut(r.Context(), id, getUser(r).ID)
	h.dataNetDone(w, r, err, "revoked")
}

// DataNetDelete: POST /datanet/clients/{id}/delete – nur Bereichsadministrator.
func (h *Handler) DataNetDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.dataNetCan(r, permDataNetAdmin, id) {
		dataNetForbidden(w)
		return
	}
	if !dataNetUUIDRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	err := h.dataNetDeleteAndCut(r.Context(), id, getUser(r).ID)
	h.dataNetDone(w, r, err, "deleted")
}

func (h *Handler) dataNetDone(w http.ResponseWriter, r *http.Request, err error, notice string) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		http.Redirect(w, r, dataNetBack("", "notfound"), http.StatusSeeOther)
	case err != nil:
		log.Warn().Err(err).Str("aktion", notice).Msg("Datennetz: Aktion fehlgeschlagen")
		http.Redirect(w, r, dataNetBack("", "failed"), http.StatusSeeOther)
	default:
		http.Redirect(w, r, dataNetBack(notice, ""), http.StatusSeeOther)
	}
}

// dataNetLog schreibt einen Eintrag ohne Client ins Ereignisprotokoll.
func (h *Handler) dataNetLog(ctx context.Context, userID, action, note string) {
	if _, err := h.db.Exec(ctx, `INSERT INTO datanet_events (user_id, client_name, action, note)
		VALUES (NULLIF($1, '')::uuid, '', $2, $3)`, userID, action, note); err != nil {
		log.Warn().Err(err).Msg("Datennetz: Ereignis nicht gespeichert")
	}
}

// DataNetSettings: POST /datanet/settings – Datennetz an/aus und Einmal-Schlüssel je Server.
func (h *Handler) DataNetSettings(w http.ResponseWriter, r *http.Request) {
	if !h.dataNetCan(r, permDataNetAdmin, "") {
		dataNetForbidden(w)
		return
	}
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, dataNetBack("", "failed"), http.StatusSeeOther)
		return
	}
	// Erst prüfen, dann speichern: ein falscher Wert ändert nichts.
	keys := map[string]string{}
	for field, vals := range r.PostForm {
		server, ok := strings.CutPrefix(field, "authkey.")
		if !ok || len(vals) == 0 {
			continue
		}
		key := strings.TrimSpace(vals[0])
		if key == "" {
			continue // leer lässt einen hinterlegten Schlüssel stehen
		}
		if !dataNetServerRe.MatchString(server) {
			http.Redirect(w, r, dataNetBack("", "server"), http.StatusSeeOther)
			return
		}
		if !strings.HasPrefix(key, "tskey-") || len(key) > 300 {
			http.Redirect(w, r, dataNetBack("", "key"), http.StatusSeeOther)
			return
		}
		keys[server] = key
	}
	enabled := r.PostForm.Get("enabled") == "on"
	was := h.dataNetSetting(ctx, dataNetKeyEnabled) == "true"
	if err := h.dataNetPut(ctx, dataNetKeyEnabled, strconv.FormatBool(enabled)); err != nil {
		log.Warn().Err(err).Msg("Datennetz: Schalter nicht gespeichert")
		http.Redirect(w, r, dataNetBack("", "failed"), http.StatusSeeOther)
		return
	}
	for server, key := range keys {
		if err := h.dataNetSetAuthKey(ctx, server, key); err != nil {
			log.Warn().Err(err).Msg("Datennetz: Einmal-Schlüssel nicht gespeichert")
			http.Redirect(w, r, dataNetBack("", "failed"), http.StatusSeeOther)
			return
		}
	}
	note := "unverändert"
	switch {
	case enabled && !was:
		note = "eingeschaltet"
	case !enabled && was:
		note = "ausgeschaltet"
	}
	if len(keys) > 0 { // nur die Zahl, nie der Schlüssel
		note += ", Einmal-Schlüssel für " + fmt.Sprint(len(keys)) + " Server hinterlegt"
	}
	h.dataNetLog(ctx, getUser(r).ID, "settings", note)
	// Diesen Server sofort neu starten; der andere liest die Einstellungen bei seinem Start.
	h.dataNetMu.RLock()
	active := h.dataNetActive
	h.dataNetMu.RUnlock()
	h.StartDataNet(context.Background(), active)
	http.Redirect(w, r, dataNetBack("saved", ""), http.StatusSeeOther)
}
