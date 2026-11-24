package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// Funktionen-Schalter je Terminal-Benutzer (Vertrag: Funktionen-Abgleich zwischen PDH-Terminal und
// PDH-Server). Sieben Schalter, je Schalter Wert und Zeitpunkt der letzten Änderung; je Schalter gewinnt
// die strikt jüngere Änderung, bei Gleichstand PDH. Ausgeschaltet heißt: Die Terminalseite zeigt die
// Funktion nicht und ihre Routen antworten mit 404.
//
//	GET /api/terminal/v1/device     liefert functions und theme (terminal_pair_api.go)
//	PUT /api/terminal/v1/functions  nimmt Änderungen des Terminals an (dieses File)
//	Ereignis settings               trägt functions und theme (terminal_events.go)

// terminalFnDef beschreibt einen Schalter und seine Spalten in terminal_settings.
type terminalFnDef struct {
	Key   string
	Label string
	Col   string // Wert (Spalte)
	AtCol string // Zeitpunkt der letzten Änderung (NULL = nie)
	Def   bool   // Grundwert, solange nie geändert
}

// terminalFnDefs: feste Reihenfolge, gleich der Anzeige am Terminal.
var terminalFnDefs = []terminalFnDef{
	{"stoerung", "Störung", "fn_stoerung", "fn_stoerung_at", true},
	{"ticket", "Ticket", "fn_ticket", "fn_ticket_at", true},
	{"ruesten", "Rüsten", "fn_ruesten", "fn_ruesten_at", true},
	{"stillstand", "Stillstand", "fn_stillstand", "fn_stillstand_at", true},
	{"hmi", "HMI", "fn_hmi", "fn_hmi_at", true},
	{"wartungen", "Wartungen", "fn_wartungen", "fn_wartungen_at", true},
	{"vorgaenge", "Vorgänge", "fn_vorgaenge", "fn_vorgaenge_at", true},
	{"schichtplan", "Schichtplan", "fn_schichtplan", "fn_schichtplan_at", true},
	{"zeiterfassung", "Zeiterfassung", "fn_zeiterfassung", "fn_zeiterfassung_at", false}, // nur PDH (0.82.53); das Terminal-Gerät kennt den Schlüssel nicht
	{"saver_slides", "Folienschau als Bildschirmschoner", "saver_slides", "saver_slides_at", false},
}

const (
	terminalFnClamp    = 5 * time.Minute // Uhrenabweichung: weiter in der Zukunft = Serverzeit
	terminalFnTimeFmt  = "2006-01-02T15:04:05.000Z"
	terminalFnPrecison = time.Millisecond
)

func terminalFnDefByKey(key string) (terminalFnDef, bool) {
	for _, d := range terminalFnDefs {
		if d.Key == key {
			return d, true
		}
	}
	return terminalFnDef{}, false
}

// terminalFn: Stand eines Schalters; At null = nie geändert (Epoche).
type terminalFn struct {
	Enabled bool
	At      time.Time
}

type terminalFnSet map[string]terminalFn

func defaultTerminalFns() terminalFnSet {
	s := terminalFnSet{}
	for _, d := range terminalFnDefs {
		s[d.Key] = terminalFn{Enabled: d.Def}
	}
	return s
}

// fnWire: Zeitpunkt auf der Leitung (UTC, Millisekunden, Z); „nie“ = Epoche.
func fnWire(t time.Time) string {
	if t.IsZero() {
		t = time.Unix(0, 0)
	}
	return t.UTC().Format(terminalFnTimeFmt)
}

// terminalFnJSON: Feld functions der Antworten (alle sieben Schlüssel).
func terminalFnJSON(s terminalFnSet) map[string]any {
	out := make(map[string]any, len(terminalFnDefs))
	for _, d := range terminalFnDefs {
		f, ok := s[d.Key]
		if !ok {
			f = terminalFn{Enabled: d.Def}
		}
		out[d.Key] = map[string]any{"enabled": f.Enabled, "updated_at": fnWire(f.At)}
	}
	return out
}

