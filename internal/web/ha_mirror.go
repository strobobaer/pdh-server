package web

// Datei-Spiegelung (Ausfallschutz Teil 6, Spec 9–11): Karte „Datei-Spiegelung“
// im Reiter Ausfallschutz. Das PDH liest apiStatus.Mirror von pdh-ha, speichert
// die Einstellungen über PUT /v1/settings und reicht die Knöpfe weiter.
//
//	POST /admin/server-config/ha/mirror              Einstellungen speichern      (system.file_mirror)
//	POST /admin/server-config/ha/mirror-run          Abgleich jetzt               (system.file_mirror)
//	POST /admin/server-config/ha/mirror-trash-empty  Papierkorb leeren, endgültig (system.file_mirror_admin)
//	POST /admin/server-config/ha/mirror-confirm      Dieser Server ist vollständig (system.file_mirror_admin)
//
// Beide Rechte gelten systemweit (wie das Wartungsfenster): ein auf eine
// Abteilung begrenztes Recht reicht nicht.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pdh/internal/ha"
)

const (
	permFileMirror      = "system.file_mirror"
	permFileMirrorAdmin = "system.file_mirror_admin"

	// mirrorFinalWarnS: länger darf der letzte Abgleich vor einer Übergabe nicht dauern,
	// ohne dass ein Update mit Rollentausch abbrechen kann.
	mirrorFinalWarnS = 150

	// haMirrorRunTimeout: so lange darf ein „Abgleich jetzt“ im Hintergrund laufen.
	haMirrorRunTimeout = 30 * time.Minute
	// mirrorMissingShown: so viele fehlende Nextcloud-Dateien nennt die Karte.
	mirrorMissingShown = 20
)

// haMirrorRunWait: so lange wartet der Knopf auf eine schnelle Ablehnung (Variable für Tests).
var haMirrorRunWait = 1500 * time.Millisecond

// haMirrorBusy: ein „Abgleich jetzt“ aus der Oberfläche läuft noch.
var haMirrorBusy atomic.Bool

// haSettingsMu: Lesen und Schreiben der Einstellungen über den Socket nie ineinander.
var haSettingsMu sync.Mutex

// Spiegel von apiMirror (cmd/pdh-ha/mirror_api.go). Am Zeugen fehlt das Feld.
type haMirror struct {
	On             bool             `json:"on"`
	Active         bool             `json:"active"`
	Running        bool             `json:"running"`
	FirstDone      bool             `json:"first_done"`
	FirstPercent   int              `json:"first_percent"`
	LastOKAgo      float64          `json:"last_ok_ago"` // -1 = noch nie
	LastRunSec     float64          `json:"last_run_s"`
	Err            string           `json:"err"`
	WatchErr       string           `json:"watch_err"`
	Slow           bool             `json:"slower_than_interval"`
	WatchLimited   bool             `json:"watch_limited"`
	TrashBytes     int64            `json:"trash_bytes"`
	Orphans        int              `json:"orphans"`
	EnvSame        bool             `json:"env_same"`
	NCVerSame      bool             `json:"nc_ver_same"`
	NCMissing      []string         `json:"nc_missing"`
	NCMissingTotal int              `json:"nc_missing_total"`
	NCChecked      int              `json:"nc_checked"`
	CertCovers     *bool            `json:"cert_covers"` // null = kein TLS
	Sources        []haMirrorSource `json:"sources"`
	Unproven       []string         `json:"unproven"`
	WaitConfirm    []string         `json:"wait_confirm"` // wartet auf Bestätigung (R47)
}

type haMirrorSource struct {
	Label     string  `json:"label"`
	LastOKAgo float64 `json:"last_ok_ago"`
	Skipped   bool    `json:"skipped"`
	Unproven  bool    `json:"unproven"`
	Err       string  `json:"err"`
	Waiting   bool    `json:"waiting"`
}

// Text der Quelle: wann zuletzt abgeglichen.
func (s haMirrorSource) AgoText() string { return haAgo(s.LastOKAgo) }

// mirrorForm: Werte des Einstellungsformulars (Sekunden, Minuten, Tage).
type mirrorForm struct {
	On, Nextcloud                       bool
	EveryS, TrashDays, AlarmMin, FinalS int
	BWLimitKB                           int
	NCDataDir, NCAppDir                 string
}

