package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"pdh/internal/integrations/omada"
	"pdh/internal/integrations/unifi"
)

// Block „Vom Controller gefunden“ auf der Seite „Netzwerk“: holt Netze und WLANs aus
// UniFi und Omada (nur lesen), hält sie 5 Minuten im Speicher und übernimmt, gleicht an
// oder blendet aus – jeweils nur auf Klick. Jeder Klick löst den Vorschlag serverseitig
// neu auf (Quelle + Art + Kennung, Fingerabdruck) und übernimmt nur Werte des Controllers;
// gesendete Formularwerte werden nie gelesen. Kennwörter gehen nie in Vorlagendaten,
// Protokoll oder Fehlertexte; gespeichert werden sie nur verschlüsselt.
//
//	GET  /admin/network/suggestions            Block (?fresh=1 fragt neu ab)
//	POST /admin/network/suggestions/adopt      eine Zeile übernehmen/angleichen
//	POST /admin/network/suggestions/adopt-all  alle Zeilen einer Quelle
//	POST /admin/network/suggestions/hide       eine Zeile ausblenden

const (
	netSuggTTL    = 5 * time.Minute
	netErrTimeout = "nicht erreichbar: Zeitüberschreitung"
	netSrcUnifi   = "unifi"
	netSrcOmada   = "omada"
)

// netCtlTimeout: Frist je Quelle (Variable, damit Tests sie kürzen).
var netCtlTimeout = 10 * time.Second

// netCtlResult: der Abruf einer Quelle. Bei Err sind Nets und WLANs leer.
type netCtlResult struct {
	Source, Label, Err string
	Nets               []netCtlNet
	WLANs              []netCtlWLAN
	At                 time.Time
}

type netUnifiReader interface {
	Networks(context.Context) ([]unifi.Network, error)
	WifiBroadcasts(context.Context) ([]unifi.WifiBroadcast, error)
}

type netOmadaReader interface {
	LanNetworks(context.Context) ([]omada.LanNetwork, error)
	SSIDs(context.Context) ([]omada.SSID, error)
}

// netCall ruft fn so auf, dass ein Leser, der die Frist ignoriert, den Abruf nicht aufhält.
func netCall[T any](ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	type res struct {
		v   T
		err error
	}
	ch := make(chan res, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				componentLog("netzwerk").Error().Str("typ", fmt.Sprintf("%T", p)).Msg("abruf beim controller abgestürzt")
				var zero T
				ch <- res{zero, errNetCtlPanic}
			}
		}()
		v, err := fn(ctx)
		ch <- res{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

func netCtlErrText(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return netErrTimeout
	}
	return err.Error()
}

// netCtlFetchUnifi liest Netze und WLANs aus UniFi; jeder Fehler ergibt einen Hinweis statt Daten.
func netCtlFetchUnifi(ctx context.Context, api netUnifiReader) netCtlResult {
	res := netCtlResult{Source: netSrcUnifi, Label: "UniFi", At: time.Now()}
	nets, err := netCall(ctx, api.Networks)
	if err != nil {
		res.Err = netCtlErrText(err)
		return res
	}
	wl, err := netCall(ctx, api.WifiBroadcasts)
	if err != nil {
		res.Err = netCtlErrText(err)
		return res
	}
	res.Nets, res.WLANs = netCtlFromUnifi(nets, wl)
	return res
}

// netCtlFetchOmada wie netCtlFetchUnifi für Omada.
func netCtlFetchOmada(ctx context.Context, api netOmadaReader) netCtlResult {
	res := netCtlResult{Source: netSrcOmada, Label: "Omada", At: time.Now()}
	nets, err := netCall(ctx, api.LanNetworks)
	if err != nil {
		res.Err = netCtlErrText(err)
		return res
	}
	ss, err := netCall(ctx, api.SSIDs)
	if err != nil {
		res.Err = netCtlErrText(err)
		return res
	}
	res.Nets, res.WLANs = netCtlFromOmada(nets, ss)
	return res
}