// Farben der Terminalseiten = PDH-Farben aus base.gohtml (dunkel: :root, hell: html[data-theme="light"]).
var terminalThemes = map[string]map[string]string{
	"dark":  {"bg": "#202020", "bg2": "#2b2b2b", "bg3": "#323232", "text": "#f3f3f3", "muted": "#a3a3a3", "border": "#3b3b3b"},
	"light": {"bg": "#f3f3f3", "bg2": "#ffffff", "bg3": "#f9f9f9", "text": "#1b1b1b", "muted": "#5f5f5f", "border": "#e2e2e2"},
}

func normScheme(s string) string {
	if s == "light" {
		return "light"
	}
	return "dark"
}

// terminalThemeJSON: Feld theme der Antworten.
func terminalThemeJSON(scheme string) map[string]string {
	scheme = normScheme(scheme)
	out := map[string]string{"scheme": scheme}
	for k, v := range terminalThemes[scheme] {
		out[k] = v
	}
	return out
}

var terminalFnSelect = func() string {
	cols := make([]string, 0, 2*len(terminalFnDefs)+1)
	for _, d := range terminalFnDefs {
		cols = append(cols, d.Col, d.AtCol)
	}
	cols = append(cols, "theme_scheme")
	return "SELECT " + strings.Join(cols, ", ") + " FROM terminal_settings WHERE user_id = $1::uuid"
}()

type terminalRowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func loadTerminalFns(ctx context.Context, q terminalRowQuerier, uid string, lock bool) (terminalFnSet, string, bool, error) {
	set, scheme := defaultTerminalFns(), "dark"
	vals := make([]bool, len(terminalFnDefs))
	ats := make([]*time.Time, len(terminalFnDefs))
	args := make([]any, 0, 2*len(terminalFnDefs)+1)
	for i := range terminalFnDefs {
		args = append(args, &vals[i], &ats[i])
	}
	args = append(args, &scheme)
	sql := terminalFnSelect
	if lock {
		sql += " FOR UPDATE"
	}
	if err := q.QueryRow(ctx, sql, uid).Scan(args...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return set, "dark", false, nil
		}
		return set, "dark", false, err
	}
	for i, d := range terminalFnDefs {
		f := terminalFn{Enabled: vals[i]}
		if ats[i] != nil {
			f.At = ats[i].UTC()
		}
		set[d.Key] = f
	}
	return set, normScheme(scheme), true, nil
}

// terminalFunctions liefert die Schalter und das Farbschema eines Terminal-Benutzers (ohne Zeile: Grundwerte).
func (h *Handler) terminalFunctions(ctx context.Context, uid string) (terminalFnSet, string, error) {
	if h.db == nil || uid == "" {
		return defaultTerminalFns(), "dark", nil
	}
	set, scheme, _, err := loadTerminalFns(ctx, h.db, uid, false)
	return set, scheme, err
}

// terminalFnDecide entscheidet je Schalter, ob und womit er überschrieben wird.
type terminalFnDecide func(d terminalFnDef, cur terminalFn) (next terminalFn, write bool)

// fnChange: ein in der Transaktion geänderter Wert (alt und neu stammen aus der gesperrten Zeile).
type fnChange struct {
	Key      string
	Old, New bool
}

func fnKeys(cs []fnChange) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Key)
	}
	return out
}

