package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"pdh/internal/modules/extsync"
)

// Abgleich mit Fremdsystemen – Aktionen der Schritte Quelle, Ziel und
// Zuordnungen. Die Handler lesen das Formular, lassen extsync prüfen
// (CheckSource, CheckMaps, ValidateFilter) und speichern über den Store
// (SaveProfile, ReplaceMaps). Namen von Tabellen und Spalten gelten nur, wenn
// sie im gelesenen Aufbau der Fremddatenbank vorkommen.

func syncUserMessage(err error) string {
	switch {
	case errors.Is(err, extsync.ErrSourceLocked):
		return extsync.ErrSourceLocked.Error()
	case errors.Is(err, extsync.ErrNotFound):
		return "Profil nicht gefunden"
	}
	return "Speichern fehlgeschlagen"
}

// syncDeptAllowed: darf die Person das Profil in diese Abteilung legen
// ("" = alle Abteilungen)?
func (h *Handler) syncDeptAllowed(r *http.Request, dept string) bool {
	opts, allowGlobal := h.syncDeptOptions(r, permSyncEdit)
	if dept == "" {
		return allowGlobal
	}
	return syncOptionLabel(opts, dept) != ""
}

// SyncNewWeb: POST /sync/new-web – neues Profil (Entwurf) anlegen.
func (h *Handler) SyncNewWeb(w http.ResponseWriter, r *http.Request) {
	if !h.syncCan(r, permSyncEdit, "") {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
		return
	}
	fail := func(msg string) { http.Redirect(w, r, syncBack("", "", "", msg), http.StatusSeeOther) }
	name := strings.TrimSpace(r.FormValue("name"))
	if l := len([]rune(name)); l < 2 || l > 150 {
		fail("Name muss 2 bis 150 Zeichen lang sein")
		return
	}
	connID := r.FormValue("connection_id")
	if syncOptionLabel(h.syncConnOptions(r.Context()), connID) == "" {
		fail("Bitte eine SQL-Server-Verbindung wählen")
		return
	}
	target := r.FormValue("target_type")
	if syncTargetLabels[target] == "" {
		fail("Bitte Ticket oder Störmeldung wählen")
		return
	}
	dept := r.FormValue("department_id")
	if !h.syncDeptAllowed(r, dept) {
		fail("Für diese Abteilung fehlt dir das Recht")
		return
	}
	p := &extsync.Profile{Name: name, ConnectionID: connID, TargetType: target}
	if dept != "" {
		p.DepartmentID = &dept
	}
	if err := h.syncStore().SaveProfile(r.Context(), p, getUser(r).ID); err != nil {
		if strings.Contains(err.Error(), "sync_profiles_name") || strings.Contains(err.Error(), "duplicate key") {
			fail("Ein Profil mit diesem Namen gibt es schon")
			return
		}
		fail(syncUserMessage(err))
		return
	}
	http.Redirect(w, r, syncBack(p.ID, "source", "Profil angelegt – jetzt die Quelle wählen", ""), http.StatusSeeOther)
}

// SyncSourceWeb: POST /sync/{id}/source-web – Verbindung, Tabelle, Schlüssel,
// Änderungserkennung, Anlagedatum und Zeitzone speichern.
func (h *Handler) SyncSourceWeb(w http.ResponseWriter, r *http.Request) {
	p, ok := h.syncProfile(w, r, permSyncEdit)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
		return
	}
	fail := func(msg string) { http.Redirect(w, r, syncBack(p.ID, "source", "", msg), http.StatusSeeOther) }
	if name := strings.TrimSpace(r.FormValue("name")); name != "" {
		if l := len([]rune(name)); l < 2 || l > 150 {
			fail("Name muss 2 bis 150 Zeichen lang sein")
			return
		}
		p.Name = name
	}
	if c := r.FormValue("connection_id"); c != "" && c != p.ConnectionID {
		if syncOptionLabel(h.syncConnOptions(r.Context()), c) == "" {
			fail("Bitte eine SQL-Server-Verbindung wählen")
			return
		}
		p.ConnectionID = c
	}
	key := r.FormValue("table")
	if key == "" {
		fail("Bitte eine Tabelle wählen")
		return
	}
	t, d, _, closeFn, err := h.syncDescribeChosen(r.Context(), p.ConnectionID, key)
	defer closeFn()
	if err != nil {
		fail(err.Error())
		return
	}
	p.SourceSchema, p.SourceTable = t.Schema, t.Name
	p.KeyColumns = nil
	for _, k := range r.Form["key"] {
		if k = strings.TrimSpace(k); k != "" {
			p.KeyColumns = append(p.KeyColumns, k)
		}
	}
	kind, col, _ := strings.Cut(r.FormValue("change"), ":")
	if kind == "" {
		kind = "none"
	}
	p.ChangeKind, p.ChangeColumn = kind, col
	p.CreatedColumn = r.FormValue("created")
	p.TimeZone = strings.TrimSpace(r.FormValue("time_zone"))
	if err := extsync.CheckSource(d, t, p); err != nil {
		fail(err.Error())
		return
	}
	if err := h.syncStore().SaveProfile(r.Context(), p, getUser(r).ID); err != nil {
		fail(syncUserMessage(err))
		return
	}
	http.Redirect(w, r, syncBack(p.ID, "source", "Quelle gespeichert", ""), http.StatusSeeOther)
}

