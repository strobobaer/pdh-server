package web

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"pdh/internal/core/users"
)

// Rechte auf Abteilungsebene und Bereichsadministratoren im Web
// (Grundlage: internal/core/rbac/scope.go, Migration 147).
//
//   - recordDepartments: Abteilung(en) eines Datensatzes – über seine Anlage
//     (infrastructure_department) und die Abteilung der zugewiesenen Gruppe.
//   - hasPermRec: Recht für genau diesen Datensatz (Abteilung beachtet).
//   - PermissionGateMiddleware: endgültiges Löschen nur für den
//     Bereichsadministrator, Löschvormerkung über die Schnittstelle nur mit
//     Bearbeiten-Recht des Bereichs.

// recordDeptSource: Tabelle eines Datensatz-Typs und ob er eine Gruppe hat.
var recordDeptSource = map[string]struct {
	table string
	group bool
}{
	"ticket": {"tickets", true}, "fault": {"faults", true}, "maintenance_task": {"maintenance_tasks", true},
	"maintenance_plan": {"maintenance_plans", true}, "task": {"tasks", true}, "project": {"projects", true},
	"it_asset": {"it_assets", false}, "kvp": {"kvp_ideas", false}, "part": {"spare_parts", false},
	setupRef:  {"setup_times", false},
	meterRef:  {"energy_meters", false},
	esppdhRef: {"esppdh_devices", false}, // ESPPDH-Geräte: Abteilung über die Anlage
}

// recordDepartments: Abteilungen eines Datensatzes (leer = keiner Abteilung
// zugeordnet – dann gilt ein abteilungsbegrenztes Recht wie überall).
func (h *Handler) recordDepartments(ctx context.Context, refType, id string) []string {
	if h.db == nil || id == "" {
		return nil
	}
	refType = refAlias(refType)
	q := ""
	if refType == "purchase_order" {
		q = `SELECT department_id::text FROM purchase_orders WHERE id = $1::uuid AND department_id IS NOT NULL`
	} else if refType == syncRef {
		q = `SELECT department_id::text FROM sync_profiles WHERE id = $1::uuid AND department_id IS NOT NULL`
	} else if refType == appRef {
		// PDH-App: Abteilung der Geräteanfrage (ohne Abteilung: leer; die App-Seiten
		// verlangen dann das globale Recht, siehe appAdminIn)
		q = `SELECT department_id::text FROM app_requests WHERE id = $1::uuid AND department_id IS NOT NULL`
	} else if refType == "infrastructure" {
		q = `SELECT department_id::text FROM infrastructure_department WHERE infrastructure_id = $1::uuid`
	} else if src, ok := recordDeptSource[refType]; ok {
		q = `SELECT idp.department_id::text FROM ` + src.table + ` r JOIN infrastructure_department idp ON idp.infrastructure_id = r.infrastructure_id WHERE r.id = $1::uuid`
		if src.group {
			q += ` UNION SELECT g.department_id::text FROM ` + src.table + ` r JOIN user_groups g ON g.id = r.assigned_group_id WHERE r.id = $1::uuid AND g.department_id IS NOT NULL`
		}
	} else {
		return nil
	}
	rows, err := h.db.Query(ctx, q, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if rows.Scan(&d) == nil && d != "" {
			out = append(out, d)
		}
	}
	return out
}

// refAlias: Modulnamen aus Pfaden und Feldsätzen auf die Datensatz-Typen.
func refAlias(t string) string {
	switch t {
	case "maintenance":
		return "maintenance_task"
	case "it":
		return "it_asset"
	case "storage", "business_partner":
		return t
	}
	if a, ok := refTypeAlias[t]; ok {
		return a
	}
	return t
}

// hasPermRec: hat die angemeldete Person das Recht für diesen Datensatz?
func (h *Handler) hasPermRec(r *http.Request, perm, refType, id string) bool {
	if perm == "" {
		return true
	}
	if h.rbac == nil || !h.hasPerm(r, perm) {
		return false
	}
	return h.hasPermIn(r, perm, h.recordDepartments(r.Context(), refType, id))
}