// updateTerminalFns wendet decide je Schalter an (Zeile gesperrt), schreibt die Änderungen und liefert die
// Schlüssel, deren Wert (enabled) sich geändert hat. Nicht-System-Benutzer ohne Zeile: nichts.
func (h *Handler) updateTerminalFns(ctx context.Context, uid string, decide terminalFnDecide) ([]fnChange, error) {
	if h.db == nil {
		return nil, errors.New("keine Datenbank")
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO terminal_settings (user_id) SELECT id FROM users WHERE id = $1::uuid AND is_system_user
		ON CONFLICT (user_id) DO NOTHING`, uid); err != nil {
		return nil, err
	}
	set, _, found, err := loadTerminalFns(ctx, tx, uid, true)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	var sets []string
	var args []any
	var changed []fnChange
	args = append(args, uid)
	for _, d := range terminalFnDefs {
		cur := set[d.Key]
		next, write := decide(d, cur)
		if !write {
			continue
		}
		args = append(args, next.Enabled, next.At.UTC())
		n := len(args)
		sets = append(sets, d.Col+" = $"+strconv.Itoa(n-1), d.AtCol+" = $"+strconv.Itoa(n))
		if next.Enabled != cur.Enabled {
			changed = append(changed, fnChange{Key: d.Key, Old: cur.Enabled, New: next.Enabled})
		}
	}
	if len(sets) == 0 {
		return nil, tx.Commit(ctx) // die ggf. angelegte Zeile bleibt (Farbschema, spätere Abgleiche)
	}
	if _, err = tx.Exec(ctx, `UPDATE terminal_settings SET `+strings.Join(sets, ", ")+`, updated_at = NOW() WHERE user_id = $1::uuid`, args...); err != nil {
		return nil, err
	}
	return changed, tx.Commit(ctx)
}

// setTerminalFunctions: Änderung in PDH (Benutzerdetail): nur geänderte Werte, Zeitpunkt = jetzt.
func (h *Handler) setTerminalFunctions(ctx context.Context, uid string, vals map[string]bool) ([]string, error) {
	cs, err := h.setTerminalFunctionsDetail(ctx, uid, vals)
	return fnKeys(cs), err
}

// setTerminalFunctionsDetail wie setTerminalFunctions, liefert aber alt und neu je geändertem Schalter.
func (h *Handler) setTerminalFunctionsDetail(ctx context.Context, uid string, vals map[string]bool) ([]fnChange, error) {
	now := time.Now().UTC().Truncate(terminalFnPrecison)
	return h.updateTerminalFns(ctx, uid, func(d terminalFnDef, cur terminalFn) (terminalFn, bool) {
		v, ok := vals[d.Key]
		if !ok || v == cur.Enabled {
			return cur, false
		}
		at := now
		if !at.After(fnBase(cur.At)) {
			at = fnBase(cur.At).Add(terminalFnPrecison) // der Zeitpunkt steigt immer
		}
		return terminalFn{Enabled: v, At: at}, true
	})
}

type terminalFnIn struct {
	Enabled bool
	At      time.Time
}

// mergeTerminalFunctions: Regel des Vertrags je Schlüssel. Zukunft über 5 Minuten wird auf die Serverzeit
// geklemmt; angenommen wird nur ein strikt jüngerer Zeitpunkt (Gleichstand gewinnt PDH), vergangene
// Zeitpunkte gelten wie gesendet. Angenommen wird Wert und Zeitpunkt so wie (geklemmt) gesendet.
func (h *Handler) mergeTerminalFunctions(ctx context.Context, uid string, in map[string]terminalFnIn, now time.Time) ([]string, error) {
	now = now.UTC().Truncate(terminalFnPrecison)
	cs, err := h.updateTerminalFns(ctx, uid, func(d terminalFnDef, cur terminalFn) (terminalFn, bool) {
		v, ok := in[d.Key]
		if !ok {
			return cur, false
		}
		at := v.At.UTC().Truncate(terminalFnPrecison)
		if at.After(now.Add(terminalFnClamp)) {
			at = now
		}
		if !at.After(fnBase(cur.At)) {
			return cur, false
		}
		return terminalFn{Enabled: v.Enabled, At: at}, true
	})
	return fnKeys(cs), err
}

// terminalFnPayload: functions und theme für Statusabfrage, PUT-Antwort und Ereignis.
func (h *Handler) terminalFnPayload(ctx context.Context, uid string) (map[string]any, map[string]string, error) {
	set, scheme, err := h.terminalFunctions(ctx, uid)
	if err != nil {
		return nil, nil, err
	}
	return terminalFnJSON(set), terminalThemeJSON(scheme), nil
}

// terminalBearerDevice: ein per Bearer-Geheimnis angemeldetes Gerät.
type terminalBearerDevice struct {
	ID, UserID, Name, First, Last, Username string
}

// terminalClientVersion: Version des Terminals aus X-Terminal-Version oder User-Agent „pdh-terminal/<Version>“
// (freiwillig; leer = unbekannt).
func terminalClientVersion(r *http.Request) string {
	v := strings.TrimSpace(r.Header.Get("X-Terminal-Version"))
	if v == "" {
		if ua := r.Header.Get("User-Agent"); strings.HasPrefix(strings.ToLower(ua), "pdh-terminal/") {
			v = strings.TrimSpace(strings.SplitN(ua[len("pdh-terminal/"):], " ", 2)[0])
		}
	}
	if len(v) > 40 {
		return ""
	}
	for _, c := range v {
		if !unicode.IsPrint(c) || unicode.IsSpace(c) {
			return ""
		}
	}
	return v
}

// terminalAdminURL: Adresse der Verwaltungsseite aus X-Terminal-Admin-URL (ab Terminal 0.4.5). Gültig ist
// nur https://<IPv4 | [IPv6] | DNS-Name>:<Port 1–65535>/admin/ ohne Zugangsdaten, Abfrage und Fragment,
// höchstens 255 Zeichen, nur ASCII. Alles andere liefert "" (der gespeicherte Wert bleibt dann stehen).
func terminalAdminURL(r *http.Request) string {
	v := r.Header.Get("X-Terminal-Admin-URL")
	if v == "" || len(v) > 255 {
		return ""
	}
	for _, c := range v {
		if c <= ' ' || c >= 0x7f || strings.ContainsRune(`\%?#@"'<>`+"`", c) {
			return ""
		}
	}
	rest, ok := strings.CutPrefix(v, "https://")
	if !ok || !strings.HasSuffix(rest, "/admin/") {
		return ""
	}
	hostport := strings.TrimSuffix(rest, "/admin/")
	i := strings.LastIndex(hostport, ":")
	if i < 1 {
		return ""
	}
	host, port := hostport[:i], hostport[i+1:]
	if len(port) == 0 || len(port) > 5 {
		return ""
	}
	for _, c := range port {
		if c < '0' || c > '9' {
			return ""
		}
	}
	if n, _ := strconv.Atoi(port); n < 1 || n > 65535 {
		return ""
	}
	if strings.HasPrefix(host, "[") {
		if !strings.HasSuffix(host, "]") {
			return ""
		}
		ip, err := netip.ParseAddr(host[1 : len(host)-1])
		if err != nil || !ip.Is6() || ip.Zone() != "" {
			return ""
		}
		return v
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !ip.Is4() {
			return ""
		}
		return v
	}
	if len(host) > 253 || strings.ContainsAny(host, ":[]/") {
		return ""
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return ""
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return ""
			}
		}
	}
	return v
}

