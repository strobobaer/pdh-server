package web

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"pdh/pkg/logger"
)

// Server-Einstellungen -> Protokoll ansehen: Filter nach Zeitraum, Stufe,
// Bereich, Benutzer, Pfad, Status, Request-ID und Volltext; Live-Ansicht,
// Details je Eintrag und Download (CSV/JSON bzw. ganze Tagesdatei).

func (h *Handler) canViewLogs(r *http.Request) bool {
	return h.actorIsAdmin(r) && h.hasPerm(r, "system.logs")
}

type logFilterView struct {
	From, To, Level, Component, Text, User, Path, Status, RequestID string
	Limit                                                           int
	Live                                                            bool
	Range                                                           string // 15m | 1h | 24h | 7d | custom
}

type logRowView struct {
	Time, TimeFull, Level, Component, Msg, User, Path, Status, Duration, RequestID string
	Details                                                                        []logDetail
	LevelClass                                                                     string
	ComponentURL, UserURL, RIDURL                                                  string
}

type logDetail struct{ Key, Value string }

type ServerLogsData struct {
	BaseData
	Filter     logFilterView
	Rows       []logRowView
	Scanned    int
	Truncated  bool
	Components []string
	Days       []logDayView
	Stats      map[string]int
	Dir        string
	Query      string // aktuelle Filter als Query (Downloads/Live)
	RowsURL    string
	CSVURL     string
	JSONURL    string
	Now        string
	Err        string
	CanConfig  bool
}

type logDayView struct{ Day, Size string }

// parseLogFilter liest die Filter aus der URL (Zeiten in Ortszeit).
func parseLogFilter(q url.Values) (logFilterView, logger.Filter) {
	v := logFilterView{
		From: q.Get("from"), To: q.Get("to"), Level: q.Get("level"), Component: q.Get("component"),
		Text: strings.TrimSpace(q.Get("q")), User: strings.TrimSpace(q.Get("user")), Path: strings.TrimSpace(q.Get("path")),
		Status: strings.TrimSpace(q.Get("status")), RequestID: strings.TrimSpace(q.Get("rid")), Live: q.Get("live") == "1",
		Range: q.Get("range"),
	}
	v.Limit, _ = strconv.Atoi(q.Get("limit"))
	if v.Limit <= 0 || v.Limit > 5000 {
		v.Limit = 300
	}
	f := logger.Filter{MinLevel: v.Level, Component: v.Component, Text: v.Text, User: v.User, Path: v.Path,
		Status: strings.ToLower(v.Status), RequestID: v.RequestID, Limit: v.Limit}
	now := time.Now()
	switch v.Range {
	case "15m":
		f.From = now.Add(-15 * time.Minute)
	case "24h":
		f.From = now.Add(-24 * time.Hour)
	case "7d":
		f.From = now.AddDate(0, 0, -7)
	case "custom":
		if t, err := time.ParseInLocation("2006-01-02T15:04", v.From, time.Local); err == nil {
			f.From = t
		}
		if t, err := time.ParseInLocation("2006-01-02T15:04", v.To, time.Local); err == nil {
			f.To = t
		}
		if f.From.IsZero() {
			f.From = now.Add(-24 * time.Hour)
		}
	default:
		v.Range = "1h"
		f.From = now.Add(-time.Hour)
	}
	return v, f
}

var levelClasses = map[string]string{"error": "lg-error", "fatal": "lg-error", "panic": "lg-error", "warn": "lg-warn", "debug": "lg-debug", "trace": "lg-debug"}

func logRow(e logger.Entry) logRowView {
	row := logRowView{
		Time: e.Time.Local().Format("15:04:05"), TimeFull: e.Time.Local().Format("02.01.2006 15:04:05.000"),
		Level: e.Level, Component: e.Component(), Msg: e.Msg, Path: e.Str("path"), Status: e.Str("status"),
		RequestID: e.Str("request_id"), LevelClass: levelClasses[e.Level],
	}
	if row.Time == "" || e.Time.IsZero() {
		row.Time, row.TimeFull = "–", "–"
	}
	if n := e.Str("user_name"); n != "" {
		row.User = n
	} else if u := e.Str("login"); u != "" {
		row.User = u
	}
	if d := e.Str("dauer_ms"); d != "" {
		row.Duration = d + " ms"
	}
	if errMsg := e.Str("error"); errMsg != "" && !strings.Contains(row.Msg, errMsg) {
		row.Msg += " – " + errMsg
	}
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		row.Details = append(row.Details, logDetail{k, e.Str(k)})
	}
	return row
}