// mirrorView: was die Karte braucht (aus Status und Sitzung berechnet).
type mirrorView struct {
	M      *haMirror
	Form   mirrorForm
	Edit   bool // Einstellungen als Formular (sonst nur Anzeige)
	CanRun bool // „Abgleich jetzt“
	// „Dieser Server ist vollständig“: nur mit dem Recht, am aktiven Server, wenn eine Quelle wartet
	CanConfirm bool
	// Papierkorb leeren: nur mit dem Recht und wenn am aktiven Server etwas liegt
	CanTrash bool

	FinalHigh bool   // Frist über 150 s
	BaseWarn  string // Adresse aus PDH_NEXTCLOUD_BASEURL, die einem Server fest gehört ("" = in Ordnung)

	MissingShown []string
	MissingMore  int
}

// ShowMirror: die Karte erscheint nur auf einem Server im Verbund (nicht am Zeugen, nicht im Einzelbetrieb).
func (v haView) ShowMirror() bool {
	return v.IsCluster() && v.Status.Self.Kind != "witness" && v.Status.Mirror != nil
}

// MirrorView berechnet die Karte; die Rechte der Sitzung (MirrorCan, MirrorAdmin) setzt der Aufrufer.
func (v haView) MirrorView() mirrorView {
	m := v.Status.Mirror
	s := v.Status.Settings
	d := ha.DefaultSettings()
	mv := mirrorView{M: m}
	if m == nil {
		return mv
	}
	pick := func(n, def int) int {
		if n == 0 {
			return def
		}
		return n
	}
	mv.Form = mirrorForm{
		On: s.MirrorOn, Nextcloud: s.MirrorNextcloud,
		EveryS:    pick(int(s.MirrorEvery/time.Second), int(d.MirrorEvery/time.Second)),
		TrashDays: pick(s.MirrorTrashDays, d.MirrorTrashDays),
		AlarmMin:  pick(int(s.MirrorAlarmAfter/time.Minute), int(d.MirrorAlarmAfter/time.Minute)),
		FinalS:    pick(int(s.MirrorFinalMax/time.Second), int(d.MirrorFinalMax/time.Second)),
		BWLimitKB: s.MirrorBWLimitKB, NCDataDir: s.NCDataDir,
		NCAppDir: s.NCAppDir,
	}
	if mv.Form.NCAppDir == "" {
		mv.Form.NCAppDir = d.NCAppDir
	}
	mv.Edit = v.MirrorCan && v.CanEditSettings()
	mv.CanRun = v.MirrorCan && m.On && m.Active
	mv.CanTrash = v.MirrorAdmin && m.Active && m.TrashBytes > 0
	mv.CanConfirm = v.MirrorAdmin && m.On && m.Active && len(m.WaitConfirm) > 0
	mv.FinalHigh = m.On && mv.Form.FinalS > mirrorFinalWarnS
	if m.On && s.MirrorNextcloud {
		mv.BaseWarn = mirrorBaseURLWarn(v.NCBaseURL, v.Status.Access.VIP, v.nodeAddrs())
	}
	mv.MissingShown = m.NCMissing
	if len(mv.MissingShown) > mirrorMissingShown {
		mv.MissingShown = mv.MissingShown[:mirrorMissingShown]
	}
	if more := max(m.NCMissingTotal, len(m.NCMissing)) - len(mv.MissingShown); more > 0 {
		mv.MissingMore = more
	}
	return mv
}

// TrashText: Größe des Papierkorbs („1.5 MB“).
func (mv mirrorView) TrashText() string { return humanSize(mv.M.TrashBytes) }

// LastText: „vor 12 s“ oder „noch nie“.
func (mv mirrorView) LastText() string { return haAgo(mv.M.LastOKAgo) }

// RunText: Laufzeit des letzten Abgleichs („3,2 s“, „2 min“).
func (mv mirrorView) RunText() string {
	if s := mv.M.LastRunSec; s >= 60 {
		return fmt.Sprintf("%d min", int(s/60))
	}
	return strings.Replace(fmt.Sprintf("%.1f", mv.M.LastRunSec), ".", ",", 1) + " s"
}

// CertBad: das Zertifikat von Nextcloud enthält die mitwandernde Adresse nicht.
func (mv mirrorView) CertBad() bool { return mv.M.CertCovers != nil && !*mv.M.CertCovers }

// HasSources: gibt es Quellen für die Liste?
func (mv mirrorView) HasSources() bool { return len(mv.M.Sources) > 0 }