// terminalDefaultLocalPort: Port der lokalen Startseite des Terminals, wenn es nichts anderes meldet.
const terminalDefaultLocalPort = 8080

// terminalLocalPort: lokaler Port aus X-Terminal-Local-Port (ab Terminal 0.4.6, nur gesendet wenn er von 8080
// abweicht). Gültig sind nur Ziffern (ohne Leerzeichen) im Bereich 1024–65534 (die lokale Admin-Seite liegt auf
// Port + 1); alles andere liefert 0 = nichts ändern.
func terminalLocalPort(r *http.Request) int {
	v := r.Header.Get("X-Terminal-Local-Port")
	if len(v) < 4 || len(v) > 5 {
		return 0
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0
		}
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1024 || n > 65534 {
		return 0
	}
	return n
}

// terminalLocalPortUpdate: was die Statusabfrage mit der Spalte local_port tut. set=true: Wert schreiben
// (port nil = NULL = Grundwert 8080, wenn die Kopfzeile fehlt oder 8080 meldet). set=false: stehen lassen
// (Kopfzeile vorhanden, aber ungültig).
func terminalLocalPortUpdate(r *http.Request) (port any, set bool) {
	if r.Header.Get("X-Terminal-Local-Port") == "" {
		return nil, true
	}
	switch n := terminalLocalPort(r); n {
	case 0:
		return nil, false
	case terminalDefaultLocalPort:
		return nil, true
	default:
		return n, true
	}
}