// areaAdminPerm: Bereichsadministrator-Recht je Modul (Feldsätze/Stammdaten).
var areaAdminPerm = map[string]string{
	"ticket": "tickets.admin", "fault": "faults.admin", "maintenance_task": "maintenance.admin", "maintenance": "maintenance.admin",
	"task": "tasks.admin", "project": "projects.admin", "kvp": "kvp.admin", "infrastructure": "infrastructure.admin", setupRef: permSetupAdmin, meterRef: permEnergyAdmin,
	"part": "inventory.admin", "storage": "inventory.admin", "purchase_order": "procurement.admin", "business_partner": "directory.admin", "it_asset": "it.admin",
	"certs":    permCertsAdmin,   // Zertifikate (Let's Encrypt): nur dieses Recht, nicht alle system.*
	syncRef:    permSyncAdmin,    // Abgleich mit Fremdsystemen: Profile löschen
	dataNetRef: permDataNetAdmin, // Datennetz: Clients endgültig entfernen
	"snmp":     permSNMPAdmin,    // SNMP-Zugangsprofile: endgültig löschen
	"network":  permNetworkAdmin, // Seite „Netzwerk“: VLANs und WLANs endgültig löschen
	esppdhRef:  permESPAdmin,     // ESPPDH: Geräte endgültig löschen
	appRef:     permAppAdmin,     // PDH-App: Geräte und Anfragen
}

// isAreaAdmin: Bereichsadministrator für diesen Datensatz?
func (h *Handler) isAreaAdmin(r *http.Request, module, id string) bool {
	perm := areaAdminPerm[module]
	return perm != "" && h.hasPermRec(r, perm, module, id)
}

// gateRule: eine Schreib-Route, die ein Recht braucht.
//   - refType "" und keine ID: Anlegen – das Recht muss irgendwo vorhanden sein.
//   - refType "*": Datensatz-Typ steht im Pfad (Gruppe 1, ID in Gruppe 2);
//     perm "edit" bzw. "done" wird je Typ aufgelöst (recordPerms).
//   - involved: Beteiligte (zugewiesen, verantwortlich, angelegt, Gruppe)
//     dürfen auch ohne das Recht – wie im Abschluss-Assistenten.
type gateRule struct {
	methods  string // z. B. "POST" oder "POST PUT DELETE"
	perm     string
	refType  string
	involved bool
	re       *regexp.Regexp
}

// recordPerms: Bearbeiten- und Abschluss-Recht je Datensatz-Typ.
var recordPerms = map[string][2]string{
	"ticket": {"tickets.edit", "tickets.resolve"}, "fault": {"faults.edit", "faults.resolve"},
	"task": {"tasks.edit", "tasks.edit"}, "project": {"projects.edit", "projects.edit"},
	"maintenance_task": {"maintenance.edit", "maintenance.complete"}, "it_asset": {"it.edit", "it.edit"},
}

func gr(methods, perm, refType string, involved bool, pattern string) gateRule {
	return gateRule{methods, perm, refType, involved, regexp.MustCompile(pattern)}
}

var gID = `/` + uuidRe

const (
	gAny  = `(?:/.*)?$`
	gEnd  = `/?$`
	apiV1 = `^/api/v1`
	wPost = "POST"
	wAll  = "POST PUT PATCH DELETE"
)