// netCtlConfigured: die eingerichteten Quellen („unifi“, „omada“).
func (h *Handler) netCtlConfigured(ctx context.Context) []string {
	var out []string
	if h.netUnifiOpen != nil || (h.certSetting(ctx, keyUnifiURL) != "" && h.certSetting(ctx, keyUnifiKey) != "" && h.certSetting(ctx, keyUnifiPin) != "") {
		out = append(out, netSrcUnifi)
	}
	if h.netOmadaOpen != nil || h.omadaConnected(ctx) {
		out = append(out, netSrcOmada)
	}
	return out
}

// netCtlConfigKey: Prüfsumme der Einstellungen von UniFi und Omada. Ändert sie sich, ist der
// Speicher der Vorschläge ungültig (anderer Controller, anderer Schlüssel).
func (h *Handler) netCtlConfigKey(ctx context.Context) string {
	sum := sha256.New()
	for _, k := range []string{keyUnifiURL, keyUnifiKey, keyUnifiSite, keyUnifiPin,
		keyOmadaURL, keyOmadaID, keyOmadaClient, keyOmadaSecret, keyOmadaSite, keyOmadaPin} {
		sum.Write([]byte(h.certSetting(ctx, k)))
		sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil))
}

var errNetCtlPanic = errors.New("interner Fehler beim Abruf")

// netCtlFetchAll fragt alle eingerichteten Quellen parallel ab, je mit eigener Frist.
func (h *Handler) netCtlFetchAll(ctx context.Context) []netCtlResult {
	srcs := h.netCtlConfigured(ctx)
	out := make([]netCtlResult, len(srcs))
	var wg sync.WaitGroup
	for i, s := range srcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			label := "UniFi"
			if s == netSrcOmada {
				label = "Omada"
			}
			// Ein Absturz beim Abruf darf nicht den ganzen Server mitnehmen.
			defer func() {
				if p := recover(); p != nil {
					componentLog("netzwerk").Error().Str("quelle", s).Str("typ", fmt.Sprintf("%T", p)).Msg("abruf beim controller abgestürzt")
					out[i] = netCtlResult{Source: s, Label: label, Err: errNetCtlPanic.Error(), At: time.Now()}
				}
			}()
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), netCtlTimeout)
			defer cancel()
			switch s {
			case netSrcUnifi:
				var c netUnifiReader
				var err error
				if h.netUnifiOpen != nil {
					c, err = h.netUnifiOpen(cctx)
				} else {
					c, err = h.unifiAPI(cctx)
				}
				if err != nil {
					out[i] = netCtlResult{Source: s, Label: label, Err: err.Error(), At: time.Now()}
					return
				}
				out[i] = netCtlFetchUnifi(cctx, c)
			default:
				var c netOmadaReader
				var err error
				if h.netOmadaOpen != nil {
					c, err = h.netOmadaOpen(cctx)
				} else {
					c, err = h.omadaAPI(cctx)
				}
				if err != nil {
					out[i] = netCtlResult{Source: s, Label: label, Err: err.Error(), At: time.Now()}
					return
				}
				out[i] = netCtlFetchOmada(cctx, c)
			}
		}()
	}
	wg.Wait()
	return out
}

// netCtlResults liefert den Speicher (höchstens netSuggTTL alt); fresh oder abgelaufen → neu abfragen.
func (h *Handler) netCtlResults(ctx context.Context, fresh bool) []netCtlResult {
	h.netSugg.mu.Lock()
	defer h.netSugg.mu.Unlock()
	cfg := h.netCtlConfigKey(ctx)
	if !fresh && !h.netSugg.at.IsZero() && time.Since(h.netSugg.at) <= netSuggTTL && h.netSugg.cfg == cfg {
		return h.netSugg.res
	}
	var res []netCtlResult
	if h.netCtlFetchHook != nil {
		res = h.netCtlFetchHook(ctx)
	} else {
		res = h.netCtlFetchAll(ctx)
	}
	now := time.Now()
	for i := range res {
		if res[i].At.IsZero() {
			res[i].At = now
		}
	}
	h.netSugg.at, h.netSugg.res, h.netSugg.cfg = now, res, cfg
	return res
}

// netSuggForget verwirft den Speicher (nach einem veralteten Vorschlag soll die nächste Ansicht neu abfragen).
func (h *Handler) netSuggForget() {
	h.netSugg.mu.Lock()
	h.netSugg.at, h.netSugg.res = time.Time{}, nil
	h.netSugg.mu.Unlock()
}