// terminalLocalPortOrDefault: gespeicherter Port (NULL oder ungültig = 8080).
func terminalLocalPortOrDefault(p *int) int {
	if p == nil || *p < 1024 || *p > 65534 {
		return terminalDefaultLocalPort
	}
	return *p
}

// localPortAttr: Wert für data-local-port; leer beim Grundwert 8080 (terminal.js nimmt dann 8080).
func localPortAttr(port int) string {
	if port < 1024 || port > 65534 || port == terminalDefaultLocalPort {
		return ""
	}
	return strconv.Itoa(port)
}

// terminalLocalOrigin: Ursprung des lokalen Kartendienstes am Terminal (Loopback, Port des Geräts).
func terminalLocalOrigin(port int) string {
	return "http://127.0.0.1:" + strconv.Itoa(port)
}

// terminalLocalAdminURLFor: lokale Admin-Seite des Terminals; sie liegt auf dem Port der Startseite + 1.
func terminalLocalAdminURLFor(port int) string {
	return terminalLocalOrigin(port+1) + "/admin/"
}

// terminalDeviceLocalPort: lokaler Port des Geräts dieser Sitzung (kein Gerät, keine Datenbank, NULL: 8080).
func (h *Handler) terminalDeviceLocalPort(r *http.Request) int {
	_, _, dev := h.sessionToken(r)
	if dev == "" || h.db == nil {
		return terminalDefaultLocalPort
	}
	var p *int
	if err := h.db.QueryRow(r.Context(), `SELECT local_port FROM terminal_devices WHERE id = $1::uuid`, dev).Scan(&p); err != nil {
		return terminalDefaultLocalPort
	}
	return terminalLocalPortOrDefault(p)
}

func hasBearer(r *http.Request) bool {
	a := r.Header.Get("Authorization")
	return len(a) > 7 && strings.EqualFold(a[:7], "bearer ")
}

// terminalBearer prüft Header, Fehlversuchsgrenze und Gerät wie GET /api/terminal/v1/device
// (401 msgDeviceInvalid, 429 msgPairBlocked, nur ein falsches Geheimnis zählt als Fehlversuch).
// ok=false: die Antwort ist schon geschrieben.
func (h *Handler) terminalBearer(w http.ResponseWriter, r *http.Request) (terminalBearerDevice, bool) {
	var dev terminalBearerDevice
	key := limiterKey(clientIP(r))
	if terminalFailLimit.blocked(key, time.Now()) {
		terminalJSONErr(w, http.StatusTooManyRequests, msgPairBlocked)
		return dev, false
	}
	secret := ""
	if hasBearer(r) {
		secret = strings.TrimSpace(r.Header.Get("Authorization")[7:])
	}
	deny := func(count bool) (terminalBearerDevice, bool) {
		if count {
			terminalFailLimit.fail(key, time.Now())
			authLog(r, false, "terminal-kopplung", "", "", "", "Geheimnis unbekannt oder Gerät gesperrt")
		}
		terminalJSONErr(w, http.StatusUnauthorized, msgDeviceInvalid)
		return dev, false
	}
	if secret == "" || h.db == nil {
		return deny(false)
	}
	err := h.db.QueryRow(r.Context(), `SELECT d.id::text, d.user_id::text, d.name, u.first_name, u.last_name, u.username
		FROM terminal_devices d JOIN users u ON u.id = d.user_id
		WHERE d.secret_sha256 = $1 AND `+deviceActiveSQL,
		hashDeviceSecret(secret)).Scan(&dev.ID, &dev.UserID, &dev.Name, &dev.First, &dev.Last, &dev.Username)
	if errors.Is(err, pgx.ErrNoRows) {
		return deny(true)
	}
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("gerätezustand fehlgeschlagen")
		terminalJSONErr(w, http.StatusInternalServerError, "Gerätezustand derzeit nicht abrufbar")
		return dev, false
	}
	return dev, true
}

