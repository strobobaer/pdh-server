package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Linke Navigation: feste Vorgabe-Gruppen, die jede Person umbauen kann
// (Gruppen umbenennen, anlegen, loeschen, Eintraege verschieben). Die eigene
// Anordnung liegt in users.nav_layout (migrations/089). Neue Eintraege, die in
// einer gespeicherten Anordnung noch fehlen, erscheinen in ihrer Vorgabe-Gruppe.

type navItem struct {
	Key, Href, Icon, Label, Page string
	External                     bool
}

type navGroup struct {
	Key, Label string // Label "" = ohne Ueberschrift (oberster Block)
	Items      []navItem
}

// navTopKey: Block ohne Ueberschrift ganz oben.
const navTopKey = "top"

type navDef struct {
	navItem
	group string
	show  func(b *BaseData) bool
}

func always(*BaseData) bool { return true }

// navDefs: alle Eintraege in Vorgabe-Reihenfolge.
var navDefs = []navDef{
	{navItem{"dashboard", "/", "ti-dashboard", "Dashboard", "dashboard", false}, navTopKey, always},
	{navItem{"leitstand", "/global/", "ti-layout-dashboard", "Leitstand", "", true}, navTopKey, always},
	{navItem{"assign", "/assignments/new", "ti-square-rounded-plus", "Neu anlegen", "assignments-new", false}, navTopKey, always},
	{navItem{"assignments", "/assignments", "ti-inbox", "Zuweisung", "assignments", false}, navTopKey, func(b *BaseData) bool { return b.IsBroker || b.CanManageRoles }},

	{navItem{"chat", "/chat", "ti-messages", "Chat", "chat", false}, "work", func(b *BaseData) bool { return b.CanChat }},
	{navItem{"tickets", "/tickets", "ti-ticket", "Tickets", "tickets", false}, "work", always},
	{navItem{"faults", "/faults", "ti-alert-triangle", "Störungen", "faults", false}, "work", always},
	{navItem{"maintenance", "/maintenance", "ti-tool", "Wartung", "maintenance", false}, "work", always},
	{navItem{"tasks", "/tasks", "ti-list-check", "Aufgaben", "tasks", false}, "work", always},
	{navItem{"projects", "/projects", "ti-timeline", "Projekte", "projects", false}, "work", always},
	{navItem{"kvp", "/kvp", "ti-bulb", "KVP", "kvp", false}, "work", always},
	{navItem{"time", "/time", "ti-clock", "Zeiterfassung", "time", false}, "work", always},
	{navItem{"shifts", "/shifts", "ti-calendar-time", "Schichtplan", "shifts", false}, "work", always},

	{navItem{"inventory", "/inventory", "ti-package", "Ersatzteile", "inventory", false}, "material", always},
	{navItem{"storage", "/storage", "ti-building-warehouse", "Lager", "storage", false}, "material", always},
	{navItem{"infrastructure", "/infrastructure", "ti-hierarchy-2", "Infrastruktur", "infrastructure", false}, "material", always},
	{navItem{"it", "/it", "ti-server-2", "IT-Assets", "it", false}, "material", always},
	{navItem{"directory", "/directory", "ti-building-factory-2", "Hersteller & Lieferanten", "directory", false}, "material", always},

	{navItem{"users", "/users", "ti-users", "Benutzer", "users", false}, "people", func(b *BaseData) bool { return b.CanManageUsers }},
	{navItem{"trainings", "/trainings", "ti-school", "Schulungen", "trainings", false}, "people", always},
	{navItem{"orgunits", "/admin/org-units", "ti-users-group", "Abteilungen & Gruppen", "org_units", false}, "people", func(b *BaseData) bool { return b.CanManageUsers }},
	{navItem{"orgchart", "/admin/orgchart", "ti-sitemap", "Organigramm", "orgchart", false}, "people", func(b *BaseData) bool { return b.CanManageRoles }},

	{navItem{"roles", "/admin/roles", "ti-shield-lock", "Rollen & Rechte", "roles", false}, "admin", func(b *BaseData) bool { return b.CanManageRoles }},
	{navItem{"core", "/core/settings", "ti-settings", "Core-Einstellungen", "core-settings", false}, "admin", func(b *BaseData) bool { return b.CanManageRoles }},
	{navItem{"icons", "/admin/icons", "ti-icons", "Symbole", "icons", false}, "admin", func(b *BaseData) bool { return b.CanManageRoles }},
	{navItem{"categories", "/categories", "ti-hash", "Kategorien", "categories", false}, "admin", always},
	{navItem{"serverconfig", "/admin/server-config", "ti-adjustments-cog", "Server-Einstellungen", "server-config", false}, "admin", func(b *BaseData) bool { return b.CanServerConfig }},
	{navItem{"backup", "/admin/backup", "ti-database-export", "Datensicherung", "backup", false}, "admin", func(b *BaseData) bool { return b.CanBackup }},
	{navItem{"cleanup", "/admin/cleanup", "ti-trash-x", "Bereinigung", "cleanup", false}, "admin", func(b *BaseData) bool { return b.CanCleanup }},
	{navItem{"printers", "/admin/printers", "ti-printer", "Drucker", "printers", false}, "admin", func(b *BaseData) bool { return b.CanPrinters }},

	{navItem{"import", "/import", "ti-database-import", "Import", "import", false}, "data", func(b *BaseData) bool { return b.CanImport }},
	{navItem{"export", "/export", "ti-database-export", "Export", "export", false}, "data", func(b *BaseData) bool { return b.CanExport }},

	{navItem{"help", "/help", "ti-book", "Handbuch", "help", false}, "links", always},
	{navItem{"nextcloud", "https://cloud.strobl-home.net", "ti-cloud", "Nextcloud", "", true}, "links", always},
}