var gateRules = []gateRule{
	// ── endgültig löschen: nur Bereichsadministrator ──
	gr("DELETE", "tasks.admin", "task", false, apiV1+`/tasks`+gID+gEnd),
	gr("DELETE", "projects.admin", "project", false, apiV1+`/projects`+gID+gEnd),
	gr(wPost, "maintenance.admin", "maintenance_task", false, `^/maintenance/tasks`+gID+`/delete-web$`),
	gr(wPost, "kvp.admin", "kvp", false, `^/kvp`+gID+`/delete$`),
	gr(wPost, permSetupAdmin, setupRef, false, `^/setup`+gID+`/delete-web$`),
	gr(wPost, permEnergyAdmin, meterRef, false, `^/energy/meters`+gID+`/delete-web$`),
	gr(wPost, permEnergyAdmin, "", false, `^/energy/thresholds`+gID+`/delete-web$`), // Datensatz (Anlage der Messstelle) prüft der Handler
	gr("DELETE", "maintenance.admin", "", false, apiV1+`/maintenance/checklists`+gID+gEnd),
	// ── Zertifikate (Let's Encrypt): Entfernen nur Bereichsadministrator, Full (strict) braucht Cloudflare ──
	gr(wPost, permCertsAdmin, "", false, `^/admin/server-config/certs/remove$`),
	gr(wPost, permCloudflare, "", false, `^/admin/server-config/certs/strict$`),
	gr(wPost, permCloudflare, "", false, `^/admin/server-config/cloudflare/mtls-(?:off|on)$`),
	gr(wPost, permCerts, "", false, `^/admin/server-config/certs/(?:settings|add|issue)$`),
	gr(wPost, permUnifi, "", false, `^/admin/server-config/(?:unifi|unifi/confirm|certs/lan|certs/lan6)$`), // Reiter „UniFi“ und Namen im Haus
	gr(wPost, permOmada, "", false, `^/admin/server-config/omada(?:/confirm|/check)?$`),                    // Reiter „Omada“
	// Seite „Netzwerk“: löschen nur Bereichsadministrator, anlegen und ändern mit system.network
	gr(wPost, permNetworkAdmin, "", false, `^/admin/network/(?:segments|wlans)`+gID+`/delete$`),
	// Freigabe für ESPPDH-Updates: ändert die Firewall (UniFi) oder legt ein Ticket an die IT an
	gr(wPost, permNetworkAdmin, "", false, `^/admin/network/segments`+gID+`/esppdh-fw$`),
	gr(wPost, permNetwork, "", false, `^/admin/network/(?:segments|wlans)(?:`+gID+`)?$`),
	gr(wPost, permNetwork, "", false, `^/admin/network/suggestions/(?:adopt|adopt-all|hide)$`),
	// Infobildschirm: Folien anlegen, ändern, verschieben, löschen mit terminal.slides.edit
	gr(wPost, permTerminalSlides, "", false, `^/terminal-slides(?:`+gID+`(?:/delete|/move)?)?/?$`),
	// Karte „Dieser Server“: Adresse fest einstellen, DNS ändern, bestätigen nur mit system.server_config
	// (der Handler verlangt zusätzlich die Rolle Administrator, canServerConfig)
	gr(wPost, "system.server_config", "", false, `^/admin/network/server/(?:fix|dns|confirm)$`),
	// Reiter „SNMP“: Profil löschen nur Bereichsadministrator, anlegen und ändern mit system.snmp
	gr(wPost, permSNMPAdmin, "", false, `^/admin/server-config/snmp/profiles`+gID+`/delete$`),
	gr(wPost, permSNMP, "", false, `^/admin/server-config/snmp/profiles(?:`+gID+`)?$`),
	// ESPPDH: Gerät löschen nur Bereichsadministrator; anlegen, ändern, sperren, neues Kennwort,
	// Firmware bauen und Bau abbrechen mit esppdh.edit (für ein Gerät in der Abteilung seiner Anlage)
	gr(wPost, permESPAdmin, esppdhRef, false, `^/import/esppdh`+gID+`/delete$`),
	gr(wPost, permESPEdit, esppdhRef, false, `^/import/esppdh`+gID+`(?:/lock|/password|/web-password(?:/new)?|/build(?:`+gID+`/cancel)?)?$`),
	gr(wPost, permESPEdit, "", false, `^/import/esppdh/?$`),
	// Firmware übers Netz aufspielen mit esppdh.ota am Gerät (esppdh_ota.go)
	gr(wPost, permESPOTA, esppdhRef, false, `^/import/esppdh`+gID+`/ota$`),
	// Sammelaktion: esppdh.ota (der Knopf prüft zusätzlich „überall“)
	gr(wPost, permESPOTA, "", false, `^/import/esppdh/ota-all(?:/start|`+gID+`/cancel)?$`),
	// ── Löschvormerkung von Stammdaten über die Schnittstelle ──
	gr("DELETE", "inventory.edit", "part", false, `^(?:/api/v1)?/inventory`+gID+gEnd),
	gr("DELETE", "inventory.edit", "storage", false, `^(?:/api/v1)?/storage`+gID+gEnd),
	gr("DELETE", "directory.edit", "business_partner", false, `^(?:/api/v1)?/directory`+gID+gEnd),
	gr("DELETE", "infrastructure.edit", "infrastructure", false, `^(?:/api/v1)?/infrastructure`+gID+gEnd),

	// ── Verbindungen: Zertifikat der Fremddatenbank lesen (wie Verbindung bearbeiten) ──
	gr(wPost, "import.write", "", false, `^/import/connections/fingerprint$`),
	// Prüfliste des Brokers mit eigenem Namen (Zustände sieht, wer die Verbindung bearbeiten darf)
	gr("GET", "import.write", "", false, `^/import/connections`+gID+`/broker/checklist$`),
	// Ports des eingebauten Brokers in der Firewall des Servers öffnen (wer den Broker einrichtet)
	gr(wPost, "import.write", "", false, `^/import/connections`+gID+`/broker/firewall$`),
	gr(wPost, "import.write", "", false, `^/import/connections`+gID+`/broker/setup$`),
	gr(wPost, "export.write", "", false, `^/export/connections/fingerprint$`),
	// ── Zuordnungen: Sammel-Anlage (Vorschläge übernehmen) wie Verbindung bearbeiten; Abteilung je Anlage im Handler ──
	gr(wPost, "import.write", "", false, `^/import/connections`+gID+`/mappings/bulk$`),
	gr(wPost, "export.write", "", false, `^/export/connections`+gID+`/mappings/bulk$`),
	// Vorschläge zum Abhaken lesen Quellwerte (Broker, Datei, Server) – wie Verbindung bearbeiten
	gr("GET", "import.write", "", false, `^/import/connections`+gID+`/suggestions$`),
	gr("GET", "export.write", "", false, `^/export/connections`+gID+`/suggestions$`),
	// ── Explorer: Testschreiben/Senden wie Verbindung bearbeiten (Erkunden selbst: import/export.execute im Handler) ──
	gr(wPost, "import.write", "", false, `^/import/connections`+gID+`/explorer/(?:mqtt/publish|opcua/write|modbus/write)$`),
	gr(wPost, "export.write", "", false, `^/export/connections`+gID+`/explorer/(?:mqtt/publish|opcua/write|modbus/write)$`),
	// ── Abgleich mit Fremdsystemen: löschen nur Bereichsadministrator, einrichten mit sync.edit ──
	gr(wPost, permSyncAdmin, syncRef, false, `^/sync`+gID+`/delete-web$`),
	gr(wPost, permSyncEdit, syncRef, false, `^/sync`+gID+`/(?:source|target|maps|test-dry|test-write|interval)-web$`),
	gr(wPost, permSyncRun, syncRef, false, `^/sync`+gID+`/(?:arm|pause|run)-web$`),
	gr(wPost, permSyncRun, syncRef, false, `^/sync`+gID+`/outbox/[0-9]+/retry-web$`),
	gr(wPost, permSyncEdit, "", false, `^/sync/new-web$`),
	// ── Datennetz: entfernen nur Bereichsadministrator, anlegen und sperren mit datanet.edit ──
	gr(wPost, permDataNetAdmin, dataNetRef, false, `^/datanet/clients`+gID+`/delete$`),
	gr(wPost, permDataNetEdit, dataNetRef, false, `^/datanet/clients`+gID+`/revoke$`),
	gr(wPost, permDataNetEdit, "", false, `^/datanet/clients$`),
	gr(wPost, permDataNetAdmin, "", false, `^/datanet/settings$`),
	gr(wPost, permDataNetAdmin, "", false, `^/datanet/check/fix$`),
	// ── Störungen (Melden bleibt für alle offen) ──
	gr(wPost, "faults.resolve", "fault", true, `^(?:/api/v1)?/faults`+gID+`/resolve$`),
	gr(wPost, "faults.edit", "fault", true, `^/faults`+gID+`/(?:analyze|infrastructure-web)$`),
	gr(wAll, "faults.edit", "fault", true, apiV1+`/faults`+gID+`/(?:analyze|cost-center|due-date|actions|pending-parts)`+gAny),
	// ── Tickets (Melden und Kommentieren bleiben offen) ──
	gr(wPost, "tickets.resolve", "ticket", true, `^/tickets`+gID+`/resolve-web$`),
	gr(wPost, "tickets.resolve", "ticket", true, apiV1+`/tickets`+gID+`/resolve$`),
	gr(wPost, "tickets.edit", "ticket", true, `^/tickets`+gID+`/(?:status-web|infrastructure-web)$`),
	gr(wAll, "tickets.edit", "ticket", true, apiV1+`/tickets`+gID+`/(?:status|cost-center|due-date|actions|pending-parts)`+gAny),
	// ── Wartung ──
	gr(wPost, "maintenance.edit", "", false, `^/maintenance/(?:generate|plans`+gID+`/duplicate-web)$`),
	gr(wPost, "maintenance.edit", "maintenance_task", true, `^/maintenance/tasks`+gID+`/(?:start-web|edit-web)$`),
	gr(wAll, "maintenance.edit", "", false, apiV1+`/maintenance/(?:plans|checklists|checklist-items)(?:/.*)?$`),
	gr(wPost, "maintenance.edit", "", false, apiV1+`/maintenance/tasks(?:/generate)?/?$`),
	gr(wPost, "maintenance.complete", "maintenance_task", true, apiV1+`/maintenance/tasks`+gID+`/complete$`),
	gr(wAll, "maintenance.edit", "maintenance_task", true, apiV1+`/maintenance/tasks`+gID+`/(?:due-date|start|steps|checklists|actions|pending-parts)`+gAny),
	// ── Aufgaben und Projekte ──
	gr(wPost, "tasks.edit", "", false, apiV1+`/tasks/?$`),
	gr(wAll, "tasks.edit", "task", true, apiV1+`/tasks`+gID+`/(?:edit|status|resolve|actions|pending-parts)`+gAny),
	gr(wPost, "projects.edit", "", false, apiV1+`/projects/?$`),
	gr(wPost, "projects.edit", "project", true, apiV1+`/projects`+gID+`/edit$`),
	// ── IT ──
	gr(wPost, "it.edit", "", false, `^(?:/api/v1)?/it/?$`),
	gr(wAll, "it.edit", "it_asset", true, `^(?:/api/v1)?/it`+gID+`/(?:edit-web|status-web|status)$`),
	// Netz-Abgleich: einrichten und Vorschläge mit it.netsync, Schalter am Asset mit it.edit (Abteilung des Assets)
	gr(wPost, permITNetsync, "", false, `^/it/net/(?:settings|run|candidates/adopt|candidates/ignore)$`),
	gr(wPost, "it.edit", "it_asset", false, `^/it`+gID+`/net-(?:monitor|detach|reattach|grace)$`),
	// SNMP-Geräteliste: pflegen, prüfen, ein-/ausschalten mit it.netsync; aus der Liste
	// entfernen nur der Bereichsadministrator IT
	gr(wPost, itNetPermAdmin, "", false, `^/it/net/snmp/targets`+gID+`/remove$`),
	gr(wPost, permITNetsync, "", false, `^/it/net/snmp/targets(?:`+gID+`(?:/enabled|/check)?)?$`),
	// IT-Kategorien: anlegen, ändern, aus-/einblenden mit it.categories (Einstellungen ohne Abteilung);
	// endgültig löschen nur der Bereichsadministrator IT (nur unbenutzte Kategorien)
	gr(wPost, itNetPermAdmin, "", false, `^/it/categories/[a-z0-9-]+/delete$`),
	gr(wPost, permITCategories, "", false, `^/it/categories(?:/[a-z0-9-]+(?:/active)?)?$`),
	// ── Lager und Lagerorte (Stammdaten) ──
	gr(wPost, "inventory.create", "", false, `^(?:/api/v1)?/inventory/?$`),
	gr(wPost, "inventory.edit", "", false, apiV1+`/inventory/(?:fields|field-sets)(?:/.*)?$`),
	gr(wPost, "inventory.edit", "part", false, apiV1+`/inventory`+gID+`(?:/field-sets?|/fields)?/?$`),
	gr(wPost, "inventory.book", "", false, apiV1+`/inventory/book/?$`),
	gr(wPost, "inventory.book", "", false, apiV1+`/inventory/stocktake(?:/.*)?$`),
	gr(wPost, "inventory.create", "", false, `^(?:/api/v1)?/storage(?:/?|`+gID+`/(?:children|children-web))$`),
	gr(wPost, "inventory.edit", "storage", false, apiV1+`/storage`+gID+gEnd),
	// ── Hersteller & Lieferanten über die Schnittstelle ──
	gr(wPost, "directory.edit", "", false, apiV1+`/directory/?$`),
	gr(wPost, "directory.edit", "business_partner", false, apiV1+`/directory`+gID+gEnd),
	// ── Energiedaten: Messstellen anlegen und ändern mit energy.edit (Anlage bzw. Datensatz prüft der Handler) ──
	gr(wPost, permEnergyEdit, "", false, `^/energy/meters/save-web$`),
	gr(wPost, permEnergyThresholds, meterRef, false, `^/energy/meters`+gID+`/thresholds/save-web$`),
	gr(wPost, permEnergyAdmin, "", false, `^/energy/settings-web$`), // Einstellungen: nur Bereichsadministrator
	// ── Rüstzeiten: anlegen, bearbeiten und beenden mit setup.edit (für den Datensatz in seiner Abteilung) ──
	gr(wPost, permSetupEdit, "", false, `^/setup/?$`),
	gr(wPost, permSetupEdit, setupRef, false, `^/setup`+gID+`/(?:edit|finish)-web$`),
	gr(wPost, permSetupEdit, "", false, `^/core/settings/setup-web$`), // maximale Rüstzeit und Karenzzeit
	// Katalog der Gründe und Rüsttasten (Stamm)
	// (pflegen mit setup.edit, löschen nur setup.admin; Grund löschen = action=delete auf reasons-web, prüft der Handler)
	gr(wPost, permSetupAdmin, "", false, `^/setup/buttons`+gID+`/delete-web$`),
	gr(wPost, permSetupEdit, "", false, `^/setup/(?:reasons-web|buttons-web|buttons`+gID+`/move-web)$`),
	// Zuordnung der Rüsttasten und Höchstdauer an der Anlage: Infrastruktur bearbeiten (in der Abteilung der Anlage)
	gr(wPost, "infrastructure.edit", "infrastructure", false, `^/infrastructure`+gID+`/setup-(?:buttons|max)-web$`),
	// ── Stillstandsgründe: Katalog pflegen mit downtime.edit (Löschen eines Grundes prüft der Handler: downtime.admin) ──
	gr(wPost, permDowntimeEdit, "", false, `^/downtime/(?:reasons-web|reasons`+gID+`/move-web)$`),
	gr(wPost, "infrastructure.edit", "infrastructure", false, `^/infrastructure`+gID+`/downtime-reasons-web$`),
	gr(wPost, permCompanyEdit, "", false, `^/infrastructure/company$`), // überall: canEditCompany
	// ── Beteiligte, Gruppe, Archiv an beliebigen Vorgängen ──
	gr(wPost, "edit", "*", true, `^/records/([a-z_]+)`+gID+`/(?:people|group|parties)(?:/.*)?$`),
	// Maßnahmen-Baustein: wie die Schnittstelle …/actions (Bearbeiten des Moduls, Beteiligte dürfen)
	gr(wPost, "edit", "*", true, `^/records/actions/([a-z_]+)`+gID+`$`),
	gr(wPost, "done", "*", true, `^/records/([a-z_]+)`+gID+`/archive$`),
}

