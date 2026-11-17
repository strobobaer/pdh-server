package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"pdh/internal/modules/it"
	"pdh/internal/modules/it/netsync"
)

// IT-Netzabgleich – Oberfläche: Seite „Netz-Abgleich“ (/it/net) mit
// Einstellungen und Vorschlägen, Schalter „überwachen“ und „Quelle trennen“
// am Asset. Ansehen mit it.view, einrichten und Vorschläge übernehmen mit
// it.netsync, Schalter am Asset mit it.edit für genau diesen Datensatz.

const (
	permITView    = "it.view"
	permITEdit    = "it.edit"
	permITNetsync = "it.netsync"
)

var itNetUUIDRe = regexp.MustCompile(`^` + uuidRe + `$`)

// itNetCan: Recht (assetID "" = irgendwo vorhanden, sonst für dieses Asset in seiner Abteilung).
func (h *Handler) itNetCan(r *http.Request, perm, assetID string) bool {
	if h.hasPermHook != nil {
		return h.hasPermHook(r, perm)
	}
	if h.rbac == nil {
		return false
	}
	if assetID == "" {
		return h.hasPerm(r, perm)
	}
	return h.hasPermRec(r, perm, "it_asset", assetID)
}

func itNetForbidden(w http.ResponseWriter) {
	http.Error(w, "Keine Berechtigung", http.StatusForbidden)
}

// itNetSource: Name der Quelle UniFi in den Assets (Tests nehmen eine eigene).
func (h *Handler) itNetSource() string {
	if n := h.itNet.of(itNetSourceUnifi).name; n != "" {
		return n
	}
	return itNetSourceUnifi
}

// itNetStateBadge: Text und Abzeichen-Klasse des Netz-Zustands.
func itNetStateBadge(state string) (string, string) {
	switch state {
	case "online":
		return "online", "b-green"
	case "offline":
		return "offline", "b-red"
	}
	return "unbekannt", "b-gray"
}

// itNetCandWhere: Vorschläge der Quellen in $1 (net_source-Namen). Ein Vorschlag
// entfällt, wenn seine MAC schon ein Asset einer anderen dieser Quellen ist –
// sonst entstünde beim Übernehmen ein Doppel (z. B. ein Omada-Access-Point, den
// UniFi als Client am Switch sieht).
const itNetCandWhere = `c.source = ANY($1::text[]) AND NOT ` + itNetCandTaken

// itNetCandTaken: die MAC des Vorschlags c gehört schon einem Asset einer anderen Quelle aus $1.
const itNetCandTaken = `EXISTS (SELECT 1 FROM it_assets t
	WHERE t.net_source = ANY($1::text[]) AND t.net_source <> c.source
	  AND regexp_replace(lower(t.mac_address), '[^0-9a-f]', '', 'g') = replace(c.mac, ':', ''))`

// itNetOpenCandidates: Zahl der offenen (nicht ausgeblendeten) Vorschläge aller Quellen.
func (h *Handler) itNetOpenCandidates(ctx context.Context) int {
	if h.db == nil {
		return 0
	}
	var n int
	if err := h.db.QueryRow(ctx, `SELECT count(*) FROM it_net_candidates c WHERE `+itNetCandWhere+` AND NOT c.ignored`, h.itNetSourceNamesInUse()).Scan(&n); err != nil {
		itNetLog().Warn().Err(err).Msg("vorschläge zählen")
	}
	return n
}

func itNetLog() *zerolog.Logger {
	return componentLog("it-netzabgleich")
}

// itNetSourceNames: Anzeigenamen der Quellen.
var itNetSourceNames = map[string]string{itNetSourceUnifi: "UniFi", itNetSourceOmada: "Omada", itNetSourceSNMP: "SNMP"}

// itNetSourceName: Anzeigename einer Quelle (unbekannt → Schlüssel selbst).
func itNetSourceName(src string) string {
	if n, ok := itNetSourceNames[src]; ok {
		return n
	}
	return src
}

// ── Seite ────────────────────────────────────────────────────

var itNetPriorities = []itNetOption{
	{"low", "Niedrig"}, {"medium", "Mittel"}, {"high", "Hoch"}, {"critical", "Kritisch"},
}

type itNetOption struct{ Value, Label string }

// ITNetCandidate: eine Zeile der Vorschlagsliste.
type ITNetCandidate struct {
	MAC, Name, IP, Kind, Uplink, LastSeen string
	Source, SourceLabel                   string // fester Schlüssel (Feld „source“) und Anzeigename
}