// TerminalFunctionsAPI: PUT /api/terminal/v1/functions mit Bearer-Geheimnis.
func (h *Handler) TerminalFunctionsAPI(w http.ResponseWriter, r *http.Request) {
	dev, ok := h.terminalBearer(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, terminalAPIMaxBody+1))
	if err != nil {
		terminalJSONErr(w, http.StatusBadRequest, "Anfrage nicht lesbar")
		return
	}
	if len(body) > terminalAPIMaxBody {
		terminalJSONErr(w, http.StatusRequestEntityTooLarge, "Anfrage zu groß")
		return
	}
	in, err := parseTerminalFnBody(body)
	if err != nil {
		terminalJSONErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	changed, err := h.mergeTerminalFunctions(ctx, dev.UserID, in, time.Now())
	if err != nil {
		componentLog("terminal").Error().Err(err).Str("device", dev.ID).Msg("funktionen übernehmen")
		terminalJSONErr(w, http.StatusInternalServerError, "Funktionen derzeit nicht speicherbar")
		return
	}
	if len(changed) > 0 {
		h.notifyTerminalSettings(dev.UserID)
	}
	fn, _, err := h.terminalFnPayload(ctx, dev.UserID)
	if err != nil {
		componentLog("terminal").Error().Err(err).Str("device", dev.ID).Msg("funktionen lesen")
		terminalJSONErr(w, http.StatusInternalServerError, "Funktionen derzeit nicht abrufbar")
		return
	}
	terminalJSON(w, http.StatusOK, map[string]any{"functions": fn})
}

// parseTerminalFnBody prüft die Anfrage; formale Fehler lehnen alles ab. Unbekannte Schlüssel werden übergangen.
func parseTerminalFnBody(body []byte) (map[string]terminalFnIn, error) {
	errBody := errors.New("Ungültige Anfrage: {\"functions\":{…}} erwartet")
	var top map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&top); err != nil || top == nil || dec.More() {
		return nil, errBody
	}
	raw, ok := top["functions"]
	if !ok {
		return nil, errBody
	}
	var fns map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fns); err != nil || fns == nil {
		return nil, errBody
	}
	out := map[string]terminalFnIn{}
	for _, d := range terminalFnDefs {
		rv, ok := fns[d.Key]
		if !ok {
			continue
		}
		var v struct {
			Enabled   *bool   `json:"enabled"`
			UpdatedAt *string `json:"updated_at"`
		}
		if err := json.Unmarshal(rv, &v); err != nil || v.Enabled == nil || v.UpdatedAt == nil {
			return nil, errors.New("Ungültiger Schalter „" + d.Key + "“: enabled (Boolean) und updated_at (RFC 3339) erwartet")
		}
		at, err := time.Parse(time.RFC3339Nano, *v.UpdatedAt)
		if err != nil {
			return nil, errors.New("Ungültiger Schalter „" + d.Key + "“: updated_at ist keine RFC-3339-Zeit")
		}
		out[d.Key] = terminalFnIn{Enabled: *v.Enabled, At: at}
	}
	return out, nil
}

// ---- Durchsetzung auf den Terminalseiten ----

// terminalFnFlags: welche Funktionen die Terminalseiten zeigen.
type terminalFnFlags struct {
	Stoerung, Ticket, Ruesten, Stillstand, HMI, Wartungen, Vorgaenge, Schichtplan, Zeiterfassung bool
}