// PermissionGateMiddleware wird im Wurzel-Router vor ChangeTrackingMiddleware
// eingehängt (main.go): alle Schreib-Routen aus gateRules brauchen ihr Recht –
// für einen Datensatz in seiner Abteilung (Rechte auf Abteilungsebene).
func (h *Handler) PermissionGateMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions || h.rbac == nil {
			next.ServeHTTP(w, r)
			return
		}
		g, perm, refType, id, hit := matchGate(r.Method, r.URL.Path)
		if !hit {
			next.ServeHTTP(w, r)
			return
		}
		// Der Benutzer wird genau wie in der Anmeldung der Route bestimmt (requestCredentials).
		// Ohne Anmeldedaten lehnt die Route selbst ab. Mit Anmeldedaten, aus denen sich kein
		// aktiver Benutzer bestimmen lässt, wird nie durchgelassen – sonst ginge eine leicht
		// veränderte Anmeldung an dieser Prüfung vorbei, die die Route trotzdem annimmt.
		uid, present := h.requestCredentials(r)
		var u *users.User
		if uid != "" && h.users != nil {
			if got, err := h.users.GetByID(r.Context(), uid); err == nil && got != nil {
				u = got
			}
		}
		if u == nil {
			if !present {
				next.ServeHTTP(w, r)
				return
			}
			componentLog("rechte").Info().Str("pfad", r.URL.Path).Msg("schreibzugriff ohne bestimmbaren benutzer abgelehnt")
			const msg = "Keine Berechtigung: Die Anmeldung ist nicht mehr gültig. Bitte neu anmelden."
			gateDeny(w, r, msg, msg)
			return
		}
		ok := gatePermOK(perm, func(p string) bool { return h.rbac.HasPermissionForUser(u.ID, string(u.Role), p) })
		if ok && id != "" {
			ok = h.rbac.HasPermissionInDepartment(u.ID, string(u.Role), perm, h.recordDepartments(r.Context(), refType, id))
		}
		if !ok && g.involved && id != "" {
			ok = h.recordInvolved(r.Context(), u.ID, refType, id)
		}
		// Bearbeiten wirkt nur an Vorgängen, die man sehen darf (Projekt, KVP: Ansichtsrecht
		// oder beteiligt, view_rights.go) – „bearbeiten“ schließt „ansehen“ nicht ein.
		if ok && id != "" {
			if _, viewed := viewRights[refType]; viewed && !h.canViewRec(r.WithContext(context.WithValue(r.Context(), "user", u)), refType, id) {
				componentLog("rechte").Info().Str("user", u.ID).Str("typ", refType).Str("pfad", r.URL.Path).Msg("schreibzugriff ohne ansichtsrecht abgelehnt")
				const msg = "Keine Berechtigung: Diesen Vorgang darfst du nicht ansehen."
				gateDeny(w, r, msg, msg)
				return
			}
		}
		if !ok {
			componentLog("rechte").Info().Str("user", u.ID).Str("recht", perm).Str("pfad", r.URL.Path).Msg("schreibzugriff ohne recht abgelehnt")
			gateDeny(w, r, "Keine Berechtigung: Recht "+perm+" fehlt", "Keine Berechtigung: dafür brauchst du das Recht „"+h.permLabel(r, perm)+"“")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// gateDeny: Ablehnung der Rechte-Sperre (403) – als JSON für Schnittstelle, htmx und fetch,
// sonst als Text.
func gateDeny(w http.ResponseWriter, r *http.Request, jsonMsg, textMsg string) {
	if strings.HasPrefix(r.URL.Path, "/api/") || r.Header.Get("HX-Request") == "true" || strings.Contains(r.Header.Get("Accept"), "json") {
		b, _ := json.Marshal(struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}{false, jsonMsg})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write(b)
		return
	}
	http.Error(w, textMsg, http.StatusForbidden)
}