// ── Vorlagendaten (ohne Kennwortfeld) ──

type NetSuggView struct {
	Sources    []NetSuggSource
	Mirror     bool
	ActiveLink string
	Err        string // z. B. Datenbankfehler: statt der Liste eine Zeile mit Hinweis
}

type NetSuggSource struct {
	Source, Label, Err, Ago string
	Rows                    []NetSuggRowView
	Same                    int
	Gone                    []string
	CanAll                  bool
	SetFP                   string // Fingerabdruck der gezeigten Zeilen (für „Alle übernehmen“)
}

type NetSuggRowView struct {
	State, Kind, ExtID, Fingerprint, Name, VLAN, Subnet, NetName, Hint string
	StateKey, PDHID                                                    string // Zustand und PDH-Eintrag zur Zeit der Anzeige (der Klick prüft sie)
	NetNew, ESPPDHUnusable                                             bool
	Diffs                                                              []string
}

func netAgo(at time.Time) string {
	m := int(time.Since(at) / time.Minute)
	if m < 1 {
		return "vor weniger als 1 Min."
	}
	return "vor " + strconv.Itoa(m) + " Min."
}

// SameText: „1 Eintrag stimmt schon überein.“ bzw. „N Einträge stimmen schon überein.“
func (s NetSuggSource) SameText() string {
	if s.Same == 1 {
		return "1 Eintrag stimmt schon überein."
	}
	return strconv.Itoa(s.Same) + " Einträge stimmen schon überein."
}

// netSuggSourceRows ordnet das Ergebnis einer Quelle zu: Zeilen und PDH-IDs ohne Gegenstück.
func netSuggSourceRows(res netCtlResult, segs []netPDHSeg, wl []netPDHWLAN, hidden []netHidden) ([]netSuggRow, []string) {
	ok := res.Err == ""
	return netSuggestFetched(res.Source, res.Nets, res.WLANs, segs, wl, hidden, netFetched{Nets: ok, WLANs: ok})
}

// netSetFP: ein Fingerabdruck über die gezeigten (nicht ausgeblendeten, nicht gleichen) Zeilen einer
// Quelle. „Alle übernehmen“ übernimmt nur genau diesen Satz.
func netSetFP(rows []netSuggRow) string {
	var l []string
	for _, r := range rows {
		if r.State != netStateSame {
			l = append(l, r.Kind+"|"+r.ExtID+"|"+r.Fingerprint+"|"+r.State+"|"+r.PDHID)
		}
	}
	sort.Strings(l)
	sum := sha256.Sum256([]byte(strings.Join(l, "\n")))
	return hex.EncodeToString(sum[:])
}

func netSuggBuildView(results []netCtlResult, segs []netPDHSeg, wl []netPDHWLAN, hidden []netHidden) NetSuggView {
	var v NetSuggView
	for _, res := range results {
		s := NetSuggSource{Source: res.Source, Label: res.Label, Err: res.Err, Ago: netAgo(res.At)}
		if res.Err == "" {
			rows, gone := netSuggSourceRows(res, segs, wl, hidden)
			s.Gone = gone
			s.SetFP = netSetFP(rows)
			for _, r := range rows {
				if r.State == netStateSame {
					s.Same++
					continue
				}
				rv := NetSuggRowView{State: "NEU", StateKey: r.State, PDHID: r.PDHID, Kind: r.Kind, ExtID: r.ExtID,
					Fingerprint: r.Fingerprint, Name: r.Name, Subnet: r.Subnet, NetName: r.NetName, Hint: r.Hint,
					NetNew: r.NetCreate != nil, ESPPDHUnusable: r.ESPPDHUnusable, Diffs: r.Diffs}
				if r.State == netStateDiff {
					rv.State = "ANDERS"
				}
				if r.VLAN > 0 {
					rv.VLAN = strconv.Itoa(r.VLAN)
				}
				s.Rows = append(s.Rows, rv)
			}
			s.CanAll = len(s.Rows) > 0
		}
		v.Sources = append(v.Sources, s)
	}
	return v
}