// nodeAddrs: feste Adressen der Server (Wege der Partner, eigene Schnittstellen).
func (v haView) nodeAddrs() []string {
	var out []string
	for _, n := range v.Nodes() {
		for _, p := range n.Paths {
			if h, _, err := net.SplitHostPort(p.Addr); err == nil {
				out = append(out, h)
			}
		}
	}
	return append(out, v.SuggestAddrs...)
}

// mirrorBaseURLWarn: PDH_NEXTCLOUD_BASEURL zeigt auf die feste Adresse eines
// Servers statt auf einen Namen oder die mitwandernde Adresse – nach einer
// Übernahme ginge die Verbindung ins Leere. Liefert die Adresse oder "".
func mirrorBaseURLWarn(baseURL, vip string, nodeAddrs []string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return ""
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		u, err = url.Parse("//" + baseURL)
		if err != nil {
			return ""
		}
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil {
		return "" // ein Name
	}
	if vip = strings.TrimSpace(vip); vip != "" {
		if vi := net.ParseIP(strings.SplitN(vip, "/", 2)[0]); vi != nil && vi.Equal(ip) {
			return ""
		}
	}
	for _, a := range nodeAddrs {
		if n := net.ParseIP(strings.TrimSpace(a)); n != nil && n.Equal(ip) {
			return ip.String()
		}
	}
	return ""
}

// ── Aktionen ────────────────────────────────────────────────

// haCurrent: aktuelle Einstellungen und Gateway-Ziele von pdh-ha.
func (h *Handler) haCurrent(ctx context.Context) (haStatus, error) {
	var st haStatus
	if err := h.haDo(ctx, http.MethodGet, "/v1/status", nil, &st); err != nil {
		return st, err
	}
	return st, nil
}

// mirrorSettingsFromForm legt die Felder des Formulars über die bisherigen
// Einstellungen. Fehlt ein Feld im Formular, bleibt der bisherige Wert.
func mirrorSettingsFromForm(cur ha.Settings, f url.Values) ha.Settings {
	s := cur
	has := func(k string) bool { _, ok := f[k]; return ok }
	// Kästchen senden versteckt „0“ und angehakt „1“: der letzte Wert zählt
	if has("mirror_on") {
		s.MirrorOn = lastIs(f["mirror_on"], "1")
	}
	if has("mirror_nextcloud") {
		s.MirrorNextcloud = lastIs(f["mirror_nextcloud"], "1")
	}
	if has("mirror_every_s") {
		s.MirrorEvery = time.Duration(drFormInt(f, "mirror_every_s", 0)) * time.Second
	}
	if has("mirror_trash_days") {
		s.MirrorTrashDays = drFormInt(f, "mirror_trash_days", 0)
	}
	if has("mirror_alarm_min") {
		s.MirrorAlarmAfter = time.Duration(drFormInt(f, "mirror_alarm_min", 0)) * time.Minute
	}
	if has("mirror_final_s") {
		s.MirrorFinalMax = time.Duration(drFormInt(f, "mirror_final_s", 0)) * time.Second
	}
	if has("mirror_bwlimit_kb") {
		s.MirrorBWLimitKB = drFormInt(f, "mirror_bwlimit_kb", 0)
	}
	if has("nc_data_dir") {
		s.NCDataDir = strings.TrimSpace(f.Get("nc_data_dir"))
	}
	if has("nc_app_dir") {
		s.NCAppDir = strings.TrimSpace(f.Get("nc_app_dir"))
	}
	return s
}

func lastIs(vals []string, want string) bool {
	return len(vals) > 0 && vals[len(vals)-1] == want
}

// haDoMirror: Einstellungen der Datei-Spiegelung speichern. Liest zuerst die
// aktuellen Einstellungen und ändert nur die Spiegel-Felder (Zeiten, Gateway-Ziele
// und Betriebsart bleiben); ungültige Werte speichern nichts.
func (h *Handler) haDoMirror(ctx context.Context, f url.Values) (string, error) {
	haSettingsMu.Lock()
	defer haSettingsMu.Unlock()
	st, err := h.haCurrent(ctx)
	if err != nil {
		return "", err
	}
	s := mirrorSettingsFromForm(st.Settings, f)
	if err := s.Validate(); err != nil {
		return "", err
	}
	if err := h.haDo(ctx, http.MethodPut, "/v1/settings", map[string]any{"settings": s, "gateway_targets": st.GatewayTargets}, nil); err != nil {
		return "", err
	}
	msg := "Einstellungen der Datei-Spiegelung gespeichert"
	if s.MirrorOn && s.MirrorFinalMax > mirrorFinalWarnS*time.Second {
		msg += " – Eine Frist über 150 s kann ein Update mit Rollentausch abbrechen lassen"
	}
	return msg, nil
}

