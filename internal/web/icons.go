package web

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"

	"pdh"
)

// Symbole: Tabler Icons liegen lokal unter /vendor/tabler-icons (web/static,
// eingebettet). Jedes änderbare Symbol hat einen Schlüssel („nav.tickets“,
// „record.fault“, „infra.plant“, „action.save“ …) mit Vorgabe; Admins ändern
// es unter /admin/icons, gespeichert in app_settings (KeyIcons) als JSON.
// Vorlagen holen das Symbol mit {{icon "schlüssel"}}, Go-Code mit iconClass.

const KeyIcons = "icons"

type iconDef struct {
	Key, Label, Default string
}

type iconGroup struct {
	Key, Label, Hint string
	Items            []iconDef
}

// iconGroupsStatic: alles außer der Navigation (die kommt aus navDefs).
var iconGroupsStatic = []iconGroup{
	{"record", "Vorgangsarten & Datensätze", "Arbeitsbereich, Anlagen-Infoseite, Dashboard, Verknüpfungen", []iconDef{
		{"record.ticket", "Ticket", "ti-ticket"},
		{"record.fault", "Störung", "ti-alert-triangle"},
		{"record.task", "Aufgabe", "ti-list-check"},
		{"record.maintenance_task", "Wartung", "ti-tool"},
		{"record.project", "Projekt", "ti-briefcase"},
		{"record.kvp", "KVP-Vorschlag", "ti-bulb"},
		{"record.infrastructure", "Anlage", "ti-building-factory-2"},
		{"record.part", "Ersatzteil", "ti-package"},
		{"record.storage", "Lagerplatz", "ti-building-warehouse"},
		{"record.business_partner", "Hersteller/Lieferant", "ti-building-store"},
	}},
	{"infra", "Anlagentypen", "Infrastruktur-Baum, Auswahl, Detailseite", []iconDef{
		{"infra.building", "Gebäude", "ti-building-factory"},
		{"infra.drying_chamber", "Trockenkammer", "ti-temperature"},
		{"infra.line", "Linie", "ti-route"},
		{"infra.plant", "Anlage", "ti-settings"},
		{"infra.device", "Gerät", "ti-plug"},
	}},
	{"storage", "Lagertypen", "Lagerbaum und Lagerplätze", []iconDef{
		{"storage.lagerort", "Lagerort", "ti-building-warehouse"},
		{"storage.regal", "Regal", "ti-stack-2"},
		{"storage.fach", "Fach", "ti-box"},
		{"storage.platz", "Platz", "ti-map-pin"},
	}},
	{"it", "IT-Typen", "IT-Assets", []iconDef{
		{"it.server", "Server", "ti-server"},
		{"it.network", "Netzwerk", "ti-network"},
		{"it.workstation", "Workstation", "ti-device-desktop"},
		{"it.printer", "Drucker", "ti-printer"},
		{"it.phone", "Telefon", "ti-device-mobile"},
		{"it.tablet", "Tablet", "ti-device-tablet"},
		{"it.other", "Sonstiges", "ti-box"},
	}},
	{"action", "Knöpfe", "Aktionsknöpfe in Listen, Formularen und Werkzeugleisten", []iconDef{
		{"action.new", "Neu / Hinzufügen", "ti-plus"},
		{"action.save", "Speichern", "ti-device-floppy"},
		{"action.cancel", "Abbrechen", "ti-x"},
		{"action.edit", "Bearbeiten", "ti-edit"},
		{"action.view", "Ansehen / Details", "ti-eye"},
		{"action.delete", "Löschen", "ti-trash"},
		{"action.delete-mark", "Zum Löschen vormerken", "ti-trash-x"},
		{"action.done", "Erledigen", "ti-check"},
		{"action.activate", "Aktivieren", "ti-circle-check"},
		{"action.deactivate", "Deaktivieren", "ti-circle-x"},
		{"action.back", "Zurück", "ti-arrow-left"},
		{"action.more", "Mehr", "ti-dots"},
		{"action.refresh", "Aktualisieren", "ti-refresh"},
		{"action.expand", "Aufklappen", "ti-arrows-maximize"},
		{"action.filter", "Filtern", "ti-filter"},
		{"action.analyze", "Analysieren (Copilot)", "ti-brain"},
		{"action.copy", "Kopieren", "ti-copy"},
		{"action.history", "Historie", "ti-history"},
		{"action.start", "Starten", "ti-player-play"},
		{"action.stop", "Stoppen", "ti-player-pause"},
		{"action.book", "Buchen", "ti-arrows-exchange"},
		{"action.send", "Senden", "ti-send"},
	}},
}