func (s terminalFnSet) flags() terminalFnFlags {
	return terminalFnFlags{
		Stoerung: s["stoerung"].Enabled, Ticket: s["ticket"].Enabled, Ruesten: s["ruesten"].Enabled,
		Stillstand: s["stillstand"].Enabled, HMI: s["hmi"].Enabled, Wartungen: s["wartungen"].Enabled, Vorgaenge: s["vorgaenge"].Enabled, Schichtplan: s["schichtplan"].Enabled, Zeiterfassung: s["zeiterfassung"].Enabled,
	}
}

const msgTerminalFnOff = "Diese Funktion ist an diesem Terminal ausgeschaltet."

// terminalFnRoute sperrt eine Route (GET und POST), wenn die Funktion für den Terminal-Benutzer aus ist: 404.
func (h *Handler) terminalFnRoute(key string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.db != nil {
			if uid := h.terminalUserID(r); uid != "" {
				set, _, err := h.terminalFunctions(r.Context(), uid)
				if err != nil {
					componentLog("terminal").Error().Err(err).Msg("funktionen prüfen")
					http.Error(w, "Daten nicht verfügbar", http.StatusInternalServerError)
					return
				}
				if !set[key].Enabled {
					http.Error(w, msgTerminalFnOff, http.StatusNotFound)
					return
				}
			}
		}
		next(w, r)
	}
}

// fnBase: Vergleichszeit eines Schalters; nie geändert = Epoche.
func fnBase(t time.Time) time.Time {
	if t.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return t
}

// fnUnchangedByForm: hat die Person den Schalter gegenüber dem angezeigten Stand (verstecktes Feld
// orig_<Schlüssel>, „1“ oder „0“) nicht geändert? Ohne das Feld (ältere Formulare) gilt der Wert als geändert.
func fnUnchangedByForm(r *http.Request, key string, on bool) bool {
	o := r.FormValue("orig_" + key)
	return (o == "1" || o == "0") && (o == "1") == on
}

// saveTerminalFunctionsForm übernimmt die Schalter (ohne Folienschau, die hat ihren eigenen Marker) und das
// Farbschema aus dem Benutzerdetail. Nur Schalter, die die Person gegenüber dem angezeigten Stand geändert hat,
// werden geschrieben (sonst überschriebe ein Speichern die Änderung, die das Terminal inzwischen geschickt hat);
// sie bekommen den Zeitpunkt „jetzt“, Verlauf (alt/neu aus der Transaktion) und Ereignis settings folgen.
// Der Aufrufer prüft Recht und Systembenutzer.
func (h *Handler) saveTerminalFunctionsForm(ctx context.Context, r *http.Request, uid string) error {
	vals := map[string]bool{}
	for _, d := range terminalFnDefs {
		if d.Key == "saver_slides" {
			continue
		}
		on := r.FormValue("terminal_fn_"+d.Key) == "on"
		if !fnUnchangedByForm(r, d.Key, on) {
			vals[d.Key] = on
		}
	}
	changed, err := h.setTerminalFunctionsDetail(ctx, uid, vals)
	if err != nil {
		return err
	}
	_, oldScheme, err := h.terminalFunctions(ctx, uid)
	if err != nil {
		return err
	}
	scheme := oldScheme
	if s := r.FormValue("terminal_scheme"); s == "light" || s == "dark" {
		scheme = s
	}
	schemeChanged := scheme != oldScheme
	if schemeChanged {
		if _, err := h.db.Exec(ctx, `UPDATE terminal_settings SET theme_scheme = $2, updated_at = NOW() WHERE user_id = $1::uuid`, uid, scheme); err != nil {
			return err
		}
	}
	yn := map[bool]string{true: "an", false: "aus"}
	by := getUser(r).ID
	for _, c := range changed {
		d, _ := terminalFnDefByKey(c.Key)
		h.addHistory(ctx, "user", uid, "update", "Terminal-Funktion "+d.Label, yn[c.Old], yn[c.New], "Terminal-Funktion "+d.Label+": "+yn[c.New], by)
	}
	if schemeChanged {
		lbl := map[string]string{"dark": "dunkel", "light": "hell"}
		h.addHistory(ctx, "user", uid, "update", "Farbschema des Terminals", lbl[oldScheme], lbl[scheme], "Farbschema des Terminals: "+lbl[scheme], by)
	}
	if len(changed) > 0 || schemeChanged {
		h.notifyTerminalSettings(uid)
	}
	return nil
}

