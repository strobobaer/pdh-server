package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Druckerintegration (Verwaltung → Drucker): Zebra ZT4xx (ZPL, Port 9100),
// Dymo LabelWriter (ueber DYMO Connect am PC) und Netzwerkdrucker (IPP oder
// RAW 9100). Je Drucker Pruefungen und Testdruck; die Etiketten-Druckansicht
// druckt direkt auf einen eingerichteten Drucker.

var printerKinds = []KindOption{
	{"zebra", "Zebra ZT4xx (Etiketten, ZPL)"},
	{"dymo", "Dymo LabelWriter (über DYMO Connect)"},
	{"network", "Netzwerkdrucker (IPP / RAW 9100)"},
}

// printerFields: erlaubte Konfigurationsfelder je Druckertyp.
var printerFields = map[string][]string{
	"zebra":   {"host", "port", "dpi", "label_width_mm", "label_height_mm", "darkness", "speed", "media"},
	"dymo":    {"printer_name", "label_type", "paper_name", "service_port"},
	"network": {"host", "protocol", "port", "ipp_path", "tls_insecure", "paper"},
}

func printerKindLabel(k string) string {
	for _, o := range printerKinds {
		if o.Value == k {
			return o.Label
		}
	}
	return k
}

type printerView struct {
	ID, Kind, KindLabel, Name, Location string
	Config                              map[string]string
	ConfigJSON                          string
	Enabled, IsDefault                  bool
	Check                               *checkReport
	CheckAt, LastPrintAt                string
	Summary                             string
}

func (p printerView) Get(k string) string { return p.Config[k] }

func printerSummary(kind string, c map[string]string) string {
	switch kind {
	case "zebra":
		return fmt.Sprintf("%s:%s · %s dpi · %s × %s mm", c["host"], firstNonEmpty(c["port"], "9100"), firstNonEmpty(c["dpi"], "203"),
			firstNonEmpty(c["label_width_mm"], "100"), firstNonEmpty(c["label_height_mm"], "50"))
	case "dymo":
		return firstNonEmpty(c["printer_name"], "Drucker in DYMO Connect") + " · " + dymoLabelTypeByKey(c["label_type"]).Label
	case "network":
		proto := strings.ToUpper(firstNonEmpty(c["protocol"], "ipp"))
		port := c["port"]
		if port == "" {
			port = map[bool]string{true: "9100", false: "631"}[c["protocol"] == "raw"]
		}
		return fmt.Sprintf("%s · %s:%s · %s", proto, c["host"], port, paperSize(c))
	}
	return ""
}