func syncFormInt(r *http.Request, name string, def, lo, hi int) int {
	n, err := strconv.Atoi(strings.TrimSpace(r.FormValue(name)))
	if err != nil {
		return def
	}
	return min(max(n, lo), hi)
}

// SyncTargetWeb: POST /sync/{id}/target-web – Zielart, Abteilung, Vorgabewerte,
// Filter, Zeitfenster und Obergrenze speichern.
func (h *Handler) SyncTargetWeb(w http.ResponseWriter, r *http.Request) {
	p, ok := h.syncProfile(w, r, permSyncEdit)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	fail := func(msg string) { http.Redirect(w, r, syncBack(p.ID, "target", "", msg), http.StatusSeeOther) }
	oldTarget := p.TargetType
	if t := r.FormValue("target_type"); t != "" && t != p.TargetType {
		if syncTargetLabels[t] == "" {
			fail("Bitte Ticket oder Störmeldung wählen")
			return
		}
		if h.syncLocked(ctx, p.ID) {
			fail("Das Ziel kann nicht mehr geändert werden, weil bereits Meldungen verknüpft sind – bitte ein neues Profil anlegen")
			return
		}
		p.TargetType = t
	}
	dept := r.FormValue("department_id")
	cur := ""
	if p.DepartmentID != nil {
		cur = *p.DepartmentID
	}
	if dept != cur {
		if !h.syncDeptAllowed(r, dept) {
			fail("Für diese Abteilung fehlt dir das Recht")
			return
		}
		p.DepartmentID = nil
		if dept != "" {
			p.DepartmentID = &dept
		}
	}
	defaults := map[string]string{}
	for _, f := range extsync.Fields(p.TargetType) {
		v := strings.TrimSpace(r.FormValue("def_" + f.Key))
		if v == "" {
			continue
		}
		if f.Kind == "enum" {
			c, ok := extsync.EnumValue(f, v)
			if !ok {
				fail("Vorgabewert für " + f.Label + " ist ungültig")
				return
			}
			v = c
		}
		if f.MaxLen > 0 && len([]rune(v)) > f.MaxLen {
			fail("Vorgabewert für " + f.Label + " ist zu lang (höchstens " + strconv.Itoa(f.MaxLen) + " Zeichen)")
			return
		}
		defaults[f.Key] = v
	}
	p.Defaults = defaults
	p.FilterSQL = strings.TrimSpace(r.FormValue("filter_sql"))
	if err := extsync.ValidateFilter(p.FilterSQL); err != nil {
		fail("Filter: " + err.Error())
		return
	}
	p.WindowDays = syncFormInt(r, "window_days", 0, 0, 3650)
	p.MaxNew = syncFormInt(r, "max_new", extsync.DefaultMaxNew, 1, 10000)
	store := h.syncStore()
	if err := store.SaveProfile(ctx, p, getUser(r).ID); err != nil {
		fail(syncUserMessage(err))
		return
	}
	// Andere Zielart: Zuordnungen auf Felder, die es dort nicht gibt (Priorität ↔
	// Schweregrad), entfallen.
	if p.TargetType != oldTarget {
		valid := map[string]bool{}
		for _, f := range extsync.Fields(p.TargetType) {
			valid[f.Key] = true
		}
		maps, err := store.ListMaps(ctx, p.ID)
		if err == nil {
			keep := maps[:0]
			for _, m := range maps {
				if valid[m.PDHField] {
					keep = append(keep, m)
				}
			}
			if len(keep) != len(maps) {
				_ = store.ReplaceMaps(ctx, p.ID, keep)
			}
		}
	}
	http.Redirect(w, r, syncBack(p.ID, "target", "Ziel gespeichert", ""), http.StatusSeeOther)
}

