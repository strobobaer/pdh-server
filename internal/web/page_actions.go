package web

// Seiteneigene Aktionen des neuen Rahmens (Entwurf 10.1, 11.10): jede umgestellte Seite
// meldet ihre Aktionen hier einmal. Band = Gruppen im Band der Seite; Start = die
// Hauptaktionen, die zusätzlich im Band Start stehen, solange die Seite offen ist.
type pageActs struct {
	Band  []ribbonGroup
	Start []ribbonItem
}

func pageActions(b *BaseData) pageActs {
	l := func(s string) string { return tr(b.Lang, s) }
	it := func(action, label, icon, when string) ribbonItem {
		return ribbonItem{Key: action, Label: l(label), Icon: icon, Action: action, When: when}
	}
	can := func(p string) bool { return b.navCan != nil && b.navCan(p) }
	switch b.Page {
	case "dashboard": // Vorlage Cockpit (Entwurf 12.1): bearbeiten, speichern, als Standard
		save := it("save-edit", "Speichern", "ti-device-floppy", "editing")
		save.Primary = true
		reset := ribbonItem{Key: "dash-reset", Label: l("Auf Vorgabe zurücksetzen"), Icon: "ti-restore", Action: "dash-reset", When: "idle"}
		a := pageActs{
			Band: []ribbonGroup{{Label: l("Startseite"), Items: []ribbonItem{
				it("dashboard-edit", "Bearbeiten", "ti-edit", "idle"),
				it("addw", "Widget hinzufügen", "ti-plus", "editing"),
				save,
				it("cancel-edit", "Abbrechen", "ti-x", "editing"),
				reset,
			}}},
			Start: []ribbonItem{it("dashboard-edit", "Bearbeiten", "ti-edit", "idle"), save, it("cancel-edit", "Abbrechen", "ti-x", "editing")},
		}
		if can(permDefaults) {
			a.Band = append(a.Band, ribbonGroup{Label: l("Firma"), Items: []ribbonItem{it("dash-standard", "Als Standard für alle setzen", "ti-users", "idle")}})
		}
		return a
	}
	return pageActs{}
}

// timeGroup: Gruppe „Zeit“ im Band Start (Entwurf 11.9) – nicht am Terminal des
// Systembenutzers. Läuft eine Zeit, zeigt die Taste „Zeit stoppen“ (zeiterfassung.go).
func timeGroup(b *BaseData) (ribbonGroup, bool) {
	if b.IsSystemUser {
		return ribbonGroup{}, false
	}
	l := func(s string) string { return tr(b.Lang, s) }
	start := ribbonItem{Key: "time-start", Label: l("Zeit starten"), Icon: "ti-player-play", Action: "time-start"}
	manual := ribbonItem{Key: "time-manual", Label: l("Manuell erfassen"), Icon: "ti-history", Action: "time-manual"}
	return ribbonGroup{Label: l("Zeit"), Items: []ribbonItem{start, manual}}, true
}