// iconGroups: alle Gruppen samt Navigation (Vorgabe = Symbol aus navDefs).
func iconGroups() []iconGroup {
	nav := iconGroup{Key: "nav", Label: "Navigation", Hint: "Menüpunkte links"}
	for _, d := range navDefs {
		nav.Items = append(nav.Items, iconDef{"nav." + d.Key, d.Label, d.Icon})
	}
	return append([]iconGroup{nav}, iconGroupsStatic...)
}

var (
	iconDefaultsOnce sync.Once
	iconDefaults     map[string]string
	iconMu           sync.RWMutex
	iconOverrides    = map[string]string{}
)

func iconDefault(key string) string {
	iconDefaultsOnce.Do(func() {
		iconDefaults = map[string]string{}
		for _, g := range iconGroups() {
			for _, it := range g.Items {
				iconDefaults[it.Key] = it.Default
			}
		}
	})
	return iconDefaults[key]
}

// iconClass: Tabler-Klasse zum Schlüssel (Änderung des Admins, sonst Vorgabe).
func iconClass(key string) string {
	iconMu.RLock()
	v := iconOverrides[key]
	iconMu.RUnlock()
	if v != "" {
		return v
	}
	if d := iconDefault(key); d != "" {
		return d
	}
	return "ti-point"
}

// loadIconOverrides liest die Änderungen aus app_settings (beim Start und nach dem Speichern).
func (h *Handler) loadIconOverrides(ctx context.Context) {
	if h.db == nil {
		return
	}
	var raw string
	m := map[string]string{}
	if h.db.QueryRow(ctx, `SELECT value FROM app_settings WHERE key = $1`, KeyIcons).Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &m)
	}
	clean := map[string]string{}
	for k, v := range m {
		if iconDefault(k) != "" && iconExists(v) {
			clean[k] = v
		}
	}
	iconMu.Lock()
	iconOverrides = clean
	iconMu.Unlock()
}

// ── lokale Bibliothek ────────────────────────────────────────

var (
	iconNamesOnce sync.Once
	iconNames     []string
	iconNameSet   map[string]bool
)

var tablerClassRe = regexp.MustCompile(`\.(ti-[a-z0-9-]+):before`)

func loadIconNames() {
	iconNamesOnce.Do(func() {
		iconNameSet = map[string]bool{}
		css, err := fs.ReadFile(pdh.Static, "web/static/vendor/tabler-icons/tabler-icons.min.css")
		if err != nil {
			return
		}
		for _, m := range tablerClassRe.FindAllStringSubmatch(string(css), -1) {
			if !iconNameSet[m[1]] {
				iconNameSet[m[1]] = true
				iconNames = append(iconNames, m[1])
			}
		}
		sort.Strings(iconNames)
	})
}

func iconExists(class string) bool {
	loadIconNames()
	return iconNameSet[class]
}

// ── Verwaltung /admin/icons ──────────────────────────────────

type iconRow struct {
	iconDef
	Current string
	Changed bool
}

type iconRowGroup struct {
	Key, Label, Hint string
	Rows             []iconRow
}

type IconsPageData struct {
	BaseData
	Groups  []iconRowGroup
	Changed int
	Message string
	Error   string
}

// IconsPage: GET /admin/icons
func (h *Handler) IconsPage(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	d := IconsPageData{BaseData: h.baseData(r, "icons", "Symbole", "Symbole"), Message: r.URL.Query().Get("msg"), Error: r.URL.Query().Get("err")}
	for _, g := range iconGroups() {
		rg := iconRowGroup{Key: g.Key, Label: g.Label, Hint: g.Hint}
		for _, it := range g.Items {
			cur := iconClass(it.Key)
			row := iconRow{iconDef: it, Current: cur, Changed: cur != it.Default}
			if row.Changed {
				d.Changed++
			}
			rg.Rows = append(rg.Rows, row)
		}
		d.Groups = append(d.Groups, rg)
	}
	h.render(w, "icons", d)
}