// navDefaultGroups: Vorgabe-Gruppen (Schluessel, deutscher Name).
var navDefaultGroups = []struct{ Key, Label string }{
	{navTopKey, ""},
	{"work", "Instandhaltung"},
	{"material", "Material & Anlagen"},
	{"people", "Personal"},
	{"admin", "Verwaltung"},
	{"data", "Daten"},
	{"links", "Hilfe & Links"},
}

type navLayoutGroup struct {
	Key   string   `json:"key"`
	Label string   `json:"label"`
	Items []string `json:"items"`
}

func navDefByKey(key string) (navDef, bool) {
	for _, d := range navDefs {
		if d.Key == key {
			return d, true
		}
	}
	return navDef{}, false
}

func defaultNavLayout() []navLayoutGroup {
	var out []navLayoutGroup
	for _, g := range navDefaultGroups {
		lg := navLayoutGroup{Key: g.Key, Label: g.Label}
		for _, d := range navDefs {
			if d.group == g.Key {
				lg.Items = append(lg.Items, d.Key)
			}
		}
		out = append(out, lg)
	}
	return out
}

// sanitizeNavLayout prueft eine gespeicherte/gesendete Anordnung: bekannte
// Eintraege, jeder hoechstens einmal, begrenzte Anzahl und Laenge.
func sanitizeNavLayout(in []navLayoutGroup) ([]navLayoutGroup, error) {
	if len(in) > 30 {
		return nil, fmt.Errorf("höchstens 30 Gruppen")
	}
	seenItem, seenGroup := map[string]bool{}, map[string]bool{}
	var out []navLayoutGroup
	for i, g := range in {
		key := strings.TrimSpace(g.Key)
		if key == "" || len(key) > 40 || seenGroup[key] {
			key = fmt.Sprintf("g%d%d", time.Now().UnixNano()%1e8, i)
		}
		seenGroup[key] = true
		label := strings.TrimSpace(g.Label)
		if r := []rune(label); len(r) > 40 {
			label = string(r[:40])
		}
		if key == navTopKey {
			label = ""
		}
		og := navLayoutGroup{Key: key, Label: label}
		for _, it := range g.Items {
			if _, ok := navDefByKey(it); ok && !seenItem[it] {
				seenItem[it] = true
				og.Items = append(og.Items, it)
			}
		}
		out = append(out, og)
	}
	return out, nil
}