// ITNetSourceView: Status einer Quelle auf der Seite.
type ITNetSourceView struct {
	Key, Label string
	Title      string // Überschrift der Karte
	Field      string // Name des Einschalters im Einstellungsformular
	Connected  bool   // eingerichtet (Adresse, Zugangsdaten, bestätigter Fingerabdruck; SNMP: mindestens ein Profil)
	Enabled    bool
	NoClients  bool   // die Quelle kennt keine Clients (SNMP)
	CanConnect bool   // Recht für den Reiter der Server-Einstellungen
	ConfigURL  string // Reiter der Server-Einstellungen
	Missing    string // Satz, solange die Quelle nicht eingerichtet ist
	Setup      string // was dort einzutragen ist
	SetupDo    string // … und was damit zu tun ist
	LinkText   string // Link zum Reiter, wenn eingerichtet
	PermHint   string // wer die Verbindung einrichtet (ohne eigenes Recht)
	Note       string // Hinweis zur Quelle
	LastRun    string
	LastError  string
	Devices    string
	Clients    string
}

// itNetOmadaNote: ehrlicher Hinweis, solange es keinen Test an einem echten Controller gab.
const itNetOmadaNote = "Die Omada-Anbindung ist noch nicht an einem echten Controller erprobt."

// itNetSNMPNote: ebenso für SNMP, solange es keine Abnahme an echten Geräten gab.
const itNetSNMPNote = "SNMP v3 ist noch nicht an echten Switches und Druckern erprobt."

// itNetSourceViews: Status aller Quellen für die Seite.
func (h *Handler) itNetSourceViews(r *http.Request) []ITNetSourceView {
	ctx := r.Context()
	var out []ITNetSourceView
	for _, s := range h.itNetSources() {
		v := ITNetSourceView{Key: s.Key, Label: s.Label, Title: "Quelle " + s.Label, Field: s.Field, Enabled: h.itNetOn(ctx, s.keyOn),
			NoClients: s.NoClients, Missing: s.Label + " ist noch nicht verbunden.",
			SetupDo: "eintragen und den Fingerabdruck bestätigen", LinkText: "Verbindung in den Server-Einstellungen, Reiter „" + s.Label + "“"}
		switch s.Key {
		case itNetSourceUnifi:
			v.Connected = h.certSetting(ctx, keyUnifiURL) != "" && h.certSetting(ctx, keyUnifiKey) != "" && h.certSetting(ctx, keyUnifiPin) != ""
			v.CanConnect = h.itNetCan(r, permUnifi, "")
			v.ConfigURL = "/admin/server-config?tab=unifi"
			v.Setup = "Adresse und API-Schlüssel"
			v.PermHint = "Die Verbindung richtet eine Person mit dem Recht „Namen im Haus eintragen (UniFi)“ in den Server-Einstellungen ein."
		case itNetSourceSNMP:
			// „verbunden“ heißt hier: Es gibt mindestens ein Zugangsprofil.
			v.Title = "Quelle SNMP v3"
			v.Connected = h.snmpProfileCount(ctx) > 0
			v.CanConnect = h.itNetCan(r, permSNMP, "") || h.itNetCan(r, permSNMPAdmin, "")
			v.ConfigURL = "/admin/server-config?tab=snmp"
			v.Missing = "Für SNMP gibt es noch kein Zugangsprofil."
			v.Setup, v.SetupDo = "Ein Zugangsprofil (Benutzer, Verfahren und Kennwörter)", "anlegen"
			v.LinkText = "Zugangsprofile in den Server-Einstellungen, Reiter „SNMP“"
			v.Note = itNetSNMPNote
			v.PermHint = "Die Zugangsprofile legt eine Person mit dem Recht „SNMP-Zugangsprofile verwalten“ (system.snmp) in den Server-Einstellungen an."
		case itNetSourceOmada:
			v.Connected = h.omadaConnected(ctx)
			v.CanConnect = h.itNetCan(r, permOmada, "")
			v.ConfigURL = "/admin/server-config?tab=omada"
			v.Setup = "Adresse, Omada-ID, Client-ID und Client-Secret"
			v.PermHint = "Die Verbindung richtet eine Person mit dem Recht „Omada-Controller anbinden“ (system.omada) in den Server-Einstellungen ein."
			v.Note = itNetOmadaNote
		}
		if t, err := time.Parse(time.RFC3339, h.appSetting(ctx, s.keyLastRun, "")); err == nil {
			v.LastRun = alarmTime(t)
		}
		if v.Enabled { // Fehler nur, solange der Abgleich eingeschaltet ist
			v.LastError = h.appSetting(ctx, s.keyLastErr, "")
		}
		if dev, cl, ok := strings.Cut(h.appSetting(ctx, s.keyLastCount, ""), "/"); ok {
			v.Devices, v.Clients = dev, cl
		}
		out = append(out, v)
	}
	return out
}