// IconNamesWeb: GET /admin/icons/names – alle Symbole der lokalen Bibliothek (Auswahl).
func (h *Handler) IconNamesWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	loadIconNames()
	w.Header().Set("Cache-Control", "private, max-age=3600")
	writeJSON(w, http.StatusOK, iconNames)
}

// IconsSaveWeb: POST /admin/icons – Formularfelder icon.<schlüssel>=ti-…; leer oder
// gleich der Vorgabe = Vorgabe. reset=1 setzt alles zurück.
func (h *Handler) IconsSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	out := map[string]string{}
	if r.FormValue("reset") != "1" {
		for k, vals := range r.Form {
			key, ok := strings.CutPrefix(k, "icon.")
			if !ok || iconDefault(key) == "" || len(vals) == 0 {
				continue
			}
			v := strings.TrimSpace(vals[0])
			if v != "" && !strings.HasPrefix(v, "ti-") {
				v = "ti-" + v
			}
			if v == "" || v == iconDefault(key) {
				continue
			}
			if !iconExists(v) {
				http.Redirect(w, r, "/admin/icons?err="+url.QueryEscape("Symbol „"+v+"“ gibt es in der Bibliothek nicht"), http.StatusSeeOther)
				return
			}
			out[key] = v
		}
	}
	b, _ := json.Marshal(out)
	if err := h.setUpdateSetting(r.Context(), KeyIcons, string(b)); err != nil {
		http.Error(w, "Symbole konnten nicht gespeichert werden", http.StatusInternalServerError)
		return
	}
	h.loadIconOverrides(r.Context())
	msg := "Symbole gespeichert"
	if len(out) == 0 {
		msg = "Alle Symbole auf die Vorgabe gesetzt"
	}
	http.Redirect(w, r, "/admin/icons?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

// iconMapAll: Schlüssel → Klasse für Skripte (Infra-Auswahl, Copilot-Vorschläge …).
func iconMapAll() map[string]string {
	out := map[string]string{}
	for _, g := range iconGroups() {
		for _, it := range g.Items {
			out[it.Key] = iconClass(it.Key)
		}
	}
	return out
}

// itTypeIcons: Symbol je IT-Asset-Typ (it.<typ>).
func itTypeIcons() map[string]string {
	out := map[string]string{}
	for _, t := range []string{"server", "network", "workstation", "printer", "phone", "tablet", "other"} {
		out[t] = iconClass("it." + t)
	}
	return out
}

// recordIcon: Symbol einer Vorgangsart bzw. eines Datensatz-Moduls
// ("maintenance" = Wartungsauftrag); unbekannte Module behalten ihr festes Symbol.
func recordIcon(module string) string {
	if module == "maintenance" {
		module = "maintenance_task"
	}
	if iconDefault("record."+module) != "" {
		return iconClass("record." + module)
	}
	return recordModules[module].Icon
}

// linkRecordKey: Datensatz-Modul zu einem Verweis (/faults/…, /inventory/… …).
func linkRecordKey(url string) string {
	for _, p := range []struct{ prefix, key string }{
		{"/faults/", "fault"}, {"/tickets/", "ticket"}, {"/tasks/", "task"}, {"/projects/", "project"},
		{"/maintenance/tasks/", "maintenance_task"}, {"/kvp/", "kvp"}, {"/infrastructure/", "infrastructure"},
		{"/inventory/", "part"}, {"/storage/", "storage"}, {"/directory/", "business_partner"},
	} {
		if strings.HasPrefix(url, p.prefix) {
			return p.key
		}
	}
	return ""
}

// LoadIconOverrides: beim Serverstart die Symbol-Änderungen laden.
func (h *Handler) LoadIconOverrides(ctx context.Context) { h.loadIconOverrides(ctx) }