// syncMapsFromForm liest die Zuordnungstabelle: je PDH-Feld Spalte, Richtung,
// fester Wert und Übersetzungspaare. Felder ohne Spalte und ohne festen Wert
// sind nicht zugeordnet.
func syncMapsFromForm(r *http.Request, target string) []extsync.FieldMap {
	var maps []extsync.FieldMap
	for i, f := range extsync.Fields(target) {
		col := strings.TrimSpace(r.FormValue("col_" + f.Key))
		fixed := strings.TrimSpace(r.FormValue("fixed_" + f.Key))
		if col == "" && fixed == "" {
			continue
		}
		m := extsync.FieldMap{ForeignColumn: col, PDHField: f.Key, Direction: r.FormValue("dir_" + f.Key), Position: i}
		if m.Direction == "" {
			m.Direction = extsync.DirIn
		}
		if fixed != "" {
			m.FixedValue = &fixed
		}
		foreign, pdh := r.Form["trf_"+f.Key], r.Form["trp_"+f.Key]
		for j := range foreign {
			fv := strings.TrimSpace(foreign[j])
			if fv == "" || j >= len(pdh) || strings.TrimSpace(pdh[j]) == "" {
				continue
			}
			if m.Translate == nil {
				m.Translate = map[string]string{}
			}
			m.Translate[fv] = strings.TrimSpace(pdh[j])
		}
		maps = append(maps, m)
	}
	return maps
}

// SyncMapsWeb: POST /sync/{id}/maps-web – Zuordnungen und Kommentartabelle speichern.
func (h *Handler) SyncMapsWeb(w http.ResponseWriter, r *http.Request) {
	p, ok := h.syncProfile(w, r, permSyncEdit)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	fail := func(msg string) { http.Redirect(w, r, syncBack(p.ID, "maps", "", msg), http.StatusSeeOther) }
	if p.SourceTable == "" {
		fail("Bitte zuerst im Schritt Quelle eine Tabelle wählen")
		return
	}
	rd, d, cfg, closeFn, err := h.syncOpenReader(ctx, p.ConnectionID)
	defer closeFn()
	if err != nil {
		fail(err.Error())
		return
	}
	t, err := rd.Describe(ctx, p.SourceSchema, p.SourceTable)
	if err != nil {
		fail("Tabelle " + syncTableKey(p.SourceSchema, p.SourceTable) + " nicht lesbar: " + err.Error())
		return
	}
	maps := syncMapsFromForm(r, p.TargetType)
	var comments *extsync.TableInfo
	p.CommentTable = nil
	if key := r.FormValue("ct_table"); key != "" {
		tables, terr := rd.Tables(ctx)
		if terr != nil {
			fail("Tabellen nicht lesbar: " + terr.Error())
			return
		}
		schema, table, found := syncFindTable(tables, key)
		if !found {
			fail("Kommentartabelle ‚" + key + "‘ gibt es in der Datenbank nicht")
			return
		}
		p.CommentTable = &extsync.CommentTable{Schema: schema, Table: table,
			FKColumn: strings.TrimSpace(r.FormValue("ct_fk")), TextColumn: strings.TrimSpace(r.FormValue("ct_text")),
			AuthorColumn: strings.TrimSpace(r.FormValue("ct_author")), TimeColumn: strings.TrimSpace(r.FormValue("ct_time"))}
		comments, _ = rd.Describe(ctx, schema, table)
	}
	if errs := extsync.CheckMaps(d, t, comments, p, maps, cfg["allow_write"] == "true"); len(errs) > 0 {
		msgs := make([]string, 0, len(errs))
		for _, e := range errs {
			msgs = append(msgs, e.Message)
		}
		fail(strings.Join(msgs, " · "))
		return
	}
	store := h.syncStore()
	if err := store.SaveProfile(ctx, p, getUser(r).ID); err != nil {
		fail(syncUserMessage(err))
		return
	}
	if err := store.ReplaceMaps(ctx, p.ID, maps); err != nil {
		fail(syncUserMessage(err))
		return
	}
	notice := "Zuordnungen gespeichert"
	if missing := extsync.MissingRequired(p, maps); len(missing) > 0 {
		notice += " – es fehlt noch ein Pflichtfeld: " + missing[0].Label
	}
	http.Redirect(w, r, syncBack(p.ID, "maps", notice, ""), http.StatusSeeOther)
}

// SyncDeleteWeb: POST /sync/{id}/delete-web – Profil samt Zuordnungen,
// Verknüpfungen, Warteliste und Läufen löschen (nur Bereichsadministrator).
// Tickets und Störmeldungen im PDH und alle Daten im Fremdsystem bleiben.
func (h *Handler) SyncDeleteWeb(w http.ResponseWriter, r *http.Request) {
	p, ok := h.syncProfile(w, r, permSyncAdmin)
	if !ok {
		return
	}
	if err := h.syncStore().DeleteProfile(r.Context(), p.ID); err != nil {
		http.Redirect(w, r, syncBack(p.ID, "", "", "Profil konnte nicht gelöscht werden"), http.StatusSeeOther)
		return
	}
	componentLog("abgleich").Info().Str("profil", p.Name).Str("benutzer", getUser(r).ID).Msg("profil gelöscht")
	http.Redirect(w, r, syncBack("", "", "Profil „"+p.Name+"“ gelöscht", ""), http.StatusSeeOther)
}