type ITNetPageData struct {
	BaseData
	CanSetup  bool              // it.netsync
	Sources   []ITNetSourceView // Status je Quelle (UniFi, Omada, SNMP)
	SNMP      ITNetSNMPCard     // Karte „SNMP-Geräte“ (snmp_web.go)
	AnyOn     bool              // mindestens eine Quelle ist eingeschaltet
	Interval  int
	Grace     int
	RespUser  string
	RespGroup string
	Priority  string
	Users     []itNetOption
	Groups    []itNetOption
	Types     []itNetOption
	Prios     []itNetOption
	Tab       string // "" = Vorschläge, "hidden" = Ausgeblendet
	Query     string
	Open      int
	Hidden    int
	Rows      []ITNetCandidate
	Notice    string
	Err       string
}

var itNetNotices = map[string]string{
	"saved":    "Einstellungen gespeichert.",
	"run":      "Abgleich ausgeführt.",
	"off":      "Der Abgleich ist ausgeschaltet – es wurde nichts abgerufen.",
	"adopted":  "Vorschlag als Asset übernommen.",
	"ignored":  "Vorschlag ausgeblendet.",
	"restored": "Vorschlag zurückgeholt.",
}

var itNetErrors = map[string]string{
	"interval": "Intervall: bitte 30 bis 3600 Sekunden eingeben.",
	"grace":    "Karenz: bitte 1 bis 1440 Minuten eingeben.",
	"priority": "Bitte eine gültige Priorität wählen.",
	"resp":     "Verantwortlich: ungültige Auswahl.",
	"both":     "Bitte entweder einen Benutzer oder eine Gruppe als verantwortlich wählen – nicht beides.",
	"save":     "Die Einstellungen konnten nicht gespeichert werden.",
	"maint":    "Wartungssperre aktiv – der Abgleich ist gerade nicht möglich.",
	"run":      "Der Abgleich ist bei mindestens einer Quelle fehlgeschlagen – siehe Fehler bei der Quelle.",
	"source":   "Unbekannte Quelle – bitte die Seite neu laden und noch einmal versuchen.",
	"othersrc": "Zu dieser MAC-Adresse gibt es schon ein Asset aus einer anderen Quelle – es wird kein zweites angelegt.",
	"type":     "Bitte einen gültigen Typ wählen.",
	"nocand":   "Diesen Vorschlag gibt es nicht mehr.",
	"adopt":    "Der Vorschlag konnte nicht übernommen werden.",
	"detached": "Zu dieser MAC-Adresse gibt es ein Asset mit getrennter Quelle – dort „Wieder verbinden“ wählen.",
}