// NetworkSuggWeb: GET /admin/network/suggestions – der Block (lädt nach).
func (h *Handler) NetworkSuggWeb(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permNetwork) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	if h.Mirror().On {
		// Am Spiegel keine Abfrage beim Controller.
		v := NetSuggView{Mirror: true}
		if l := mirrorActiveLink(r, h.mirrorPeerState(ctx)); l != "" {
			v.ActiveLink = l + "admin/network"
		}
		h.renderFragment(w, "nw-sugg", v)
		return
	}
	results := h.netCtlResults(ctx, r.URL.Query().Get("fresh") == "1")
	segs, wl, err := h.netPDHState(ctx)
	var hidden []netHidden
	if err == nil {
		hidden, err = h.netHiddenList(ctx)
	}
	if err != nil {
		componentLog("netzwerk").Error().Err(err).Msg("vorschläge: stand lesen")
		h.renderFragment(w, "nw-sugg", NetSuggView{Err: "Die Netze und WLANs konnten nicht gelesen werden – bitte später erneut versuchen."})
		return
	}
	h.renderFragment(w, "nw-sugg", netSuggBuildView(results, segs, wl, hidden))
}

// ── Klicks ──

// netSuggRows löst die Zeilen einer Quelle neu auf: Ergebnis aus dem Speicher (oder frisch abgefragt),
// zugeordnet zum jetzigen PDH-Stand. errKey ≠ "" = Meldung für den Anwender (nichts übernehmen).
func (h *Handler) netSuggRows(ctx context.Context, source string) (rows []netSuggRow, errKey string) {
	if source != netSrcUnifi && source != netSrcOmada {
		return nil, "sugg-stale"
	}
	var res *netCtlResult
	results := h.netCtlResults(ctx, false)
	for i := range results {
		if results[i].Source == source {
			res = &results[i]
		}
	}
	if res == nil {
		return nil, "sugg-stale"
	}
	if res.Err != "" {
		return nil, "sugg-ctl"
	}
	segs, wl, err := h.netPDHState(ctx)
	var hidden []netHidden
	if err == nil {
		hidden, err = h.netHiddenList(ctx)
	}
	if err != nil {
		componentLog("netzwerk").Error().Err(err).Msg("vorschläge: stand lesen")
		return nil, "save"
	}
	rows, _ = netSuggSourceRows(*res, segs, wl, hidden)
	return rows, ""
}

// netSuggFindOne löst die Zeile eines Klicks neu auf. Art, Kennung, Fingerabdruck, Zustand und PDH-Eintrag
// müssen mit dem übereinstimmen, was jetzt gilt – sonst „veraltet“ (nichts übernehmen).
func (h *Handler) netSuggFindOne(ctx context.Context, f url.Values) (row *netSuggRow, errKey string) {
	kind, ext := f.Get("kind"), f.Get("ext")
	if (kind != netKindSegment && kind != netKindWLAN) || ext == "" {
		return nil, "sugg-stale"
	}
	rows, key := h.netSuggRows(ctx, f.Get("source"))
	if key != "" {
		return nil, key
	}
	for i := range rows {
		r := &rows[i]
		if r.Kind == kind && r.ExtID == ext {
			if r.State == netStateSame || r.Fingerprint != f.Get("fp") || r.State != f.Get("state") || r.PDHID != f.Get("pdh") {
				return nil, "sugg-stale"
			}
			return r, ""
		}
	}
	return nil, "sugg-stale"
}

func (h *Handler) netSuggRefuse(w http.ResponseWriter, r *http.Request, key string) {
	if key == "sugg-stale" {
		h.netSuggForget()
	}
	networkBack(w, r, "", key)
}

// netAdoptError trägt den Meldungsschlüssel für die Seite; der Text nennt nie Werte.
type netAdoptError struct {
	Key string
	Err error
}

func (e *netAdoptError) Error() string { return "Übernehmen fehlgeschlagen (" + e.Key + ")" }
func (e *netAdoptError) Unwrap() error { return e.Err }

func netAdoptKey(err error, def string) string {
	var ae *netAdoptError
	if errors.As(err, &ae) {
		return ae.Key
	}
	return def
}