// gateAltPerms: Rechte, die ein Gate-Recht ersetzen (eines von beiden genügt). Nur für
// Regeln ohne Datensatz-ID – die Abteilungsprüfung danach gilt dem Gate-Recht.
var gateAltPerms = map[string][]string{
	permCerts: {permCertsAdmin}, // Bereichsadministrator der Zertifikate darf auch alles andere der Karte
	permSNMP:  {permSNMPAdmin},  // ebenso: Bereichsadministrator der SNMP-Zugangsprofile
}

// gatePermOK: hat jemand das Gate-Recht oder eines seiner Ersatzrechte?
func gatePermOK(perm string, has func(string) bool) bool {
	if has(perm) {
		return true
	}
	for _, alt := range gateAltPerms[perm] {
		if has(alt) {
			return true
		}
	}
	return false
}

// matchGate: Regel, nötiges Recht, Datensatz-Typ und -ID für eine Schreib-Route.
func matchGate(method, path string) (gateRule, string, string, string, bool) {
	for _, g := range gateRules {
		if !strings.Contains(" "+g.methods+" ", " "+method+" ") {
			continue
		}
		m := g.re.FindStringSubmatch(path)
		if m == nil {
			continue
		}
		perm, refType, id := g.perm, g.refType, ""
		if refType == "*" {
			refType, id = refAlias(m[1]), m[2]
			p, ok := recordPerms[refType]
			if !ok {
				return g, "", "", "", false // unbekannter Typ: die Route prüft selbst
			}
			perm = p[0]
			if g.perm == "done" {
				perm = p[1]
			}
		} else if refType != "" && len(m) > 1 {
			id = m[1]
		}
		return g, perm, refType, id, true
	}
	return gateRule{}, "", "", "", false
}

// recordInvolved: ist die Person am Datensatz beteiligt (zugewiesen,
// verantwortlich, angelegt oder über ihre Gruppe)?
func (h *Handler) recordInvolved(ctx context.Context, uid, refType, id string) bool {
	st, ok := scopeTables[refType]
	if !ok || h.db == nil {
		return false
	}
	var yes bool
	_ = h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+st.table+` r WHERE r.id = $2::uuid AND (`+st.people+`))`, uid, id).Scan(&yes)
	return yes
}

// permLabel: Bezeichnung eines Rechts (für Meldungen).
func (h *Handler) permLabel(r *http.Request, key string) string {
	if perms, err := h.rbac.ListPermissions(r.Context()); err == nil {
		for _, p := range perms {
			if p.Key == key {
				return p.Label
			}
		}
	}
	return key
}