// terminalFnFlagsFor: Schalter und Farbschema für die Seiten; bei einem Lesefehler gelten die Grundwerte
// (Anzeige bleibt benutzbar, die Routen prüfen selbst und sperren bei Fehlern).
func (h *Handler) terminalFnFlagsFor(ctx context.Context, uid string) (terminalFnFlags, string) {
	set, scheme, err := h.terminalFunctions(ctx, uid)
	if err != nil {
		componentLog("terminal").Error().Err(err).Msg("funktionen der seite")
		set, scheme = defaultTerminalFns(), "dark"
	}
	return set.flags(), scheme
}

// ---- Liste der Kiosk-Terminals: gekoppelte PDH-Terminals ----

// terminalOnlineWindow: so lange nach der letzten Statusabfrage (alle 30 s) gilt ein PDH-Terminal als online.
const terminalOnlineWindow = 2 * time.Minute

// pdhTerminalView: eine Zeile „PDH-Terminal“ in Core-Einstellungen → Kiosk-Terminals.
type pdhTerminalView struct {
	DeviceID, Name, UserID, UserName, Type, Version string
	AdminURL                                        string // Verwaltungsseite des Terminals, leer = nicht gemeldet
	LocalPort                                       int    // lokaler Port der Startseite, nur gesetzt wenn er von 8080 abweicht
	Online, Revoked                                 bool
	LastSeen, Paired, RevokedAt, Link               string
}

// loadPDHTerminals listet die gekoppelten Geräte der Terminal-Benutzer (aktive zuerst, dann nach Name).
func (h *Handler) loadPDHTerminals(ctx context.Context) ([]pdhTerminalView, error) {
	rows, err := h.db.Query(ctx, `SELECT d.id::text, d.name, u.id::text, u.first_name, u.last_name, u.username,
			d.client_version, d.admin_url, d.local_port, d.created_at, d.last_seen_at, d.revoked_at
		FROM terminal_devices d JOIN users u ON u.id = d.user_id
		ORDER BY (d.revoked_at IS NOT NULL), lower(d.name), d.created_at LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pdhTerminalView
	now := time.Now()
	for rows.Next() {
		var v pdhTerminalView
		var first, last, uname string
		var created time.Time
		var seen, revoked *time.Time
		var lport *int
		if err := rows.Scan(&v.DeviceID, &v.Name, &v.UserID, &first, &last, &uname, &v.Version, &v.AdminURL, &lport, &created, &seen, &revoked); err != nil {
			return nil, err
		}
		if p := terminalLocalPortOrDefault(lport); p != terminalDefaultLocalPort {
			v.LocalPort = p
		}
		v.UserName = displayUserName(first, last, uname)
		v.Type = "PDH-Terminal"
		if v.Version == "" {
			v.Version = "unbekannt"
		}
		v.Paired = created.Local().Format(kioskTimeDisplay)
		v.LastSeen = "nie"
		if seen != nil {
			v.LastSeen = seen.Local().Format(kioskTimeDisplay)
			v.Online = revoked == nil && now.Sub(*seen) < terminalOnlineWindow
		}
		if revoked != nil {
			v.Revoked, v.RevokedAt = true, revoked.Local().Format(kioskTimeDisplay)
		}
		v.Link = "/users/" + v.UserID + "?tab=master"
		out = append(out, v)
	}
	return out, rows.Err()
}
