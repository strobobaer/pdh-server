package web

import (
	"context"
	"time"
)

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
	case "leitstand": // Vorlage Cockpit; die Bausteine bearbeitet die Verwaltungsseite des Leitstands
		var a pageActs
		if b.CanManageLeitstand && b.LeitstandID != "" {
			a.Band = append(a.Band,
				ribbonGroup{Label: l("Leitstand"), Items: []ribbonItem{{Key: "leit-edit", Label: l("Bearbeiten"), Icon: "ti-edit", Href: "/leitstaende/" + b.LeitstandID}}},
				ribbonGroup{Label: l("Firma"), Items: []ribbonItem{{Key: "leit-standard", Label: l("Als Standard für alle setzen"), Icon: "ti-users", Action: "leit-standard", Arg: b.LeitstandID}}})
			a.Start = []ribbonItem{{Key: "leit-edit", Label: l("Bearbeiten"), Icon: "ti-edit", Href: "/leitstaende/" + b.LeitstandID}}
		}
		a.Band = append(a.Band, ribbonGroup{Label: l("Anzeige"), Items: []ribbonItem{
			{Key: "leit-full", Label: l("Vollbild (Wandbildschirm)"), Icon: "ti-arrows-maximize", Href: "/global/?vollbild=1", Blank: true}}})
		return a
	}
	return pageActs{}
}

// leitstandLoginCorner: ohne Anmeldung zeigt der Leitstand im neuen Rahmen links unten nur
// „Anmelden“ (Entwurf 11.4) – nicht im Vollbild am Wandbildschirm und nicht eingebettet.
func leitstandLoginCorner(loggedIn, newFrame, full, embedded bool) bool {
	return !loggedIn && newFrame && !full && !embedded
}

// timeGroup: Gruppe „Zeit“ im Band Start (Entwurf 11.9) – nicht am Terminal des
// Systembenutzers. Läuft eine Zeit, zeigt die Taste „Zeit stoppen“ (zeiterfassung.go).
func timeGroup(b *BaseData) (ribbonGroup, bool) {
	if b.IsSystemUser {
		return ribbonGroup{}, false
	}
	l := func(s string) string { return tr(b.Lang, s) }
	first := ribbonItem{Key: "time-start", Label: l("Zeit starten"), Icon: "ti-player-play", Action: "time-start"}
	if r := b.TimeRunning; r != nil {
		first = ribbonItem{Key: "time-stop", Label: l("Zeit stoppen"), Icon: "ti-player-stop", Action: "time-stop", Arg: r.ID,
			Since: r.Since.UTC().Format(time.RFC3339), Primary: true}
	}
	manual := ribbonItem{Key: "time-manual", Label: l("Manuell erfassen"), Icon: "ti-history", Action: "time-manual"}
	return ribbonGroup{Label: l("Zeit"), Items: []ribbonItem{first, manual}}, true
}

// timeRunning: laufende Zeitbuchung des Benutzers (Taste „Zeit stoppen“ mit Dauer).
type timeRunning struct {
	ID    string
	Since time.Time
}

// timeRunningFor: laufende Zeit des Benutzers, nil ohne (auch an einem anderen Gerät gestartet).
func (h *Handler) timeRunningFor(ctx context.Context, uid string) *timeRunning {
	if h.time == nil || uid == "" {
		return nil
	}
	e, err := h.time.GetRunning(ctx, uid)
	if err != nil || e == nil {
		return nil
	}
	return &timeRunning{ID: e.ID, Since: e.StartedAt}
}