func (h *Handler) loadLogs(r *http.Request) ServerLogsData {
	fv, f := parseLogFilter(r.URL.Query())
	s := logger.Store()
	d := ServerLogsData{
		BaseData: h.baseData(r, "server-logs", "Server-Protokoll", "Protokoll"),
		Filter:   fv, Components: s.Components(), Stats: s.Stats(), Dir: s.Dir(), CanConfig: h.canServerConfig(r),
	}
	q := r.URL.Query()
	q.Del("live")
	d.Query = q.Encode()
	entries, scanned, err := s.Query(f)
	if err != nil {
		d.Err = err.Error()
	}
	d.Scanned, d.Truncated = scanned, len(entries) >= f.Limit
	with := func(k, v string) string {
		q2 := r.URL.Query()
		q2.Set(k, v)
		q2.Del("live")
		return "/admin/server-config/logs?" + q2.Encode()
	}
	d.RowsURL = "/admin/server-config/logs/rows?" + r.URL.Query().Encode()
	d.CSVURL = "/admin/server-config/logs/download?format=csv&" + d.Query
	d.JSONURL = "/admin/server-config/logs/download?format=json&" + d.Query
	d.Now = time.Now().Format("15:04:05")
	for _, e := range entries {
		d.Rows = append(d.Rows, logRow(e))
		row := &d.Rows[len(d.Rows)-1]
		row.ComponentURL = with("component", row.Component)
		if row.User != "" {
			row.UserURL = with("user", row.User)
		}
		if row.RequestID != "" {
			row.RIDURL = "/admin/server-config/logs?" + url.Values{"range": {"7d"}, "rid": {row.RequestID}, "level": {"debug"}}.Encode()
		}
	}
	for _, day := range s.Days() {
		d.Days = append(d.Days, logDayView{day.Day, humanSize(day.Size)})
	}
	return d
}

// ServerLogsPage: GET /admin/server-config/logs
func (h *Handler) ServerLogsPage(w http.ResponseWriter, r *http.Request) {
	if !h.canViewLogs(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	h.render(w, "server_logs", h.loadLogs(r))
}

// ServerLogsRowsWeb: GET /admin/server-config/logs/rows - nur die Tabelle (Live-Ansicht)
func (h *Handler) ServerLogsRowsWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canViewLogs(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	h.renderFragment(w, "server-log-rows", h.loadLogs(r))
}

// ServerLogsDownloadWeb: GET /admin/server-config/logs/download?format=csv|json (gefiltert)
// bzw. ?day=YYYY-MM-DD (ganze Tagesdatei).
func (h *Handler) ServerLogsDownloadWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canViewLogs(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if day := r.URL.Query().Get("day"); day != "" {
		if _, err := time.Parse("2006-01-02", day); err != nil {
			http.Error(w, "ungültiger Tag", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Disposition", `attachment; filename="pdh-`+day+`.jsonl"`)
		if err := logger.Store().CopyDay(w, day); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
		}
		return
	}
	_, f := parseLogFilter(r.URL.Query())
	if f.Limit < 5000 {
		f.Limit = 5000
	}
	entries, _, err := logger.Store().Query(f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stamp := time.Now().Format("20060102-150405")
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Disposition", `attachment; filename="pdh-protokoll-`+stamp+`.jsonl"`)
		for i := len(entries) - 1; i >= 0; i-- {
			fmt.Fprintln(w, entries[i].Raw)
		}
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="pdh-protokoll-`+stamp+`.csv"`)
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // Excel erkennt UTF-8
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	_ = cw.Write([]string{"Zeit", "Stufe", "Bereich", "Meldung", "Benutzer", "Methode", "Pfad", "Status", "Dauer (ms)", "IP", "Request-ID", "Details"})
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		b, _ := json.Marshal(e.Fields)
		user := e.Str("user_name")
		if user == "" {
			user = e.Str("login")
		}
		_ = cw.Write(csvSafe([]string{e.Time.Local().Format("2006-01-02 15:04:05.000"), e.Level, e.Component(), e.Msg, user,
			e.Str("method"), e.Str("path"), e.Str("status"), e.Str("dauer_ms"), e.Str("ip"), e.Str("request_id"), string(b)}))
	}
	cw.Flush()
}

// csvSafe verhindert Formel-Injektion beim Oeffnen in Excel.
func csvSafe(rec []string) []string {
	for i, v := range rec {
		if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
			rec[i] = "'" + v
		}
	}
	return rec
}