func netTrunc(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// netAdopt übernimmt Zeilen in einer Transaktion: erst Netze, dann WLANs; je Art erst Angleichen (UPDATE),
// dann Neuanlegen (INSERT), damit ein frei gewordener Name oder eine frei gewordene VLAN-Nummer in
// derselben Übernahme wieder vergeben werden kann. Alles oder nichts.
// Nur Werte des Controllers (row.net / row.wlan) – Zweck, Notiz und ESPPDH-Haken bleiben unberührt.
func (h *Handler) netAdopt(ctx context.Context, rows []netSuggRow) error {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	key := h.networkKey()

	vlanArg := func(v int) any {
		if v >= 1 && v <= 4094 {
			return v
		}
		return nil
	}
	insertSeg := func(src string, n *netCtlNet) (string, error) {
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO net_segments (name, vlan_id, subnet, source, ext_id, adopted_at)
			VALUES ($1, $2, $3, $4, $5, now()) RETURNING id::text`,
			netTrunc(n.Name, 80), vlanArg(n.VLAN), n.Subnet, src, n.ExtID).Scan(&id)
		if networkDup(err) {
			return "", &netAdoptError{Key: "seg-dup", Err: err}
		}
		return id, err
	}
	doSeg := func(r netSuggRow) error {
		n := r.net
		if r.State == netStateNew {
			_, err := insertSeg(r.Source, n)
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE net_segments SET
				name = COALESCE(NULLIF($2, ''), name),
				vlan_id = COALESCE($3, vlan_id),
				subnet = CASE WHEN $4 <> '' THEN $4 ELSE subnet END,
				source = $5, ext_id = $6, adopted_at = now(), updated_at = now()
			WHERE id = $1::uuid`, r.PDHID, netTrunc(n.Name, 80), vlanArg(n.VLAN), n.Subnet, r.Source, n.ExtID)
		if networkDup(err) {
			return &netAdoptError{Key: "seg-dup", Err: err}
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return &netAdoptError{Key: "sugg-stale"}
		}
		return nil
	}
	doWLAN := func(r netSuggRow) error {
		w := r.wlan
		// Netz des WLANs: bekanntes PDH-Netz; sonst – wenn es mit angelegt werden soll – erst nach
		// Quelle+Kennung, dann nach VLAN neu suchen (ein Netz derselben Übernahme gibt es jetzt schon).
		var seg any
		switch {
		case r.NetPDHID != "":
			seg = r.NetPDHID
		case r.NetCreate != nil:
			c := r.NetCreate
			var id string
			err := tx.QueryRow(ctx, `SELECT id::text FROM net_segments WHERE source = $1 AND ext_id = $2 AND ext_id <> ''`, r.Source, c.ExtID).Scan(&id)
			if err != nil && c.VLAN > 0 {
				err = tx.QueryRow(ctx, `SELECT id::text FROM net_segments WHERE vlan_id = $1`, c.VLAN).Scan(&id)
			}
			if err != nil {
				if id, err = insertSeg(r.Source, c); err != nil {
					return err
				}
			}
			seg = id
		}
		enc := ""
		if r.PwKnown {
			var err error
			if enc, err = encryptSecret(key, w.Password); err != nil {
				return errors.New("Kennwort konnte nicht verschlüsselt werden")
			}
		}
		if r.State == netStateNew {
			ssid := strings.TrimSpace(w.SSID)
			if ssid == "" || utf8.RuneCountInString(ssid) > 32 {
				return &netAdoptError{Key: "ssid"}
			}
			_, err := tx.Exec(ctx, `INSERT INTO net_wlans (ssid, segment_id, password_enc, hidden, esppdh_ok, source, ext_id, adopted_at)
				VALUES ($1, $2::uuid, $3, $4, false, $5, $6, now())`, ssid, seg, enc, w.Hidden, r.Source, w.ExtID)
			if networkDup(err) {
				return &netAdoptError{Key: "wlan-dup", Err: err}
			}
			return err
		}
		open := w.Security == unifi.SecOpen
		tag, err := tx.Exec(ctx, `UPDATE net_wlans SET
				ssid = CASE WHEN $2 <> '' THEN $2 ELSE ssid END,
				segment_id = COALESCE($3::uuid, segment_id),
				hidden = $4,
				password_enc = CASE WHEN $5 THEN '' WHEN $6 <> '' THEN $6 ELSE password_enc END,
				source = $7, ext_id = $8, adopted_at = now(), updated_at = now()
			WHERE id = $1::uuid`, r.PDHID, strings.TrimSpace(w.SSID), seg, w.Hidden, open, enc, r.Source, w.ExtID)
		if networkDup(err) {
			return &netAdoptError{Key: "wlan-dup", Err: err}
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return &netAdoptError{Key: "sugg-stale"}
		}
		return nil
	}

	for _, kind := range []string{netKindSegment, netKindWLAN} {
		for _, state := range []string{netStateDiff, netStateNew} {
			for _, r := range rows {
				if r.Kind != kind || r.State != state || (kind == netKindSegment && r.net == nil) || (kind == netKindWLAN && r.wlan == nil) {
					continue
				}
				do := doSeg
				if kind == netKindWLAN {
					do = doWLAN
				}
				if err := do(r); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit(ctx)
}

func netCount(rows []netSuggRow) (nets, wlans int) {
	for _, r := range rows {
		if r.Kind == netKindSegment {
			nets++
		} else {
			wlans++
		}
	}
	return
}

func (h *Handler) netSuggDone(w http.ResponseWriter, r *http.Request, source string, rows []netSuggRow, notice string, err error) {
	if err != nil {
		key := netAdoptKey(err, "save")
		if key == "sugg-stale" {
			h.netSuggForget()
		}
		// nur der Fehlertext (ohne Werte) ins Protokoll
		componentLog("netzwerk").Error().Err(err).Str("quelle", source).Msg("vorschlag übernehmen")
		networkBack(w, r, "", key)
		return
	}
	n, m := netCount(rows)
	componentLog("netzwerk").Info().Str("benutzer", getUser(r).ID).Str("quelle", source).Int("netze", n).Int("wlans", m).Msg("vom controller übernommen")
	networkBack(w, r, notice, "")
}

// NetworkSuggAdoptWeb: POST /admin/network/suggestions/adopt (source, kind, ext, fp, state, pdh).
func (h *Handler) NetworkSuggAdoptWeb(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permNetwork) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	row, key := h.netSuggFindOne(r.Context(), r.PostForm)
	if key != "" {
		h.netSuggRefuse(w, r, key)
		return
	}
	err := h.netAdopt(r.Context(), []netSuggRow{*row})
	h.netSuggDone(w, r, r.PostForm.Get("source"), []netSuggRow{*row}, "sugg-adopted", err)
}

// NetworkSuggAdoptAllWeb: POST /admin/network/suggestions/adopt-all (source, set). Übernimmt nur, wenn
// der Satz der gezeigten Zeilen (set) noch genau dem entspricht, was jetzt gilt.
func (h *Handler) NetworkSuggAdoptAllWeb(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permNetwork) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	src := r.PostForm.Get("source")
	all, key := h.netSuggRows(r.Context(), src)
	if key != "" {
		h.netSuggRefuse(w, r, key)
		return
	}
	var todo []netSuggRow
	for _, x := range all {
		if x.State != netStateSame {
			todo = append(todo, x)
		}
	}
	if len(todo) == 0 || netSetFP(all) != r.PostForm.Get("set") {
		h.netSuggRefuse(w, r, "sugg-stale")
		return
	}
	err := h.netAdopt(r.Context(), todo)
	h.netSuggDone(w, r, src, todo, "sugg-all", err)
}

// NetworkSuggHideWeb: POST /admin/network/suggestions/hide (source, kind, ext, fp, state, pdh).
func (h *Handler) NetworkSuggHideWeb(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permNetwork) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	row, key := h.netSuggFindOne(r.Context(), r.PostForm)
	if key != "" {
		h.netSuggRefuse(w, r, key)
		return
	}
	src, uid := r.PostForm.Get("source"), getUser(r).ID
	if err := h.netHide(r.Context(), netHidden{Source: src, Kind: row.Kind, ExtID: row.ExtID, Fingerprint: row.Fingerprint}, uid); err != nil {
		componentLog("netzwerk").Error().Err(err).Msg("vorschlag ausblenden")
		networkBack(w, r, "", "save")
		return
	}
	componentLog("netzwerk").Info().Str("benutzer", uid).Str("quelle", src).Str("art", row.Kind).Msg("vorschlag ausgeblendet")
	networkBack(w, r, "sugg-hidden", "")
}