// ITNetPage: GET /it/net.
func (h *Handler) ITNetPage(w http.ResponseWriter, r *http.Request) {
	if !h.itNetCan(r, permITView, "") {
		itNetForbidden(w)
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	d := ITNetPageData{
		BaseData: h.baseData(r, "it-net", "Netz-Abgleich", "IT-Infrastruktur"),
		CanSetup: h.itNetCan(r, permITNetsync, ""),
		Prios:    itNetPriorities,
		Tab:      q.Get("tab"), Query: strings.TrimSpace(q.Get("q")),
		Notice: itNetNotices[q.Get("n")], Err: itNetErrors[q.Get("e")],
	}
	if d.Tab != "hidden" {
		d.Tab = ""
	}
	// Typauswahl beim Übernehmen: aktive Kategorien außer „Netzwerk“ (das Netzgeräte vorbehalten ist)
	cats, _ := h.itCategories(ctx)
	for _, c := range cats {
		if c.Active && c.Key != "network" {
			d.Types = append(d.Types, itNetOption{c.Key, c.Name})
		}
	}
	d.Sources = h.itNetSourceViews(r)
	for _, s := range d.Sources {
		d.AnyOn = d.AnyOn || s.Enabled
	}
	d.SNMP = h.itNetSNMPCard(r)
	d.Interval = int(h.itNetInterval(ctx) / time.Second)
	d.Grace = int(h.itNetGrace(ctx) / time.Minute)
	d.RespUser = h.appSetting(ctx, keyITNetRespUser, "")
	d.RespGroup = h.appSetting(ctx, keyITNetRespGroup, "")
	d.Priority = string(h.itNetPriority(ctx))
	if d.CanSetup {
		d.Users, d.Groups = h.itNetUserOptions(ctx), h.itNetGroupOptions(ctx)
	}
	// Vorschläge über alle Quellen; die Zeile nennt ihre Quelle.
	srcs := h.itNetSources()
	names := h.itNetSourceNamesInUse()
	if err := h.db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE NOT c.ignored), count(*) FILTER (WHERE c.ignored)
		FROM it_net_candidates c WHERE `+itNetCandWhere, names).Scan(&d.Open, &d.Hidden); err != nil {
		itNetLog().Warn().Err(err).Msg("vorschläge zählen")
	}
	like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(d.Query) + "%"
	rows, err := h.db.Query(ctx, `SELECT c.source, c.mac, c.name, c.ip, c.kind, c.uplink, c.last_seen FROM it_net_candidates c
		WHERE `+itNetCandWhere+` AND c.ignored = $2
		  AND ($3 = '%%' OR c.name ILIKE $3 OR c.ip ILIKE $3 OR c.mac ILIKE $3 OR c.uplink ILIKE $3)
		ORDER BY c.last_seen DESC, c.name, c.source LIMIT 500`, names, d.Tab == "hidden", like)
	if err != nil {
		itNetLog().Warn().Err(err).Msg("vorschläge lesen")
	} else {
		for rows.Next() {
			var c ITNetCandidate
			var seen time.Time
			var name string
			if err := rows.Scan(&name, &c.MAC, &c.Name, &c.IP, &c.Kind, &c.Uplink, &seen); err != nil {
				itNetLog().Warn().Err(err).Msg("vorschlag lesen")
				continue
			}
			for _, s := range srcs {
				if s.Name == name {
					c.Source, c.SourceLabel = s.Key, s.Label
				}
			}
			c.LastSeen = alarmTime(seen)
			d.Rows = append(d.Rows, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			itNetLog().Warn().Err(err).Msg("vorschläge lesen")
		}
	}
	h.render(w, "it_net", d)
}

func (h *Handler) itNetUserOptions(ctx context.Context) []itNetOption {
	rows, err := h.db.Query(ctx, `SELECT id::text, trim(first_name || ' ' || last_name) FROM users
		WHERE active AND id <> $1::uuid ORDER BY lower(last_name), lower(first_name)`, pdhSystemUserID)
	if err != nil {
		itNetLog().Warn().Err(err).Msg("benutzer lesen")
		return nil
	}
	defer rows.Close()
	var out []itNetOption
	for rows.Next() {
		var o itNetOption
		if rows.Scan(&o.Value, &o.Label) == nil {
			out = append(out, o)
		}
	}
	if err := rows.Err(); err != nil {
		itNetLog().Warn().Err(err).Msg("benutzer lesen")
	}
	return out
}

func (h *Handler) itNetGroupOptions(ctx context.Context) []itNetOption {
	var out []itNetOption
	for _, g := range h.loadGroups(ctx) {
		label := g.Name
		if g.DepartmentName != "" {
			label += " (" + g.DepartmentName + ")"
		}
		out = append(out, itNetOption{g.ID, label})
	}
	return out
}

func itNetBack(w http.ResponseWriter, r *http.Request, notice, errKey string) {
	v := url.Values{}
	if notice != "" {
		v.Set("n", notice)
	}
	if errKey != "" {
		v.Set("e", errKey)
	}
	if tab := r.FormValue("tab"); tab == "hidden" {
		v.Set("tab", tab)
	}
	target := "/it/net"
	if len(v) > 0 {
		target += "?" + v.Encode()
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// ── Einstellungen ────────────────────────────────────────────

type itNetSettings struct {
	Enabled           bool // UniFi
	OmadaEnabled      bool
	SNMPEnabled       bool
	Interval, Grace   int
	Priority          string
	RespUser, RespGrp string
}

// itNetSettingsFromForm prüft das Formular; Rückgabe "" oder Schlüssel aus itNetErrors.
func itNetSettingsFromForm(f url.Values) (itNetSettings, string) {
	s := itNetSettings{
		Enabled:      f.Get("enabled") != "",
		OmadaEnabled: f.Get("omada_enabled") != "",
		SNMPEnabled:  f.Get("snmp_enabled") != "",
		Priority:     strings.TrimSpace(f.Get("priority")),
		RespUser:     strings.TrimSpace(f.Get("responsible_user")),
		RespGrp:      strings.TrimSpace(f.Get("responsible_group")),
	}
	var err error
	if s.Interval, err = strconv.Atoi(strings.TrimSpace(f.Get("interval_s"))); err != nil || s.Interval < 30 || s.Interval > 3600 {
		return s, "interval"
	}
	if s.Grace, err = strconv.Atoi(strings.TrimSpace(f.Get("grace_min"))); err != nil || s.Grace < 1 || s.Grace > 1440 {
		return s, "grace"
	}
	ok := false
	for _, p := range itNetPriorities {
		ok = ok || p.Value == s.Priority
	}
	if !ok {
		return s, "priority"
	}
	if s.RespUser != "" && s.RespGrp != "" {
		return s, "both"
	}
	for _, id := range []string{s.RespUser, s.RespGrp} {
		if id != "" && !itNetUUIDRe.MatchString(id) {
			return s, "resp"
		}
	}
	return s, ""
}

// ITNetSettingsWeb: POST /it/net/settings (it.netsync).
func (h *Handler) ITNetSettingsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.itNetCan(r, permITNetsync, "") {
		itNetForbidden(w)
		return
	}
	_ = r.ParseForm()
	s, bad := itNetSettingsFromForm(r.PostForm)
	if bad != "" {
		itNetBack(w, r, "", bad)
		return
	}
	ctx := r.Context()
	want := map[string]bool{itNetSourceUnifi: s.Enabled, itNetSourceOmada: s.OmadaEnabled, itNetSourceSNMP: s.SNMPEnabled}
	srcs := h.itNetSources()
	wasOn := map[string]bool{}
	var kvs [][2]string
	for _, src := range srcs {
		wasOn[src.Key] = h.itNetOn(ctx, src.keyOn)
		on := "0"
		if want[src.Key] {
			on = "1"
		}
		kvs = append(kvs, [2]string{src.keyOn, on})
	}
	kvs = append(kvs, [][2]string{
		{keyITNetInterval, strconv.Itoa(s.Interval)}, {keyITNetGrace, strconv.Itoa(s.Grace)},
		{keyITNetPriority, s.Priority}, {keyITNetRespUser, s.RespUser}, {keyITNetRespGroup, s.RespGrp},
	}...)
	for _, kv := range kvs {
		if err := h.setAppSetting(ctx, kv[0], kv[1]); err != nil {
			itNetLog().Error().Err(err).Str("einstellung", kv[0]).Msg("einstellung speichern")
			itNetBack(w, r, "", "save")
			return
		}
	}
	for _, src := range srcs {
		if wasOn[src.Key] && !want[src.Key] {
			// Quelle ausgeschaltet: ihre alten Zustandsmarken wären sonst veraltet.
			if err := h.itNetStore().MarkUnknown(ctx, src.Name, time.Now()); err != nil {
				itNetLog().Warn().Err(err).Str("quelle", src.Key).Msg("zustand nicht auf unbekannt gesetzt")
			}
		}
	}
	itNetBack(w, r, "saved", "")
}

// ITNetRunWeb: POST /it/net/run (it.netsync) – ein Abgleich sofort über alle
// eingeschalteten Quellen; ruht in der Wartungssperre.
func (h *Handler) ITNetRunWeb(w http.ResponseWriter, r *http.Request) {
	if !h.itNetCan(r, permITNetsync, "") {
		itNetForbidden(w)
		return
	}
	if h.MaintLocked() {
		itNetBack(w, r, "", "maint")
		return
	}
	ctx := r.Context()
	if !h.itNetAnyEnabled(ctx) {
		itNetBack(w, r, "off", "")
		return
	}
	// Der Lauf soll nicht abbrechen, wenn der Browser die Anfrage schließt.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	if err := h.itNetRunOnce(cctx, time.Now()); err != nil {
		itNetBack(w, r, "", "run")
		return
	}
	itNetBack(w, r, "run", "")
}

// ── Vorschläge ───────────────────────────────────────────────

// itNetFormSource: Quelle aus dem Feld „source“ (fehlt es: UniFi). Nur bekannte Quellen.
func (h *Handler) itNetFormSource(r *http.Request) (itNetSrc, bool) {
	s, ok := h.itNetSrcByKey(strings.TrimSpace(r.FormValue("source")))
	// Quellen ohne Clients (SNMP) haben keine Vorschläge – dort gibt es nichts zu übernehmen.
	return s, ok && !s.NoClients
}

// ITNetAdoptWeb: POST /it/net/candidates/adopt (source, mac, type).
func (h *Handler) ITNetAdoptWeb(w http.ResponseWriter, r *http.Request) {
	if !h.itNetCan(r, permITNetsync, "") {
		itNetForbidden(w)
		return
	}
	src, ok := h.itNetFormSource(r)
	if !ok {
		itNetBack(w, r, "", "source")
		return
	}
	typ := r.FormValue("type") // ungültige oder ausgeblendete Kategorie lehnt Adopt mit ErrBadType ab
	uid := getUser(r).ID
	if uid == "" {
		uid = pdhSystemUserID
	}
	store := &netsync.Store{DB: h.db, SystemUserID: pdhSystemUserID}
	// Gehört die MAC schon einem Asset der anderen Quelle, entstünde ein Doppel.
	var taken bool
	if err := h.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM it_net_candidates c
		WHERE c.source = $2 AND c.mac = $3 AND `+itNetCandTaken+`)`,
		h.itNetSourceNamesInUse(), src.Name, netsync.NormMAC(r.FormValue("mac"))).Scan(&taken); err != nil {
		itNetLog().Error().Err(err).Msg("vorschlag prüfen")
		itNetBack(w, r, "", "adopt")
		return
	}
	if taken {
		itNetBack(w, r, "", "othersrc")
		return
	}
	_, err := store.Adopt(r.Context(), src.Name, r.FormValue("mac"), typ, uid, time.Now())
	switch {
	case errors.Is(err, netsync.ErrNoCandidate):
		itNetBack(w, r, "", "nocand")
	case errors.Is(err, netsync.ErrDetached):
		itNetBack(w, r, "", "detached")
	case errors.Is(err, netsync.ErrBadType):
		itNetBack(w, r, "", "type")
	case err != nil:
		itNetLog().Error().Err(err).Msg("vorschlag übernehmen")
		itNetBack(w, r, "", "adopt")
	default:
		itNetBack(w, r, "adopted", "")
	}
}

