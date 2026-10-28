package web

// Design-Stil: Formensprache der ganzen Oberflaeche (Flaechen, Ecken,
// Schatten, Knoepfe, Reiter, Tabellen) als Ergaenzung zu Farbschema und
// Schrift (appearance.go). „Fluent“ ist die bisherige Windows-11-Optik,
// „Fiori“ lehnt sich an SAP Fiori (Horizon) an und ist der Standard.
// Umgesetzt allein in CSS:
// base.gohtml setzt html[data-style], die Regeln dazu stehen dort.
// Firmenstandard in den Server-Einstellungen, je Benutzer im Benutzermenue
// waehlbar (wie das Farbschema nur, wenn die Wahl freigegeben ist).

const (
	keyBrandStyle = "branding.style"
	defaultStyle  = "fiori"
)

// UIStyle: waehlbarer Design-Stil.
// Short steht im Umschalter des Benutzermenues, Name und Hint im Tooltip.
type UIStyle struct {
	ID, Short, Name, Hint string
}

var uiStyles = []UIStyle{
	{"fiori", "Fiori", "Fiori (SAP)", "weiße Kopfleiste, Karten mit Schatten, blaue Textknöpfe"},
	{"fluent", "Fluent", "Fluent (Windows 11)", "Mica-Flächen, feine Linien, kleine Ecken"},
}

func validStyle(id string) bool {
	for _, s := range uiStyles {
		if s.ID == id {
			return true
		}
	}
	return false
}

// Styles: alle waehlbaren Design-Stile.
func (b Branding) Styles() []UIStyle { return uiStyles }

// StyleID: Firmenstandard-Stil.
func (b Branding) StyleID() string {
	if validStyle(b.Style) {
		return b.Style
	}
	return defaultStyle
}

// StyleID: wirksamer Stil dieses Benutzers (fuer data-style).
func (a Appearance) StyleID() string {
	if a.Allow && validStyle(a.User.Style) {
		return a.User.Style
	}
	return a.Brand.StyleID()
}