func (h *Handler) userNavLayout(ctx context.Context, uid string) []navLayoutGroup {
	if h.db == nil || uid == "" {
		return nil
	}
	var raw []byte
	if row := userRowFrom(ctx, uid); row != nil {
		raw = row.navLayout
	} else if h.db.QueryRow(ctx, `SELECT nav_layout FROM users WHERE id = $1::uuid`, uid).Scan(&raw) != nil {
		return nil
	}
	if len(raw) == 0 {
		return nil
	}
	var l []navLayoutGroup
	if json.Unmarshal(raw, &l) != nil {
		return nil
	}
	l, _ = sanitizeNavLayout(l)
	return l
}

// buildNav: sichtbare Gruppen fuer eine Person in ihrer Sprache.
func buildNav(b *BaseData, layout []navLayoutGroup) []navGroup {
	custom := layout != nil
	if !custom {
		layout = defaultNavLayout()
	}
	placed := map[string]bool{}
	for _, g := range layout {
		for _, it := range g.Items {
			placed[it] = true
		}
	}
	// Eintraege, die in einer eigenen Anordnung fehlen (z. B. neu), in ihre Vorgabe-Gruppe
	if custom {
		for _, d := range navDefs {
			if placed[d.Key] {
				continue
			}
			idx := -1
			for i, g := range layout {
				if g.Key == d.group {
					idx = i
				}
			}
			if idx < 0 {
				label := ""
				for _, dg := range navDefaultGroups {
					if dg.Key == d.group {
						label = dg.Label
					}
				}
				layout = append(layout, navLayoutGroup{Key: d.group, Label: label})
				idx = len(layout) - 1
			}
			layout[idx].Items = append(layout[idx].Items, d.Key)
		}
	}
	defaultLabel := map[string]string{}
	for _, dg := range navDefaultGroups {
		defaultLabel[dg.Key] = dg.Label
	}
	var out []navGroup
	for _, g := range layout {
		label := g.Label
		if label != "" && label == defaultLabel[g.Key] {
			label = tr(b.Lang, label) // Vorgabe-Namen uebersetzen, eigene Namen nicht
		}
		ng := navGroup{Key: g.Key, Label: label}
		for _, key := range g.Items {
			d, ok := navDefByKey(key)
			if !ok || !d.show(b) {
				continue
			}
			it := d.navItem
			it.Icon = iconClass("nav." + key) // änderbar unter /admin/icons
			it.Label = tr(b.Lang, it.Label)
			if key == "trainings" && !b.CanTrainings {
				it.Label = tr(b.Lang, "Meine Schulungen")
			}
			ng.Items = append(ng.Items, it)
		}
		// leere Gruppen nur zeigen, wenn sie selbst angelegt sind (zum Befuellen)
		if len(ng.Items) > 0 || (custom && g.Key != navTopKey && !isDefaultNavGroup(g.Key)) {
			out = append(out, ng)
		}
	}
	return out
}

func isDefaultNavGroup(key string) bool {
	for _, dg := range navDefaultGroups {
		if dg.Key == key {
			return true
		}
	}
	return false
}

// NavLayoutSaveWeb: POST /account/nav-layout (JSON {"groups": [...]} oder {"reset": true}).
func (h *Handler) NavLayoutSaveWeb(w http.ResponseWriter, r *http.Request) {
	uid := getUser(r).ID
	if uid == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "error": "nicht angemeldet"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	var req struct {
		Reset  bool             `json:"reset"`
		Groups []navLayoutGroup `json:"groups"`
	}
	if err == nil {
		err = json.Unmarshal(body, &req)
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "ungültige Daten"})
		return
	}
	if req.Reset {
		_, err = h.db.Exec(r.Context(), `UPDATE users SET nav_layout = NULL WHERE id = $1::uuid`, uid)
	} else {
		var l []navLayoutGroup
		if l, err = sanitizeNavLayout(req.Groups); err == nil {
			data, _ := json.Marshal(l)
			_, err = h.db.Exec(r.Context(), `UPDATE users SET nav_layout = $1::jsonb WHERE id = $2::uuid`, string(data), uid)
		}
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}