// ITNetIgnoreWeb: POST /it/net/candidates/ignore (source, mac, ignored=1|0).
func (h *Handler) ITNetIgnoreWeb(w http.ResponseWriter, r *http.Request) {
	if !h.itNetCan(r, permITNetsync, "") {
		itNetForbidden(w)
		return
	}
	src, ok := h.itNetFormSource(r)
	if !ok {
		itNetBack(w, r, "", "source")
		return
	}
	ignored := r.FormValue("ignored") == "1"
	store := &netsync.Store{DB: h.db, SystemUserID: pdhSystemUserID}
	if err := store.Ignore(r.Context(), src.Name, r.FormValue("mac"), ignored); err != nil {
		if !errors.Is(err, netsync.ErrNoCandidate) {
			itNetLog().Error().Err(err).Msg("vorschlag ausblenden")
			itNetBack(w, r, "", "save")
			return
		}
		itNetBack(w, r, "", "nocand")
		return
	}
	if ignored {
		itNetBack(w, r, "ignored", "")
		return
	}
	itNetBack(w, r, "restored", "")
}

// ── Asset: Karte „Netz“, überwachen, Quelle trennen ─────────

type ITNetPortView struct {
	Idx       int
	Connector string
	State     string
	Up        bool
	Speed     string
	PoE       string
}