func (h *Handler) loadPrinters(ctx context.Context, onlyEnabled bool) ([]printerView, error) {
	q := `SELECT id::text, kind, name, location, config, enabled, is_default, last_check,
	             COALESCE(to_char(last_check_at, 'DD.MM.YYYY HH24:MI'), ''), COALESCE(to_char(last_print_at, 'DD.MM.YYYY HH24:MI'), '')
	      FROM printers`
	if onlyEnabled {
		q += ` WHERE enabled`
	}
	rows, err := h.db.Query(ctx, q+` ORDER BY kind, is_default DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []printerView
	for rows.Next() {
		var p printerView
		var cfg, check []byte
		if err := rows.Scan(&p.ID, &p.Kind, &p.Name, &p.Location, &cfg, &p.Enabled, &p.IsDefault, &check, &p.CheckAt, &p.LastPrintAt); err != nil {
			return nil, err
		}
		p.Config = map[string]string{}
		_ = json.Unmarshal(cfg, &p.Config)
		p.ConfigJSON = string(cfg)
		if len(check) > 0 {
			var r checkReport
			if json.Unmarshal(check, &r) == nil {
				p.Check = &r
			}
		}
		p.KindLabel = printerKindLabel(p.Kind)
		p.Summary = printerSummary(p.Kind, p.Config)
		list = append(list, p)
	}
	return list, rows.Err()
}

func (h *Handler) loadPrinter(ctx context.Context, id string) (printerView, error) {
	list, err := h.loadPrinters(ctx, false)
	if err != nil {
		return printerView{}, err
	}
	for _, p := range list {
		if p.ID == id {
			return p, nil
		}
	}
	return printerView{}, errors.New("Drucker nicht gefunden")
}

// ── Seite ────────────────────────────────────────────────────

type PrintersPageData struct {
	BaseData
	Printers       []printerView
	Kinds          []KindOption
	DymoLabelTypes []dymoLabelType
	Notice, Err    string
	PublicURL      string
	ZebraCount     int
	DymoCount      int
	NetworkCount   int
}

func (h *Handler) canManagePrinters(r *http.Request) bool { return h.hasPerm(r, "printers.manage") }

func (h *Handler) PrintersPage(w http.ResponseWriter, r *http.Request) {
	if !h.canManagePrinters(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	list, err := h.loadPrinters(r.Context(), false)
	d := PrintersPageData{
		BaseData: h.baseData(r, "printers", "Drucker", "Etiketten- und Netzwerkdrucker"),
		Printers: list, Kinds: printerKinds, DymoLabelTypes: dymoLabelTypes,
		Notice: r.URL.Query().Get("notice"), Err: r.URL.Query().Get("err"), PublicURL: h.publicBaseURL(r),
	}
	if err != nil {
		d.Err = "Drucker konnten nicht geladen werden: " + err.Error()
	}
	for _, p := range list {
		switch p.Kind {
		case "zebra":
			d.ZebraCount++
		case "dymo":
			d.DymoCount++
		case "network":
			d.NetworkCount++
		}
	}
	h.render(w, "printers", d)
}

func printersBack(w http.ResponseWriter, r *http.Request, notice, errMsg string) {
	p := "/admin/printers?"
	if notice != "" {
		p += "notice=" + url.QueryEscape(notice) + "&"
	}
	if errMsg != "" {
		p += "err=" + url.QueryEscape(errMsg) + "&"
	}
	if a := r.FormValue("anchor"); a != "" {
		p += "#" + url.PathEscape(a)
	}
	http.Redirect(w, r, strings.TrimRight(p, "&?"), http.StatusSeeOther)
}

// validatePrinterConfig prueft die Angaben je Typ.
func validatePrinterConfig(kind string, c map[string]string) error {
	switch kind {
	case "zebra", "network":
		if c["host"] == "" {
			return errors.New("Bitte die IP-Adresse oder den Namen des Druckers eintragen")
		}
		if strings.ContainsAny(c["host"], "/ @") {
			return errors.New("Host nur als Name oder IP-Adresse, ohne http:// und Pfad")
		}
		if c["port"] != "" && parsePort(c["port"], "") == "" {
			return errors.New("Port 1 bis 65535")
		}
	}
	switch kind {
	case "zebra":
		z := zebraSettingsFrom(c)
		if z.WidthMM < 10 || z.WidthMM > 168 || z.HeightMM < 5 || z.HeightMM > 1000 {
			return errors.New("Etikettengröße: Breite 10–168 mm (ZT4xx bis 104 bzw. 168 mm), Höhe 5–1000 mm")
		}
		if c["dpi"] != "" && c["dpi"] != "203" && c["dpi"] != "300" && c["dpi"] != "600" {
			return errors.New("Auflösung: 203, 300 oder 600 dpi")
		}
	case "dymo":
		if c["printer_name"] == "" {
			return errors.New("Bitte den Druckernamen wie in DYMO Connect angezeigt eintragen (z. B. DYMO LabelWriter 450)")
		}
		if c["service_port"] != "" && parsePort(c["service_port"], "") == "" {
			return errors.New("Port des DYMO-Webdienstes 1 bis 65535 (Standard 41951)")
		}
	case "network":
		switch c["protocol"] {
		case "", "ipp", "ipps", "raw":
		default:
			return errors.New("Protokoll: ipp, ipps oder raw")
		}
	}
	return nil
}

// PrinterSaveWeb: POST /admin/printers – anlegen oder (printer_id) aendern.
func (h *Handler) PrinterSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManagePrinters(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	ctx := r.Context()
	id := strings.TrimSpace(r.FormValue("printer_id"))
	kind := r.FormValue("kind")
	if id != "" {
		p, err := h.loadPrinter(ctx, id)
		if err != nil {
			printersBack(w, r, "", err.Error())
			return
		}
		kind = p.Kind
	}
	fields, ok := printerFields[kind]
	if !ok {
		printersBack(w, r, "", "Unbekannter Druckertyp")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if l := len([]rune(name)); l < 2 || l > 150 {
		printersBack(w, r, "", "Name: 2 bis 150 Zeichen")
		return
	}
	location := strings.TrimSpace(r.FormValue("location"))
	if len([]rune(location)) > 200 {
		printersBack(w, r, "", "Standort: höchstens 200 Zeichen")
		return
	}
	cfg := map[string]string{}
	for _, f := range fields {
		v := strings.TrimSpace(r.FormValue(kind + "_" + f))
		if f == "tls_insecure" {
			v = map[bool]string{true: "true", false: "false"}[v == "on"]
		}
		cfg[f] = v
	}
	if err := validatePrinterConfig(kind, cfg); err != nil {
		printersBack(w, r, "", err.Error())
		return
	}
	b, _ := json.Marshal(cfg)
	enabled := r.FormValue("enabled") == "on"
	if id == "" {
		err := h.db.QueryRow(ctx, `INSERT INTO printers (kind, name, location, config, enabled, is_default, created_by)
			VALUES ($1, $2, $3, $4, $5, NOT EXISTS (SELECT 1 FROM printers WHERE kind = $1), $6) RETURNING id::text`,
			kind, name, location, b, enabled, nullID(getUser(r).ID)).Scan(&id)
		if err != nil {
			printersBack(w, r, "", "Drucker konnte nicht angelegt werden: "+err.Error())
			return
		}
		r.Form.Set("anchor", "p-"+id)
		printersBack(w, r, "Drucker „"+name+"“ angelegt – jetzt „Prüfen“ und „Testdruck“", "")
		return
	}
	if _, err := h.db.Exec(ctx, `UPDATE printers SET name = $1, location = $2, config = $3, enabled = $4, updated_at = NOW() WHERE id::text = $5`,
		name, location, b, enabled, id); err != nil {
		printersBack(w, r, "", "Drucker konnte nicht gespeichert werden")
		return
	}
	r.Form.Set("anchor", "p-"+id)
	printersBack(w, r, "Drucker „"+name+"“ gespeichert", "")
}

func (h *Handler) PrinterDeleteWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManagePrinters(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if _, err := h.db.Exec(r.Context(), `DELETE FROM printers WHERE id::text = $1`, chi.URLParam(r, "id")); err != nil {
		printersBack(w, r, "", "Drucker konnte nicht gelöscht werden")
		return
	}
	printersBack(w, r, "Drucker gelöscht", "")
}

func (h *Handler) PrinterDefaultWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManagePrinters(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	_, err := h.db.Exec(r.Context(), `UPDATE printers SET is_default = (id::text = $1) WHERE kind = (SELECT kind FROM printers WHERE id::text = $1)`, id)
	if err != nil {
		printersBack(w, r, "", "Standard konnte nicht gesetzt werden")
		return
	}
	r.Form.Set("anchor", "p-"+id)
	printersBack(w, r, "Standarddrucker gesetzt", "")
}

// ── Pruefungen ───────────────────────────────────────────────

func (h *Handler) storePrinterCheck(ctx context.Context, id string, rep *checkReport) {
	b, _ := json.Marshal(rep)
	_, _ = h.db.Exec(ctx, `UPDATE printers SET last_check = $1, last_check_at = NOW() WHERE id::text = $2`, b, id)
}

// runPrinterChecks prueft Zebra- und Netzwerkdrucker vom Server aus (Dymo
// prueft der Browser am PC, siehe printers.gohtml).
func runPrinterChecks(ctx context.Context, p printerView) *checkReport {
	rep := &checkReport{At: time.Now().Format("02.01.2006 15:04:05")}
	c := p.Config
	if err := validatePrinterConfig(p.Kind, c); err != nil {
		rep.add("Konfiguration", "Angaben", "fail", err.Error(), 0)
		return rep
	}
	rep.add("Konfiguration", "Angaben", "ok", "vollständig", 0)
	if !p.Enabled {
		rep.add("Konfiguration", "Status", "warn", "Drucker ist deaktiviert – er wird beim Etikettendruck nicht angeboten", 0)
	}
	host := c["host"]
	port := parsePort(c["port"], map[bool]string{true: "9100", false: "631"}[p.Kind == "zebra" || c["protocol"] == "raw"])
	if !checkNetwork(ctx, rep, host, port, p.Kind == "network" && c["protocol"] == "ipps" && c["tls_insecure"] != "true") {
		if p.Kind == "zebra" {
			rep.add("Drucker", "Tipp", "info", "Am Drucker: Menü → Netzwerk → IP-Adresse ablesen; Drucker und PDH-Server müssen sich im Netz erreichen (Port 9100).", 0)
		}
		return rep
	}
	switch p.Kind {
	case "zebra":
		z := zebraSettingsFrom(c)
		start := time.Now()
		if hi, err := zebraQuery(ctx, host, port, "~HI", 1); err == nil && len(hi) > 0 {
			model, fw, dpi := parseZebraHI(hi)
			st := "ok"
			detail := model + ", Firmware " + fw
			if !strings.HasPrefix(strings.ToUpper(model), "ZT4") {
				st, detail = "info", detail+" – kein ZT4xx, ZPL sollte trotzdem gehen"
			}
			rep.add("Drucker", "Modell", st, detail, time.Since(start).Milliseconds())
			if dpi > 0 && dpi != z.DPI {
				rep.add("Drucker", "Auflösung", "fail", fmt.Sprintf("Drucker hat %d dpi, eingestellt sind %d dpi – Etiketten würden zu groß oder zu klein", dpi, z.DPI), 0)
			} else if dpi > 0 {
				rep.add("Drucker", "Auflösung", "ok", fmt.Sprintf("%d dpi", dpi), 0)
			}
		} else {
			rep.add("Drucker", "Modell", "warn", "keine Antwort auf ~HI – ist das ein Zebra-Drucker mit ZPL?", time.Since(start).Milliseconds())
		}
		start = time.Now()
		hs, err := zebraQuery(ctx, host, port, "~HS", 3)
		if err != nil {
			rep.add("Drucker", "Zustand", "fail", err.Error(), time.Since(start).Milliseconds())
			return rep
		}
		st, err := parseZebraHS(hs)
		if err != nil {
			rep.add("Drucker", "Zustand", "warn", err.Error(), time.Since(start).Milliseconds())
			return rep
		}
		if prob := st.Problems(); len(prob) > 0 {
			rep.add("Drucker", "Zustand", "fail", strings.Join(prob, ", "), time.Since(start).Milliseconds())
		} else {
			rep.add("Drucker", "Zustand", "ok", "bereit", time.Since(start).Milliseconds())
		}
		if st.ThermalTransfer != z.ThermalTransfer {
			want := map[bool]string{true: "Thermotransfer (mit Farbband)", false: "Thermodirekt (ohne Farbband)"}
			rep.add("Drucker", "Druckverfahren", "warn", "Drucker steht auf "+want[st.ThermalTransfer]+", eingestellt ist "+want[z.ThermalTransfer]+" – PDH stellt es bei jedem Druck um; Material prüfen", 0)
		}
		if st.LabelLengthDots > 0 {
			mm := float64(st.LabelLengthDots) / float64(z.DPI) * 25.4
			if diff := mm - z.HeightMM; diff > 3 || diff < -3 {
				rep.add("Drucker", "Etikettenlänge", "warn", fmt.Sprintf("Drucker hat %.0f mm kalibriert, eingestellt sind %.0f mm – Größe prüfen oder Drucker kalibrieren (Menü → Kalibrieren)", mm, z.HeightMM), 0)
			} else {
				rep.add("Drucker", "Etikettenlänge", "ok", fmt.Sprintf("%.0f mm", mm), 0)
			}
		}
		if st.FormatsInBuffer > 0 {
			rep.add("Drucker", "Warteschlange", "info", fmt.Sprintf("%d Etikett(en) im Puffer", st.FormatsInBuffer), 0)
		}
	case "network":
		if c["protocol"] == "raw" {
			rep.add("Drucker", "Protokoll", "info", "RAW 9100: kein Zustand abfragbar. Der Drucker muss PDF direkt drucken können (bei vielen Bürogeräten ja) – sonst IPP wählen.", 0)
			return rep
		}
		start := time.Now()
		info, err := ippGetPrinter(ctx, ippTargetFrom(c))
		if err != nil {
			rep.add("Drucker", "IPP", "fail", err.Error(), time.Since(start).Milliseconds())
			return rep
		}
		rep.add("Drucker", "Modell", "ok", firstNonEmpty(info.MakeModel, info.Name, "unbekannt"), time.Since(start).Milliseconds())
		state := map[int]string{3: "bereit", 4: "druckt gerade", 5: "angehalten"}[info.State]
		var reasons []string
		for _, rs := range info.Reasons {
			reasons = append(reasons, ippReasonText(rs))
		}
		switch {
		case info.State == 5 || !info.Accepting:
			rep.add("Drucker", "Zustand", "fail", strings.TrimSpace(firstNonEmpty(state, "angehalten")+" "+strings.Join(reasons, ", ")), 0)
		case len(reasons) > 0:
			rep.add("Drucker", "Zustand", "warn", firstNonEmpty(state, "?")+" – "+strings.Join(reasons, ", "), 0)
		default:
			rep.add("Drucker", "Zustand", "ok", firstNonEmpty(state, "bereit"), 0)
		}
		pdf := false
		for _, f := range info.Formats {
			if f == "application/pdf" {
				pdf = true
			}
		}
		if pdf {
			rep.add("Drucker", "PDF-Druck", "ok", "druckt PDF direkt", 0)
		} else if len(info.Formats) > 0 {
			rep.add("Drucker", "PDF-Druck", "fail", "kann kein PDF direkt drucken ("+strings.Join(info.Formats, ", ")+") – Druck über den Browser nutzen", 0)
		}
		if len(info.Markers) > 0 {
			st := "ok"
			for _, m := range info.Markers {
				if f := strings.Fields(m); len(f) >= 2 {
					if n, _ := strconv.Atoi(f[len(f)-2]); n <= 10 {
						st = "warn"
					}
				}
			}
			rep.add("Drucker", "Toner/Tinte", st, strings.Join(info.Markers, " · "), 0)
		}
		if info.QueuedJobs > 0 {
			rep.add("Drucker", "Warteschlange", "info", fmt.Sprintf("%d Auftrag/Aufträge in der Warteschlange", info.QueuedJobs), 0)
		}
	}
	return rep
}

func (h *Handler) PrinterCheckWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManagePrinters(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	id := chi.URLParam(r, "id")
	p, err := h.loadPrinter(ctx, id)
	if err != nil {
		printersBack(w, r, "", err.Error())
		return
	}
	r.Form = url.Values{"anchor": {"p-" + id}}
	if p.Kind == "dymo" {
		printersBack(w, r, "", "Dymo-Drucker prüft der Browser am PC mit DYMO Connect – dort „Prüfen“ verwenden")
		return
	}
	rep := runPrinterChecks(ctx, p)
	h.storePrinterCheck(ctx, id, rep)
	msg := fmt.Sprintf("„%s“ geprüft: %d in Ordnung, %d Hinweis(e), %d Fehler", p.Name, rep.OK, rep.Warn, rep.Fail)
	printersBack(w, r, msg, "")
}

// PrinterClientCheckWeb: POST /admin/printers/{id}/client-check – Ergebnis
// der Dymo-Pruefung aus dem Browser speichern.
func (h *Handler) PrinterClientCheckWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManagePrinters(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	var in struct {
		Items []checkResult `json:"items"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || len(in.Items) > 30 {
		writeGlobalBoardError(w, http.StatusBadRequest, "Ungültige Eingabe")
		return
	}
	rep := &checkReport{At: time.Now().Format("02.01.2006 15:04:05")}
	for _, it := range in.Items {
		switch it.Status {
		case "ok", "warn", "fail", "info":
		default:
			continue
		}
		rep.add(trimRunes(it.Group, 40), trimRunes(it.Name, 80), it.Status, trimRunes(it.Detail+" (geprüft am PC von "+strings.TrimSpace(getUser(r).FirstName+" "+getUser(r).LastName)+")", 400), 0)
	}
	h.storePrinterCheck(r.Context(), chi.URLParam(r, "id"), rep)
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func trimRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// ── Testdruck ────────────────────────────────────────────────

func (h *Handler) PrinterTestWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManagePrinters(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	id := chi.URLParam(r, "id")
	p, err := h.loadPrinter(ctx, id)
	if err != nil {
		printersBack(w, r, "", err.Error())
		return
	}
	r.Form = url.Values{"anchor": {"p-" + id}}
	link := h.publicBaseURL(r) + "/admin/printers"
	switch p.Kind {
	case "zebra":
		err = rawSend(ctx, p.Config["host"], parsePort(p.Config["port"], "9100"), []byte(zplTestLabel(p.Name, link, zebraSettingsFrom(p.Config))))
	case "network":
		var pdf []byte
		if pdf, err = networkTestPDF(p.Name, p.Location, link, p.Config); err == nil {
			err = sendToNetworkPrinter(ctx, p.Config, "PDH Testseite", pdf, 1)
		}
	default:
		err = errors.New("Dymo-Testdruck läuft über den Browser am PC (DYMO Connect)")
	}
	if err != nil {
		printersBack(w, r, "", "Testdruck auf „"+p.Name+"“ fehlgeschlagen: "+err.Error())
		return
	}
	_, _ = h.db.Exec(ctx, `UPDATE printers SET last_print_at = NOW() WHERE id::text = $1`, id)
	printersBack(w, r, "Testdruck an „"+p.Name+"“ gesendet", "")
}

// PrinterDymoLabelWeb: GET /admin/printers/{id}/dymo-test – Testetikett als
// XML fuer DYMO Connect (der Browser schickt es an den Drucker).
func (h *Handler) PrinterDymoTestWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManagePrinters(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	p, err := h.loadPrinter(r.Context(), chi.URLParam(r, "id"))
	if err != nil || p.Kind != "dymo" {
		writeGlobalBoardError(w, http.StatusNotFound, "Dymo-Drucker nicht gefunden")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"printer_name": p.Config["printer_name"], "service_port": parsePort(p.Config["service_port"], "41951"),
		"labels": []string{dymoTestLabel(p.Name, h.publicBaseURL(r)+"/admin/printers", dymoLabelTypeByKey(p.Config["label_type"]), p.Config["paper_name"])},
	})
}

// ── Etiketten direkt drucken ─────────────────────────────────

type labelPrintOption struct {
	ID, Name, Kind, KindLabel, Location string
	IsDefault                           bool
}

// usablePrinters: aktive Drucker fuer die Etiketten-Druckansicht.
func (h *Handler) usablePrinters(ctx context.Context) []labelPrintOption {
	list, err := h.loadPrinters(ctx, true)
	if err != nil {
		return nil
	}
	var out []labelPrintOption
	for _, p := range list {
		out = append(out, labelPrintOption{p.ID, p.Name, p.Kind, p.KindLabel, p.Location, p.IsDefault})
	}
	return out
}

// PartLabelsPrintWeb: POST /inventory/labels/print – Auswahl wie die
// Druckansicht plus printer=ID. Zebra: ZPL je Etikett; Netzwerk: PDF;
// Dymo: XML zurueck an den Browser (der schickt es an DYMO Connect).
func (h *Handler) PartLabelsPrintWeb(w http.ResponseWriter, r *http.Request) {
	if (!h.hasPerm(r, "inventory.view") && !h.hasPerm(r, "inventory.edit")) || !h.hasPerm(r, "printers.use") {
		writeGlobalBoardError(w, http.StatusForbidden, "keine Berechtigung")
		return
	}
	_ = r.ParseForm()
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	p, err := h.loadPrinter(ctx, r.FormValue("printer"))
	if err != nil || !p.Enabled {
		writeGlobalBoardError(w, http.StatusBadRequest, "Bitte einen aktiven Drucker wählen")
		return
	}
	items, err := h.loadLabelItems(ctx, r.Form)
	if err != nil {
		writeGlobalBoardError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(items) == 0 {
		writeGlobalBoardError(w, http.StatusBadRequest, "Keine Etiketten für diese Auswahl")
		return
	}
	base := h.publicBaseURL(r)
	for i := range items {
		items[i].URL = base + "/inventory/" + items[i].PartID
	}
	copies := clampInt(r.FormValue("copies"), 1, 1, 50)
	showCat := r.FormValue("cat_line") != "0"
	switch p.Kind {
	case "zebra":
		z := zebraSettingsFrom(p.Config)
		var zpl strings.Builder
		for _, it := range items {
			zpl.WriteString(zplPartLabel(it, z, showCat, copies))
		}
		err = rawSend(ctx, p.Config["host"], parsePort(p.Config["port"], "9100"), []byte(zpl.String()))
	case "network":
		size := labelSizeByKey(r.FormValue("size"))
		var pdf []byte
		if pdf, err = labelsPDF(items, size, copies, clampInt(r.FormValue("start"), 0, 0, 40), showCat); err == nil {
			err = sendToNetworkPrinter(ctx, p.Config, "PDH Etiketten", pdf, 1)
		}
	case "dymo":
		t := dymoLabelTypeByKey(p.Config["label_type"])
		var labels []string
		for _, it := range items {
			x := dymoPartLabel(it, t, p.Config["paper_name"], showCat)
			for c := 0; c < copies; c++ {
				labels = append(labels, x)
			}
		}
		_, _ = h.db.Exec(ctx, `UPDATE printers SET last_print_at = NOW() WHERE id::text = $1`, p.ID)
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "dymo": true, "printer_name": p.Config["printer_name"],
			"service_port": parsePort(p.Config["service_port"], "41951"), "labels": labels})
		return
	}
	if err != nil {
		writeGlobalBoardError(w, http.StatusBadGateway, "Druck auf „"+p.Name+"“ fehlgeschlagen: "+err.Error())
		return
	}
	_, _ = h.db.Exec(ctx, `UPDATE printers SET last_print_at = NOW() WHERE id::text = $1`, p.ID)
	n := len(items) * copies
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": fmt.Sprintf("%d Etikett(en) an „%s“ gesendet", n, p.Name)})
}