// haDoMirrorRun: Abgleich jetzt. pdh-ha wartet auf einen laufenden Abgleich und
// läuft dann selbst – das kann lange dauern. Der Aufruf läuft darum im
// Hintergrund (eigene lange Frist, haDoT); eine schnelle Ablehnung (nicht
// aktiv, Spiegelung aus) kommt binnen kurzer Zeit noch an die Anzeige zurück.
func (h *Handler) haDoMirrorRun(_ context.Context, _ url.Values) (string, error) {
	if !haMirrorBusy.CompareAndSwap(false, true) {
		return "Ein Abgleich läuft bereits – das Ergebnis erscheint in der Karte", nil
	}
	done := make(chan error, 1)
	go func() {
		defer haMirrorBusy.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), haMirrorRunTimeout)
		defer cancel()
		err := h.haDoT(ctx, haMirrorRunTimeout, http.MethodPost, "/v1/mirror/run", map[string]any{}, nil)
		if err != nil {
			componentLog("ha").Warn().Err(err).Msg("datei-spiegelung: abgleich jetzt")
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			return "", err
		}
		return "Abgleich abgeschlossen", nil
	case <-time.After(haMirrorRunWait):
		return "Abgleich gestartet – das Ergebnis erscheint in der Karte", nil
	}
}

// haDoMirrorTrash: Papierkorb am passiven Server leeren (endgültig, vom aktiven Server angefordert).
func (h *Handler) haDoMirrorTrash(ctx context.Context, _ url.Values) (string, error) {
	if err := h.haDo(ctx, http.MethodPost, "/v1/mirror/trash-empty", map[string]any{}, nil); err != nil {
		return "", err
	}
	return "Papierkorb der Datei-Spiegelung wird geleert", nil
}

// HAMirrorWeb: POST /admin/server-config/ha/mirror
func (h *Handler) HAMirrorWeb(w http.ResponseWriter, r *http.Request) {
	h.haRoutePerm(permFileMirror, h.haDoMirror, "mirror")(w, r)
}

// HAMirrorRunWeb: POST /admin/server-config/ha/mirror-run
func (h *Handler) HAMirrorRunWeb(w http.ResponseWriter, r *http.Request) {
	h.haRoutePerm(permFileMirror, h.haDoMirrorRun, "mirror-run")(w, r)
}

// HAMirrorTrashEmptyWeb: POST /admin/server-config/ha/mirror-trash-empty
func (h *Handler) HAMirrorTrashEmptyWeb(w http.ResponseWriter, r *http.Request) {
	h.haRoutePerm(permFileMirrorAdmin, h.haDoMirrorTrash, "mirror-trash-empty")(w, r)
}

// haDoMirrorConfirm: Dieser Server ist vollständig: Der nächste Abgleich löscht am Partner.
// Der passive Server lehnt über pdh-ha ab. R61: Die Karte sendet die Quellen mit, die sie beim
// Klick zeigte (labels); pdh-ha gibt nur diese frei und nennt sie; später wartende bleiben wartend.
func (h *Handler) haDoMirrorConfirm(ctx context.Context, f url.Values) (string, error) {
	var res struct {
		Labels []string `json:"labels"`
	}
	labels := []string{}
	for _, l := range f["labels"] {
		if l = strings.TrimSpace(l); l != "" {
			labels = append(labels, l)
		}
	}
	if err := h.haDo(ctx, http.MethodPost, "/v1/mirror/confirm", map[string]any{"labels": labels}, &res); err != nil {
		return "", err
	}
	return "Bestätigt für " + strings.Join(res.Labels, ", ") + ": Der nächste Abgleich löscht, abweichende Dateien am Partner wandern in den Papierkorb", nil
}

// HAMirrorConfirmWeb: POST /admin/server-config/ha/mirror-confirm
func (h *Handler) HAMirrorConfirmWeb(w http.ResponseWriter, r *http.Request) {
	h.haRoutePerm(permFileMirrorAdmin, h.haDoMirrorConfirm, "mirror-confirm")(w, r)
}