type ITNetDetailView struct {
	Source         string
	Role           string // Netzgerät oder Client
	StateLabel     string
	StateClass     string
	StateSince     string
	LastSeen       string
	Firmware       string
	FirmwareUpdate bool
	Uplink         string
	Monitored      bool
	Gone           bool
	Ports          []ITNetPortView
	HasPoE         bool // mindestens ein Anschluss meldet PoE – sonst entfällt die Spalte (SNMP liest kein PoE)
	// Karenz bis zum Alarm: wirksamer Wert in Minuten und Herkunft (eigener Wert, Kategorie, allgemein).
	GraceMin    int
	GraceSource string
	GraceOwn    string // eigener Wert fürs Feld (leer = keiner)
}

// Hinweise und Fehler der Detailseite (feste Schlüssel in der Adresse).
var itNetDetailNotices = map[string]string{
	"grace":       "Karenz gespeichert.",
	"grace-reset": "Eigener Wert entfernt – es gilt wieder der Wert der Kategorie bzw. der allgemeine.",
}

var itNetDetailErrors = map[string]string{
	"grace": "Karenz: bitte 1 bis 1440 Minuten eingeben oder das Feld leeren.",
	"save":  "Die Karenz konnte nicht gespeichert werden.",
}

func itNetSpeed(mbps int) string {
	switch {
	case mbps <= 0:
		return "–"
	case mbps%1000 == 0:
		return fmt.Sprintf("%d Gbit/s", mbps/1000)
	case mbps > 1000:
		return strings.Replace(fmt.Sprintf("%.1f Gbit/s", float64(mbps)/1000), ".", ",", 1)
	}
	return fmt.Sprintf("%d Mbit/s", mbps)
}

