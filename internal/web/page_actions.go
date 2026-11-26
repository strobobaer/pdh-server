package web

// Seiteneigene Aktionen des neuen Rahmens (Entwurf 10.1, 11.10): jede umgestellte Seite
// meldet ihre Aktionen hier einmal. Band = Gruppen im Band der Seite; Start = die
// Hauptaktionen, die zusätzlich im Band Start stehen, solange die Seite offen ist.
type pageActs struct {
	Band  []ribbonGroup
	Start []ribbonItem
}

func pageActions(b *BaseData) pageActs {
	switch b.Page {
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