// itNetPortState: Port-Zustand von UniFi (UP/DOWN/UNKNOWN) auf Deutsch.
func itNetPortState(s string) string {
	switch strings.ToUpper(s) {
	case "UP":
		return "aktiv"
	case "DOWN":
		return "getrennt"
	}
	return "unbekannt"
}

// itNetPoE: PoE-Zustand von UniFi auf Deutsch; leer, wenn der Port kein PoE hat.
func itNetPoE(s string) string {
	switch strings.ToUpper(s) {
	case "":
		return ""
	case "UP":
		return "an"
	case "DOWN":
		return "aus"
	case "LIMITED":
		return "begrenzt"
	}
	return "unbekannt"
}

// itNetDetail: Daten der Karte „Netz“ samt Ports.
func (h *Handler) itNetDetail(ctx context.Context, a *it.Asset, cats map[string]it.Category) *ITNetDetailView {
	v := &ITNetDetailView{Source: itNetSourceName(a.NetSource), Firmware: a.Firmware, FirmwareUpdate: a.FirmwareUpdate,
		Uplink: a.NetUplink, Monitored: a.NetMonitored, Gone: a.NetGone, Role: "Client"}
	// Karenz: eigener Wert, sonst der der Kategorie, sonst der allgemeine (wie der Wächter).
	v.GraceMin, v.GraceSource = int(h.itNetGrace(ctx)/time.Minute), "allgemein"
	if c, ok := cats[string(a.Type)]; ok && c.GraceMinutes != nil {
		v.GraceMin, v.GraceSource = *c.GraceMinutes, "Kategorie"
	}
	if h.db != nil {
		var own *int
		if err := h.db.QueryRow(ctx, `SELECT net_grace_minutes FROM it_assets WHERE id = $1::uuid`, a.ID).Scan(&own); err != nil {
			itNetLog().Warn().Err(err).Msg("karenz lesen")
		} else if own != nil {
			v.GraceMin, v.GraceSource, v.GraceOwn = *own, "eigener Wert", strconv.Itoa(*own)
		}
	}
	if a.NetRole == "device" {
		v.Role = "Netzgerät"
	}
	v.StateLabel, v.StateClass = itNetStateBadge(a.NetState)
	if a.NetStateSince != nil {
		v.StateSince = alarmTime(*a.NetStateSince)
	}
	if a.NetLastSeen != nil {
		v.LastSeen = alarmTime(*a.NetLastSeen)
	}
	if h.db == nil {
		return v
	}
	rows, err := h.db.Query(ctx, `SELECT idx, connector, state, speed_mbps, poe_state FROM it_asset_ports WHERE asset_id = $1::uuid ORDER BY idx`, a.ID)
	if err != nil {
		itNetLog().Warn().Err(err).Msg("ports lesen")
		return v
	}
	defer rows.Close()
	for rows.Next() {
		var p ITNetPortView
		var speed int
		if rows.Scan(&p.Idx, &p.Connector, &p.State, &speed, &p.PoE) == nil {
			p.Up = strings.EqualFold(p.State, "UP")
			p.State, p.PoE = itNetPortState(p.State), itNetPoE(p.PoE)
			p.Speed = itNetSpeed(speed)
			if !p.Up {
				p.Speed = "–"
			}
			v.HasPoE = v.HasPoE || p.PoE != ""
			v.Ports = append(v.Ports, p)
		}
	}
	if err := rows.Err(); err != nil {
		itNetLog().Warn().Err(err).Msg("ports lesen")
	}
	return v
}

// itNetAssetGuard: ID prüfen und it.edit für dieses Asset verlangen.
func (h *Handler) itNetAssetGuard(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !itNetUUIDRe.MatchString(id) {
		http.NotFound(w, r)
		return "", false
	}
	if !h.itNetCan(r, permITEdit, id) {
		itNetForbidden(w)
		return "", false
	}
	return id, true
}

// ITNetMonitorWeb: POST /it/{id}/net-monitor (on=1|0) – Ausfälle melden oder nicht.
func (h *Handler) ITNetMonitorWeb(w http.ResponseWriter, r *http.Request) {
	id, ok := h.itNetAssetGuard(w, r)
	if !ok {
		return
	}
	on := r.FormValue("on") == "1"
	if _, err := h.db.Exec(r.Context(), `UPDATE it_assets SET net_monitored = $2, updated_at = now()
		WHERE id = $1::uuid AND net_source IS NOT NULL AND NOT net_detached`, id, on); err != nil {
		itNetLog().Error().Err(err).Msg("überwachung umschalten")
		http.Error(w, "Speichern fehlgeschlagen", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/it/"+id, http.StatusSeeOther)
}

// ITNetGraceWeb: POST /it/{id}/net-grace (minutes) – eigene Karenz des Assets setzen; leer = zurücksetzen.
func (h *Handler) ITNetGraceWeb(w http.ResponseWriter, r *http.Request) {
	id, ok := h.itNetAssetGuard(w, r)
	if !ok {
		return
	}
	back := func(key string) { http.Redirect(w, r, "/it/"+id+"?"+key, http.StatusSeeOther) }
	var minutes *int
	if s := strings.TrimSpace(r.FormValue("minutes")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 1440 {
			back("e=grace")
			return
		}
		minutes = &n
	}
	switch err := h.it.SetAssetGrace(r.Context(), id, minutes); {
	case errors.Is(err, pgx.ErrNoRows):
		http.NotFound(w, r)
	case errors.Is(err, it.ErrCategoryInvalid):
		back("e=grace")
	case err != nil:
		itNetLog().Error().Err(err).Msg("karenz speichern")
		back("e=save")
	case minutes == nil:
		back("n=grace-reset")
	default:
		back("n=grace")
	}
}

// itNetDetachTx trennt ein Asset von seiner Quelle: Es bleibt bestehen, wird aber nicht
// mehr abgeglichen und nicht überwacht; Anschlüsse und Wächterzustand entfallen.
func itNetDetachTx(ctx context.Context, tx pgx.Tx, assetID string) error {
	if _, err := tx.Exec(ctx, `UPDATE it_assets SET net_detached = true, net_monitored = false,
			net_state = 'unknown', net_state_since = NULL, net_gone = false, updated_at = now()
		WHERE id = $1::uuid AND net_source IS NOT NULL`, assetID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM it_asset_ports WHERE asset_id = $1::uuid`, assetID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM it_net_alarm_state WHERE asset_id = $1::uuid`, assetID)
	return err
}

// ITNetDetachWeb: POST /it/{id}/net-detach – Asset bleibt, wird nicht mehr abgeglichen.
// Quelle und ID bleiben stehen (sonst legte der nächste Abgleich ein Doppel an);
// net_detached sorgt dafür, dass Abgleich, Vorschläge und Wächter es auslassen.
func (h *Handler) ITNetDetachWeb(w http.ResponseWriter, r *http.Request) {
	id, ok := h.itNetAssetGuard(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	err := func() error {
		tx, err := h.db.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := itNetDetachTx(ctx, tx, id); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}()
	if err != nil {
		itNetLog().Error().Err(err).Msg("quelle trennen")
		http.Error(w, "Trennen fehlgeschlagen", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/it/"+id, http.StatusSeeOther)
}

// ITNetReattachWeb: POST /it/{id}/net-reattach – getrenntes Asset wieder abgleichen.
// Überwacht wird es wieder nach der Vorgabe seiner Kategorie („Neu übernommene
// überwachen“); der Zustand bleibt „unbekannt“ bis zum nächsten Abgleich.
func (h *Handler) ITNetReattachWeb(w http.ResponseWriter, r *http.Request) {
	id, ok := h.itNetAssetGuard(w, r)
	if !ok {
		return
	}
	if _, err := h.db.Exec(r.Context(), `UPDATE it_assets SET net_detached = false,
			net_monitored = `+netsync.MonitoredForCategorySQL("type::text")+`, updated_at = now()
		WHERE id = $1::uuid AND net_source IS NOT NULL AND net_detached`, id); err != nil {
		itNetLog().Error().Err(err).Msg("wieder verbinden")
		http.Error(w, "Wieder verbinden fehlgeschlagen", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/it/"+id, http.StatusSeeOther)
}

// itListURL: Adresse der IT-Liste mit Typ- und Netzfilter (leere Werte entfallen).
func itListURL(typ, net string) string {
	v := []string{}
	if typ != "" {
		v = append(v, "type="+url.QueryEscape(typ))
	}
	if net != "" {
		v = append(v, "net="+url.QueryEscape(net))
	}
	if len(v) == 0 {
		return "/it"
	}
	return "/it?" + strings.Join(v, "&")
}

// TypeURL: Typfilter setzen, Netzfilter behalten (Vorlage it.gohtml).
func (d ITPageData) TypeURL(typ string) string { return itListURL(typ, d.NetFilter) }

// OfflineURL: „Nur offline“ umschalten, Typfilter behalten.
func (d ITPageData) OfflineURL() string {
	if d.NetFilter == "offline" {
		return itListURL(d.Filter, "")
	}
	return itListURL(d.Filter, "offline")
}
